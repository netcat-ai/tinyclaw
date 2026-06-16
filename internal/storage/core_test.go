package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

func TestAgentRunDoesNotExpandWindowWhenNewMessageArrivesDuringRun(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStorageTestStore(t, ctx)
	suffix := time.Now().UnixNano()

	roomResult, err := store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-core-test",
		ChannelRoomID:   fmt.Sprintf("room-%d", suffix),
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Core Test",
		OutboundAlias:   "storage-core-test",
		AgentEnabled:    true,
		TriggerPolicy:   json.RawMessage(`{"mode":"always"}`),
	})
	if err != nil {
		t.Fatalf("register room: %v", err)
	}

	firstMessage, err := store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:    roomResult.Room.ID,
		Source:    "storage-core-test",
		MsgID:     fmt.Sprintf("msg-1-%d", suffix),
		Action:    "send",
		FromID:    "alice",
		MsgTime:   time.Now().UTC().Unix(),
		MsgType:   "text",
		Body:      json.RawMessage(`{"content":"first"}`),
		ToList:    []string{"tinyclaw"},
		RoomIDRaw: "room",
	})
	if err != nil {
		t.Fatalf("create first message: %v", err)
	}
	if !firstMessage.Triggered {
		t.Fatal("first message triggered = false, want true")
	}

	firstRun, ok, err := store.ClaimNextAgentRun(ctx, "test-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim first run: %v", err)
	}
	if !ok {
		t.Fatal("claim first run ok = false, want true")
	}
	if firstRun.SourceMessageToID != firstMessage.Message.ID {
		t.Fatalf("first run source to = %d, want %d", firstRun.SourceMessageToID, firstMessage.Message.ID)
	}

	secondMessage, err := store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:    roomResult.Room.ID,
		Source:    "storage-core-test",
		MsgID:     fmt.Sprintf("msg-2-%d", suffix),
		Action:    "send",
		FromID:    "bob",
		MsgTime:   time.Now().UTC().Unix(),
		MsgType:   "text",
		Body:      json.RawMessage(`{"content":"second"}`),
		ToList:    []string{"tinyclaw"},
		RoomIDRaw: "room",
	})
	if err != nil {
		t.Fatalf("create second message: %v", err)
	}
	if !secondMessage.Triggered {
		t.Fatal("second message triggered = false, want true")
	}

	if _, err := store.CompleteAgentRun(ctx, firstRun, core.AgentRunResult{FinalOutput: ""}); err != nil {
		t.Fatalf("complete first run: %v", err)
	}
	secondRun, ok, err := store.ClaimNextAgentRun(ctx, "test-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim second run: %v", err)
	}
	if !ok {
		t.Fatal("claim second run ok = false, want true")
	}
	if secondRun.SourceMessageFromID != secondMessage.Message.ID || secondRun.SourceMessageToID != secondMessage.Message.ID {
		t.Fatalf("second run window = [%d,%d], want second message %d", secondRun.SourceMessageFromID, secondRun.SourceMessageToID, secondMessage.Message.ID)
	}
}
