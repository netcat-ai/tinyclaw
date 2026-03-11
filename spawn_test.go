package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestChildSessionKeyFormat(t *testing.T) {
	parentKey := "room-123"
	childKey := childSessionKey(parentKey)

	if !strings.HasPrefix(childKey, "room-123:subagent:") {
		t.Fatalf("childSessionKey() = %q, want prefix %q", childKey, "room-123:subagent:")
	}

	parts := strings.Split(childKey, ":")
	if len(parts) != 3 {
		t.Fatalf("childSessionKey() parts = %d, want 3", len(parts))
	}
	if len(parts[2]) != 16 {
		t.Fatalf("childSessionKey() id length = %d, want 16", len(parts[2]))
	}
}

func TestNewIDGeneratesUniqueIDs(t *testing.T) {
	id1 := newID()
	id2 := newID()

	if id1 == id2 {
		t.Fatalf("newID() generated duplicate: %q", id1)
	}
	if len(id1) != 16 {
		t.Fatalf("newID() length = %d, want 16", len(id1))
	}
}
