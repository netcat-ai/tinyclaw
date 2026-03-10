package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	sandboxclient "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned"
)

const (
	lockPrefix = "lock:ensure:"
	lockTTL    = 3 * time.Second
)

// RedisCredential holds the username and password for a per-sandbox Redis user.
type RedisCredential struct {
	Username string
	Password string
}

// UserProvisioner creates a scoped Redis ACL user for a room.
type UserProvisioner func(ctx context.Context, roomID string) (RedisCredential, error)

type Config struct {
	Namespace    string // K8s namespace for sandboxes
	Image        string // Agent container image
	RedisAddr    string // Passed to sandbox as env var
	StreamPrefix string // Passed to sandbox as env var
}

type Orchestrator struct {
	client      sandboxclient.Interface
	redis       *redis.Client
	cfg         Config
	provisionFn UserProvisioner
}

func NewOrchestrator(client sandboxclient.Interface, rdb *redis.Client, cfg Config) *Orchestrator {
	o := &Orchestrator{
		client: client,
		redis:  rdb,
		cfg:    cfg,
	}
	o.provisionFn = o.provisionRedisUser
	return o
}

// Ensure creates or confirms the Sandbox CR for a room.
// Uses a Redis lock to prevent ensure storms. All errors are logged, never returned.
func (o *Orchestrator) Ensure(ctx context.Context, roomID, tenantID, chatType string) {
	locked, err := o.redis.SetNX(ctx, lockPrefix+roomID, "1", lockTTL).Result()
	if err != nil {
		slog.Error("ensure lock check failed", "room_id", roomID, "err", err)
		return
	}
	if !locked {
		return
	}

	cred, err := o.provisionFn(ctx, roomID)
	if err != nil {
		slog.Error("ensure redis user failed", "room_id", roomID, "err", err)
		return
	}

	name := sandboxName(roomID)
	sbx := buildSandbox(name, o.cfg, roomID, tenantID, chatType, cred)

	_, err = o.client.AgentsV1alpha1().Sandboxes(o.cfg.Namespace).Create(ctx, sbx, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return
	}
	if err != nil {
		slog.Error("ensure sandbox create failed", "room_id", roomID, "sandbox", name, "err", err)
		return
	}
	slog.Info("ensure sandbox created", "room_id", roomID, "sandbox", name)
}

// provisionRedisUser creates a Redis ACL user scoped to the room's stream key.
// The user can only run read-side stream commands on its own stream.
func (o *Orchestrator) provisionRedisUser(ctx context.Context, roomID string) (RedisCredential, error) {
	username := "sb:" + roomID
	password, err := generatePassword(16)
	if err != nil {
		return RedisCredential{}, fmt.Errorf("generate password: %w", err)
	}

	streamPattern := o.cfg.StreamPrefix + ":" + roomID

	// ACL SETUSER <user> on ><password> ~<key-pattern> +allowed-commands
	err = o.redis.Do(ctx, "ACL", "SETUSER", username,
		"on",
		">"+password,
		"~"+streamPattern,
		"+xreadgroup", "+xack", "+xinfo", "+xgroup", "+ping",
	).Err()
	if err != nil {
		return RedisCredential{}, fmt.Errorf("acl setuser: %w", err)
	}

	slog.Info("redis user provisioned", "username", username, "room_id", roomID)
	return RedisCredential{Username: username, Password: password}, nil
}

func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func buildSandbox(name string, cfg Config, roomID, tenantID, chatType string, cred RedisCredential) *sandboxv1alpha1.Sandbox {
	return &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app": "tinyclaw-sandbox",
			},
		},
		Spec: sandboxv1alpha1.SandboxSpec{
			PodTemplate: sandboxv1alpha1.PodTemplate{
				ObjectMeta: sandboxv1alpha1.PodMetadata{
					Labels: map[string]string{
						"tinyclaw/room-id":   roomID,
						"tinyclaw/tenant-id": tenantID,
						"tinyclaw/chat-type": chatType,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: cfg.Image,
							Env: []corev1.EnvVar{
								{Name: "ROOM_ID", Value: roomID},
								{Name: "REDIS_ADDR", Value: cfg.RedisAddr},
								{Name: "REDIS_USERNAME", Value: cred.Username},
								{Name: "REDIS_PASSWORD", Value: cred.Password},
								{Name: "STREAM_PREFIX", Value: cfg.StreamPrefix},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
}

// sandboxName returns a deterministic Sandbox name for a room ID.
func sandboxName(roomID string) string {
	return "tinyclaw-agent-" + roomID
}
