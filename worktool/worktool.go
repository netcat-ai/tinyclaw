// Package worktool provides a client for sending messages via the WorkTool API.
package worktool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

var baseURL = "https://api.worktool.ymdyes.cn"

const (
	sendMessageEndpoint = "/wework/sendRawMessage"
	defaultTimeout      = 30 * time.Second
	maxRetries          = 3
)

type Client struct {
	robotID    string
	httpClient *http.Client
}

func NewClient(robotID string) *Client {
	return &Client{
		robotID: robotID,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

type SendMessageRequest struct {
	SocketType int           `json:"socketType"`
	List       []MessageItem `json:"list"`
}

type MessageItem struct {
	Type            int      `json:"type"`
	ChatID          string   `json:"chatId,omitempty"`
	TitleList       []string `json:"titleList"`
	ReceivedContent string   `json:"receivedContent"`
	AtList          []string `json:"atList,omitempty"`
	FileURL         string   `json:"fileUrl,omitempty"`
	FileType        string   `json:"fileType,omitempty"`
	ExtraText       string   `json:"extraText,omitempty"`
}

type SendMessageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (c *Client) SendTextMessage(target string, content string, atList []string) error {
	req := &SendMessageRequest{
		SocketType: 2,
		List: []MessageItem{
			{
				Type:            203,
				TitleList:       []string{target},
				ReceivedContent: content,
				AtList:          atList,
			},
		},
	}
	_, err := c.sendMessage(req)
	return err
}

func (c *Client) SendRawMessage(req *SendMessageRequest) (*SendMessageResponse, error) {
	return c.sendMessage(req)
}

func (c *Client) sendMessage(req *SendMessageRequest) (*SendMessageResponse, error) {
	url := fmt.Sprintf("%s%s?robotId=%s", baseURL, sendMessageEndpoint, c.robotID)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		resp, err := c.httpClient.Post(url, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %w", err)
			slog.Warn("worktool request failed, retrying", "attempt", attempt+1, "error", err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("failed to read response: %w", readErr)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("http error: status code %d", resp.StatusCode)
			continue
		}

		var result SendMessageResponse
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("failed to unmarshal response: %w", err)
			continue
		}

		if result.Code != 0 && result.Code != 200 {
			lastErr = fmt.Errorf("API error: code=%d, message=%s", result.Code, result.Message)
			slog.Warn("worktool API returned error", "code", result.Code, "message", result.Message)
			continue
		}

		target := ""
		if len(req.List) > 0 && len(req.List[0].TitleList) > 0 {
			target = req.List[0].TitleList[0]
		}
		slog.Info("worktool message sent", "target", target)
		return &result, nil
	}

	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}
