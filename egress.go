package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"tinyclaw/worktool"
)

const (
	targetPrefix    = "wecom:target:"
	egressGroup     = "cg:egress"
	egressConsumer  = "clawman"
	egressMaxRetries = 3
)

// EgressConsumer reads agent replies from per-room egress streams and sends them via WorkTool.
type EgressConsumer struct {
	redis    *redis.Client
	worktool *worktool.Client
	pollInterval time.Duration

	mu       sync.Mutex
	rooms    map[string]bool // registered room IDs
	failures map[string]int  // message ID -> consecutive failure count
}

func NewEgressConsumer(rdb *redis.Client, wt *worktool.Client) *EgressConsumer {
	return &EgressConsumer{
		redis:        rdb,
		worktool:     wt,
		pollInterval: 500 * time.Millisecond,
		rooms:        make(map[string]bool),
		failures:     make(map[string]int),
	}
}

// RegisterRoom ensures the egress stream consumer group exists for a room
// and adds it to the polling set. Safe to call multiple times for the same room.
func (c *EgressConsumer) RegisterRoom(ctx context.Context, roomID string) {
	c.mu.Lock()
	if c.rooms[roomID] {
		c.mu.Unlock()
		return
	}
	c.rooms[roomID] = true
	c.mu.Unlock()

	stream := "stream:o:" + roomID
	err := c.redis.XGroupCreateMkStream(ctx, stream, egressGroup, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		slog.Error("egress group create failed", "stream", stream, "err", err)
	}
}

// Run blocks until ctx is cancelled, consuming from all registered egress streams.
func (c *EgressConsumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		streams := c.buildStreamArgs()
		if len(streams) == 0 {
			// No rooms registered yet, wait a bit
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}

		results, err := c.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    egressGroup,
			Consumer: egressConsumer,
			Streams:  streams,
			Count:    10,
			Block:    c.pollInterval,
		}).Result()
		if err != nil {
			if err == redis.Nil || ctx.Err() != nil {
				continue
			}
			slog.Error("egress xreadgroup failed", "err", err)
			time.Sleep(time.Second)
			continue
		}

		for _, stream := range results {
			for _, msg := range stream.Messages {
				c.processMessage(ctx, stream.Stream, msg)
			}
		}
	}
}

// buildStreamArgs returns ["stream:o:room1", "stream:o:room2", ..., ">", ">", ...]
func (c *EgressConsumer) buildStreamArgs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.rooms) == 0 {
		return nil
	}

	keys := make([]string, 0, len(c.rooms)*2)
	for roomID := range c.rooms {
		keys = append(keys, "stream:o:"+roomID)
	}
	for range c.rooms {
		keys = append(keys, ">")
	}
	return keys
}

func (c *EgressConsumer) processMessage(ctx context.Context, streamKey string, msg redis.XMessage) {
	roomID, _ := msg.Values["room_id"].(string)
	text, _ := msg.Values["text"].(string)

	if roomID == "" || text == "" {
		slog.Warn("egress skip invalid message", "id", msg.ID, "stream", streamKey)
		c.redis.XAck(ctx, streamKey, egressGroup, msg.ID)
		return
	}

	target, err := c.redis.Get(ctx, targetPrefix+roomID).Result()
	if err != nil {
		slog.Error("egress target lookup failed", "room_id", roomID, "err", err)
		c.redis.XAck(ctx, streamKey, egressGroup, msg.ID)
		return
	}

	if err := c.worktool.SendTextMessage(target, text, nil); err != nil {
		c.mu.Lock()
		c.failures[msg.ID]++
		retries := c.failures[msg.ID]
		c.mu.Unlock()

		if retries >= egressMaxRetries {
			slog.Error("egress send failed max retries, dropping", "room_id", roomID, "retries", retries, "err", err)
			c.redis.XAck(ctx, streamKey, egressGroup, msg.ID)
			c.mu.Lock()
			delete(c.failures, msg.ID)
			c.mu.Unlock()
		} else {
			slog.Error("egress send failed", "room_id", roomID, "target", target, "retries", retries, "err", err)
		}
		return
	}

	c.mu.Lock()
	delete(c.failures, msg.ID)
	c.mu.Unlock()
	c.redis.XAck(ctx, streamKey, egressGroup, msg.ID)
	slog.Info("egress sent", "room_id", roomID, "target", target)
}

func isGroupExistsErr(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}
