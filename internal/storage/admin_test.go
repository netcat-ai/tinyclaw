package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"tinyclaw/internal/core"
)

func TestAdminStorageQueriesExecuteAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStorageTestStore(t, ctx)
	suffix := time.Now().UnixNano()
	roomPrompt := "Use short room replies."

	roomResult, err := store.RegisterRoom(ctx, core.RegisterRoomInput{
		Channel:         "storage-admin-test",
		ChannelRoomID:   fmt.Sprintf("room-%d", suffix),
		ChannelRoomType: core.RoomChatTypeGroup,
		DisplayName:     "Storage Admin Test",
		OutboundAlias:   "storage-admin-test",
		AgentEnabled:    true,
		TriggerPolicy:   json.RawMessage(`{"mode":"always"}`),
		Prompt:          &roomPrompt,
	})
	if err != nil {
		t.Fatalf("register room: %v", err)
	}

	firstMessage, err := store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:          roomResult.Room.ID,
		SourceMessageID: fmt.Sprintf("msg-1-%d", suffix),
		Source:          "admin-test",
		SenderID:        "tester",
		SenderName:      "Tester",
		MessageTime:     time.Now().UTC().Add(-time.Minute),
		Payload:         json.RawMessage(`{"type":"text","text":"first"}`),
	})
	if err != nil {
		t.Fatalf("create first message: %v", err)
	}
	secondMessage, err := store.CreateMessage(ctx, core.CreateMessageInput{
		RoomID:          roomResult.Room.ID,
		SourceMessageID: fmt.Sprintf("msg-2-%d", suffix),
		Source:          "admin-test",
		SenderID:        "tester",
		SenderName:      "Tester",
		MessageTime:     time.Now().UTC(),
		Payload:         json.RawMessage(`{"type":"text","text":"second"}`),
	})
	if err != nil {
		t.Fatalf("create second message: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO deliveries (room_id, agent_session_id, source_message_from_id, source_message_to_id, payload, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, roomResult.Room.ID, roomResult.AgentSession.ID, firstMessage.Message.ID, secondMessage.Message.ID, json.RawMessage(`{"type":"text","text":"pending"}`), core.DeliveryStatusPending); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO memory_items (
			room_id, type, key, content, status,
			source_message_from_id, source_message_to_id,
			created_by_agent_session_id, updated_by_agent_session_id
		)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $8),
			($1, $9, $10, $11, $12, $6, $7, $8, $8)
	`, roomResult.Room.ID,
		core.MemoryTypeFact, fmt.Sprintf("fact.%d", suffix), "admin room queries execute", core.MemoryStatusActive,
		firstMessage.Message.ID, secondMessage.Message.ID, roomResult.AgentSession.ID,
		core.MemoryTypeTodo, fmt.Sprintf("todo.%d", suffix), "old todo", core.MemoryStatusStale,
	); err != nil {
		t.Fatalf("insert memory items: %v", err)
	}

	rooms, err := store.ListAdminRooms(ctx, 200)
	if err != nil {
		t.Fatalf("list admin rooms: %v", err)
	}
	summary := findAdminRoomSummary(t, rooms, roomResult.Room.ID)
	if summary.PendingDeliveryCount != 1 {
		t.Fatalf("pending delivery count = %d, want 1", summary.PendingDeliveryCount)
	}
	if summary.AgentSession.ID != roomResult.AgentSession.ID || !summary.AgentSession.Enabled {
		t.Fatalf("agent session summary = %+v, want enabled session %d", summary.AgentSession, roomResult.AgentSession.ID)
	}
	if summary.Room.Prompt != roomPrompt {
		t.Fatalf("room prompt = %q, want %q", summary.Room.Prompt, roomPrompt)
	}
	if summary.LastMessageTime.IsZero() {
		t.Fatal("last message time is zero")
	}

	timeline, err := store.GetAdminRoomTimeline(ctx, roomResult.Room.ID, 0, 10)
	if err != nil {
		t.Fatalf("get admin room timeline: %v", err)
	}
	if len(timeline.Messages) != 2 {
		t.Fatalf("timeline messages len = %d, want 2", len(timeline.Messages))
	}
	if timeline.Room.Prompt != roomPrompt {
		t.Fatalf("timeline room prompt = %q, want %q", timeline.Room.Prompt, roomPrompt)
	}
	if timeline.Messages[0].ID != firstMessage.Message.ID || timeline.Messages[1].ID != secondMessage.Message.ID {
		t.Fatalf("timeline message order = [%d,%d], want [%d,%d]", timeline.Messages[0].ID, timeline.Messages[1].ID, firstMessage.Message.ID, secondMessage.Message.ID)
	}
	if len(timeline.Deliveries) != 1 || timeline.Deliveries[0].Status != core.DeliveryStatusPending {
		t.Fatalf("timeline deliveries = %+v, want one pending delivery", timeline.Deliveries)
	}

	activeFacts, err := store.ListAdminRoomMemory(ctx, core.AdminMemoryListInput{
		RoomID: roomResult.Room.ID,
		Status: "active",
		Types:  []string{core.MemoryTypeFact},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list active fact memory: %v", err)
	}
	if len(activeFacts) != 1 || activeFacts[0].Key != fmt.Sprintf("fact.%d", suffix) {
		t.Fatalf("active facts = %+v, want inserted fact", activeFacts)
	}

	allTodos, err := store.ListAdminRoomMemory(ctx, core.AdminMemoryListInput{
		RoomID: roomResult.Room.ID,
		Status: "all",
		Types:  []string{core.MemoryTypeTodo},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list all todo memory: %v", err)
	}
	if len(allTodos) != 1 || allTodos[0].Status != core.MemoryStatusStale {
		t.Fatalf("all todos = %+v, want inserted stale todo", allTodos)
	}
}

func openPostgresStorageTestStore(t *testing.T, ctx context.Context) *CoreStore {
	t.Helper()
	dsn := os.Getenv("STORAGE_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("CORE_E2E_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("STORAGE_TEST_DATABASE_URL or CORE_E2E_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close postgres: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		TRUNCATE
			memory_change_audit,
			memory_write_jobs,
			memory_capability_tokens,
			memory_items,
			deliveries,
			agents,
			agent_sessions,
			messages,
			rooms,
			api_clients
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("reset storage test database: %v", err)
	}
	return NewCoreStore(db)
}

func findAdminRoomSummary(t *testing.T, rooms []core.AdminRoomSummary, roomID int64) core.AdminRoomSummary {
	t.Helper()
	for _, room := range rooms {
		if room.Room.ID == roomID {
			return room
		}
	}
	t.Fatalf("room %d not found in admin summary: %+v", roomID, rooms)
	return core.AdminRoomSummary{}
}
