package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultBaseURL = "https://qyapi.weixin.qq.com"

var baseURL = defaultBaseURL

// SetBaseURL overrides the API base URL for tests.
func SetBaseURL(u string) { baseURL = strings.TrimRight(u, "/") }

// ResetBaseURL restores the default API base URL.
func ResetBaseURL() { baseURL = defaultBaseURL }

// APIRes is the common error envelope for WeCom API responses.
type APIRes struct {
	Errcode int    `json:"errcode"`
	Errmsg  string `json:"errmsg"`
}

func (r *APIRes) Error() error {
	if r.Errcode != 0 {
		return &APIError{Code: r.Errcode, Msg: r.Errmsg}
	}
	return nil
}

// APIError is a structured WeCom API error.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("wecom api error %d: %s", e.Code, e.Msg)
}

// Client is a minimal WeCom API client with automatic token management.
type Client struct {
	corpID string
	secret string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func NewClient(corpID, secret string) *Client {
	return &Client{corpID: corpID, secret: secret}
}

// GetAccessToken returns a valid access token, refreshing if expired.
// Uses double-checked locking to avoid redundant refreshes.
func (c *Client) GetAccessToken() (string, error) {
	if time.Now().Before(c.expiresAt) {
		return c.accessToken, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expiresAt) {
		return c.accessToken, nil
	}

	url := baseURL + "/cgi-bin/gettoken?corpid=" + c.corpID + "&corpsecret=" + c.secret
	res := struct {
		APIRes
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}{}
	if err := doHTTP(context.Background(), http.MethodGet, url, nil, &res); err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	if err := res.Error(); err != nil {
		return "", err
	}
	c.accessToken = res.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
	return c.accessToken, nil
}

func (c *Client) genURL(path string, args ...string) (string, error) {
	token, err := c.GetAccessToken()
	if err != nil {
		return "", err
	}
	u := baseURL + path + "?access_token=" + token
	if len(args) > 0 {
		u += "&" + strings.Join(args, "&")
	}
	return u, nil
}

// Get performs a GET request to the given WeCom API path.
func (c *Client) Get(ctx context.Context, path string, result any, args ...string) error {
	u, err := c.genURL(path, args...)
	if err != nil {
		return err
	}
	return doHTTP(ctx, http.MethodGet, u, nil, result)
}

// Post performs a POST request to the given WeCom API path.
func (c *Client) Post(ctx context.Context, path string, req, result any) error {
	u, err := c.genURL(path)
	if err != nil {
		return err
	}
	return doHTTP(ctx, http.MethodPost, u, req, result)
}

func doHTTP(ctx context.Context, method, url string, req, res any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	var body io.Reader
	if req != nil {
		b, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	r, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
	}
	if res != nil {
		if err := json.NewDecoder(resp.Body).Decode(res); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
