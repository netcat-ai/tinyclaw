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
	AgentSessionID:      100,
	RoomID:              10,
	SourceMessageFromID: 1,
	SourceMessageToID:   2,
}

func TestBuildCodexPromptIncludesContextMessages(t *testing.T) {
	prompt := BuildCodexPrompt(AgentRunRequest{
		AgentRun:          testAgentRun,
		MemorySearchURL:   "http://127.0.0.1:8081/internal/memory/search",
		MemorySearchToken: "token-1",
		ContextMessages: []core.Message{
			{ID: 1, SenderName: "Alice", MsgType: "text", Body: []byte(`{"content":"hello"}`)},
			{ID: 2, SenderID: "bob", MsgType: "text", Body: []byte(`{"content":"@agent help"}`)},
		},
	})

	for _, want := range []string{
		"只返回一个符合 Agent Run Result 的 JSON 对象",
		"memory_search_requests",
		"background_codex_tasks",
		"不要包含 room_id",
		"Context messages (JSONL):",
		`"kind":"capabilities"`,
		`"memory_search":true`,
		`"id":1`,
		`"sender":"Alice"`,
		`"text":{"content":"hello"}`,
		`"id":2`,
		`"sender":"bob"`,
		`"text":{"content":"@agent help"}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		`"run_context"`,
		`"agent_session_id"`,
		`"room_id"`,
		`"message_window"`,
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt has redundant context field %q:\n%s", unwanted, prompt)
		}
	}
}

func TestBuildCodexPromptIncludesImageMediaRule(t *testing.T) {
	prompt := BuildCodexPrompt(AgentRunRequest{
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{ID: 42, SenderName: "Alice", MsgType: "image", Body: []byte(`{"content":"[图片]"}`)},
		},
	})

	for _, want := range []string{
		`"id":42`,
		`"sender":"Alice"`,
		`"type":"image"`,
		`"image":{"content":"[图片]"}`,
		"下方输入是 JSONL",
		"带 kind 的行是 typed context messages",
		"房间消息的 type 是 image",
		"使用房间消息顶层 id 作为 message_id",
		"主回复阶段不要直接生成或编辑图片",
		"媒体：",
		`curl -L "$TINYCLAW_MEDIA_BASE_URL/internal/media?msgid=$message_id"`,
		`"$TINYCLAW_MEDIA_DOWNLOAD_DIR/$message_id"`,
		"只下载对应消息",
		"读取下载后的本地文件",
		"后台任务：",
		"返回 background_codex_tasks",
		`"instruction":"..."`,
		`"source_message_ids":[42]`,
		`"expected_artifacts":["image/jpeg"]`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCodexPromptIncludesRoomPrompt(t *testing.T) {
	run := testAgentRun
	run.RoomPrompt = "Default to short replies for this room."
	prompt := BuildCodexPrompt(AgentRunRequest{
		AgentRun: run,
		ContextMessages: []core.Message{
			{ID: 1, SenderName: "Alice", MsgType: "text", Body: []byte(`{"content":"hello"}`)},
		},
	})

	for _, want := range []string{
		`"kind":"room_prompt"`,
		`"content":"Default to short replies for this room."`,
		"Context messages (JSONL):",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCodexPromptIncludesReferencedImageMetadata(t *testing.T) {
	prompt := BuildCodexPrompt(AgentRunRequest{
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{
				ID:         43,
				SenderName: "Alice",
				MsgType:    "text",
				Body:       []byte(`{"content":"edit this image","quote":{"msgtype":"image","from":"Bob","msgid":"132","image":{"content":"[图片]"}}}`),
			},
		},
	})

	for _, want := range []string{
		`"id":43`,
		`"text":{"content":"edit this image","quote":{"msgtype":"image","from":"Bob","msgid":"132","image":{"content":"[图片]"}}}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCodexBackgroundTaskPromptIncludesMediaReuseRule(t *testing.T) {
	prompt := BuildCodexBackgroundTaskPrompt(AgentRunRequest{
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{ID: 42, SenderName: "Alice", MsgType: "image", Body: []byte(`{"content":"[图片]"}`)},
		},
	}, core.BackgroundCodexTask{
		Instruction:       "把图片改成水彩风格",
		SourceMessageIDs:  []int64{42},
		ExpectedArtifacts: []string{"image/jpeg"},
	})

	for _, want := range []string{
		"你是 TinyClaw 后台 Codex Task",
		`"instruction":"把图片改成水彩风格"`,
		`"source_message_ids":[42]`,
		`"$TINYCLAW_MEDIA_DOWNLOAD_DIR/$message_id"`,
		`curl -L "$TINYCLAW_MEDIA_BASE_URL/internal/media?msgid=$message_id"`,
		`"$TINYCLAW_TASK_OUTPUT_DIR"`,
		"Context messages (JSONL):",
		`"id":42`,
		`"type":"image"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCodexPromptIncludesRunScopedSubagents(t *testing.T) {
	prompt := BuildCodexPrompt(AgentRunRequest{
		AgentRun: testAgentRun,
		SelectedAgents: []core.Agent{{
			Key:         "product",
			DisplayName: "Product",
			Description: "Clarifies requirements.",
			Prompt:      "Focus on scope and tradeoffs.",
			Enabled:     true,
		}},
		ContextMessages: []core.Message{
			{ID: 2, SenderID: "bob", MsgType: "text", Body: []byte(`{"content":"@product 看下这个方案"}`), Payload: []byte(`{"content":"@product 看下这个方案"}`)},
		},
	})

	for _, want := range []string{
		"selected_agents",
		`"key":"product"`,
		`"display_name":"Product"`,
		`"description":"Clarifies requirements."`,
		`"prompt":"Focus on scope and tradeoffs."`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCodexExecArgsIncludesOpenAIBaseURL(t *testing.T) {
	runner := NewCodexRunner(CodexRunnerConfig{
		WorkDir:       "/workspace",
		OpenAIBaseURL: "https://code.v4.chat",
	})

	args := strings.Join(runner.codexExecArgs("/tmp/schema.json", "/tmp/output.txt", ""), "\n")
	if !strings.Contains(args, "-c\nopenai_base_url=\"https://code.v4.chat\"") {
		t.Fatalf("args missing openai_base_url config:\n%s", args)
	}
}

func TestCodexRunnerUsesOutputLastMessage(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	argsPath := filepath.Join(dir, "args")
	script := `#!/bin/sh
output=""
printf '%s\n' "$@" > "` + argsPath + `"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
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
	if result.CodexSessionID != "thread-1" {
		t.Fatalf("codex session id = %q, want thread-1", result.CodexSessionID)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsData)
	for _, want := range []string{"--disable\napps\n", "--disable\ntool_suggest\n", "--disable\nplugins\n", "exec\n", "--json\n", "--output-schema\n"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--ephemeral\n") {
		t.Fatalf("args include --ephemeral:\n%s", args)
	}
}

func TestCodexRunnerLeavesImageMessagesInPrompt(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	argsPath := filepath.Join(dir, "args")
	promptPath := filepath.Join(dir, "prompt")
	envPath := filepath.Join(dir, "media_env")
	script := `#!/bin/sh
output=""
printf '%s\n' "$@" > "` + argsPath + `"
{
  printf '%s\n' "$TINYCLAW_MEDIA_BASE_URL"
  printf '%s\n' "$TINYCLAW_MEDIA_DOWNLOAD_DIR"
} > "` + envPath + `"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat > "` + promptPath + `"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf "image answer" > "$output"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:          bin,
		WorkDir:      dir,
		MediaBaseURL: "http://media.example",
		Timeout:      5 * time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{ID: 42, SenderName: "Alice", MsgType: "image", Body: []byte(`{"content":"[图片]"}`)},
		},
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if result.FinalOutput != "image answer" {
		t.Fatalf("output = %q, want image answer", result.FinalOutput)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsData)
	if strings.Contains(args, "--image\n") {
		t.Fatalf("args include image attachment:\n%s", args)
	}
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	prompt := string(promptData)
	if !strings.Contains(prompt, `"image":{"content":"[图片]"}`) {
		t.Fatalf("prompt missing image metadata:\n%s", promptData)
	}
	if !strings.Contains(prompt, `curl -L "$TINYCLAW_MEDIA_BASE_URL/internal/media?msgid=$message_id"`) {
		t.Fatalf("prompt missing media env curl command:\n%s", promptData)
	}
	if !strings.Contains(prompt, `"$TINYCLAW_MEDIA_DOWNLOAD_DIR/$message_id"`) {
		t.Fatalf("prompt missing media download dir env:\n%s", promptData)
	}
	if strings.Contains(prompt, "http://media.example") {
		t.Fatalf("prompt should not inline configured media URL:\n%s", promptData)
	}
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read media env: %v", err)
	}
	envLines := strings.Split(strings.TrimSpace(string(envData)), "\n")
	if len(envLines) != 2 {
		t.Fatalf("media env lines = %q, want base URL and download dir", envData)
	}
	if envLines[0] != "http://media.example" {
		t.Fatalf("media base env = %q, want configured media base URL", envLines[0])
	}
	if envLines[1] != "/tmp/tinyclaw/10" {
		t.Fatalf("media download dir env = %q, want room download dir", envLines[1])
	}
}

func TestCodexRunnerDoesNotFetchImageAttachments(t *testing.T) {
	mediaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected image fetch")
	}))
	defer mediaServer.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	argsPath := filepath.Join(dir, "args")
	script := `#!/bin/sh
output=""
printf '%s\n' "$@" > "` + argsPath + `"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf "fallback answer" > "$output"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:          bin,
		WorkDir:      dir,
		MediaBaseURL: mediaServer.URL,
		Timeout:      5 * time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{ID: 42, SenderName: "Alice", MsgType: "image", Body: []byte(`{"content":"[图片]"}`)},
		},
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if result.FinalOutput != "fallback answer" {
		t.Fatalf("output = %q, want fallback answer", result.FinalOutput)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if strings.Contains(string(argsData), "--image\n") {
		t.Fatalf("args include image attachment:\n%s", argsData)
	}
}

func TestCodexRunnerRunsBackgroundCodexTask(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	envPath := filepath.Join(dir, "env")
	promptPath := filepath.Join(dir, "prompt")
	script := `#!/bin/sh
output=""
{
  printf '%s\n' "$TINYCLAW_MEDIA_BASE_URL"
  printf '%s\n' "$TINYCLAW_MEDIA_DOWNLOAD_DIR"
  printf '%s\n' "$TINYCLAW_TASK_OUTPUT_DIR"
} > "` + envPath + `"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat > "` + promptPath + `"
printf 'fake image bytes' > "$TINYCLAW_TASK_OUTPUT_DIR/result.jpg"
printf '%s' '{"final_output":"done","artifacts":[{"path":"result.jpg","mime_type":"image/jpeg"}]}' > "$output"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:          bin,
		WorkDir:      dir,
		MediaBaseURL: "http://media.example",
		Timeout:      5 * time.Second,
	})

	result, err := runner.RunBackgroundCodexTask(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
		ContextMessages: []core.Message{
			{ID: 42, SenderName: "Alice", MsgType: "image", Body: []byte(`{"content":"[图片]"}`)},
		},
	}, core.BackgroundCodexTask{
		Instruction:       "生成图片",
		SourceMessageIDs:  []int64{42},
		ExpectedArtifacts: []string{"image/jpeg"},
	})
	if err != nil {
		t.Fatalf("RunBackgroundCodexTask error: %v", err)
	}
	defer func() { _ = os.RemoveAll(result.OutputDir) }()
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v, want one", result.Artifacts)
	}
	if result.Artifacts[0].MIMEType != "image/jpeg" {
		t.Fatalf("artifact mime = %q", result.Artifacts[0].MIMEType)
	}
	if !filepath.IsAbs(result.Artifacts[0].Path) {
		t.Fatalf("artifact path = %q, want absolute", result.Artifacts[0].Path)
	}
	if _, err := os.Stat(result.Artifacts[0].Path); err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	envLines := strings.Split(strings.TrimSpace(string(envData)), "\n")
	if len(envLines) != 3 {
		t.Fatalf("env lines = %q", envData)
	}
	if envLines[0] != "http://media.example" || envLines[1] != "/tmp/tinyclaw/10" {
		t.Fatalf("env = %q", envData)
	}
	expectedOutputRoot := filepath.Join(os.TempDir(), "tinyclaw", "tasks", "10") + string(filepath.Separator)
	if !strings.HasPrefix(envLines[2], expectedOutputRoot) {
		t.Fatalf("task output dir = %q", envLines[2])
	}
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if !strings.Contains(string(promptData), `"instruction":"生成图片"`) {
		t.Fatalf("prompt missing task JSON:\n%s", promptData)
	}
}

func TestCodexRunnerReturnsBackgroundCodexTasks(t *testing.T) {
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
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s' '{"final_output":"开始生成图片","memory_search_requests":[],"memory_write_proposals":[],"background_codex_tasks":[{"instruction":"画一朵花","source_message_ids":[],"expected_artifacts":["image/jpeg"]}]}' > "$output"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{AgentRun: testAgentRun})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if result.FinalOutput != "开始生成图片" {
		t.Fatalf("output = %q", result.FinalOutput)
	}
	if result.BackgroundTaskCount != 1 || len(result.BackgroundCodexTasks) != 1 {
		t.Fatalf("background task count=%d tasks=%+v", result.BackgroundTaskCount, result.BackgroundCodexTasks)
	}
	if result.BackgroundCodexTasks[0].Instruction != "画一朵花" {
		t.Fatalf("task instruction = %q", result.BackgroundCodexTasks[0].Instruction)
	}
}

func TestCodexRunnerAllowsBackgroundCodexTasksWithoutTool(t *testing.T) {
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
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s' '{"final_output":"开始生成图片","memory_search_requests":[],"memory_write_proposals":[],"background_codex_tasks":[{"instruction":"画一朵花","source_message_ids":[],"expected_artifacts":["image/jpeg"]}]}' > "$output"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{AgentRun: testAgentRun})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if len(result.BackgroundCodexTasks) != 1 {
		t.Fatalf("background tasks = %+v, want one task", result.BackgroundCodexTasks)
	}
}

func TestCodexRunnerResumesCodexSession(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	argsPath := filepath.Join(dir, "args")
	script := `#!/bin/sh
output=""
printf '%s\n' "$@" > "` + argsPath + `"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-existing"}'
printf "resumed answer" > "$output"
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
		AgentRun: core.AgentRun{
			AgentSessionID:      100,
			RoomID:              10,
			CodexSessionID:      "thread-existing",
			SourceMessageFromID: 0,
			SourceMessageToID:   2,
		},
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if result.FinalOutput != "resumed answer" {
		t.Fatalf("output = %q, want resumed answer", result.FinalOutput)
	}
	if result.CodexSessionID != "thread-existing" {
		t.Fatalf("codex session id = %q, want thread-existing", result.CodexSessionID)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsData)
	for _, want := range []string{"exec\n", "resume\n", "thread-existing\n"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--output-schema\n") {
		t.Fatalf("resume args include unsupported output schema:\n%s", args)
	}
}

func TestCodexRunnerFallsBackWhenResumeRolloutIsMissing(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	countPath := filepath.Join(dir, "count")
	argsPath := filepath.Join(dir, "args")
	script := fmt.Sprintf(`#!/bin/sh
output=""
printf '%%s\n' '---' "$@" >> %[1]q
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  shift
done
cat >/dev/null
count=0
if [ -f %[2]q ]; then
  count=$(cat %[2]q)
fi
count=$((count + 1))
printf "%%s" "$count" > %[2]q
if [ "$count" -eq 1 ]; then
  echo 'Error: thread/resume: thread/resume failed: no rollout found for thread id thread-stale (code -32600)' >&2
  exit 1
fi
printf '%%s\n' '{"type":"thread.started","thread_id":"thread-fresh"}'
printf "fresh answer" > "$output"
`, argsPath, countPath)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})

	result, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: core.AgentRun{
			AgentSessionID:      100,
			RoomID:              10,
			CodexSessionID:      "thread-stale",
			SourceMessageFromID: 0,
			SourceMessageToID:   2,
		},
	})
	if err != nil {
		t.Fatalf("RunAgent error: %v", err)
	}
	if result.FinalOutput != "fresh answer" {
		t.Fatalf("output = %q, want fresh answer", result.FinalOutput)
	}
	if result.CodexSessionID != "thread-fresh" {
		t.Fatalf("codex session id = %q, want thread-fresh", result.CodexSessionID)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsData)
	if !strings.Contains(args, "resume\nthread-stale\n") {
		t.Fatalf("args missing initial stale resume:\n%s", args)
	}
	if !strings.Contains(args, "--output-schema\n") {
		t.Fatalf("args missing fresh output schema retry:\n%s", args)
	}
}

