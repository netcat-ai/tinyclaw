package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	clawmanv1 "tinyclaw/clawman/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func pickFreeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free addr: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

func TestMessageGatewayConnectAndDispatch(t *testing.T) {
	addr := pickFreeAddr(t)
	resolveRoom := func(ctx context.Context, sandboxID string) (string, error) {
		if sandboxID == "sandbox-1" {
			return "room-1", nil
		}
		return "", fmt.Errorf("unknown sandbox")
	}
	gateway := NewMessageGateway(Config{ClawmanGRPCListenAddr: addr}, resolveRoom)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := gateway.Serve(ctx); err != nil {
			t.Errorf("gateway serve failed: %v", err)
		}
	}()

	roomID := "room-1"

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	conn, err := grpc.DialContext(
		dialCtx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("grpc dial failed: %v", err)
	}
	defer conn.Close()

	client := clawmanv1.NewClawmanClient(conn)
	stream, err := client.RoomChat(ctx)
	if err != nil {
		t.Fatalf("room chat stream failed: %v", err)
	}

	if err := stream.Send(&clawmanv1.Message{
		Kind:      "connect",
		SandboxId: "sandbox-1",
	}); err != nil {
		t.Fatalf("send connect failed: %v", err)
	}

	respCh, err := gateway.SendBatch(ctx, roomID, &clawmanv1.Message{
		Kind:      "messages",
		RequestId: "req-1",
		Messages: []*clawmanv1.AgentMessage{
			{
				Seq:     1,
				Msgid:   "msg-1",
				RoomId:  roomID,
				FromId:  "u-1",
				Payload: `{"msgtype":"text","text":{"content":"hello"}}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("SendBatch error: %v", err)
	}

	inbound, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv message failed: %v", err)
	}
	if inbound.GetKind() != "messages" {
		t.Fatalf("kind = %q, want messages", inbound.GetKind())
	}
	if inbound.GetRequestId() != "req-1" {
		t.Fatalf("request_id = %q, want req-1", inbound.GetRequestId())
	}

	if err := stream.Send(&clawmanv1.Message{
		Kind:      "result",
		RequestId: "req-1",
		Output:    "done",
	}); err != nil {
		t.Fatalf("send result failed: %v", err)
	}

	select {
	case resp := <-respCh:
		if resp.err != nil {
			t.Fatalf("response err = %v", resp.err)
		}
		if resp.output != "done" {
			t.Fatalf("output = %q, want done", resp.output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for gateway response")
	}
}
