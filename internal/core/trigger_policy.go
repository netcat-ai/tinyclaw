package core

import (
	"encoding/json"
	"strings"
)

func ShouldTriggerMessage(room Room, session AgentSession, input CreateMessageInput) bool {
	if decision, ok := EvaluateTriggerPolicy(session.TriggerPolicy, room.ChannelRoomType, input); ok {
		return decision
	}
	if room.ChannelRoomType == RoomChatTypeDirect {
		return true
	}
	var payload struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(input.Payload, &payload)
	text := strings.TrimSpace(payload.Text)
	return strings.Contains(text, "虾虾") || strings.Contains(text, "@agent") || strings.Contains(text, "/ask")
}

func EvaluateTriggerPolicy(policy json.RawMessage, channelRoomType string, input CreateMessageInput) (bool, bool) {
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
	if channelRoomType == RoomChatTypeDirect && parsed.DirectDefault != nil {
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
