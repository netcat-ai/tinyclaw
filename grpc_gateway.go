package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	clawmanv1 "tinyclaw/clawman/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const roomConnectPollInterval = 200 * time.Millisecond

type gatewayResponse struct {
	output string
	err    error
}

type pendingInvocation struct {
	roomID string
	ch     chan gatewayResponse
}

type roomStream struct {
	roomID string
	stream grpc.BidiStreamingServer[clawmanv1.Message, clawmanv1.Message]
}

type MessageGateway struct {
	clawmanv1.UnimplementedClawmanServer

	cfg         Config
	resolveRoom func(context.Context, string) (string, error)

	mu        sync.Mutex
	rooms     map[string]*roomStream
	pending   map[string]pendingInvocation
	grpcSrv   *grpc.Server
	listener  net.Listener
	closeOnce sync.Once
}

func NewMessageGateway(cfg Config, resolveRoom func(context.Context, string) (string, error)) *MessageGateway {
	return &MessageGateway{
		cfg:         cfg,
		resolveRoom: resolveRoom,
		rooms:       make(map[string]*roomStream),
		pending:     make(map[string]pendingInvocation),
	}
}

func (g *MessageGateway) Serve(ctx context.Context) error {
	lis, err := net.Listen("tcp", g.cfg.ClawmanGRPCListenAddr)
	if err != nil {
		return fmt.Errorf("listen clawman grpc: %w", err)
	}
	g.listener = lis
	g.grpcSrv = grpc.NewServer()
	clawmanv1.RegisterClawmanServer(g.grpcSrv, g)

	errCh := make(chan error, 1)
	go func() {
		errCh <- g.grpcSrv.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		g.Close()
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve clawman grpc: %w", err)
		}
		return nil
	}
}

func (g *MessageGateway) Close() {
	g.closeOnce.Do(func() {
		if g.grpcSrv != nil {
			g.grpcSrv.GracefulStop()
		}
		if g.listener != nil {
			_ = g.listener.Close()
		}
	})
}

func (g *MessageGateway) RoomChat(stream grpc.BidiStreamingServer[clawmanv1.Message, clawmanv1.Message]) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "connect message required: %v", err)
	}
	if first.GetKind() != "connect" {
		return status.Error(codes.InvalidArgument, "first message must be connect")
	}

	sandboxID := first.GetSandboxId()
	if sandboxID == "" {
		return status.Error(codes.InvalidArgument, "sandbox_id is required")
	}
	roomID := first.GetRoomId()
	if roomID == "" {
		if g.resolveRoom == nil {
			return status.Error(codes.FailedPrecondition, "room resolver is not configured")
		}
		resolved, err := g.resolveRoom(stream.Context(), sandboxID)
		if err != nil {
			return status.Errorf(codes.NotFound, "resolve room for sandbox %s: %v", sandboxID, err)
		}
		roomID = resolved
	}

	g.registerRoom(roomID, stream)
	defer g.unregisterRoom(roomID, stream)

	slog.Info("sandbox grpc connected", "room_id", roomID, "sandbox_id", sandboxID)

	for {
		msg, err := stream.Recv()
		if err != nil {
			g.failPendingForRoom(roomID, fmt.Errorf("room stream closed: %w", err))
			return nil
		}
		switch msg.GetKind() {
		case "result":
			g.resolve(msg.GetRequestId(), gatewayResponse{output: msg.GetOutput()})
		case "error":
			g.resolve(msg.GetRequestId(), gatewayResponse{err: errors.New(msg.GetError())})
		default:
			return status.Error(codes.InvalidArgument, "unsupported sandbox payload")
		}
	}
}

func (g *MessageGateway) SendBatch(ctx context.Context, roomID string, batch *clawmanv1.Message) (<-chan gatewayResponse, error) {
	stream, err := g.waitForRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}

	ch := make(chan gatewayResponse, 1)

	g.mu.Lock()
	g.pending[batch.GetRequestId()] = pendingInvocation{roomID: roomID, ch: ch}
	g.mu.Unlock()

	if err := stream.Send(batch); err != nil {
		g.mu.Lock()
		delete(g.pending, batch.GetRequestId())
		g.mu.Unlock()
		return nil, fmt.Errorf("send batch to room %s: %w", roomID, err)
	}

	return ch, nil
}

func (g *MessageGateway) registerRoom(roomID string, stream grpc.BidiStreamingServer[clawmanv1.Message, clawmanv1.Message]) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rooms[roomID] = &roomStream{roomID: roomID, stream: stream}
}

func (g *MessageGateway) unregisterRoom(roomID string, stream grpc.BidiStreamingServer[clawmanv1.Message, clawmanv1.Message]) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.rooms[roomID]
	if current != nil && current.stream == stream {
		delete(g.rooms, roomID)
	}
}

func (g *MessageGateway) waitForRoom(ctx context.Context, roomID string) (grpc.BidiStreamingServer[clawmanv1.Message, clawmanv1.Message], error) {
	ticker := time.NewTicker(roomConnectPollInterval)
	defer ticker.Stop()

	for {
		g.mu.Lock()
		room := g.rooms[roomID]
		g.mu.Unlock()
		if room != nil {
			return room.stream, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for room %s connection: %w", roomID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (g *MessageGateway) resolve(requestID string, resp gatewayResponse) {
	g.mu.Lock()
	pending, ok := g.pending[requestID]
	if ok {
		delete(g.pending, requestID)
	}
	g.mu.Unlock()

	if ok {
		pending.ch <- resp
		close(pending.ch)
	}
}

func (g *MessageGateway) failPendingForRoom(roomID string, err error) {
	g.mu.Lock()
	toFail := make([]pendingInvocation, 0)
	for requestID, pending := range g.pending {
		if pending.roomID != roomID {
			continue
		}
		delete(g.pending, requestID)
		toFail = append(toFail, pending)
	}
	g.mu.Unlock()

	for _, pending := range toFail {
		pending.ch <- gatewayResponse{err: err}
		close(pending.ch)
	}
}
