package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithAdminUIServesSPAForAdminDeepLinks(t *testing.T) {
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<main>admin app</main>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	handler := withAdminUI(http.NotFoundHandler(), distDir)
	req := httptest.NewRequest(http.MethodGet, "/admin/rooms/10", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "admin app") {
		t.Fatalf("body = %q, want index fallback", rec.Body.String())
	}
}

func TestWithAdminUIKeepsMissingAssetsAsNotFound(t *testing.T) {
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<main>admin app</main>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	handler := withAdminUI(http.NotFoundHandler(), distDir)
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/missing.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
