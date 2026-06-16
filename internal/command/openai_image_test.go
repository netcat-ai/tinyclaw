package command

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
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

func TestOpenAIImageClientEditsImageWithMultipart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("path = %q, want /v1/images/edits", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Fatalf("content-type = %q, want multipart", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if r.FormValue("model") != "gpt-image-2" || r.FormValue("prompt") != "make it watercolor" || r.FormValue("size") != "1024x1024" {
			t.Fatalf("form values model=%q prompt=%q size=%q", r.FormValue("model"), r.FormValue("prompt"), r.FormValue("size"))
		}
		files := r.MultipartForm.File["image[]"]
		if len(files) != 1 || files[0].Filename != "source.jpg" {
			t.Fatalf("files = %+v, want source.jpg", files)
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatalf("open multipart file: %v", err)
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read multipart file: %v", err)
		}
		if string(data) != "\xff\xd8\xff" {
			t.Fatalf("multipart file bytes = %q", string(data))
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}))
	}))
	defer server.Close()

	client := OpenAIImageClient{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-2"}
	image, err := client.GenerateImage(context.Background(), ImageGenerationInput{
		Prompt: "make it watercolor",
		Size:   "1024x1024",
		SourceImages: []SourceImage{{
			Bytes:    []byte{0xff, 0xd8, 0xff},
			MIMEType: "image/jpeg",
			Filename: "source.jpg",
		}},
	})
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
