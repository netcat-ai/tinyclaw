package core

import (
	"encoding/json"
	"testing"
)

func TestEvaluateTriggerPolicyUsesMentionsAndKeywords(t *testing.T) {
	policy := json.RawMessage(`{
		"mode": "mentions_or_keywords",
		"mentions": ["小爪"],
		"keywords": ["/ask", "虾虾"]
	}`)

	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "mention", text: "小爪 帮我看看", want: true},
		{name: "keyword", text: "/ask hello", want: true},
		{name: "keyword in middle", text: "我想问虾虾一个问题", want: true},
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

func TestEvaluateTriggerPolicyIgnoresConfiguredSender(t *testing.T) {
	got, ok := EvaluateTriggerPolicy(json.RawMessage(`{
		"mentions": ["@私云虾虾"],
		"keywords": ["虾虾"],
		"ignored_senders": ["私云虾虾"]
	}`), RoomChatTypeGroup, CreateMessageInput{
		FromID:  "私云虾虾",
		Payload: json.RawMessage(`{"type":"text","text":"虾虾已经回复了"}`),
	})
	if !ok {
		t.Fatal("policy was not evaluated")
	}
	if got {
		t.Fatal("trigger = true, want false")
	}
}

func TestEvaluateTriggerPolicyStillTriggersOtherSenders(t *testing.T) {
	got, ok := EvaluateTriggerPolicy(json.RawMessage(`{
		"keywords": ["虾虾"],
		"ignored_senders": ["私云虾虾"]
	}`), RoomChatTypeGroup, CreateMessageInput{
		FromID:  "fish",
		Payload: json.RawMessage(`{"type":"text","text":"虾虾出来一下"}`),
	})
	if !ok {
		t.Fatal("policy was not evaluated")
	}
	if !got {
		t.Fatal("trigger = false, want true")
	}
}

func TestEvaluateTriggerPolicyTreatsBatchAsExplicitPolicy(t *testing.T) {
	got, ok := EvaluateTriggerPolicy(json.RawMessage(`{
		"batch": {"enabled": true, "min_messages": 3}
	}`), RoomChatTypeGroup, CreateMessageInput{
		Payload: json.RawMessage(`{"type":"text","text":"普通聊天"}`),
	})
	if !ok {
		t.Fatal("policy was not evaluated")
	}
	if got {
		t.Fatal("trigger = true, want false for immediate decision")
	}
}

func TestEvaluateBatchTriggerPolicyIgnoresModeNever(t *testing.T) {
	_, ok := EvaluateBatchTriggerPolicy(json.RawMessage(`{
		"mode": "never",
		"batch": {"enabled": true, "min_messages": 3}
	}`), RoomChatTypeGroup, CreateMessageInput{
		Payload: json.RawMessage(`{"type":"text","text":"普通聊天"}`),
	})
	if ok {
		t.Fatal("batch policy ok = true, want false")
	}
}

func mustJSONString(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}
