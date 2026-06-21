package executor

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

const defaultCodexRunnerTimeout = 5 * time.Minute
const maxMemorySearchRounds = 2

//go:embed codex_agent_prompt.md
var codexAgentPromptText string

type CodexRunnerConfig struct {
	Bin              string
	WorkDir          string
	Model            string
	Sandbox          string
	OpenAIBaseURL    string
	DisabledFeatures []string
	MediaBaseURL     string
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
	if strings.TrimSpace(run.MediaBaseURL) == "" {
		run.MediaBaseURL = r.config.MediaBaseURL
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
			result.ImageGenerationCount = len(result.ImageGenerationRequests)
			result.MemorySearchCount = len(run.MemorySearchResults)
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
	cmd.Env = append(os.Environ(),
		"TINYCLAW_MEDIA_BASE_URL="+codexMediaBaseURL(run.MediaBaseURL),
		"TINYCLAW_MEDIA_DOWNLOAD_DIR="+codexMediaDownloadDir(run),
	)
	cmd.Stdin = strings.NewReader(BuildCodexPrompt(run))
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	cmdErr := cmd.Run()
	combinedText := combined.String()
	events := summarizeCodexEvents(combinedText)
	if cmdErr != nil {
		detail := strings.TrimSpace(combinedText)
		if detail == "" {
			detail = cmdErr.Error()
		}
		if events.TurnFailure != "" {
			return r.failCodexExec(ctx, run, codexSessionID, events.TurnFailure)
		}
		result, source, parseErr := readCodexRunResult(outputPath, events, combinedText, false)
		if parseErr == nil && source != "" && hasAgentRunResultContent(result) && (source == codexResultSourceOutputFile || events.TurnCompleted) {
			return attachCodexSessionID(result, events, codexSessionID), nil
		}
		if events.LastError != "" {
			return r.failCodexExec(ctx, run, codexSessionID, events.LastError)
		}
		return r.failCodexExec(ctx, run, codexSessionID, detail)
	}
	if events.TurnFailure != "" {
		return r.failCodexExec(ctx, run, codexSessionID, events.TurnFailure)
	}
	result, _, err := readCodexRunResult(outputPath, events, combinedText, true)
	if err != nil {
		if events.LastError != "" {
			return r.failCodexExec(ctx, run, codexSessionID, events.LastError)
		}
		return core.AgentRunResult{}, err
	}
	return attachCodexSessionID(result, events, codexSessionID), nil
}

func (r *CodexRunner) failCodexExec(ctx context.Context, run AgentRunRequest, codexSessionID string, detail string) (core.AgentRunResult, error) {
	if strings.TrimSpace(codexSessionID) != "" && isCodexResumeStale(detail) {
		run.AgentRun.CodexSessionID = ""
		return r.runCodexExec(ctx, run)
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "unknown error"
	}
	return core.AgentRunResult{}, fmt.Errorf("codex exec failed: %s", detail)
}

const (
	codexResultSourceOutputFile       = "output_file"
	codexResultSourceAgentMessage     = "agent_message"
	codexResultSourceRawEventFallback = "raw_events"
)

func readCodexRunResult(outputPath string, events codexEventSummary, eventOutput string, allowRawEventFallback bool) (core.AgentRunResult, string, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return core.AgentRunResult{}, "", fmt.Errorf("read codex output: %w", err)
	}
	text := strings.TrimSpace(string(data))
	source := codexResultSourceOutputFile
	if text == "" {
		source = ""
		text = events.LastAgentMessage
		if text != "" {
			source = codexResultSourceAgentMessage
		}
	}
	if text == "" && allowRawEventFallback && !events.HasEvents {
		text = strings.TrimSpace(eventOutput)
		if text != "" {
			source = codexResultSourceRawEventFallback
		}
	}
	if text == "" {
		return core.AgentRunResult{}, source, fmt.Errorf("codex output is empty")
	}
	result, err := ParseAgentRunResult(text)
	if err != nil {
		return core.AgentRunResult{}, source, err
	}
	return result, source, nil
}

func attachCodexSessionID(result core.AgentRunResult, events codexEventSummary, fallback string) core.AgentRunResult {
	result.CodexSessionID = events.ThreadID
	if result.CodexSessionID == "" {
		result.CodexSessionID = fallback
	}
	return result
}

func hasAgentRunResultContent(result core.AgentRunResult) bool {
	return strings.TrimSpace(result.FinalOutput) != "" ||
		len(result.MemorySearchRequests) > 0 ||
		len(result.MemoryWriteProposals) > 0 ||
		len(result.ImageGenerationRequests) > 0
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
	if strings.TrimSpace(r.config.OpenAIBaseURL) != "" {
		args = append(args, "-c", "openai_base_url="+strconv.Quote(strings.TrimSpace(r.config.OpenAIBaseURL)))
	}
	if strings.TrimSpace(r.config.Model) != "" {
		args = append(args, "--model", r.config.Model)
	}
	if strings.TrimSpace(codexSessionID) != "" {
		return append(args, "resume", codexSessionID, "-")
	}
	return append(args, "--output-schema", schemaPath, "-")
}

func codexMediaBaseURL(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return strings.TrimRight(trimmed, "/")
	}
	return "http://127.0.0.1:8081"
}

func codexMediaDownloadDir(run AgentRunRequest) string {
	return "/tmp/tinyclaw/" + strconv.FormatInt(run.AgentRun.RoomID, 10)
}

type codexPromptSubagent struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt,omitempty"`
}

