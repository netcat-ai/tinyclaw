package core

import (
	"encoding/json"
	"testing"
)

func TestEvaluateTriggerPolicyUsesMentionsAndKeywords(t *testing.T) {
	policy := json.RawMessage(`{
		"mode": "mentions_or_keywords",
		"mentions": ["小爪"],
		"keywords": ["/ask"]
	}`)

	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "mention", text: "小爪 帮我看看", want: true},
		{name: "keyword", text: "/ask hello", want: true},
		{name: "no match", text: "普通聊天", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := EvaluateTriggerPolicy(policy, RoomChatTypeGroup, CreateMessageInput{
				Payload: json.RawMessage(`{"type":"text","text":` + mustJSONString(tc.text) + `}`),
			})
			if !ok {
				t.Fatal("policy was not evaluated")
			}
			if got != tc.want {
				t.Fatalf("trigger = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvaluateTriggerPolicyUsesDirectDefault(t *testing.T) {
	got, ok := EvaluateTriggerPolicy(json.RawMessage(`{"direct_default":false}`), RoomChatTypeDirect, CreateMessageInput{
		Payload: json.RawMessage(`{"type":"text","text":"hello"}`),
	})
	if !ok {
		t.Fatal("policy was not evaluated")
	}
	if got {
		t.Fatal("trigger = true, want false")
	}
}

func mustJSONString(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}
