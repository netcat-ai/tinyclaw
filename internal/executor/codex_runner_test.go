package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

var testAgentRun = core.AgentRun{
	AgentSessionID:       100,
	RoomID:               10,
	SourceMessageAfterID: 0,
	SourceMessageUntilID: 2,
}

func TestBuildCodexPromptIncludesContextMessages(t *testing.T) {
	prompt := BuildCodexPrompt(AgentRunRequest{
		AgentRun:          testAgentRun,
		MemorySearchURL:   "http://127.0.0.1:8081/internal/memory/search",
		MemorySearchToken: "token-1",
		ContextMessages: []core.Message{
			{ID: 1, SenderName: "Alice", Payload: []byte(`{"type":"text","text":"hello"}`)},
			{ID: 2, SenderID: "bob", Payload: []byte(`{"type":"text","text":"@agent help"}`)},
		},
	})

	for _, want := range []string{
		"Agent Session ID: 100",
		"Room ID: 10",
		"Message Window: (0, 2]",
		"Return an Agent Run Result matching the provided output schema.",
		"Room Memory Search:",
		"memory_search_requests",
		"Do not include room_id",
		"id=1 sender=Alice text=\"hello\"",
		"id=2 sender=bob text=\"@agent help\"",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCodexRunnerUsesResponsesAPI(t *testing.T) {
	t.Setenv("TEST_CODEX_KEY", "test-key")
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `{"output":[{"content":[{"type":"output_text","text":"api answer"}]}]}`)
	}))
	defer server.Close()

	runner := NewCodexRunner(CodexRunnerConfig{
		BaseURL:   server.URL,
		APIKeyEnv: "TEST_CODEX_KEY",
		Model:     "gpt-5.5",
		Timeout:   time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want bearer key", gotAuth)
	}
	if result.FinalOutput != "api answer" {
		t.Fatalf("output = %q, want api answer", result.FinalOutput)
	}
}

func TestCodexRunnerUsesOutputLastMessage(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/bin/sh
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat >/dev/null
printf "fake codex answer" > "$output"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if result.FinalOutput != "fake codex answer" {
		t.Fatalf("output = %q, want fake codex answer", result.FinalOutput)
	}
}

func TestCodexRunnerRunsMemorySearchLoop(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	promptPath := filepath.Join(dir, "prompt")
	bin := filepath.Join(dir, "codex")
	script := fmt.Sprintf(`#!/bin/sh
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat > %[1]q
count=0
if [ -f %[2]q ]; then
  count=$(cat %[2]q)
fi
count=$((count + 1))
printf "%%s" "$count" > %[2]q
if [ "$count" -eq 1 ]; then
  printf '%%s' '{"final_output":"","memory_search_requests":[{"query":"language preference","types":["preference"],"limit":5,"include_inactive":false}],"memory_write_proposals":[]}' > "$output"
else
  if ! grep -q 'reply_language' %[1]q; then
    echo "missing memory search result" >&2
    exit 1
  fi
  printf '%%s' '{"final_output":"中文回复","memory_search_requests":[],"memory_write_proposals":[]}' > "$output"
fi
`, promptPath, countPath)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/internal/memory/search" {
			t.Fatalf("path = %q, want /internal/memory/search", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer memory-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var input core.MemorySearchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if input.RoomID != 0 {
			t.Fatalf("room id = %d, want zero-value omitted by runner", input.RoomID)
		}
		if input.Query != "language preference" {
			t.Fatalf("query = %q", input.Query)
		}
		_, _ = fmt.Fprint(w, `{"items":[{"id":1,"room_id":10,"type":"preference","key":"reply_language","content":"中文回复","status":"active"}]}`)
	}))
	defer server.Close()

	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})
	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun:          testAgentRun,
		MemorySearchURL:   server.URL + "/internal/memory/search",
		MemorySearchToken: "memory-token",
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if !called {
		t.Fatalf("memory search endpoint was not called")
	}
	if result.FinalOutput != "中文回复" {
		t.Fatalf("output = %q, want 中文回复", result.FinalOutput)
	}
}

func TestParseAgentRunResultWithMemoryWriteProposals(t *testing.T) {
	result, err := ParseAgentRunResult(`<final>
收到
</final>
<memory_write_proposals>
[
  {"op":"upsert_fact","key":"project.identity","content":"TinyClaw owns Room Memory."}
]
</memory_write_proposals>`)
	if err != nil {
		t.Fatalf("ParseAgentRunResult error: %v", err)
	}
	if result.FinalOutput != "收到" {
		t.Fatalf("final output = %q, want 收到", result.FinalOutput)
	}
	if len(result.MemoryWriteProposals) != 1 {
		t.Fatalf("proposal count = %d, want 1", len(result.MemoryWriteProposals))
	}
	if result.MemoryWriteProposals[0].Key != "project.identity" {
		t.Fatalf("proposal key = %q", result.MemoryWriteProposals[0].Key)
	}
}

func TestParseAgentRunResultJSON(t *testing.T) {
	result, err := ParseAgentRunResult(`{
		"final_output":"收到",
		"memory_search_requests":[],
		"memory_write_proposals":[
			{"op":"set_preference","key":"reply_language","content":"中文回复"}
		]
	}`)
	if err != nil {
		t.Fatalf("ParseAgentRunResult error: %v", err)
	}
	if result.FinalOutput != "收到" {
		t.Fatalf("final output = %q", result.FinalOutput)
	}
	if len(result.MemoryWriteProposals) != 1 || result.MemoryWriteProposals[0].Op != core.MemoryWriteOpSetPreference {
		t.Fatalf("unexpected proposals: %+v", result.MemoryWriteProposals)
	}
}

func TestCodexRunnerRealMemorySearch(t *testing.T) {
	if os.Getenv("RUN_CODEX_E2E") != "1" {
		t.Skip("RUN_CODEX_E2E=1 is not set")
	}
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/internal/memory/search" {
			t.Fatalf("path = %q, want /internal/memory/search", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer memory-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"items":[{"id":1,"room_id":10,"type":"preference","key":"reply_language","content":"中文回复","status":"active"}]}`)
	}))
	defer server.Close()

	runner := NewCodexRunner(CodexRunnerConfig{
		Timeout: 2 * time.Minute,
		Sandbox: "read-only",
	})
	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: core.AgentRun{
			AgentSessionID:       100,
			RoomID:               10,
			SourceMessageAfterID: 0,
			SourceMessageUntilID: 1,
		},
		MemorySearchURL:   server.URL + "/internal/memory/search",
		MemorySearchToken: "memory-token",
		ContextMessages: []core.Message{{
			ID:         1,
			SenderName: "Alice",
			Payload:    []byte(`{"type":"text","text":"Use memory_search_requests first, then answer with my reply language preference from Room Memory."}`),
		}},
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if !called {
		t.Fatalf("memory search endpoint was not called; final output=%q proposals=%+v", result.FinalOutput, result.MemoryWriteProposals)
	}
	if !strings.Contains(result.FinalOutput, "中文") {
		t.Fatalf("final output = %q, want Chinese preference", result.FinalOutput)
	}
}
