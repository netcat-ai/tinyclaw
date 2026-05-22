package command

import (
	"testing"
	"time"
)

func TestGeneratedMediaObjectKeyUsesDateAndMediaID(t *testing.T) {
	got := generatedMediaObjectKey(time.Date(2026, 5, 22, 1, 2, 3, 0, time.UTC), "gm_20260522_7f3a9c")
	want := "generated-media/2026/05/22/gm_20260522_7f3a9c.png"
	if got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}

func TestParseS3Endpoint(t *testing.T) {
	for _, test := range []struct {
		raw      string
		endpoint string
		secure   bool
	}{
		{raw: "https://s3.example.com", endpoint: "s3.example.com", secure: true},
		{raw: "http://minio.local:9000", endpoint: "minio.local:9000", secure: false},
		{raw: "r2.example.com", endpoint: "r2.example.com", secure: true},
	} {
		gotEndpoint, gotSecure, err := parseS3Endpoint(test.raw)
		if err != nil {
			t.Fatalf("parseS3Endpoint(%q) error: %v", test.raw, err)
		}
		if gotEndpoint != test.endpoint || gotSecure != test.secure {
			t.Fatalf("parseS3Endpoint(%q) = %q,%v want %q,%v", test.raw, gotEndpoint, gotSecure, test.endpoint, test.secure)
		}
	}
}
