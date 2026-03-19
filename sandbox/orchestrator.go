package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	sdksandbox "sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

const (
	defaultReadyTimeout = 3 * time.Minute
	defaultServerPort   = 8888
)

type Config struct {
	Namespace    string
	TemplateName string
	ServerPort   int
	ReadyTimeout time.Duration
	RestConfig   *rest.Config
}

type sdkHandle interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
	IsReady() bool
	Run(ctx context.Context, command string, opts ...sdksandbox.CallOption) (*sdksandbox.ExecutionResult, error)
}

type sdkFactory func(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error)

type roomSession struct {
	mu     sync.Mutex
	client sdkHandle
}

type Orchestrator struct {
	cfg     Config
	factory sdkFactory

	mu    sync.Mutex
	rooms map[string]*roomSession
}

func NewOrchestrator(cfg Config) *Orchestrator {
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	if cfg.ServerPort <= 0 {
		cfg.ServerPort = defaultServerPort
	}

	return &Orchestrator{
		cfg:     cfg,
		factory: newSDKHandle,
		rooms:   make(map[string]*roomSession),
	}
}

func newSDKHandle(ctx context.Context, opts sdksandbox.Options) (sdkHandle, error) {
	return sdksandbox.New(ctx, opts)
}

func (o *Orchestrator) InvokeAgent(ctx context.Context, roomID string, req AgentRequest) (ExecutionResult, error) {
	state, err := o.getReadyRoomSession(ctx, roomID)
	if err != nil {
		return ExecutionResult{}, err
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	command, err := buildAgentInvokeCommand(req, o.cfg.ServerPort)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("build sdk agent command: %w", err)
	}

	result, err := state.client.Run(ctx, command)
	if err != nil {
		if errors.Is(err, sdksandbox.ErrNotReady) && !state.client.IsReady() {
			if openErr := state.client.Open(ctx); openErr != nil {
				return ExecutionResult{}, fmt.Errorf("re-open sdk sandbox for room %s: %w", roomID, openErr)
			}
			result, err = state.client.Run(ctx, command)
		}
		if err != nil {
			return ExecutionResult{}, fmt.Errorf("invoke sdk sandbox for room %s: %w", roomID, err)
		}
	}

	return decodeAgentExecutionResult(result)
}

func (o *Orchestrator) Close(ctx context.Context) error {
	o.mu.Lock()
	states := make([]*roomSession, 0, len(o.rooms))
	for _, state := range o.rooms {
		states = append(states, state)
	}
	o.mu.Unlock()

	var errs []error
	for _, state := range states {
		state.mu.Lock()
		client := state.client
		state.mu.Unlock()
		if client == nil {
			continue
		}
		if err := client.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (o *Orchestrator) getReadyRoomSession(ctx context.Context, roomID string) (*roomSession, error) {
	if roomID == "" {
		return nil, fmt.Errorf("roomID is required")
	}
	if o.cfg.TemplateName == "" {
		return nil, fmt.Errorf("sandbox template name is required")
	}
	if o.cfg.Namespace == "" {
		return nil, fmt.Errorf("sandbox namespace is required")
	}

	state := o.getOrCreateRoomSession(roomID)
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.client == nil {
		client, err := o.factory(ctx, sdksandbox.Options{
			TemplateName:        o.cfg.TemplateName,
			Namespace:           o.cfg.Namespace,
			ServerPort:          o.cfg.ServerPort,
			SandboxReadyTimeout: o.cfg.ReadyTimeout,
			RestConfig:          o.cfg.RestConfig,
			Quiet:               true,
		})
		if err != nil {
			return nil, fmt.Errorf("create sdk sandbox for room %s: %w", roomID, err)
		}
		state.client = client
	}

	if !state.client.IsReady() {
		if err := state.client.Open(ctx); err != nil {
			return nil, fmt.Errorf("open sdk sandbox for room %s: %w", roomID, err)
		}
	}

	return state, nil
}

func (o *Orchestrator) getOrCreateRoomSession(roomID string) *roomSession {
	o.mu.Lock()
	defer o.mu.Unlock()

	state, ok := o.rooms[roomID]
	if !ok {
		state = &roomSession{}
		o.rooms[roomID] = state
	}
	return state
}

func buildAgentInvokeCommand(req AgentRequest, serverPort int) (string, error) {
	if serverPort <= 0 {
		serverPort = defaultServerPort
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal agent request: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(payload)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/agent", serverPort)

	lines := []string{
		"set -eu",
		"tmp_req=$(mktemp)",
		"tmp_body=$(mktemp)",
		"trap 'rm -f \"$tmp_req\" \"$tmp_body\"' EXIT",
		fmt.Sprintf("printf '%%s' '%s' | base64 -d > \"$tmp_req\"", encoded),
		fmt.Sprintf("status=$(curl -sS -o \"$tmp_body\" -w '%%{http_code}' -X POST -H 'Content-Type: application/json' --data-binary @\"$tmp_req\" %s)", shellQuote(endpoint)),
		"if [ \"$status\" -lt 200 ] || [ \"$status\" -ge 300 ]; then",
		"  cat \"$tmp_body\" >&2",
		"  exit 1",
		"fi",
		"cat \"$tmp_body\"",
	}

	return strings.Join(lines, "\n"), nil
}

func decodeAgentExecutionResult(result *sdksandbox.ExecutionResult) (ExecutionResult, error) {
	if result == nil {
		return ExecutionResult{}, fmt.Errorf("sdk execution result is nil")
	}

	var decoded ExecutionResult
	if err := json.Unmarshal([]byte(result.Stdout), &decoded); err != nil {
		return ExecutionResult{}, fmt.Errorf("decode agent response: %w", err)
	}
	if decoded.ExitCode != 0 {
		errText := strings.TrimSpace(decoded.Stderr)
		if errText == "" {
			errText = strings.TrimSpace(decoded.Stdout)
		}
		if errText == "" {
			errText = "unknown agent runtime failure"
		}
		return ExecutionResult{}, fmt.Errorf("sandbox agent failed with exit_code=%d: %s", decoded.ExitCode, errText)
	}
	if strings.TrimSpace(decoded.Stdout) == "" {
		return ExecutionResult{}, fmt.Errorf("sandbox response stdout is empty")
	}
	return decoded, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
