package ingest

import (
	"context"

	"tinyclaw/internal/core"
)

type MessageStore interface {
	CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
}

type MessageIngestor struct {
	store MessageStore
}

func NewMessageIngestor(store MessageStore) *MessageIngestor {
	return &MessageIngestor{
		store: store,
	}
}

func (i *MessageIngestor) IngestMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error) {
	return i.store.CreateMessage(ctx, input)
}
