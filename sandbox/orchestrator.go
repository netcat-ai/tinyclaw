package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	APIURL       string
	ServerPort   int
	ReadyTimeout time.Duration
	RestConfig   *rest.Config
}

type sdkHandle interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
	IsReady() bool
	ClaimName() string
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
	http    *http.Client

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
		http:    &http.Client{},
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

	result, err := o.invokeAgentHTTP(ctx, state.client, req)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("invoke sdk sandbox for room %s: %w", roomID, err)
	}
	return result, nil
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
	if o.cfg.APIURL == "" {
		return nil, fmt.Errorf("sandbox api url is required")
	}

	state := o.getOrCreateRoomSession(roomID)
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.client == nil {
		if err := o.createRoomSessionClientLocked(ctx, roomID, state); err != nil {
			return nil, err
		}
	}

	if err := o.openRoomSessionLocked(ctx, roomID, state); err != nil {
		return nil, err
	}

	return state, nil
}

func (o *Orchestrator) createRoomSessionClientLocked(ctx context.Context, roomID string, state *roomSession) error {
	client, err := o.factory(ctx, sdksandbox.Options{
		TemplateName:        o.cfg.TemplateName,
		Namespace:           o.cfg.Namespace,
		APIURL:              o.cfg.APIURL,
		ServerPort:          o.cfg.ServerPort,
		SandboxReadyTimeout: o.cfg.ReadyTimeout,
		RestConfig:          o.cfg.RestConfig,
		Quiet:               true,
	})
	if err != nil {
		return fmt.Errorf("create sdk sandbox for room %s: %w", roomID, err)
	}
	state.client = client
	return nil
}

func (o *Orchestrator) openRoomSessionLocked(ctx context.Context, roomID string, state *roomSession) error {
	if state.client == nil {
		if err := o.createRoomSessionClientLocked(ctx, roomID, state); err != nil {
			return err
		}
	}
	if state.client.IsReady() {
		return nil
	}
	if err := state.client.Open(ctx); err != nil {
		if !errors.Is(err, sdksandbox.ErrOrphanedClaim) {
			return fmt.Errorf("open sdk sandbox for room %s: %w", roomID, err)
		}
		if closeErr := state.client.Close(ctx); closeErr != nil {
			return fmt.Errorf("cleanup orphaned sdk sandbox for room %s: %w", roomID, closeErr)
		}
		state.client = nil
		if err := o.createRoomSessionClientLocked(ctx, roomID, state); err != nil {
			return err
		}
		if err := state.client.Open(ctx); err != nil {
			return fmt.Errorf("re-open sdk sandbox for room %s after orphan cleanup: %w", roomID, err)
		}
	}
	return nil
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

func (o *Orchestrator) invokeAgentHTTP(ctx context.Context, client sdkHandle, req AgentRequest) (ExecutionResult, error) {
	if client == nil {
		return ExecutionResult{}, fmt.Errorf("sdk client is nil")
	}
	claimName := strings.TrimSpace(client.ClaimName())
	if claimName == "" {
		return ExecutionResult{}, fmt.Errorf("sdk sandbox claim name is empty")
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("marshal agent request: %w", err)
	}

	endpoint := strings.TrimRight(o.cfg.APIURL, "/") + "/agent"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("build agent request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Sandbox-ID", claimName)
	httpReq.Header.Set("X-Sandbox-Namespace", o.cfg.Namespace)
	httpReq.Header.Set("X-Sandbox-Port", strconv.Itoa(o.cfg.ServerPort))

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("post /agent: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("read agent response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		var errorBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errorBody); err == nil && strings.TrimSpace(errorBody.Error) != "" {
			message = strings.TrimSpace(errorBody.Error)
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return ExecutionResult{}, fmt.Errorf("post /agent returned status %d: %s", resp.StatusCode, message)
	}

	var decoded ExecutionResult
	if err := json.Unmarshal(body, &decoded); err != nil {
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
