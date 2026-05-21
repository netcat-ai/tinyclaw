package storage

import (
	"testing"

	"tinyclaw/internal/core"
)

func TestMemoryWriteJobFromProposalNormalizesType(t *testing.T) {
	run := core.AgentRun{
		AgentSessionID:       100,
		RoomID:               10,
		AgentKey:             core.DefaultAgentKey,
		SourceMessageAfterID: 20,
		SourceMessageUntilID: 22,
	}
	job, err := memoryWriteJobFromProposal(run, core.MemoryWriteProposal{
		Op:      core.MemoryWriteOpSetPreference,
		Key:     " reply_language ",
		Content: " 中文回复 ",
	})
	if err != nil {
		t.Fatalf("memoryWriteJobFromProposal error: %v", err)
	}
	if job.Type != core.MemoryTypePreference {
		t.Fatalf("type = %q, want preference", job.Type)
	}
	if job.Key != "reply_language" || job.Content != "中文回复" {
		t.Fatalf("job was not trimmed: %+v", job)
	}
	if job.OperationKey == "" {
		t.Fatal("operation key is empty")
	}
}

func TestMemoryWriteJobFromProposalRequiresMarkStaleType(t *testing.T) {
	_, err := memoryWriteJobFromProposal(core.AgentRun{AgentSessionID: 100, RoomID: 10}, core.MemoryWriteProposal{
		Op:  core.MemoryWriteOpMarkStale,
		Key: "reply_language",
	})
	if err == nil {
		t.Fatal("error = nil, want mark_stale type error")
	}
}

func TestMemoryWriteJobFromProposalIsIdempotentForSameWindow(t *testing.T) {
	run := core.AgentRun{
		AgentSessionID:       100,
		RoomID:               10,
		SourceMessageAfterID: 20,
		SourceMessageUntilID: 22,
	}
	proposal := core.MemoryWriteProposal{
		Op:      core.MemoryWriteOpUpsertFact,
		Key:     "project.identity",
		Content: "TinyClaw owns Room Memory.",
	}
	first, err := memoryWriteJobFromProposal(run, proposal)
	if err != nil {
		t.Fatalf("first proposal error: %v", err)
	}
	second, err := memoryWriteJobFromProposal(run, proposal)
	if err != nil {
		t.Fatalf("second proposal error: %v", err)
	}
	if first.OperationKey != second.OperationKey {
		t.Fatalf("operation keys differ: %s != %s", first.OperationKey, second.OperationKey)
	}
}
