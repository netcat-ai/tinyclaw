package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

func TestBuildCodexPromptIncludesContextMessages(t *testing.T) {
	prompt := BuildCodexPrompt(InvocationRun{
		Invocation: core.Invocation{ID: 1000, RoomID: 10},
		ContextMessages: []core.Message{
			{ID: 1, SenderName: "Alice", Payload: []byte(`{"type":"text","text":"hello"}`)},
			{ID: 2, SenderID: "bob", Payload: []byte(`{"type":"text","text":"@agent help"}`)},
		},
	})

	for _, want := range []string{
		"Invocation ID: 1000",
		"Room ID: 10",
		"id=1 sender=Alice text=\"hello\"",
		"id=2 sender=bob text=\"@agent help\"",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
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

	output, err := runner.RunInvocation(context.Background(), InvocationRun{
		Invocation: core.Invocation{ID: 1000, RoomID: 10},
	})
	if err != nil {
		t.Fatalf("RunInvocation error: %v", err)
	}
	if output != "fake codex answer" {
		t.Fatalf("output = %q, want fake codex answer", output)
	}
}
