package sandbox

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	extensionsclientset "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned"
	extensionsclient "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/typed/api/v1alpha1"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

const (
	defaultReadyTimeout = 3 * time.Minute
	roomIDAnnotationKey = "tinyclaw/room-id"
)

type Config struct {
	Namespace    string
	TemplateName string
	ReadyTimeout time.Duration
	RestConfig   *rest.Config
}

type claimClient interface {
	Create(ctx context.Context, sandboxClaim *extensionsv1alpha1.SandboxClaim, opts metav1.CreateOptions) (*extensionsv1alpha1.SandboxClaim, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*extensionsv1alpha1.SandboxClaim, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

type extensionGetter interface {
	SandboxClaims(namespace string) claimClient
}

type extensionClientAdapter struct {
	inner extensionsclient.ExtensionsV1alpha1Interface
}

func (a *extensionClientAdapter) SandboxClaims(namespace string) claimClient {
	return a.inner.SandboxClaims(namespace)
}

type roomSession struct {
	roomID    string
	claimName string
}

type Orchestrator struct {
	cfg    Config
	client extensionGetter

	mu    sync.Mutex
	rooms map[string]*roomSession
}

func NewOrchestrator(cfg Config) *Orchestrator {
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	cs, err := extensionsclientset.NewForConfig(cfg.RestConfig)
	if err != nil {
		panic(err)
	}
	return &Orchestrator{
		cfg:    cfg,
		client: &extensionClientAdapter{inner: cs.ExtensionsV1alpha1()},
		rooms:  make(map[string]*roomSession),
	}
}

func deterministicClaimName(roomID string) string {
	sum := sha1.Sum([]byte(roomID))
	return "tinyclaw-room-" + hex.EncodeToString(sum[:8])
}

func (o *Orchestrator) EnsureRoom(ctx context.Context, roomID string) (string, error) {
	if roomID == "" {
		return "", fmt.Errorf("roomID is required")
	}
	if o.cfg.TemplateName == "" {
		return "", fmt.Errorf("sandbox template name is required")
	}
	if o.cfg.Namespace == "" {
		return "", fmt.Errorf("sandbox namespace is required")
	}

	session := o.getOrCreateRoomSession(roomID)
	claimName := session.claimName
	claims := o.client.SandboxClaims(o.cfg.Namespace)

	if _, err := claims.Get(ctx, claimName, metav1.GetOptions{}); err != nil {
		if _, err := claims.Create(ctx, &extensionsv1alpha1.SandboxClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claimName,
				Namespace: o.cfg.Namespace,
				Annotations: map[string]string{
					roomIDAnnotationKey: roomID,
				},
			},
			Spec: extensionsv1alpha1.SandboxClaimSpec{
				TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
					Name: o.cfg.TemplateName,
				},
			},
		}, metav1.CreateOptions{}); err != nil {
			existing, getErr := claims.Get(ctx, claimName, metav1.GetOptions{})
			if getErr != nil {
				return "", fmt.Errorf("create sandbox claim for room %s: %w", roomID, err)
			}
			if existing.Annotations[roomIDAnnotationKey] != roomID {
				return "", fmt.Errorf("claim %s already bound to another room", claimName)
			}
		}
	}

	if err := o.waitReady(ctx, claims, claimName); err != nil {
		return "", err
	}
	return claimName, nil
}

func (o *Orchestrator) ResolveRoomID(ctx context.Context, sandboxID string) (string, error) {
	claim, err := o.client.SandboxClaims(o.cfg.Namespace).Get(ctx, sandboxID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get sandbox claim %s: %w", sandboxID, err)
	}
	roomID := claim.Annotations[roomIDAnnotationKey]
	if roomID == "" {
		return "", fmt.Errorf("sandbox claim %s has no room annotation", sandboxID)
	}
	return roomID, nil
}

func (o *Orchestrator) Close(ctx context.Context) error {
	o.mu.Lock()
	rooms := make([]*roomSession, 0, len(o.rooms))
	for _, room := range o.rooms {
		rooms = append(rooms, room)
	}
	o.mu.Unlock()

	var firstErr error
	claims := o.client.SandboxClaims(o.cfg.Namespace)
	for _, room := range rooms {
		if err := claims.Delete(ctx, room.claimName, metav1.DeleteOptions{}); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("delete sandbox claim %s: %w", room.claimName, err)
		}
	}
	return firstErr
}

func (o *Orchestrator) getOrCreateRoomSession(roomID string) *roomSession {
	o.mu.Lock()
	defer o.mu.Unlock()

	session, ok := o.rooms[roomID]
	if !ok {
		session = &roomSession{
			roomID:    roomID,
			claimName: deterministicClaimName(roomID),
		}
		o.rooms[roomID] = session
	}
	return session
}

func (o *Orchestrator) waitReady(ctx context.Context, claims claimClient, claimName string) error {
	deadlineCtx, cancel := context.WithTimeout(ctx, o.cfg.ReadyTimeout)
	defer cancel()

	for {
		claim, err := claims.Get(deadlineCtx, claimName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get sandbox claim %s: %w", claimName, err)
		}
		for _, cond := range claim.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				return nil
			}
		}

		select {
		case <-deadlineCtx.Done():
			return fmt.Errorf("wait sandbox claim %s ready: %w", claimName, deadlineCtx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
