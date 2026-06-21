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

func TestRegisterRoomPromptUpdatesAndPreservesExistingPrompt(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStorageTestStore(t, ctx)
	suffix := time.Now().UnixNano()
	roomID := fmt.Sprintf("prompt-room-%d", suffix)
	prompt := "Default to short replies."

	result, err := store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-core-test",
		ChannelRoomID:   roomID,
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Core Prompt Test",
		OutboundAlias:   "storage-core-test",
		AgentEnabled:    true,
		TriggerPolicy:   json.RawMessage(`{"mode":"never"}`),
		Prompt:          &prompt,
	})
	if err != nil {
		t.Fatalf("register room with prompt: %v", err)
	}
	if result.Room.Prompt != prompt {
		t.Fatalf("room prompt = %q, want %q", result.Room.Prompt, prompt)
	}

	result, err = store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-core-test",
		ChannelRoomID:   roomID,
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Core Prompt Test Updated",
		OutboundAlias:   "storage-core-test",
		AgentEnabled:    true,
		TriggerPolicy:   json.RawMessage(`{"mode":"never"}`),
	})
	if err != nil {
		t.Fatalf("register room without prompt: %v", err)
	}
	if result.Room.Prompt != prompt {
		t.Fatalf("room prompt after omitted update = %q, want %q", result.Room.Prompt, prompt)
	}

	emptyPrompt := ""
	result, err = store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-core-test",
		ChannelRoomID:   roomID,
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Core Prompt Test Updated",
		OutboundAlias:   "storage-core-test",
		AgentEnabled:    true,
		TriggerPolicy:   json.RawMessage(`{"mode":"never"}`),
		Prompt:          &emptyPrompt,
	})
	if err != nil {
		t.Fatalf("clear room prompt: %v", err)
	}
	if result.Room.Prompt != "" {
		t.Fatalf("room prompt after clear = %q, want empty", result.Room.Prompt)
	}
}

func TestBatchTriggerPolicyTriggersAfterMessageThreshold(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStorageTestStore(t, ctx)
	suffix := time.Now().UnixNano()

	roomResult, err := store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-core-test",
		ChannelRoomID:   fmt.Sprintf("batch-room-%d", suffix),
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Core Batch Test",
		OutboundAlias:   "storage-core-test",
		AgentEnabled:    true,
		TriggerPolicy:   json.RawMessage(`{"batch":{"enabled":true,"min_messages":3}}`),
	})
	if err != nil {
		t.Fatalf("register room: %v", err)
	}

	for i := 1; i <= 3; i++ {
		result, err := store.CreateMessage(ctx, core.CreateMessageInput{
			RoomID:    roomResult.Room.ID,
			Source:    "storage-core-test",
			MsgID:     fmt.Sprintf("batch-msg-%d-%d", i, suffix),
			Action:    "send",
			FromID:    "alice",
			MsgTime:   time.Now().UTC().Unix(),
			MsgType:   "text",
			Body:      json.RawMessage(fmt.Sprintf(`{"content":"ordinary message %d"}`, i)),
			ToList:    []string{"tinyclaw"},
			RoomIDRaw: "room",
		})
		if err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
		wantTriggered := i == 3
		if result.Triggered != wantTriggered {
			t.Fatalf("message %d triggered = %v, want %v", i, result.Triggered, wantTriggered)
		}
	}

	run, ok, err := store.ClaimNextAgentRun(ctx, "test-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if !ok {
		t.Fatal("claim run ok = false, want true")
	}
	if run.SourceMessageFromID <= 0 || run.SourceMessageToID <= run.SourceMessageFromID {
		t.Fatalf("run window = [%d,%d], want batched window", run.SourceMessageFromID, run.SourceMessageToID)
	}
}

func TestLatestImageMessageBeforeReturnsPreviousImage(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStorageTestStore(t, ctx)
	suffix := time.Now().UnixNano()

	roomResult, err := store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-core-test",
		ChannelRoomID:   fmt.Sprintf("image-room-%d", suffix),
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Core Image Test",
		OutboundAlias:   "storage-core-test",
		AgentEnabled:    false,
		TriggerPolicy:   json.RawMessage(`{"mode":"always"}`),
	})
	if err != nil {
		t.Fatalf("register room: %v", err)
	}

	firstImage, err := store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:    roomResult.Room.ID,
		Source:    "storage-core-test",
		MsgID:     fmt.Sprintf("image-1-%d", suffix),
		Action:    "send",
		FromID:    "alice",
		MsgTime:   time.Now().UTC().Unix(),
		MsgType:   "image",
		Body:      json.RawMessage(`{"content":"[图片]"}`),
		ToList:    []string{"tinyclaw"},
		RoomIDRaw: "room",
	})
	if err != nil {
		t.Fatalf("create image message: %v", err)
	}
	textMessage, err := store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:    roomResult.Room.ID,
		Source:    "storage-core-test",
		MsgID:     fmt.Sprintf("text-%d", suffix),
		Action:    "send",
		FromID:    "bob",
		MsgTime:   time.Now().UTC().Unix(),
		MsgType:   "text",
		Body:      json.RawMessage(`{"content":"把上图改成水彩"}`),
		ToList:    []string{"tinyclaw"},
		RoomIDRaw: "room",
	})
	if err != nil {
		t.Fatalf("create text message: %v", err)
	}
	_, err = store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:    roomResult.Room.ID,
		Source:    "storage-core-test",
		MsgID:     fmt.Sprintf("image-2-%d", suffix),
		Action:    "send",
		FromID:    "carol",
		MsgTime:   time.Now().UTC().Unix(),
		MsgType:   "image",
		Body:      json.RawMessage(`{"content":"[图片]"}`),
		ToList:    []string{"tinyclaw"},
		RoomIDRaw: "room",
	})
	if err != nil {
		t.Fatalf("create later image message: %v", err)
	}

	got, err := store.LatestImageMessageBefore(ctx, roomResult.Room.ID, textMessage.Message.ID)
	if err != nil {
		t.Fatalf("latest image before: %v", err)
	}
	if got.ID != firstImage.Message.ID {
		t.Fatalf("latest image id = %d, want %d", got.ID, firstImage.Message.ID)
	}
}

func TestDeliveryGeneratedMediaPayload(t *testing.T) {
	payload := deliveryGeneratedMediaPayload(core.Room{
		Channel:       "wechat",
		ChannelRoomID: "room-1",
		DisplayName:   "测试群",
	}, "agent_output", core.GeneratedMediaOutput{
		MediaID:      "gm_test",
		MediaURL:     "https://media.example/gm_test.png",
		MediaURLKind: "presigned_s3",
		MIMEType:     "image/png",
		ExpiresAt:    time.Unix(1700000000, 0).UTC(),
	})
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got["kind"] != "agent_output" || got["type"] != "image" || got["media_id"] != "gm_test" {
		t.Fatalf("payload media fields = %+v", got)
	}
	if got["app"] != "wechat" || got["channel_room_id"] != "room-1" || got["recipient_alias"] != "测试群" {
		t.Fatalf("payload route fields = %+v", got)
	}
}
