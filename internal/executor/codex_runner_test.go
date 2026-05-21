package executor

import (
	"context"
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
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{ID: 1, SenderName: "Alice", Payload: []byte(`{"type":"text","text":"hello"}`)},
			{ID: 2, SenderID: "bob", Payload: []byte(`{"type":"text","text":"@agent help"}`)},
		},
	})

	for _, want := range []string{
		"Agent Session ID: 100",
		"Room ID: 10",
		"Message Window: (0, 2]",
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

	output, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want bearer key", gotAuth)
	}
	if output != "api answer" {
		t.Fatalf("output = %q, want api answer", output)
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
		Timeout: time.Second,
	})

	output, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if output != "fake codex answer" {
		t.Fatalf("output = %q, want fake codex answer", output)
	}
}