func TestCodexRunnerFailsOnCodexErrorEvent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"turn.failed","error":{"message":"bad schema"}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})

	_, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
	})
	if err == nil || !strings.Contains(err.Error(), "bad schema") {
		t.Fatalf("RunAgent error = %v, want bad schema", err)
	}
}

func TestCodexRunnerAcceptsCompletedAgentMessageWhenExitCodeIsNonZero(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\"final_output\":\"你好呀！\",\"memory_write_proposals\":[],\"memory_search_requests\":[]}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
exit 1
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
	if result.FinalOutput != "你好呀！" {
		t.Fatalf("output = %q, want 你好呀！", result.FinalOutput)
	}
	if result.CodexSessionID != "thread-1" {
		t.Fatalf("codex session id = %q, want thread-1", result.CodexSessionID)
	}
}

func TestCodexRunnerAcceptsResultAfterTransientErrorEvent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"error","message":"Reconnecting... 1/5 (unexpected status 502 Bad Gateway)"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\"final_output\":\"E2E_OK\",\"memory_write_proposals\":[],\"memory_search_requests\":[]}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
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
	if result.FinalOutput != "E2E_OK" {
		t.Fatalf("output = %q, want E2E_OK", result.FinalOutput)
	}
}

func TestCodexRunnerFailsOnEmptyCodexOutput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	runner := NewCodexRunner(CodexRunnerConfig{
		Bin:     bin,
		WorkDir: dir,
		Timeout: 5 * time.Second,
	})

	_, err := runner.RunAgent(context.Background(), AgentRunRequest{
		AgentRun: testAgentRun,
	})
	if err == nil || !strings.Contains(err.Error(), "codex output is empty") {
		t.Fatalf("RunAgent error = %v, want empty output error", err)
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
	if result.MemorySearchCount != 1 {
		t.Fatalf("memory search count = %d, want 1", result.MemorySearchCount)
	}
}

