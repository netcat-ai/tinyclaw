package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

func (r *CodexRunner) runAttachmentObservationRequests(ctx context.Context, run AgentRunRequest, requests []core.AttachmentObservationRequest) []core.AttachmentObservationResult {
	results := make([]core.AttachmentObservationResult, 0, len(requests))
	for _, request := range requests {
		request.MessageID = normalizeAttachmentMessageID(request.MessageID)
		request.MediaType = strings.TrimSpace(request.MediaType)
		request.Purpose = strings.TrimSpace(request.Purpose)
		if request.MessageID <= 0 {
			results = append(results, core.AttachmentObservationResult{
				MediaType: request.MediaType,
				Purpose:   request.Purpose,
				Error:     "message_id is required",
			})
			continue
		}

		started := time.Now()
		slog.Info("attachment observation started",
			"agent_session_id", run.AgentRun.AgentSessionID,
			"room_id", run.AgentRun.RoomID,
			"message_id", request.MessageID,
			"media_type", request.MediaType,
			"purpose", truncateLogValue(request.Purpose, 300),
		)
		result := r.runAttachmentObservation(ctx, run, request)
		duration := time.Since(started)
		if result.Error != "" {
			slog.Warn("attachment observation failed",
				"agent_session_id", run.AgentRun.AgentSessionID,
				"room_id", run.AgentRun.RoomID,
				"message_id", request.MessageID,
				"duration_ms", duration.Milliseconds(),
				"err", result.Error,
			)
		} else {
			slog.Info("attachment observation completed",
				"agent_session_id", run.AgentRun.AgentSessionID,
				"room_id", run.AgentRun.RoomID,
				"message_id", request.MessageID,
				"duration_ms", duration.Milliseconds(),
			)
		}
		results = append(results, result)
	}
	return results
}

func (r *CodexRunner) runAttachmentObservation(ctx context.Context, run AgentRunRequest, request core.AttachmentObservationRequest) core.AttachmentObservationResult {
	if strings.TrimSpace(run.MediaBaseURL) == "" {
		run.MediaBaseURL = r.config.MediaBaseURL
	}
	localPath, contentType, err := downloadCodexAttachment(ctx, run, request.MessageID)
	if err != nil {
		return attachmentObservationError(request, err)
	}
	result, err := r.runCodexAttachmentObservation(ctx, run, request, localPath, contentType)
	if err != nil {
		return attachmentObservationError(request, err)
	}
	return result
}

func (r *CodexRunner) runCodexAttachmentObservation(ctx context.Context, run AgentRunRequest, request core.AttachmentObservationRequest, localPath string, contentType string) (core.AttachmentObservationResult, error) {
	outputFile, err := os.CreateTemp("", "tinyclaw-codex-attachment-observation-output-*.txt")
	if err != nil {
		return core.AttachmentObservationResult{}, fmt.Errorf("create attachment observation output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer func() { _ = os.Remove(outputPath) }()

	schemaPath, cleanupSchema, err := writeAttachmentObservationResultSchema()
	if err != nil {
		return core.AttachmentObservationResult{}, err
	}
	defer cleanupSchema()

	imagePaths := []string{}
	if isCodexImageAttachment(request, contentType) {
		imagePaths = append(imagePaths, localPath)
	}
	args := r.codexExecArgs(schemaPath, outputPath, "", "", imagePaths...)
	cmd := exec.CommandContext(ctx, r.config.Bin, args...)
	cmd.Dir = r.config.WorkDir
	cmd.Env = append(os.Environ(),
		"TINYCLAW_MEDIA_BASE_URL="+codexMediaBaseURL(run.MediaBaseURL),
		"TINYCLAW_MEDIA_DOWNLOAD_DIR="+codexMediaDownloadDir(run),
		"TINYCLAW_ATTACHMENT_PATH="+localPath,
		"TINYCLAW_ATTACHMENT_CONTENT_TYPE="+contentType,
	)
	cmd.Stdin = strings.NewReader(BuildCodexAttachmentObservationPrompt(run, request, localPath, contentType))
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
		return core.AttachmentObservationResult{}, fmt.Errorf("attachment observation codex failed: %s", detail)
	}
	if events.TurnFailure != "" {
		return core.AttachmentObservationResult{}, fmt.Errorf("attachment observation codex failed: %s", events.TurnFailure)
	}
	result, err := readAttachmentObservationResult(outputPath, events)
	if err != nil {
		return core.AttachmentObservationResult{}, err
	}
	result.MessageID = request.MessageID
	result.MediaType = firstNonEmptyString(result.MediaType, request.MediaType, contentType)
	result.Purpose = firstNonEmptyString(result.Purpose, request.Purpose)
	result.Observation = strings.TrimSpace(result.Observation)
	result.Error = strings.TrimSpace(result.Error)
	return result, nil
}

func BuildCodexAttachmentObservationPrompt(run AgentRunRequest, request core.AttachmentObservationRequest, localPath string, contentType string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(codexAttachmentObservationPromptText))
	builder.WriteString("\n\nAttachment Observation Request (JSON):\n")
	data, _ := json.Marshal(map[string]any{
		"message_id":   request.MessageID,
		"media_type":   strings.TrimSpace(request.MediaType),
		"purpose":      strings.TrimSpace(request.Purpose),
		"local_path":   localPath,
		"content_type": contentType,
	})
	builder.WriteString(string(data))
	builder.WriteString("\n\nContext messages (JSONL):\n")
	writeCodexPromptContextMessages(&builder, run)
	for _, message := range run.ContextMessages {
		builder.WriteString(formatCodexPromptMessage(message))
		builder.WriteString("\n")
	}
	return builder.String()
}

