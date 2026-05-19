package main

import (
	"encoding/json"
	"strings"
)

func shouldTriggerCoreMessage(room CoreRoom, input InboundMessageInput) bool {
	if decision, ok := evaluateTriggerPolicy(room.TriggerPolicy, input); ok {
		return decision
	}
	if input.ChannelRoomType == roomChatTypeDirect {
		return true
	}
	var payload struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(input.Payload, &payload)
	text := strings.TrimSpace(payload.Text)
	return strings.Contains(text, "@agent") || strings.Contains(text, "/ask")
}

func evaluateTriggerPolicy(policy json.RawMessage, input InboundMessageInput) (bool, bool) {
	if len(policy) == 0 {
		return false, false
	}
	var parsed struct {
		Mode          string   `json:"mode"`
		Mentions      []string `json:"mentions"`
		Keywords      []string `json:"keywords"`
		DirectDefault *bool    `json:"direct_default"`
	}
	if err := json.Unmarshal(policy, &parsed); err != nil {
		return false, false
	}
	if input.ChannelRoomType == roomChatTypeDirect && parsed.DirectDefault != nil {
		return *parsed.DirectDefault, true
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Mode)) {
	case "always":
		return true, true
	case "never":
		return false, true
	}
	var payload struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(input.Payload, &payload)
	text := payload.Text
	for _, mention := range parsed.Mentions {
		if mention = strings.TrimSpace(mention); mention != "" && strings.Contains(text, mention) {
			return true, true
		}
	}
	for _, keyword := range parsed.Keywords {
		if keyword = strings.TrimSpace(keyword); keyword != "" && strings.Contains(text, keyword) {
			return true, true
		}
	}
	if len(parsed.Mentions) > 0 || len(parsed.Keywords) > 0 || parsed.DirectDefault != nil || parsed.Mode != "" {
		return false, true
	}
	return false, false
}
