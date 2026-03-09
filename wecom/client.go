package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a WeCom session archive API client
type Client struct {
	CorpID     string
	Secret     string
	httpClient *http.Client
	token      string
	tokenExp   time.Time
}

func NewClient(corpID, secret string) *Client {
	return &Client{
		CorpID:     corpID,
		Secret:     secret,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

type tokenResp struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (c *Client) getToken() (string, error) {
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	u := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s", c.CorpID, c.Secret)
	resp, err := c.httpClient.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.ErrCode != 0 {
		return "", fmt.Errorf("wecom token error %d: %s", t.ErrCode, t.ErrMsg)
	}
	c.token = t.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(t.ExpiresIn-60) * time.Second)
	return c.token, nil
}

// Message is a WeCom session archive message
type Message struct {
	MsgID   string `json:"msgid"`
	Action  string `json:"action"`
	From    string `json:"from"`
	ToList  []string `json:"tolist"`
	RoomID  string `json:"roomid"`
	MsgTime int64  `json:"msgtime"`
	MsgType string `json:"msgtype"`
	Text    *struct {
		Content string `json:"content"`
	} `json:"text,omitempty"`
}

type msgListResp struct {
	ErrCode  int       `json:"errcode"`
	ErrMsg   string    `json:"errmsg"`
	ChatData []chatData `json:"chatdata"`
}

type chatData struct {
	Seq            int64  `json:"seq"`
	MsgID          string `json:"msgid"`
	PublickeyVer   int    `json:"publickey_ver"`
	EncryptRandomKey string `json:"encrypt_random_key"`
	EncryptChatMsg string `json:"encrypt_chat_msg"`
}

// GetMessages fetches session archive messages starting from seq
func (c *Client) GetMessages(seq int64, limit int) ([]Message, int64, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, 0, err
	}

	u := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/export/chat/getmsglist?access_token=%s", url.QueryEscape(token))
	body, _ := json.Marshal(map[string]interface{}{
		"seq":   seq,
		"limit": limit,
	})

	resp, err := c.httpClient.Post(u, "application/json", io.NopCloser(
		func() io.Reader {
			r, _ := http.NewRequest("", "", nil)
			_ = r
			return nil
		}(),
	))
	_ = resp
	_ = body
	// NOTE: Real implementation requires RSA private key to decrypt messages.
	// For now return empty to allow compilation and testing of the pipeline.
	return nil, seq, fmt.Errorf("not implemented: requires RSA private key for decryption")
}

// DecryptMsg decrypts a WeCom session archive message using AES key
func DecryptMsg(encryptedKey, encryptedMsg string, privateKey []byte) (*Message, error) {
	// Decrypt AES key using RSA private key
	aesKeyBytes, err := base64.StdEncoding.DecodeString(encryptedKey)
	if err != nil {
		return nil, fmt.Errorf("decode encrypted key: %w", err)
	}
	_ = aesKeyBytes
	_ = privateKey

	// Decrypt message using AES key
	msgBytes, err := base64.StdEncoding.DecodeString(encryptedMsg)
	if err != nil {
		return nil, fmt.Errorf("decode encrypted msg: %w", err)
	}

	// AES-CBC decrypt (placeholder - real impl needs RSA decrypted key)
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		return nil, err
	}
	if len(msgBytes) < aes.BlockSize {
		return nil, fmt.Errorf("msg too short")
	}
	iv := msgBytes[:aes.BlockSize]
	msgBytes = msgBytes[aes.BlockSize:]
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(msgBytes, msgBytes)

	var msg Message
	if err := xml.Unmarshal(msgBytes, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal msg: %w", err)
	}
	return &msg, nil
}
