package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"tinyclaw/internal/core"
)

func TestHTTPMediaFetcherFetchesInternalMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/media" || r.URL.Query().Get("msgid") != "42" {
			t.Fatalf("url = %s, want /internal/media?msgid=42", r.URL.String())
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
	}))
	defer server.Close()

	fetcher := HTTPMediaFetcher{BaseURL: server.URL}
	image, err := fetcher.FetchMessageMedia(context.Background(), core.Message{ID: 42})
	if err != nil {
		t.Fatalf("FetchMessageMedia error: %v", err)
	}
	if string(image.Bytes) != "\xff\xd8\xff" || image.MIMEType != "image/jpeg" || image.Filename != "message-42.jpg" {
		t.Fatalf("image = %+v, want jpeg message filename", image)
	}
}

func TestHTTPMediaFetcherReportsHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	fetcher := HTTPMediaFetcher{BaseURL: server.URL}
	_, err := fetcher.FetchMessageMedia(context.Background(), core.Message{ID: 42})
	if err == nil {
		t.Fatal("error = nil, want status error")
	}
}
