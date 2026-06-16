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
	text := strings.TrimSpace(MessageInputText(input))
	return strings.Contains(text, "虾虾") || strings.Contains(text, "@agent") || strings.Contains(text, "/ask")
}

func EvaluateTriggerPolicy(policy json.RawMessage, channelRoomType string, input CreateMessageInput) (bool, bool) {
	if len(policy) == 0 {
		return false, false
	}
	var parsed struct {
		Mode           string   `json:"mode"`
		Mentions       []string `json:"mentions"`
		Keywords       []string `json:"keywords"`
		IgnoredSenders []string `json:"ignored_senders"`
		DirectDefault  *bool    `json:"direct_default"`
	}
	if err := json.Unmarshal(policy, &parsed); err != nil {
		return false, false
	}
	if matchesPolicyValue(input.FromID, parsed.IgnoredSenders) {
		return false, true
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
	text := MessageInputText(input)
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

func matchesPolicyValue(value string, candidates []string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, candidate := range candidates {
		if value == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

func MessageInputText(input CreateMessageInput) string {
	if text := rawMessageText(input.Body); text != "" {
		return text
	}
	return rawMessageText(input.Payload)
}

func rawMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Content string `json:"content"`
		Text    any    `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	if text := strings.TrimSpace(parsed.Content); text != "" {
		return text
	}
	switch value := parsed.Text.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		if content, ok := value["content"].(string); ok {
			return strings.TrimSpace(content)
		}
	}
	return ""
}
