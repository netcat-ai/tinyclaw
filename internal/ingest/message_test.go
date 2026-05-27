package ingest

import (
	"context"
	"encoding/json"
	"testing"

	"tinyclaw/internal/core"
)

type fakeMessageStore struct {
	input     core.CreateMessageInput
	result    core.CreateMessageResult
	duplicate bool
}

func (s *fakeMessageStore) CreateMessage(_ context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	s.input = input
	if s.result.Message.ID == 0 {
		s.result = core.CreateMessageResult{
			Message: core.Message{
				ID:      10,
				RoomID:  input.RoomID,
				Payload: input.Payload,
			},
			Duplicate: s.duplicate,
		}
	}
	return s.result, nil
}

type fakeCommandHandler struct {
	calls   int
	message core.Message
}

func (h *fakeCommandHandler) HandleMessage(_ context.Context, message core.Message) bool {
	h.calls++
	h.message = message
	return true
}

func TestMessageIngestorSuppressesAgentTriggerAndHandlesDrawCommand(t *testing.T) {
	store := &fakeMessageStore{}
	commands := &fakeCommandHandler{}
	ingestor := NewMessageIngestor(store, commands)

	result, err := ingestor.IngestMessage(context.Background(), core.CreateMessageInput{
		RoomID:  1,
		Payload: json.RawMessage(`{"type":"text","text":"/draw 一朵花"}`),
	})
	if err != nil {
		t.Fatalf("ingest message: %v", err)
	}
	if !store.input.SuppressAgentTrigger {
		t.Fatal("SuppressAgentTrigger = false, want true")
	}
	var payload struct {
		CommandKind string `json:"command_kind"`
	}
	if err := json.Unmarshal(store.input.Payload, &payload); err != nil {
		t.Fatalf("decode stored payload: %v", err)
	}
	if payload.CommandKind != "draw" {
		t.Fatalf("command kind = %q, want draw", payload.CommandKind)
	}
	if result.Triggered {
		t.Fatal("Triggered = true, want false")
	}
	if commands.calls != 1 || commands.message.ID != result.Message.ID {
		t.Fatalf("command calls=%d message=%+v, want one call for message %d", commands.calls, commands.message, result.Message.ID)
	}
}

func TestMessageIngestorDoesNotHandleDuplicateMessages(t *testing.T) {
	tests := []struct {
		name      string
		input     core.CreateMessageInput
		duplicate bool
	}{
		{
			name: "duplicate",
			input: core.CreateMessageInput{
				RoomID:  1,
				Payload: json.RawMessage(`{"type":"text","text":"/draw 一朵花"}`),
			},
			duplicate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeMessageStore{duplicate: tt.duplicate}
			commands := &fakeCommandHandler{}
			ingestor := NewMessageIngestor(store, commands)

			if _, err := ingestor.IngestMessage(context.Background(), tt.input); err != nil {
				t.Fatalf("ingest message: %v", err)
			}
			if commands.calls != 0 {
				t.Fatalf("command calls=%d, want 0", commands.calls)
			}
		})
	}
}
