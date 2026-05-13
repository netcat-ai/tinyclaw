package main

import (
	"encoding/json"
	"fmt"
)

type wecomPayload struct {
	Text *struct {
		Content string `json:"content"`
	} `json:"text,omitempty"`
	Markdown *struct {
		Content string `json:"content"`
	} `json:"markdown,omitempty"`
	Image *struct {
		URL       string `json:"url"`
		SDKFileID string `json:"sdkfileid"`
	} `json:"image,omitempty"`
	File *struct {
		Name      string `json:"name"`
		SDKFileID string `json:"sdkfileid"`
	} `json:"file,omitempty"`
	MsgType string `json:"msgtype"`
}

func parseWeComPayload(raw string) (wecomPayload, error) {
	if raw == "" {
		return wecomPayload{}, fmt.Errorf("empty raw payload")
	}

	var payload wecomPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return wecomPayload{}, fmt.Errorf("invalid wecom raw payload: %w", err)
	}
	return payload, nil
}

func extractWeComMessageText(raw string) (string, error) {
	payload, err := parseWeComPayload(raw)
	if err != nil {
		return "", err
	}

	switch {
	case payload.Text != nil && payload.Text.Content != "":
		return payload.Text.Content, nil
	case payload.Markdown != nil && payload.Markdown.Content != "":
		return payload.Markdown.Content, nil
	case payload.Image != nil && payload.Image.URL != "":
		return payload.Image.URL, nil
	case payload.File != nil && payload.File.Name != "":
		return payload.File.Name, nil
	case payload.MsgType != "":
		return "[" + payload.MsgType + "]", nil
	default:
		return "", fmt.Errorf("unsupported wecom message payload")
	}
}

func extractWeComImageSDKFileID(raw string) (string, bool, error) {
	payload, err := parseWeComPayload(raw)
	if err != nil {
		return "", false, err
	}
	if payload.MsgType != "image" || payload.Image == nil {
		return "", false, nil
	}
	return payload.Image.SDKFileID, payload.Image.SDKFileID != "", nil
}