func TestCodexRunnerContinuesWhenMemorySearchFails(t *testing.T) {
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
  printf '%%s' '{"final_output":"","memory_search_requests":[{"query":"prior decision","types":["fact"],"limit":5,"include_inactive":false}],"memory_write_proposals":[]}' > "$output"
else
  if ! grep -q '"error"' %[1]q; then
    echo "missing memory search error result" >&2
    exit 1
  fi
  printf '%%s' '{"final_output":"暂时无法读取记忆，我先继续回答。","memory_search_requests":[],"memory_write_proposals":[]}' > "$output"
fi
`, promptPath, countPath)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "memory backend unavailable", http.StatusServiceUnavailable)
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
	if result.FinalOutput != "暂时无法读取记忆，我先继续回答。" {
		t.Fatalf("output = %q", result.FinalOutput)
	}
	if result.MemorySearchCount != 1 {
		t.Fatalf("memory search count = %d, want 1", result.MemorySearchCount)
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
			AgentSessionID:      100,
			RoomID:              10,
			SourceMessageFromID: 0,
			SourceMessageToID:   1,
		},
		MemorySearchURL:   server.URL + "/internal/memory/search",
		MemorySearchToken: "memory-token",
		ContextMessages: []core.Message{{
			ID:         1,
			SenderName: "Alice",
			MsgType:    "text",
			Body:       []byte(`{"content":"Use memory_search_requests first, then answer with my reply language preference from Room Memory."}`),
			Payload:    []byte(`{"content":"Use memory_search_requests first, then answer with my reply language preference from Room Memory."}`),
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
