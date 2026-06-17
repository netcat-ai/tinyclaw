package command

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

func TestNormalizeGeneratedImageToJPEG(t *testing.T) {
	normalized, err := NormalizeGeneratedImageToJPEG(GeneratedImage{
		Bytes:    validGeneratedPNGForTest(),
		MIMEType: "image/png",
	})
	if err != nil {
		t.Fatalf("NormalizeGeneratedImageToJPEG error: %v", err)
	}
	if normalized.MIMEType != "image/jpeg" {
		t.Fatalf("mime type = %q, want image/jpeg", normalized.MIMEType)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(normalized.Bytes))
	if err != nil {
		t.Fatalf("decode jpeg: %v", err)
	}
	if decoded.Bounds() != image.Rect(0, 0, 1, 1) {
		t.Fatalf("bounds = %v, want 1x1", decoded.Bounds())
	}
}

func TestNormalizeGeneratedImageToJPEGRejectsInvalidImage(t *testing.T) {
	_, err := NormalizeGeneratedImageToJPEG(GeneratedImage{Bytes: []byte("not an image")})
	if err == nil {
		t.Fatal("NormalizeGeneratedImageToJPEG error = nil, want decode error")
	}
}
