//go:build integration

package sandbox

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// Run with: go test ./sandbox/ -tags integration -run TestACL -v
// Requires a real Redis at REDIS_ADDR (default localhost:6379).

func integrationRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: cannot connect to Redis at %s: %v", addr, err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestACL_ProvisionAndScope(t *testing.T) {
	ctx := context.Background()
	rdb := integrationRedis(t)

	orch := &Orchestrator{
		redis: rdb,
		cfg: Config{
			StreamPrefix: "stream:room",
		},
	}

	roomID := "integration-test-room"
	username := "sb:" + roomID
	streamKey := "stream:room:" + roomID
	otherStream := "stream:room:other-room"

	// Cleanup after test
	t.Cleanup(func() {
		rdb.Do(ctx, "ACL", "DELUSER", username)
		rdb.Del(ctx, streamKey, otherStream)
	})
	// Pre-clean in case previous run left state
	rdb.Do(ctx, "ACL", "DELUSER", username)
	rdb.Del(ctx, streamKey, otherStream)

	// Provision the user
	cred, err := orch.provisionRedisUser(ctx, roomID)
	if err != nil {
		t.Fatalf("provisionRedisUser: %v", err)
	}
	if cred.Username != username {
		t.Errorf("username = %q, want %q", cred.Username, username)
	}
	if cred.Password == "" {
		t.Fatal("password is empty")
	}

	// Connect as the provisioned user
	userClient := redis.NewClient(&redis.Options{
		Addr:     rdb.Options().Addr,
		Username: cred.Username,
		Password: cred.Password,
	})
	t.Cleanup(func() { userClient.Close() })

	// PING should work
	if err := userClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("user ping failed: %v", err)
	}

	// XGROUP CREATE on own stream should work
	err = userClient.XGroupCreateMkStream(ctx, streamKey, "test-group", "0").Err()
	if err != nil {
		t.Fatalf("xgroup create on own stream failed: %v", err)
	}

	// XINFO on own stream should work
	err = userClient.Do(ctx, "XINFO", "STREAM", streamKey).Err()
	if err != nil {
		t.Fatalf("xinfo on own stream failed: %v", err)
	}

	// XACK on own stream should work (no-op, returns 0)
	err = userClient.XAck(ctx, streamKey, "test-group", "0-0").Err()
	if err != nil {
		t.Fatalf("xack on own stream failed: %v", err)
	}

	// Access to a different room's stream should be denied
	err = userClient.XGroupCreateMkStream(ctx, otherStream, "test-group", "0").Err()
	if err == nil {
		t.Fatal("expected NOPERM error accessing other room's stream, got nil")
	}

	// SET should be denied (not in allowed commands)
	err = userClient.Set(ctx, "some-key", "value", 0).Err()
	if err == nil {
		t.Fatal("expected NOPERM error for SET command, got nil")
	}
}
