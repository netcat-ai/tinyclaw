package ingest

import (
	"context"

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
