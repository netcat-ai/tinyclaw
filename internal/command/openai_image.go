package command

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
)

type OpenAIImageClient struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

func (c OpenAIImageClient) GenerateImage(ctx context.Context, input ImageGenerationInput) (GeneratedImage, error) {
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		return GeneratedImage{}, fmt.Errorf("image provider base url is required")
	}
	apiKey := strings.TrimSpace(c.APIKey)
	if apiKey == "" {
		return GeneratedImage{}, fmt.Errorf("image provider api key is required")
	}
	model := strings.TrimSpace(c.Model)
	if model == "" {
		return GeneratedImage{}, fmt.Errorf("image provider model is required")
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return GeneratedImage{}, fmt.Errorf("image prompt is required")
	}
	size := strings.TrimSpace(input.Size)
	if size == "" {
		size = defaultDrawImageSize
	}
	if len(input.SourceImages) > 0 {
		return c.editImage(ctx, baseURL, apiKey, model, prompt, size, input.SourceImages)
	}

	endpoint, err := imageGenerationEndpoint(baseURL)
	if err != nil {
		return GeneratedImage{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model":  model,
		"prompt": prompt,
		"size":   size,
		"n":      1,
	})
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("encode image generation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("create image generation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("call image provider: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("read image provider response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GeneratedImage{}, fmt.Errorf("image provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeGeneratedImage(data)
}

func (c OpenAIImageClient) editImage(ctx context.Context, baseURL string, apiKey string, model string, prompt string, size string, sourceImages []SourceImage) (GeneratedImage, error) {
	endpoint, err := imageEditEndpoint(baseURL)
	if err != nil {
		return GeneratedImage{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"model":  model,
		"prompt": prompt,
		"size":   size,
		"n":      "1",
	} {
		if err := writer.WriteField(key, value); err != nil {
			return GeneratedImage{}, fmt.Errorf("encode image edit field %s: %w", key, err)
		}
	}
	for index, image := range sourceImages {
		if len(image.Bytes) == 0 {
			return GeneratedImage{}, fmt.Errorf("source image %d is empty", index)
		}
		filename := strings.TrimSpace(image.Filename)
		if filename == "" {
			filename = fmt.Sprintf("source-%d%s", index+1, mediaExtension(image.MIMEType))
		}
		filename = filepath.Base(filename)
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image[]"; filename="%s"`, strings.ReplaceAll(filename, `"`, "")))
		if mimeType := strings.TrimSpace(image.MIMEType); mimeType != "" {
			header.Set("Content-Type", mimeType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			return GeneratedImage{}, fmt.Errorf("encode source image %d: %w", index, err)
		}
		if _, err := part.Write(image.Bytes); err != nil {
			return GeneratedImage{}, fmt.Errorf("write source image %d: %w", index, err)
		}
	}
	if err := writer.Close(); err != nil {
		return GeneratedImage{}, fmt.Errorf("finalize image edit request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("create image edit request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("call image provider: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("read image provider response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GeneratedImage{}, fmt.Errorf("image provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeGeneratedImage(data)
}

func decodeGeneratedImage(data []byte) (GeneratedImage, error) {
	var parsed struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return GeneratedImage{}, fmt.Errorf("decode image provider response: %w", err)
	}
	if len(parsed.Data) == 0 || strings.TrimSpace(parsed.Data[0].B64JSON) == "" {
		return GeneratedImage{}, fmt.Errorf("image provider response missing b64_json")
	}
	imageBytes, err := base64.StdEncoding.DecodeString(parsed.Data[0].B64JSON)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("decode image b64_json: %w", err)
	}
	return GeneratedImage{Bytes: imageBytes, MIMEType: "image/png"}, nil
}

func imageGenerationEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("parse image provider base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("image provider base url must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/images/generations"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func imageEditEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("parse image provider base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("image provider base url must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/images/edits"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