type codexEventSummary struct {
	ThreadID         string
	LastAgentMessage string
	LastError        string
	TurnFailure      string
	HasEvents        bool
	TurnCompleted    bool
}

func summarizeCodexEvents(output string) codexEventSummary {
	var summary codexEventSummary
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if strings.TrimSpace(event.Type) == "" {
			continue
		}
		summary.HasEvents = true
		switch event.Type {
		case "thread.started":
			if strings.TrimSpace(event.ThreadID) != "" {
				summary.ThreadID = strings.TrimSpace(event.ThreadID)
			}
		case "item.completed":
			if event.Item.Type == "agent_message" {
				if text := strings.TrimSpace(event.Item.Text); text != "" {
					summary.LastAgentMessage = text
				}
			}
		case "turn.completed":
			summary.TurnCompleted = true
		case "turn.failed":
			summary.TurnFailure = codexEventMessage(event.Message, event.Error.Message, event.Type)
			summary.LastError = summary.TurnFailure
		case "error":
			summary.LastError = codexEventMessage(event.Message, event.Error.Message, event.Type)
		}
	}
	return summary
}

func codexEventMessage(message string, errorMessage string, fallback string) string {
	if strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if strings.TrimSpace(errorMessage) != "" {
		return strings.TrimSpace(errorMessage)
	}
	return fallback
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
	builder.WriteString(strings.TrimSpace(codexAgentPromptText))
	builder.WriteString("\n\nContext messages (JSONL):\n")
	writeCodexPromptContextMessages(&builder, run)
	for _, message := range run.ContextMessages {
		builder.WriteString(formatCodexPromptMessage(message))
		builder.WriteString("\n")
	}
	return builder.String()
}

func writeCodexPromptContextMessages(builder *strings.Builder, run AgentRunRequest) {
	if strings.TrimSpace(run.MemorySearchURL) != "" && strings.TrimSpace(run.MemorySearchToken) != "" {
		writeCodexPromptJSONLine(builder, map[string]any{
			"kind":          "capabilities",
			"memory_search": true,
		})
	}
	if prompt := strings.TrimSpace(run.AgentRun.RoomPrompt); prompt != "" {
		writeCodexPromptJSONLine(builder, map[string]any{
			"kind":    "room_prompt",
			"content": prompt,
		})
	}
	if len(run.MemorySearchResults) > 0 {
		writeCodexPromptJSONLine(builder, map[string]any{
			"kind":  "memory_search_results",
			"items": run.MemorySearchResults,
		})
	}
	agents := make([]codexPromptSubagent, 0, len(run.SelectedAgents))
	for _, agent := range run.SelectedAgents {
		agents = append(agents, codexPromptSubagent{
			Key:         strings.TrimSpace(agent.Key),
			DisplayName: strings.TrimSpace(agent.DisplayName),
			Description: strings.TrimSpace(agent.Description),
			Prompt:      strings.TrimSpace(agent.Prompt),
		})
	}
	if len(agents) > 0 {
		writeCodexPromptJSONLine(builder, map[string]any{
			"kind":  "selected_agents",
			"items": agents,
		})
	}
}

func writeCodexPromptJSONLine(builder *strings.Builder, value any) {
	data, _ := json.Marshal(value)
	builder.WriteString(string(data))
	builder.WriteString("\n")
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
    },
    "image_generation_requests": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "prompt": {
            "type": "string"
          },
          "mode": {
            "type": "string",
            "enum": ["generate", "edit"]
          },
          "source_message_ids": {
            "type": "array",
            "items": {
              "type": "integer"
            }
          },
          "size": {
            "type": "string"
          },
          "source_image_summary": {
            "type": "string"
          },
          "edit_instruction": {
            "type": "string"
          },
          "preserve": {
            "type": "array",
            "items": {
              "type": "string"
            }
          },
          "negative": {
            "type": "array",
            "items": {
              "type": "string"
            }
          },
          "output_format": {
            "type": "string",
            "enum": ["jpeg"]
          }
        },
        "required": ["mode", "prompt", "source_message_ids", "size", "source_image_summary", "edit_instruction", "preserve", "negative", "output_format"]
      }
    }
  },
  "required": ["final_output", "memory_write_proposals", "memory_search_requests", "image_generation_requests"]
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
	msgType := strings.TrimSpace(message.MsgType)
	if msgType == "" {
		msgType = "text"
	}
	output := map[string]any{
		"id":     message.ID,
		"sender": sender,
		"type":   msgType,
		msgType:  json.RawMessage(message.Body),
	}
	if commandKind := extractMessageCommandKind(message.Body); commandKind != "" {
		output["handled_command"] = commandKind
	}
	data, err := json.Marshal(output)
	if err != nil {
		text := extractMessageText(message.Body)
		return fmt.Sprintf(`{"id":%d,"sender":%q,"type":"text","text":{"content":%q}}`, message.ID, sender, text)
	}
	return string(data)
}

func extractMessageCommandKind(payload json.RawMessage) string {
	var parsed struct {
		CommandKind string `json:"command_kind"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.CommandKind)
}

func extractMessageText(payload json.RawMessage) string {
	var parsed struct {
		Text    any    `json:"text"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(payload, &parsed); err == nil {
		if text := strings.TrimSpace(parsed.Content); text != "" {
			return text
		}
		switch value := parsed.Text.(type) {
		case string:
			if text := strings.TrimSpace(value); text != "" {
				return text
			}
		case map[string]any:
			if content, ok := value["content"].(string); ok {
				if text := strings.TrimSpace(content); text != "" {
					return text
				}
			}
		}
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
