package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"tinyclaw/worktool"
)

const (
	maxDeliveryAttempts = 5
)

type deliveryStore interface {
	ClaimNextDelivery(ctx context.Context, lease time.Duration) (*Delivery, error)
	MarkDeliverySent(ctx context.Context, id int64) error
	MarkDeliveryRetry(ctx context.Context, id int64, backoff time.Duration, errText string) error
	MarkDeliveryFailed(ctx context.Context, id int64, errText string) error
}

// EgressConsumer reads pending outbox rows from PostgreSQL and sends them via WorkTool.
type EgressConsumer struct {
	store        deliveryStore
	worktool     *worktool.Client
	pollInterval time.Duration
}

func NewEgressConsumer(store deliveryStore, wt *worktool.Client) *EgressConsumer {
	return &EgressConsumer{
		store:        store,
		worktool:     wt,
		pollInterval: 500 * time.Millisecond,
	}
}

// Run blocks until ctx is cancelled, consuming pending outbox deliveries.
func (c *EgressConsumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		delivery, err := c.store.ClaimNextDelivery(ctx, defaultDeliveryLease)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("egress claim delivery failed", "err", err)
			time.Sleep(time.Second)
			continue
		}
		if delivery == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(c.pollInterval):
				continue
			}
		}

		if err := c.processDelivery(ctx, delivery); err != nil {
			slog.Error("egress process delivery failed", "delivery_id", delivery.ID, "room_id", delivery.RoomID, "err", err)
		}
	}
}

func (c *EgressConsumer) processDelivery(ctx context.Context, delivery *Delivery) error {
	if delivery.RoomID == "" || delivery.TargetName == "" || delivery.Content == "" {
		errText := fmt.Sprintf("invalid delivery payload: room_id=%q target=%q content_len=%d", delivery.RoomID, delivery.TargetName, len(delivery.Content))
		if err := c.store.MarkDeliveryFailed(ctx, delivery.ID, errText); err != nil {
			return err
		}
		return nil
	}

	if err := c.worktool.SendTextMessage(delivery.TargetName, delivery.Content, nil); err != nil {
		if delivery.AttemptCount >= maxDeliveryAttempts {
			return c.store.MarkDeliveryFailed(ctx, delivery.ID, err.Error())
		}
		return c.store.MarkDeliveryRetry(ctx, delivery.ID, defaultDeliveryBackoff, err.Error())
	}

	if err := c.store.MarkDeliverySent(ctx, delivery.ID); err != nil {
		return err
	}
	slog.Info("egress sent", "room_id", delivery.RoomID, "target", delivery.TargetName, "delivery_id", delivery.ID)
	return nil
}
