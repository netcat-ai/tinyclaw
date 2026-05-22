package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

const defaultCodexRunnerTimeout = 5 * time.Minute
const maxMemorySearchRounds = 2

type CodexRunnerConfig struct {
	Bin              string
	WorkDir          string
	Model            string
	Sandbox          string
	DisabledFeatures []string
	Timeout          time.Duration
}

type CodexRunner struct {
	config CodexRunnerConfig
}

func NewCodexRunner(config CodexRunnerConfig) *CodexRunner {
	if strings.TrimSpace(config.Bin) == "" {
		config.Bin = "codex"
	}
	if strings.TrimSpace(config.WorkDir) == "" {
		config.WorkDir = "."
	}
	if strings.TrimSpace(config.Sandbox) == "" {
		config.Sandbox = "workspace-write"
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultCodexRunnerTimeout
	}
	if config.DisabledFeatures == nil {
		config.DisabledFeatures = []string{"apps", "tool_suggest", "plugins"}
	}
	return &CodexRunner{config: config}
}

func (r *CodexRunner) RunAgent(ctx context.Context, run AgentRunRequest) (core.AgentRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

	var result core.AgentRunResult
	for round := 0; round <= maxMemorySearchRounds; round++ {
		var err error
		result, err = r.runCodexExec(runCtx, run)
		if err != nil {
			return core.AgentRunResult{}, err
		}
		if strings.TrimSpace(result.CodexSessionID) != "" {
			run.AgentRun.CodexSessionID = strings.TrimSpace(result.CodexSessionID)
		}
		if len(result.MemorySearchRequests) == 0 {
			return result, nil
		}
		if strings.TrimSpace(run.MemorySearchURL) == "" || strings.TrimSpace(run.MemorySearchToken) == "" {
			return core.AgentRunResult{}, fmt.Errorf("agent requested memory search but no memory capability is configured")
		}
		if round == maxMemorySearchRounds {
			return core.AgentRunResult{}, fmt.Errorf("agent exceeded memory search round limit")
		}
		searchResults := runMemorySearchRequests(runCtx, run, result.MemorySearchRequests)
		run.MemorySearchResults = append(run.MemorySearchResults, searchResults...)
	}
	return result, nil
}

func (r *CodexRunner) runCodexExec(ctx context.Context, run AgentRunRequest) (core.AgentRunResult, error) {
	outputFile, err := os.CreateTemp("", "tinyclaw-codex-output-*.txt")
	if err != nil {
		return core.AgentRunResult{}, fmt.Errorf("create codex output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer func() { _ = os.Remove(outputPath) }()

	schemaPath, cleanupSchema, err := writeAgentRunResultSchema()
	if err != nil {
		return core.AgentRunResult{}, err
	}
	defer cleanupSchema()

	codexSessionID := strings.TrimSpace(run.AgentRun.CodexSessionID)
	args := r.codexExecArgs(schemaPath, outputPath, codexSessionID)
	cmd := exec.CommandContext(ctx, r.config.Bin, args...)
	cmd.Dir = r.config.WorkDir
	cmd.Stdin = strings.NewReader(BuildCodexPrompt(run))
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(combined.String())
		if detail == "" {
			detail = err.Error()
		}
		if codexSessionID != "" && isCodexResumeStale(detail) {
			run.AgentRun.CodexSessionID = ""
			return r.runCodexExec(ctx, run)
		}
		return core.AgentRunResult{}, fmt.Errorf("codex exec failed: %s", detail)
	}
	eventErr := codexErrorFromEvents(combined.String())
	if eventErr != "" {
		if codexSessionID != "" && isCodexResumeStale(eventErr) {
			run.AgentRun.CodexSessionID = ""
			return r.runCodexExec(ctx, run)
		}
		return core.AgentRunResult{}, fmt.Errorf("codex exec failed: %s", eventErr)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return core.AgentRunResult{}, fmt.Errorf("read codex output: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		text = strings.TrimSpace(combined.String())
	}
	result, err := ParseAgentRunResult(text)
	if err != nil {
		return core.AgentRunResult{}, err
	}
	result.CodexSessionID = codexSessionIDFromEvents(combined.String())
	if result.CodexSessionID == "" {
		result.CodexSessionID = codexSessionID
	}
	return result, nil
}

func (r *CodexRunner) codexExecArgs(schemaPath string, outputPath string, codexSessionID string) []string {
	args := []string{
		"-a", "never",
	}
	for _, feature := range r.config.DisabledFeatures {
		feature = strings.TrimSpace(feature)
		if feature == "" {
			continue
		}
		args = append(args, "--disable", feature)
	}
	args = append(args,
		"exec",
		"--skip-git-repo-check",
		"--json",
		"--cd", r.config.WorkDir,
		"--sandbox", r.config.Sandbox,
		"--output-last-message", outputPath,
	)
	if strings.TrimSpace(r.config.Model) != "" {
		args = append(args, "--model", r.config.Model)
	}
	if strings.TrimSpace(codexSessionID) != "" {
		return append(args, "resume", codexSessionID, "-")
	}
	return append(args, "--output-schema", schemaPath, "-")
}

func codexSessionIDFromEvents(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" && strings.TrimSpace(event.ThreadID) != "" {
			return strings.TrimSpace(event.ThreadID)
		}
	}
	return ""
}

