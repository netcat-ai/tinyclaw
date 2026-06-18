package core

import (
	"encoding/json"
	"strings"
)

type BatchTriggerPolicy struct {
	Enabled            bool `json:"enabled"`
	MinIntervalSeconds int  `json:"min_interval_seconds"`
	MinMessages        int  `json:"min_messages"`
	MaxIntervalSeconds int  `json:"max_interval_seconds"`
}

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

// EvaluateTriggerPolicy returns (decision, ok). ok means the policy made an
// explicit immediate-trigger decision; false ok lets caller use defaults.
func EvaluateTriggerPolicy(policy json.RawMessage, channelRoomType string, input CreateMessageInput) (bool, bool) {
	if len(policy) == 0 {
		return false, false
	}
	var parsed struct {
		Mode           string              `json:"mode"`
		Mentions       []string            `json:"mentions"`
		Keywords       []string            `json:"keywords"`
		IgnoredSenders []string            `json:"ignored_senders"`
		DirectDefault  *bool               `json:"direct_default"`
		Batch          *BatchTriggerPolicy `json:"batch"`
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
	if parsed.Batch != nil && parsed.Batch.Enabled {
		return false, true
	}
	return false, false
}

func EvaluateBatchTriggerPolicy(policy json.RawMessage, channelRoomType string, input CreateMessageInput) (BatchTriggerPolicy, bool) {
	if len(policy) == 0 || channelRoomType == RoomChatTypeDirect {
		return BatchTriggerPolicy{}, false
	}
	var parsed struct {
		Mode           string              `json:"mode"`
		IgnoredSenders []string            `json:"ignored_senders"`
		Batch          *BatchTriggerPolicy `json:"batch"`
	}
	if err := json.Unmarshal(policy, &parsed); err != nil {
		return BatchTriggerPolicy{}, false
	}
	if matchesPolicyValue(input.FromID, parsed.IgnoredSenders) {
		return BatchTriggerPolicy{}, false
	}
	if strings.EqualFold(strings.TrimSpace(parsed.Mode), "never") {
		return BatchTriggerPolicy{}, false
	}
	if parsed.Batch == nil || !parsed.Batch.Enabled {
		return BatchTriggerPolicy{}, false
	}
	return *parsed.Batch, true
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
