package command

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"tinyclaw/internal/core"
)

const (
	defaultMediaFetchBaseURL = "http://127.0.0.1:8081"
	maxSourceImageBytes      = 32 << 20
)

type HTTPMediaFetcher struct {
	BaseURL    string
	HTTPClient *http.Client
	MaxBytes   int64
}

func (f HTTPMediaFetcher) FetchMessageMedia(ctx context.Context, message core.Message) (SourceImage, error) {
	if message.ID <= 0 {
		return SourceImage{}, fmt.Errorf("message id is required")
	}
	endpoint, err := internalMediaURL(f.BaseURL, message.ID)
	if err != nil {
		return SourceImage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return SourceImage{}, fmt.Errorf("create media request: %w", err)
	}
	client := f.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return SourceImage{}, fmt.Errorf("fetch media: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return SourceImage{}, fmt.Errorf("fetch media status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	limit := f.MaxBytes
	if limit <= 0 {
		limit = maxSourceImageBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return SourceImage{}, fmt.Errorf("read media: %w", err)
	}
	if int64(len(data)) > limit {
		return SourceImage{}, fmt.Errorf("media exceeds %d bytes", limit)
	}
	mimeType := mediaContentType(resp.Header.Get("Content-Type"))
	return SourceImage{
		Bytes:    data,
		MIMEType: mimeType,
		Filename: "message-" + strconv.FormatInt(message.ID, 10) + mediaExtension(mimeType),
	}, nil
}

func internalMediaURL(baseURL string, messageID int64) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultMediaFetchBaseURL
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("parse media base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("media base url must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/internal/media"
	query := parsed.Query()
	query.Set("msgid", strconv.FormatInt(messageID, 10))
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

func mediaContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

func mediaExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		ext := filepath.Ext(strings.TrimSpace(mimeType))
		if ext != "" && !strings.Contains(ext, "/") {
			return ext
		}
		return ".img"
	}
}
