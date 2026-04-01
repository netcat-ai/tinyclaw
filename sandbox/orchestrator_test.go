package sandbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

type fakeClaimClient struct {
	claims map[string]*extensionsv1alpha1.SandboxClaim
}

func newFakeClaimClient() *fakeClaimClient {
	return &fakeClaimClient{claims: make(map[string]*extensionsv1alpha1.SandboxClaim)}
}

func (f *fakeClaimClient) Create(ctx context.Context, claim *extensionsv1alpha1.SandboxClaim, opts metav1.CreateOptions) (*extensionsv1alpha1.SandboxClaim, error) {
	if _, exists := f.claims[claim.Name]; exists {
		return nil, fmt.Errorf("already exists")
	}
	copyClaim := claim.DeepCopy()
	copyClaim.Status.Conditions = []metav1.Condition{{
		Type:   "Ready",
		Status: metav1.ConditionTrue,
	}}
	f.claims[claim.Name] = copyClaim
	return copyClaim, nil
}

func (f *fakeClaimClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	delete(f.claims, name)
	return nil
}

func (f *fakeClaimClient) Get(ctx context.Context, name string, opts metav1.GetOptions) (*extensionsv1alpha1.SandboxClaim, error) {
	claim, ok := f.claims[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return claim.DeepCopy(), nil
}

func (f *fakeClaimClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return watch.NewEmptyWatch(), nil
}

type fakeExtensions struct {
	claims *fakeClaimClient
}

func (f *fakeExtensions) SandboxClaims(namespace string) claimClient {
	return f.claims
}

func TestEnsureRoomCreatesClaim(t *testing.T) {
	claims := newFakeClaimClient()
	orch := &Orchestrator{
		cfg: Config{
			Namespace:    "claw",
			TemplateName: "tinyclaw-agent-template",
			ReadyTimeout: time.Second,
		},
		client: &fakeExtensions{claims: claims},
		rooms:  make(map[string]*roomSession),
	}

	claimName, err := orch.EnsureRoom(context.Background(), "room-1")
	if err != nil {
		t.Fatalf("EnsureRoom error: %v", err)
	}
	if claimName == "" {
		t.Fatal("claimName is empty")
	}
	got, err := claims.Get(context.Background(), claimName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get claim error: %v", err)
	}
	if got.Annotations[roomIDAnnotationKey] != "room-1" {
		t.Fatalf("room annotation = %q", got.Annotations[roomIDAnnotationKey])
	}
}

func TestResolveRoomID(t *testing.T) {
	claims := newFakeClaimClient()
	claims.claims["tinyclaw-room-a"] = &extensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tinyclaw-room-a",
			Annotations: map[string]string{
				roomIDAnnotationKey: "room-1",
			},
		},
	}
	orch := &Orchestrator{
		cfg:    Config{Namespace: "claw"},
		client: &fakeExtensions{claims: claims},
	}

	roomID, err := orch.ResolveRoomID(context.Background(), "tinyclaw-room-a")
	if err != nil {
		t.Fatalf("ResolveRoomID error: %v", err)
	}
	if roomID != "room-1" {
		t.Fatalf("roomID = %q, want room-1", roomID)
	}
}
