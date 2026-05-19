package main

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
			got, ok := evaluateTriggerPolicy(policy, InboundMessageInput{
				ChannelRoomType: roomChatTypeGroup,
				Payload:         json.RawMessage(`{"type":"text","text":` + mustJSONString(tc.text) + `}`),
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
	got, ok := evaluateTriggerPolicy(json.RawMessage(`{"direct_default":false}`), InboundMessageInput{
		ChannelRoomType: roomChatTypeDirect,
		Payload:         json.RawMessage(`{"type":"text","text":"hello"}`),
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
