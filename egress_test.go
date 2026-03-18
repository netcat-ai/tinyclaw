package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tinyclaw/worktool"
)

type fakeDeliveryStore struct {
	mu        sync.Mutex
	queue     []*Delivery
	sent      []int64
	retried   []int64
	failed    []int64
	lastError map[int64]string
}

func newFakeDeliveryStore(deliveries ...*Delivery) *fakeDeliveryStore {
	return &fakeDeliveryStore{
		queue:     deliveries,
		lastError: make(map[int64]string),
	}
}

func (s *fakeDeliveryStore) ClaimNextDelivery(ctx context.Context, lease time.Duration) (*Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil, nil
	}
	d := s.queue[0]
	s.queue = s.queue[1:]
	return d, nil
}

func (s *fakeDeliveryStore) MarkDeliverySent(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, id)
	return nil
}

func (s *fakeDeliveryStore) MarkDeliveryRetry(ctx context.Context, id int64, backoff time.Duration, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, id)
	s.lastError[id] = errText
	return nil
}

func (s *fakeDeliveryStore) MarkDeliveryFailed(ctx context.Context, id int64, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, id)
	s.lastError[id] = errText
	return nil
}

func TestEgress_RunSendsPendingDelivery(t *testing.T) {
	var gotBody worktool.SendMessageRequest
	called := make(chan struct{}, 1)
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode worktool request: %v", err)
		}
		if err := json.NewEncoder(w).Encode(worktool.SendMessageResponse{Code: 200, Message: "ok"}); err != nil {
			t.Fatalf("encode worktool response: %v", err)
		}
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	defer wtServer.Close()

	worktool.SetBaseURL(wtServer.URL)
	defer worktool.ResetBaseURL()

	store := newFakeDeliveryStore(&Delivery{
		ID:         1,
		RoomID:     "room-1",
		TargetName: "测试群",
		Content:    "hello from agent",
	})
	consumer := NewEgressConsumer(store, worktool.NewClient("test-robot"))
	consumer.pollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = consumer.Run(ctx)
		close(done)
	}()

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worktool call")
	}
	cancel()
	<-done

	if len(gotBody.List) != 1 {
		t.Fatalf("worktool got %d items, want 1", len(gotBody.List))
	}
	if gotBody.List[0].TitleList[0] != "测试群" {
		t.Fatalf("target = %q, want %q", gotBody.List[0].TitleList[0], "测试群")
	}
	if gotBody.List[0].ReceivedContent != "hello from agent" {
		t.Fatalf("content = %q, want %q", gotBody.List[0].ReceivedContent, "hello from agent")
	}
	if len(store.sent) != 1 || store.sent[0] != 1 {
		t.Fatalf("sent ids = %v, want [1]", store.sent)
	}
}

func TestEgress_InvalidDeliveryIsMarkedFailed(t *testing.T) {
	worktool.SetBaseURL("http://127.0.0.1:1")
	defer worktool.ResetBaseURL()

	store := newFakeDeliveryStore(&Delivery{
		ID:         2,
		RoomID:     "",
		TargetName: "缺少 room",
		Content:    "hello",
	})
	consumer := NewEgressConsumer(store, worktool.NewClient("test-robot"))

	if err := consumer.processDelivery(context.Background(), &Delivery{
		ID:         2,
		RoomID:     "",
		TargetName: "缺少 room",
		Content:    "hello",
	}); err != nil {
		t.Fatalf("processDelivery error: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != 2 {
		t.Fatalf("failed ids = %v, want [2]", store.failed)
	}
}

func TestEgress_SendFailureMarksRetry(t *testing.T) {
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer wtServer.Close()

	worktool.SetBaseURL(wtServer.URL)
	defer worktool.ResetBaseURL()

	store := newFakeDeliveryStore()
	consumer := NewEgressConsumer(store, worktool.NewClient("test-robot"))

	err := consumer.processDelivery(context.Background(), &Delivery{
		ID:           3,
		RoomID:       "room-3",
		TargetName:   "测试群",
		Content:      "hello",
		AttemptCount: 1,
	})
	if err != nil {
		t.Fatalf("processDelivery error: %v", err)
	}
	if len(store.retried) != 1 || store.retried[0] != 3 {
		t.Fatalf("retried ids = %v, want [3]", store.retried)
	}
	if store.lastError[3] == "" {
		t.Fatal("retry should record last error")
	}
}

func TestEgress_SendFailureMarksPermanentFailureAtRetryLimit(t *testing.T) {
	wtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer wtServer.Close()

	worktool.SetBaseURL(wtServer.URL)
	defer worktool.ResetBaseURL()

	store := newFakeDeliveryStore()
	consumer := NewEgressConsumer(store, worktool.NewClient("test-robot"))

	err := consumer.processDelivery(context.Background(), &Delivery{
		ID:           4,
		RoomID:       "room-4",
		TargetName:   "测试群",
		Content:      "hello",
		AttemptCount: maxDeliveryAttempts,
	})
	if err != nil {
		t.Fatalf("processDelivery error: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != 4 {
		t.Fatalf("failed ids = %v, want [4]", store.failed)
	}
}

func TestEgress_RunReturnsOnContextCancel(t *testing.T) {
	store := newFakeDeliveryStore()
	consumer := NewEgressConsumer(store, worktool.NewClient("test-robot"))
	consumer.pollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want nil/context canceled", err)
	}
}
