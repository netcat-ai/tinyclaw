package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tinyclaw/internal/core"
)

func (r *CodexRunner) RunBackgroundCodexTask(ctx context.Context, run AgentRunRequest, task core.BackgroundCodexTask) (core.BackgroundCodexTaskResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(run.MediaBaseURL) == "" {
		run.MediaBaseURL = r.config.MediaBaseURL
	}
	if strings.TrimSpace(task.Instruction) == "" {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("background task instruction is required")
	}

	outputFile, err := os.CreateTemp("", "tinyclaw-codex-background-output-*.txt")
	if err != nil {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("create background codex output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer func() { _ = os.Remove(outputPath) }()

	schemaPath, cleanupSchema, err := writeBackgroundCodexTaskResultSchema()
	if err != nil {
		return core.BackgroundCodexTaskResult{}, err
	}
	defer cleanupSchema()

	taskOutputRoot := codexBackgroundTaskOutputRoot(run)
	if err := os.MkdirAll(taskOutputRoot, 0o755); err != nil {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("create background task output root: %w", err)
	}
	taskOutputDir, err := os.MkdirTemp(taskOutputRoot, "task-*")
	if err != nil {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("create background task output dir: %w", err)
	}

	args := r.codexExecArgs(schemaPath, outputPath, "")
	cmd := exec.CommandContext(ctx, r.config.Bin, args...)
	cmd.Dir = r.config.WorkDir
	cmd.Env = append(os.Environ(),
		"TINYCLAW_MEDIA_BASE_URL="+codexMediaBaseURL(run.MediaBaseURL),
		"TINYCLAW_MEDIA_DOWNLOAD_DIR="+codexMediaDownloadDir(run),
		"TINYCLAW_TASK_OUTPUT_DIR="+taskOutputDir,
	)
	cmd.Stdin = strings.NewReader(BuildCodexBackgroundTaskPrompt(run, task))
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
			detail = events.TurnFailure
		} else if events.LastError != "" {
			detail = events.LastError
		}
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("background codex task failed: %s", detail)
	}
	if events.TurnFailure != "" {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("background codex task failed: %s", events.TurnFailure)
	}
	result, err := readBackgroundCodexTaskResult(outputPath, events, taskOutputDir)
	if err != nil {
		return core.BackgroundCodexTaskResult{}, err
	}
	return result, nil
}

func BuildCodexBackgroundTaskPrompt(run AgentRunRequest, task core.BackgroundCodexTask) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(codexBackgroundTaskPromptText))
	builder.WriteString("\n\nBackground Task (JSON):\n")
	data, _ := json.Marshal(task)
	builder.WriteString(string(data))
	builder.WriteString("\n\nContext messages (JSONL):\n")
	writeCodexPromptContextMessages(&builder, run)
	for _, message := range run.ContextMessages {
		builder.WriteString(formatCodexPromptMessage(message))
		builder.WriteString("\n")
	}
	return builder.String()
}

func readBackgroundCodexTaskResult(outputPath string, events codexEventSummary, outputDir string) (core.BackgroundCodexTaskResult, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("read background codex output: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		text = strings.TrimSpace(events.LastAgentMessage)
	}
	if text == "" {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("background codex output is empty")
	}
	var result core.BackgroundCodexTaskResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return core.BackgroundCodexTaskResult{}, fmt.Errorf("decode background codex output: %w", err)
	}
	result.FinalOutput = strings.TrimSpace(result.FinalOutput)
	result.OutputDir = outputDir
	if err := validateBackgroundArtifacts(&result, outputDir); err != nil {
		return core.BackgroundCodexTaskResult{}, err
	}
	return result, nil
}

func validateBackgroundArtifacts(result *core.BackgroundCodexTaskResult, outputDir string) error {
	if len(result.Artifacts) == 0 {
		return fmt.Errorf("background task produced no artifacts")
	}
	outputAbs, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve background output dir: %w", err)
	}
	for i := range result.Artifacts {
		path := strings.TrimSpace(result.Artifacts[i].Path)
		if path == "" {
			return fmt.Errorf("artifact path is required")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(outputAbs, path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve artifact path: %w", err)
		}
		rel, err := filepath.Rel(outputAbs, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("artifact path must be under task output dir")
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("stat artifact: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("artifact path is a directory")
		}
		result.Artifacts[i].Path = abs
		result.Artifacts[i].MIMEType = strings.TrimSpace(result.Artifacts[i].MIMEType)
	}
	return nil
}

func codexBackgroundTaskOutputRoot(run AgentRunRequest) string {
	return filepath.Join(os.TempDir(), "tinyclaw", "tasks", fmt.Sprintf("%d", run.AgentRun.RoomID))
}

func writeBackgroundCodexTaskResultSchema() (string, func(), error) {
	return writeCodexSchemaTempFile("tinyclaw-background-codex-task-schema-*.json", backgroundCodexTaskResultSchema)
}

const backgroundCodexTaskResultSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "final_output": {
      "type": "string"
    },
    "artifacts": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "path": {
            "type": "string"
          },
          "mime_type": {
            "type": "string"
          }
        },
        "required": ["path", "mime_type"]
      }
    }
  },
  "required": ["final_output", "artifacts"]
}`
