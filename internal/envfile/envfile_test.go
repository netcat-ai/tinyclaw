package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsDotEnvWithoutOverridingExistingEnv(t *testing.T) {
	t.Setenv("ENVFILE_EXISTING", "from-env")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(`
ENVFILE_PLAIN=value
ENVFILE_QUOTED="quoted value"
export ENVFILE_EXPORTED=exported
ENVFILE_EXISTING=from-file
`), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	if err := Load(path); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got := os.Getenv("ENVFILE_PLAIN"); got != "value" {
		t.Fatalf("ENVFILE_PLAIN = %q, want value", got)
	}
	if got := os.Getenv("ENVFILE_QUOTED"); got != "quoted value" {
		t.Fatalf("ENVFILE_QUOTED = %q, want quoted value", got)
	}
	if got := os.Getenv("ENVFILE_EXPORTED"); got != "exported" {
		t.Fatalf("ENVFILE_EXPORTED = %q, want exported", got)
	}
	if got := os.Getenv("ENVFILE_EXISTING"); got != "from-env" {
		t.Fatalf("ENVFILE_EXISTING = %q, want from-env", got)
	}
}
