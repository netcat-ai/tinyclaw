package sandbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	extensionsfake "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

func newTestOrchestrator(t *testing.T, client *extensionsfake.Clientset) *Orchestrator {
	t.Helper()

	return NewOrchestrator(client, Config{
		Namespace:    "claw",
		TemplateName: "tinyclaw-agent-template",
		ReadyTimeout: 2 * time.Second,
		PollInterval: 10 * time.Millisecond,
	})
}

func markClaimReady(
	t *testing.T,
	ctx context.Context,
	client *extensionsfake.Clientset,
	namespace string,
	roomID string,
) {
	t.Helper()

	go func() {
		deadline := time.Now().Add(time.Second)
		name := sandboxName(roomID)
		for time.Now().Before(deadline) {
			claim, err := client.ExtensionsV1alpha1().SandboxClaims(namespace).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				claim = claim.DeepCopy()
				claim.Status.Conditions = []metav1.Condition{
					{
						Type:   string(sandboxv1alpha1.SandboxConditionReady),
						Status: metav1.ConditionTrue,
						Reason: "SandboxReady",
					},
				}
				claim.Status.SandboxStatus.Name = claim.Name
				if _, updateErr := client.ExtensionsV1alpha1().SandboxClaims(namespace).Update(ctx, claim, metav1.UpdateOptions{}); updateErr != nil {
					t.Errorf("update sandbox claim ready status: %v", updateErr)
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Errorf("sandbox claim %s was not created before timeout", name)
	}()
}

func TestEnsureReady_CreatesSandboxClaim(t *testing.T) {
	client := extensionsfake.NewSimpleClientset()
	orch := newTestOrchestrator(t, client)
	ctx := context.Background()
	markClaimReady(t, ctx, client, "claw", "test-room-123")

	sandboxID, err := orch.EnsureReady(ctx, "test-room-123")
	if err != nil {
		t.Fatalf("EnsureReady returned error: %v", err)
	}

	list, err := client.ExtensionsV1alpha1().SandboxClaims("claw").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list sandbox claims: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 sandbox claim, got %d", len(list.Items))
	}

	claim := list.Items[0]
	expected := sandboxName("test-room-123")
	if claim.Name != expected {
		t.Errorf("sandbox claim name = %q, want %q", claim.Name, expected)
	}
	if sandboxID != expected {
		t.Errorf("sandboxID = %q, want %q", sandboxID, expected)
	}
	if claim.Spec.TemplateRef.Name != "tinyclaw-agent-template" {
		t.Errorf("template ref = %q, want %q", claim.Spec.TemplateRef.Name, "tinyclaw-agent-template")
	}
	if claim.Labels["tinyclaw/room-id"] != "test-room-123" {
		t.Errorf("claim label tinyclaw/room-id = %q, want %q", claim.Labels["tinyclaw/room-id"], "test-room-123")
	}
}

func TestEnsureReady_LocalDebounce(t *testing.T) {
	client := extensionsfake.NewSimpleClientset()
	orch := newTestOrchestrator(t, client)
	ctx := context.Background()
	markClaimReady(t, ctx, client, "claw", "test-room-123")

	if _, err := orch.EnsureReady(ctx, "test-room-123"); err != nil {
		t.Fatalf("first EnsureReady: %v", err)
	}

	createCount := 0
	client.Fake.PrependReactor("create", "sandboxclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createCount++
		return false, nil, nil
	})

	if _, err := orch.EnsureReady(ctx, "test-room-123"); err != nil {
		t.Fatalf("second EnsureReady: %v", err)
	}
	if createCount != 0 {
		t.Errorf("expected 0 create calls while debounce is active, got %d", createCount)
	}
}

func TestEnsureReady_AlreadyExists(t *testing.T) {
	client := extensionsfake.NewSimpleClientset(&extensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName("user-abc"),
			Namespace: "claw",
		},
		Spec: extensionsv1alpha1.SandboxClaimSpec{
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{Name: "tinyclaw-agent-template"},
		},
		Status: extensionsv1alpha1.SandboxClaimStatus{
			Conditions: []metav1.Condition{
				{Type: string(sandboxv1alpha1.SandboxConditionReady), Status: metav1.ConditionTrue},
			},
			SandboxStatus: extensionsv1alpha1.SandboxStatus{Name: sandboxName("user-abc")},
		},
	})
	orch := newTestOrchestrator(t, client)
	ctx := context.Background()

	sandboxID, err := orch.EnsureReady(ctx, "user-abc")
	if err != nil {
		t.Fatalf("EnsureReady returned error: %v", err)
	}
	if sandboxID != sandboxName("user-abc") {
		t.Errorf("sandboxID = %q, want %q", sandboxID, sandboxName("user-abc"))
	}
}

func TestEnsureReady_K8sError(t *testing.T) {
	client := extensionsfake.NewSimpleClientset()
	client.Fake.PrependReactor("create", "sandboxclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated k8s error")
	})
	orch := newTestOrchestrator(t, client)

	_, err := orch.EnsureReady(context.Background(), "test-room-123")
	if err == nil {
		t.Fatal("EnsureReady error = nil, want non-nil")
	}
}

func TestSandboxName_Deterministic(t *testing.T) {
	name1 := sandboxName("room-abc-123")
	name2 := sandboxName("room-abc-123")
	if name1 != name2 {
		t.Errorf("sandboxName not deterministic: %q != %q", name1, name2)
	}

	name3 := sandboxName("room-xyz-456")
	if name1 == name3 {
		t.Errorf("different room IDs produced same sandbox name: %q", name1)
	}
}

func TestSandboxName_LowercasesRoomID(t *testing.T) {
	got := sandboxName("wrg-oKJwAA6siw1rBtGAKgpPhDzwmdOA")
	want := "clawagent-wrg-okjwaa6siw1rbtgakgpphdzwmdoa"
	if got != want {
		t.Errorf("sandboxName = %q, want %q", got, want)
	}
}

func TestEnsureReady_DebounceExpiry(t *testing.T) {
	client := extensionsfake.NewSimpleClientset()
	orch := newTestOrchestrator(t, client)
	ctx := context.Background()
	markClaimReady(t, ctx, client, "claw", "test-room-123")

	if _, err := orch.EnsureReady(ctx, "test-room-123"); err != nil {
		t.Fatalf("first EnsureReady: %v", err)
	}

	orch.mu.Lock()
	orch.recent["test-room-123"] = time.Now().Add(-ensureDebounceTTL - time.Millisecond)
	orch.mu.Unlock()

	createCount := 0
	client.Fake.PrependReactor("create", "sandboxclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createCount++
		return false, nil, nil
	})

	if _, err := orch.EnsureReady(ctx, "test-room-123"); err != nil {
		t.Fatalf("second EnsureReady: %v", err)
	}
	if createCount != 1 {
		t.Errorf("expected 1 create attempt after debounce expiry, got %d", createCount)
	}
}