func readAttachmentObservationResult(outputPath string, events codexEventSummary) (core.AttachmentObservationResult, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return core.AttachmentObservationResult{}, fmt.Errorf("read attachment observation output: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		text = strings.TrimSpace(events.LastAgentMessage)
	}
	if text == "" {
		return core.AttachmentObservationResult{}, fmt.Errorf("attachment observation output is empty")
	}
	var result core.AttachmentObservationResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return core.AttachmentObservationResult{}, fmt.Errorf("decode attachment observation output: %w", err)
	}
	return result, nil
}

func downloadCodexAttachment(ctx context.Context, run AgentRunRequest, messageID int64) (string, string, error) {
	if messageID <= 0 {
		return "", "", fmt.Errorf("message_id is required")
	}
	dir := codexMediaDownloadDir(run)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create media download dir: %w", err)
	}
	path := filepath.Join(dir, strconv.FormatInt(messageID, 10))
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() > 0 {
		contentType, detectErr := detectLocalFileContentType(path)
		if detectErr != nil {
			return "", "", detectErr
		}
		return path, contentType, nil
	}

	mediaURL, err := codexAttachmentDownloadURL(run, messageID)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create media download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("download media: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return "", "", fmt.Errorf("download media status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	tmp, err := os.CreateTemp(dir, strconv.FormatInt(messageID, 10)+"-*")
	if err != nil {
		return "", "", fmt.Errorf("create media temp file: %w", err)
	}
	tmpPath := tmp.Name()
	written, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxAttachmentDownloadBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("write media file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("close media file: %w", closeErr)
	}
	if written > maxAttachmentDownloadBytes {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("media exceeds %d bytes", maxAttachmentDownloadBytes)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("save media file: %w", err)
	}

	contentType := normalizeContentType(resp.Header.Get("Content-Type"))
	if contentType == "" || contentType == "application/octet-stream" {
		contentType, err = detectLocalFileContentType(path)
		if err != nil {
			return "", "", err
		}
	}
	return path, contentType, nil
}

func codexAttachmentDownloadURL(run AgentRunRequest, messageID int64) (string, error) {
	base := codexMediaBaseURL(run.MediaBaseURL)
	parsed, err := url.Parse(base + "/internal/media")
	if err != nil {
		return "", fmt.Errorf("parse media base url: %w", err)
	}
	values := parsed.Query()
	values.Set("msgid", strconv.FormatInt(messageID, 10))
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func detectLocalFileContentType(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open media file: %w", err)
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read media file header: %w", err)
	}
	return normalizeContentType(http.DetectContentType(buffer[:n])), nil
}

func normalizeContentType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(value)
	}
	return strings.ToLower(strings.TrimSpace(mediaType))
}

func isCodexImageAttachment(request core.AttachmentObservationRequest, contentType string) bool {
	contentType = normalizeContentType(contentType)
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	mediaType := strings.ToLower(strings.TrimSpace(request.MediaType))
	switch mediaType {
	case "image", "图片", "emotion", "表情":
		return true
	default:
		return false
	}
}

func attachmentObservationError(request core.AttachmentObservationRequest, err error) core.AttachmentObservationResult {
	return core.AttachmentObservationResult{
		MessageID: request.MessageID,
		MediaType: request.MediaType,
		Purpose:   request.Purpose,
		Error:     err.Error(),
	}
}

func normalizeAttachmentMessageID(messageID int64) int64 {
	if messageID < 0 {
		return 0
	}
	return messageID
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func writeAttachmentObservationResultSchema() (string, func(), error) {
	return writeCodexSchemaTempFile("tinyclaw-attachment-observation-result-schema-*.json", attachmentObservationResultSchema)
}

const attachmentObservationResultSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "message_id": {
      "type": "integer"
    },
    "media_type": {
      "type": "string"
    },
    "purpose": {
      "type": "string"
    },
    "observation": {
      "type": "string"
    },
    "error": {
      "type": "string"
    }
  },
  "required": ["message_id", "media_type", "purpose", "observation", "error"]
}`
