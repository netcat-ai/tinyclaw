//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"tinyclaw/worktool"
)

func integrationRedisForEgress(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: cannot connect to Redis at %s: %v", addr, err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func setupEgressConsumer(t *testing.T, wtServer *httptest.Server) (*EgressConsumer, *redis.Client) {
	t.Helper()
	rdb := integrationRedisForEgress(t)

	worktool.SetBaseURL(wtServer.URL)
	t.Cleanup(func() { worktool.ResetBaseURL() })

	wt := worktool.NewClient("test-robot")
	consumer := NewEgressConsumer(rdb, wt)
	consumer.pollInterval = 100 * time.Millisecond
	return consumer, rdb
}

func TestEgress_SendText(t *testing.T) {
	var gotBody worktool.SendMessageRequest
	called := make(chan struct{}, 1)
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(worktool.SendMessageResponse{Code: 200, Message: "ok"})
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	defer wtServer.Close()

	consumer, rdb := setupEgressConsumer(t, wtServer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	roomID := "egress-test-send"
	streamKey := "stream:o:" + roomID
	targetKey := "wecom:target:" + roomID

	// Pre-clean
	rdb.Del(ctx, streamKey, targetKey)
	t.Cleanup(func() {
		rdb.Del(context.Background(), streamKey, targetKey)
	})

	// Set up target mapping
	rdb.Set(ctx, targetKey, "测试群", 0)

	// Register the room so consumer group exists
	consumer.RegisterRoom(ctx, roomID)

	// Run consumer in background
	done := make(chan struct{})
	go func() {
		consumer.Run(ctx)
		close(done)
	}()

	// Add message after consumer is running
	time.Sleep(50 * time.Millisecond)
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]any{
			"room_id": roomID,
			"text":    "hello from agent",
			"msgid":   "1234-0",
		},
	})

	// Wait for WorkTool to be called
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worktool call")
	}

	cancel()
	<-done

	if len(gotBody.List) != 1 {
		t.Fatalf("worktool got %d items, want 1", len(gotBody.List))
	}
	if gotBody.List[0].TitleList[0] != "测试群" {
		t.Errorf("target = %q, want %q", gotBody.List[0].TitleList[0], "测试群")
	}
	if gotBody.List[0].ReceivedContent != "hello from agent" {
		t.Errorf("content = %q, want %q", gotBody.List[0].ReceivedContent, "hello from agent")
	}
}

func TestEgress_TargetNotFound(t *testing.T) {
	wtCalled := make(chan struct{}, 1)
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(worktool.SendMessageResponse{Code: 200, Message: "ok"})
		select {
		case wtCalled <- struct{}{}:
		default:
		}
	}))
	defer wtServer.Close()

	consumer, rdb := setupEgressConsumer(t, wtServer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	roomID := "egress-test-notarget"
	streamKey := "stream:o:" + roomID

	rdb.Del(ctx, streamKey)
	t.Cleanup(func() {
		rdb.Del(context.Background(), streamKey)
	})

	// Register room
	consumer.RegisterRoom(ctx, roomID)

	// Run consumer
	done := make(chan struct{})
	go func() {
		consumer.Run(ctx)
		close(done)
	}()

	// No target set — add message
	time.Sleep(50 * time.Millisecond)
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]any{
			"room_id": roomID,
			"text":    "hello",
			"msgid":   "1234-0",
		},
	})

	// Give consumer time to process, then verify worktool was NOT called
	select {
	case <-wtCalled:
		t.Error("worktool was called despite missing target")
	case <-time.After(500 * time.Millisecond):
		// Expected: worktool not called
	}

	cancel()
	<-done
}

func TestEgress_RecoversExistingStreamOnStartup(t *testing.T) {
	var gotBody worktool.SendMessageRequest
	called := make(chan struct{}, 1)
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(worktool.SendMessageResponse{Code: 200, Message: "ok"})
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	defer wtServer.Close()

	consumer, rdb := setupEgressConsumer(t, wtServer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	roomID := "egress-test-recover"
	streamKey := "stream:o:" + roomID
	targetKey := "wecom:target:" + roomID

	rdb.Del(ctx, streamKey, targetKey)
	t.Cleanup(func() {
		rdb.Del(context.Background(), streamKey, targetKey)
	})

	rdb.Set(ctx, targetKey, "恢复测试群", 0)
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]any{
			"room_id": roomID,
			"text":    "recover existing egress stream",
			"msgid":   "recover-1234-0",
		},
	})

	done := make(chan struct{})
	go func() {
		consumer.Run(ctx)
		close(done)
	}()

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worktool call from recovered egress stream")
	}

	cancel()
	<-done

	if len(gotBody.List) != 1 {
		t.Fatalf("worktool got %d items, want 1", len(gotBody.List))
	}
	if gotBody.List[0].TitleList[0] != "恢复测试群" {
		t.Errorf("target = %q, want %q", gotBody.List[0].TitleList[0], "恢复测试群")
	}
	if gotBody.List[0].ReceivedContent != "recover existing egress stream" {
		t.Errorf("content = %q, want %q", gotBody.List[0].ReceivedContent, "recover existing egress stream")
	}
}

func TestEgress_InvalidMessage(t *testing.T) {
	wtCalled := make(chan struct{}, 1)
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(worktool.SendMessageResponse{Code: 200, Message: "ok"})
		select {
		case wtCalled <- struct{}{}:
		default:
		}
	}))
	defer wtServer.Close()

	consumer, rdb := setupEgressConsumer(t, wtServer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	roomID := "egress-test-invalid"
	streamKey := "stream:o:" + roomID

	rdb.Del(ctx, streamKey)
	t.Cleanup(func() {
		rdb.Del(context.Background(), streamKey)
	})

	// Register room
	consumer.RegisterRoom(ctx, roomID)

	// Run consumer
	done := make(chan struct{})
	go func() {
		consumer.Run(ctx)
		close(done)
	}()

	// Missing room_id field
	time.Sleep(50 * time.Millisecond)
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]any{
			"text": "hello",
		},
	})

	// Verify worktool was NOT called
	select {
	case <-wtCalled:
		t.Fatal("worktool should not be called for invalid message")
	case <-time.After(500 * time.Millisecond):
		// Expected
	}

	cancel()
	<-done
}
