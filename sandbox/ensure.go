package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	extensionsclient "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

const (
	ensureDebounceTTL   = 3 * time.Second
	defaultReadyTimeout = 3 * time.Minute
	defaultPollInterval = time.Second
)

type Config struct {
	Namespace    string
	TemplateName string
	ReadyTimeout time.Duration
	PollInterval time.Duration
}

type Orchestrator struct {
	client extensionsclient.Interface
	cfg    Config
	mu     sync.Mutex
	recent map[string]time.Time
}

func NewOrchestrator(client extensionsclient.Interface, cfg Config) *Orchestrator {
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}

	return &Orchestrator{
		client: client,
		cfg:    cfg,
		recent: make(map[string]time.Time),
	}
}

func (o *Orchestrator) EnsureReady(ctx context.Context, roomID string) (string, error) {
	if roomID == "" {
		return "", fmt.Errorf("roomID is required")
	}
	if o.cfg.TemplateName == "" {
		return "", fmt.Errorf("sandbox template name is required")
	}

	claimName := sandboxName(roomID)
	if o.shouldCreate(roomID) {
		claim := buildSandboxClaim(claimName, o.cfg, roomID)
		_, err := o.client.ExtensionsV1alpha1().SandboxClaims(o.cfg.Namespace).Create(
			ctx,
			claim,
			metav1.CreateOptions{},
		)
		switch {
		case err == nil:
			slog.Info("sandbox claim created", "room_id", roomID, "sandbox_claim", claimName)
		case apierrors.IsAlreadyExists(err):
			// Another caller or a previous ensure already created it.
		default:
			return "", fmt.Errorf("create sandbox claim %s: %w", claimName, err)
		}
	}

	sandboxID, err := o.waitUntilReady(ctx, claimName)
	if err != nil {
		return "", err
	}

	return sandboxID, nil
}

func (o *Orchestrator) shouldCreate(roomID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	if last, ok := o.recent[roomID]; ok && now.Sub(last) < ensureDebounceTTL {
		return false
	}
	o.recent[roomID] = now
	return true
}

func (o *Orchestrator) waitUntilReady(ctx context.Context, claimName string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, o.cfg.ReadyTimeout)
	defer cancel()

	ticker := time.NewTicker(o.cfg.PollInterval)
	defer ticker.Stop()

	for {
		claim, err := o.client.ExtensionsV1alpha1().SandboxClaims(o.cfg.Namespace).Get(
			waitCtx,
			claimName,
			metav1.GetOptions{},
		)
		if err == nil {
			if sandboxID := readySandboxID(claim); sandboxID != "" {
				return sandboxID, nil
			}
		} else if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("get sandbox claim %s: %w", claimName, err)
		}

		select {
		case <-waitCtx.Done():
			return "", fmt.Errorf("wait sandbox claim %s ready: %w", claimName, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func readySandboxID(claim *extensionsv1alpha1.SandboxClaim) string {
	for _, condition := range claim.Status.Conditions {
		if condition.Type != string(sandboxv1alpha1.SandboxConditionReady) {
			continue
		}
		if condition.Status != metav1.ConditionTrue {
			return ""
		}
		if claim.Status.SandboxStatus.Name != "" {
			return claim.Status.SandboxStatus.Name
		}
		return claim.Name
	}
	return ""
}

func buildSandboxClaim(name string, cfg Config, roomID string) *extensionsv1alpha1.SandboxClaim {
	return &extensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app":              "tinyclaw-sandbox",
				"tinyclaw/room-id": roomID,
			},
		},
		Spec: extensionsv1alpha1.SandboxClaimSpec{
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
				Name: cfg.TemplateName,
			},
		},
	}
}

func sandboxName(roomID string) string {
	return "clawagent-" + strings.ToLower(roomID)
}
