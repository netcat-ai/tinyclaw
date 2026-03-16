package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultSandboxServerPort = 8888

type RouterConfig struct {
	BaseURL    string
	Namespace  string
	ServerPort int
}

type RouterClient struct {
	baseURL    string
	namespace  string
	serverPort int
	httpClient *http.Client
}

type ChatRequest struct {
	MsgID    string `json:"msgid"`
	RoomID   string `json:"room_id"`
	TenantID string `json:"tenant_id"`
	ChatType string `json:"chat_type"`
	Text     string `json:"text"`
}

type ChatResponse struct {
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func NewRouterClient(httpClient *http.Client, cfg RouterConfig) *RouterClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.ServerPort <= 0 {
		cfg.ServerPort = defaultSandboxServerPort
	}

	return &RouterClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		namespace:  cfg.Namespace,
		serverPort: cfg.ServerPort,
		httpClient: httpClient,
	}
}

func (c *RouterClient) Invoke(ctx context.Context, sandboxID string, req ChatRequest) (ChatResponse, error) {
	if sandboxID == "" {
		return ChatResponse{}, fmt.Errorf("sandboxID is required")
	}
	if c.baseURL == "" {
		return ChatResponse{}, fmt.Errorf("router base URL is required")
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/v1/chat",
		bytes.NewReader(payload),
	)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build router request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Sandbox-ID", sandboxID)
	httpReq.Header.Set("X-Sandbox-Namespace", c.namespace)
	httpReq.Header.Set("X-Sandbox-Port", fmt.Sprintf("%d", c.serverPort))

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call sandbox router: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read sandbox response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ChatResponse{}, fmt.Errorf("sandbox router returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result ChatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return ChatResponse{}, fmt.Errorf("decode sandbox response: %w", err)
	}
	if strings.TrimSpace(result.Text) == "" {
		return ChatResponse{}, fmt.Errorf("sandbox response text is empty")
	}

	return result, nil
}
