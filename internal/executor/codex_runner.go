package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

const defaultCodexRunnerTimeout = 5 * time.Minute

type CodexRunnerConfig struct {
	Bin       string
	WorkDir   string
	Model     string
	Sandbox   string
	Timeout   time.Duration
	BaseURL   string
	APIKeyEnv string
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
	if strings.TrimSpace(config.APIKeyEnv) == "" {
		config.APIKeyEnv = "OPENAI_API_KEY"
	}
	return &CodexRunner{config: config}
}

func (r *CodexRunner) RunAgent(ctx context.Context, run AgentRunRequest) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

	if strings.TrimSpace(r.config.BaseURL) != "" {
		return r.runResponsesAPI(runCtx, run)
	}

	outputFile, err := os.CreateTemp("", "tinyclaw-codex-output-*.txt")
	if err != nil {
		return "", fmt.Errorf("create codex output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	args := []string{
		"-a", "never",
		"exec",
		"--skip-git-repo-check",
		"--ephemeral",
		"--cd", r.config.WorkDir,
		"--sandbox", r.config.Sandbox,
		"--output-last-message", outputPath,
		"-",
	}
	if strings.TrimSpace(r.config.Model) != "" {
		args = append([]string{"-a", "never", "exec", "--model", r.config.Model}, args[3:]...)
	}

	cmd := exec.CommandContext(runCtx, r.config.Bin, args...)
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
		return "", fmt.Errorf("codex exec failed: %s", detail)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("read codex output: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		text = strings.TrimSpace(combined.String())
	}
	return text, nil
}

func (r *CodexRunner) runResponsesAPI(ctx context.Context, run AgentRunRequest) (string, error) {
	endpoint, err := responsesEndpoint(r.config.BaseURL)
	if err != nil {
		return "", err
	}
	apiKey := strings.TrimSpace(os.Getenv(r.config.APIKeyEnv))
	if apiKey == "" {
		return "", fmt.Errorf("%s is required for CODEX_BASE_URL", r.config.APIKeyEnv)
	}
	model := strings.TrimSpace(r.config.Model)
	if model == "" {
		model = "gpt-5.5"
	}

	body, err := json.Marshal(map[string]any{
		"model":  model,
		"input":  BuildCodexPrompt(run),
		"stream": false,
	})
	if err != nil {
		return "", fmt.Errorf("encode codex responses request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create codex responses request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call codex responses api: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read codex responses api: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("codex responses api status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	text, err := extractResponsesText(data)
	if err != nil {
		return "", err
	}
	return text, nil
}

func responsesEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse CODEX_BASE_URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("CODEX_BASE_URL must be an absolute URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/responses"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func extractResponsesText(data []byte) (string, error) {
	var parsed struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("decode codex responses api: %w", err)
	}
	var parts []string
	for _, output := range parsed.Output {
		for _, content := range output.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, strings.TrimSpace(content.Text))
			}
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return "", fmt.Errorf("codex responses api returned empty output")
	}
	return text, nil
}

func BuildCodexPrompt(run AgentRunRequest) string {
	var builder strings.Builder
	builder.WriteString("You are Codex, running as the TinyClaw agent runner. Answer the latest user request for this room.\n")
	builder.WriteString("Do not claim to be Kiro, Claude, ChatGPT, or any other assistant identity. If identity is relevant, say you are Codex.\n")
	builder.WriteString("Return only the message text that should be sent back to the room.\n\n")
	builder.WriteString("Agent Session ID: ")
	builder.WriteString(fmt.Sprintf("%d", run.AgentRun.AgentSessionID))
	builder.WriteString("\nRoom ID: ")
	builder.WriteString(fmt.Sprintf("%d", run.AgentRun.RoomID))
	builder.WriteString("\nMessage Window: ")
	builder.WriteString(fmt.Sprintf("(%d, %d]", run.AgentRun.SourceMessageAfterID, run.AgentRun.SourceMessageUntilID))
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
