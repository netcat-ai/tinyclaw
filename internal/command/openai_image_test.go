package command

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIImageClientGeneratesPNGFromB64JSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %q, want /v1/images/generations", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}))
	}))
	defer server.Close()

	client := OpenAIImageClient{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-2"}
	image, err := client.GenerateImage(context.Background(), ImageGenerationInput{Prompt: "flower", Size: "1024x1024"})
	if err != nil {
		t.Fatalf("GenerateImage error: %v", err)
	}
	if string(image.Bytes) != "\x89PNG" || image.MIMEType != "image/png" {
		t.Fatalf("image = %+v", image)
	}
}

func TestOpenAIImageClientRejectsMissingB64JSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{}]}`))
	}))
	defer server.Close()

	client := OpenAIImageClient{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-2"}
	_, err := client.GenerateImage(context.Background(), ImageGenerationInput{Prompt: "flower"})
	if err == nil || !strings.Contains(err.Error(), "missing b64_json") {
		t.Fatalf("error = %v, want missing b64_json", err)
	}
}

func TestOpenAIImageClientReportsUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	client := OpenAIImageClient{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-2"}
	_, err := client.GenerateImage(context.Background(), ImageGenerationInput{Prompt: "flower"})
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("error = %v, want status 502", err)
	}
}
