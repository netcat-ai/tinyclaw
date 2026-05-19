package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

const defaultCodexRunnerTimeout = 5 * time.Minute

type CodexRunnerConfig struct {
	Bin     string
	WorkDir string
	Model   string
	Sandbox string
	Timeout time.Duration
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
	return &CodexRunner{config: config}
}

func (r *CodexRunner) RunInvocation(ctx context.Context, run InvocationRun) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

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

func BuildCodexPrompt(run InvocationRun) string {
	var builder strings.Builder
	builder.WriteString("You are the TinyClaw agent runner. Answer the latest user request for this room.\n")
	builder.WriteString("Return only the message text that should be sent back to the room.\n\n")
	builder.WriteString("Invocation ID: ")
	builder.WriteString(strconv.FormatInt(run.Invocation.ID, 10))
	builder.WriteString("\nRoom ID: ")
	builder.WriteString(strconv.FormatInt(run.Invocation.RoomID, 10))
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