func codexErrorFromEvents(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type != "error" && event.Type != "turn.failed" {
			continue
		}
		if strings.TrimSpace(event.Message) != "" {
			return strings.TrimSpace(event.Message)
		}
		if strings.TrimSpace(event.Error.Message) != "" {
			return strings.TrimSpace(event.Error.Message)
		}
		return event.Type
	}
	return ""
}

func isCodexResumeStale(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "no conversation found") ||
		strings.Contains(detail, "thread/resume failed") ||
		strings.Contains(detail, "no rollout found for thread id") ||
		strings.Contains(detail, "session not found") ||
		strings.Contains(detail, "not found") ||
		strings.Contains(detail, "no such file")
}

func runMemorySearchRequests(ctx context.Context, run AgentRunRequest, requests []core.MemorySearchInput) []core.MemorySearchResult {
	results := make([]core.MemorySearchResult, 0, len(requests))
	for _, search := range requests {
		search.RoomID = 0
		body, err := json.Marshal(search)
		if err != nil {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("encode memory search request: %w", err)))
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, run.MemorySearchURL, bytes.NewReader(body))
		if err != nil {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("create memory search request: %w", err)))
			continue
		}
		req.Header.Set("Authorization", "Bearer "+run.MemorySearchToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("call memory search: %w", err)))
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("read memory search response: %w", readErr)))
			continue
		}
		if closeErr != nil {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("close memory search response: %w", closeErr)))
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("memory search status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))))
			continue
		}
		var parsed struct {
			Items []core.MemoryItem `json:"items"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			results = append(results, memorySearchErrorResult(search, fmt.Errorf("decode memory search response: %w", err)))
			continue
		}
		results = append(results, core.MemorySearchResult{
			Request: search,
			Items:   parsed.Items,
		})
	}
	return results
}

func memorySearchErrorResult(search core.MemorySearchInput, err error) core.MemorySearchResult {
	slog.Warn("memory search failed; continuing agent run", "query", search.Query, "err", err)
	return core.MemorySearchResult{
		Request: search,
		Items:   []core.MemoryItem{},
		Error:   err.Error(),
	}
}

func BuildCodexPrompt(run AgentRunRequest) string {
	var builder strings.Builder
	builder.WriteString("Return only one JSON object matching Agent Run Result: {\"final_output\":\"...\",\"memory_write_proposals\":[],\"memory_search_requests\":[]}. ")
	builder.WriteString("Put the user-visible reply in final_output and durable Room Memory proposals in memory_write_proposals. ")
	builder.WriteString("If you need durable Room Memory before answering, put requests in memory_search_requests and leave final_output empty. ")
	builder.WriteString("Each proposal must include op, type, key, and content; use an empty string for unused content. ")
	builder.WriteString("Only propose durable Room Memory changes for stable facts, preferences, or todos. Prefer an empty memory_write_proposals array when unsure.\n\n")
	if strings.TrimSpace(run.MemorySearchURL) != "" && strings.TrimSpace(run.MemorySearchToken) != "" {
		builder.WriteString("Room Memory Search:\n")
		builder.WriteString("- Request Memory Search by returning memory_search_requests in Agent Run Result.\n")
		builder.WriteString("- If the user asks about memory, preferences, prior decisions, todos, or durable context, request memory_search before answering.\n")
		builder.WriteString("- Do not include room_id; Clawman binds the Room from the capability token.\n")
		builder.WriteString("- Request shape: {\"query\":\"...\",\"types\":[\"fact\",\"preference\",\"todo\"],\"limit\":5,\"include_inactive\":false}\n\n")
	}
	if len(run.MemorySearchResults) > 0 {
		builder.WriteString("Room Memory Search Results:\n")
		data, _ := json.Marshal(run.MemorySearchResults)
		builder.WriteString(string(data))
		builder.WriteString("\n\n")
	}
	builder.WriteString("Agent Session ID: ")
	fmt.Fprintf(&builder, "%d", run.AgentRun.AgentSessionID)
	builder.WriteString("\nRoom ID: ")
	fmt.Fprintf(&builder, "%d", run.AgentRun.RoomID)
	builder.WriteString("\nMessage Window: ")
	fmt.Fprintf(&builder, "[%d, %d]", run.AgentRun.SourceMessageFromID, run.AgentRun.SourceMessageToID)
	builder.WriteString("\n\nConversation messages:\n")
	if len(run.ContextMessages) == 0 {
		builder.WriteString("(empty)\n")
	}
	for _, message := range run.ContextMessages {
		builder.WriteString("- ")
		builder.WriteString(formatCodexPromptMessage(message))
		builder.WriteString("\n")
	}
	return builder.String()
}

func ParseAgentRunResult(text string) (core.AgentRunResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return core.AgentRunResult{}, nil
	}
	var result core.AgentRunResult
	if err := json.Unmarshal([]byte(text), &result); err == nil {
		result.FinalOutput = strings.TrimSpace(result.FinalOutput)
		return result, nil
	}
	final, hasFinal := extractTaggedBlock(text, "final")
	rawProposals, hasProposals := extractTaggedBlock(text, "memory_write_proposals")
	result = core.AgentRunResult{FinalOutput: strings.TrimSpace(final)}
	if !hasFinal {
		result.FinalOutput = text
	}
	if hasProposals && strings.TrimSpace(rawProposals) != "" {
		if err := json.Unmarshal([]byte(rawProposals), &result.MemoryWriteProposals); err != nil {
			return core.AgentRunResult{}, fmt.Errorf("decode memory write proposals: %w", err)
		}
	}
	return result, nil
}

func writeAgentRunResultSchema() (string, func(), error) {
	file, err := os.CreateTemp("", "tinyclaw-agent-run-result-schema-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("create agent run result schema: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	_, writeErr := file.WriteString(agentRunResultSchema)
	closeErr := file.Close()
	if writeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("write agent run result schema: %w", writeErr)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("close agent run result schema: %w", closeErr)
	}
	return path, cleanup, nil
}

const agentRunResultSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "final_output": {
      "type": "string"
    },
    "memory_write_proposals": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "op": {
            "type": "string",
            "enum": ["upsert_fact", "set_preference", "add_todo", "close_todo", "mark_stale"]
          },
          "type": {
            "type": "string",
            "enum": ["fact", "preference", "todo"]
          },
          "key": {
            "type": "string"
          },
          "content": {
            "type": "string"
          }
        },
        "required": ["op", "type", "key", "content"]
      }
    },
    "memory_search_requests": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "query": {
            "type": "string"
          },
          "types": {
            "type": "array",
            "items": {
              "type": "string",
              "enum": ["fact", "preference", "todo"]
            }
          },
          "limit": {
            "type": "integer"
          },
          "include_inactive": {
            "type": "boolean"
          }
        },
        "required": ["query", "types", "limit", "include_inactive"]
      }
    }
  },
  "required": ["final_output", "memory_write_proposals", "memory_search_requests"]
}`

func extractTaggedBlock(text string, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return "", false
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return "", false
	}
	return text[start : start+end], true
}

func formatCodexPromptMessage(message core.Message) string {
	sender := strings.TrimSpace(message.SenderName)
	if sender == "" {
		sender = strings.TrimSpace(message.SenderID)
	}
	if sender == "" {
		sender = "unknown"
	}
	text := extractMessageText(message.Payload)
	return fmt.Sprintf("id=%d sender=%s text=%q", message.ID, sender, text)
}

func extractMessageText(payload json.RawMessage) string {
	var parsed struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &parsed); err == nil && strings.TrimSpace(parsed.Text) != "" {
		return strings.TrimSpace(parsed.Text)
	}
	return strings.TrimSpace(string(payload))
}

func AbsoluteCodexWorkDir(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		return "."
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return workDir
	}
	return abs
}
