package sandbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	sandboxfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
)

func newTestOrchestrator(t *testing.T, client *sandboxfake.Clientset) (*Orchestrator, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	orch := NewOrchestrator(client, rdb, Config{
		Namespace:    "claw",
		Image:        "ghcr.io/test/agent:latest",
		RedisAddr:    "redis:6379",
		StreamPrefix: "stream:room",
	})
	return orch, mr
}

func TestEnsure_CreatesSandbox(t *testing.T) {
	client := sandboxfake.NewSimpleClientset()
	orch, _ := newTestOrchestrator(t, client)
	ctx := context.Background()

	orch.Ensure(ctx, "test-room-123", "corp1", "group")

	list, err := client.AgentsV1alpha1().Sandboxes("claw").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(list.Items))
	}

	sbx := list.Items[0]

	// Verify deterministic name
	expected := sandboxName("test-room-123")
	if sbx.Name != expected {
		t.Errorf("sandbox name = %q, want %q", sbx.Name, expected)
	}

	// Verify top-level labels
	if sbx.Labels["app"] != "tinyclaw-sandbox" {
		t.Errorf("top-level label app = %q, want %q", sbx.Labels["app"], "tinyclaw-sandbox")
	}

	// Verify pod template labels
	ptLabels := sbx.Spec.PodTemplate.ObjectMeta.Labels
	wantLabels := map[string]string{
		"tinyclaw/room-id":   "test-room-123",
		"tinyclaw/tenant-id": "corp1",
		"tinyclaw/chat-type": "group",
	}
	for k, v := range wantLabels {
		if ptLabels[k] != v {
			t.Errorf("pod template label %s = %q, want %q", k, ptLabels[k], v)
		}
	}

	// Verify env vars
	c0 := sbx.Spec.PodTemplate.Spec.Containers[0]
	envMap := make(map[string]string)
	for _, e := range c0.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["ROOM_ID"] != "test-room-123" {
		t.Errorf("ROOM_ID = %q, want %q", envMap["ROOM_ID"], "test-room-123")
	}
	if envMap["REDIS_ADDR"] != "redis:6379" {
		t.Errorf("REDIS_ADDR = %q, want %q", envMap["REDIS_ADDR"], "redis:6379")
	}
	if envMap["STREAM_PREFIX"] != "stream:room" {
		t.Errorf("STREAM_PREFIX = %q, want %q", envMap["STREAM_PREFIX"], "stream:room")
	}

	// Verify restartPolicy
	if sbx.Spec.PodTemplate.Spec.RestartPolicy != "Always" {
		t.Errorf("restartPolicy = %q, want Always", sbx.Spec.PodTemplate.Spec.RestartPolicy)
	}
}

func TestEnsure_DebounceLock(t *testing.T) {
	client := sandboxfake.NewSimpleClientset()
	orch, _ := newTestOrchestrator(t, client)
	ctx := context.Background()

	// First call creates the sandbox
	orch.Ensure(ctx, "test-room-123", "corp1", "group")

	// Track create calls
	createCount := 0
	client.Fake.PrependReactor("create", "sandboxes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createCount++
		return false, nil, nil
	})

	// Second call within 3s should be debounced by Redis lock
	orch.Ensure(ctx, "test-room-123", "corp1", "group")

	if createCount != 0 {
		t.Errorf("expected 0 create calls (debounced), got %d", createCount)
	}
}

func TestEnsure_AlreadyExists(t *testing.T) {
	existing := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName("user-abc"),
			Namespace: "claw",
		},
	}

	client := sandboxfake.NewSimpleClientset(existing)
	orch, _ := newTestOrchestrator(t, client)
	ctx := context.Background()

	// Should not panic — AlreadyExists is silently handled
	orch.Ensure(ctx, "user-abc", "corp1", "dm")

	list, err := client.AgentsV1alpha1().Sandboxes("claw").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(list.Items))
	}
}

func TestEnsure_K8sError_NoPanic(t *testing.T) {
	client := sandboxfake.NewSimpleClientset()
	client.Fake.PrependReactor("create", "sandboxes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated k8s error")
	})
	orch, _ := newTestOrchestrator(t, client)
	ctx := context.Background()

	// Should log but not panic
	orch.Ensure(ctx, "test-room-123", "corp1", "group")
}

func TestSandboxName_Deterministic(t *testing.T) {
	name1 := sandboxName("room-abc-123")
	name2 := sandboxName("room-abc-123")
	if name1 != name2 {
		t.Errorf("sandboxName not deterministic: %q != %q", name1, name2)
	}

	// Different room IDs produce different names
	name3 := sandboxName("room-xyz-456")
	if name1 == name3 {
		t.Errorf("different room IDs produced same sandbox name: %q", name1)
	}
}

func TestEnsure_LockExpiry(t *testing.T) {
	client := sandboxfake.NewSimpleClientset()
	orch, mr := newTestOrchestrator(t, client)
	ctx := context.Background()

	orch.Ensure(ctx, "test-room-123", "corp1", "group")

	// Fast-forward miniredis past the lock TTL
	mr.FastForward(lockTTL)

	createCount := 0
	client.Fake.PrependReactor("create", "sandboxes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createCount++
		return false, nil, nil
	})

	orch.Ensure(ctx, "test-room-123", "corp1", "group")

	// After lock expiry, should attempt create again (will get AlreadyExists)
	if createCount != 1 {
		t.Errorf("expected 1 create attempt after lock expiry, got %d", createCount)
	}
}
