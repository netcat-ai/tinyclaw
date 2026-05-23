package ingest

import (
	"context"
	"encoding/json"

	"tinyclaw/internal/command"
	"tinyclaw/internal/core"
)

type MessageStore interface {
	CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
}

type CommandHandler interface {
	HandleMessage(ctx context.Context, message core.Message) bool
}

type MessageIngestor struct {
	store    MessageStore
	commands CommandHandler
}

func NewMessageIngestor(store MessageStore, commands CommandHandler) *MessageIngestor {
	return &MessageIngestor{
		store:    store,
		commands: commands,
	}
}

func (i *MessageIngestor) IngestMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	if !input.Skipped && command.IsDrawPayload(input.Payload) {
		input.SuppressAgentTrigger = true
		input.Payload = markCommandPayload(input.Payload, "draw")
	}
	result, err := i.store.CreateMessage(ctx, input)
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	if !result.Duplicate && !result.Message.Skipped && i.commands != nil {
		i.commands.HandleMessage(context.Background(), result.Message)
	}
	return result, nil
}

func markCommandPayload(payload json.RawMessage, kind string) json.RawMessage {
	var values map[string]any
	if err := json.Unmarshal(payload, &values); err != nil {
		return payload
	}
	values["command_kind"] = kind
	data, err := json.Marshal(values)
	if err != nil {
		return payload
	}
	return data
}
