package executor

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tinyclaw/internal/command"
	"tinyclaw/internal/core"
)

const maxBackgroundArtifactBytes = 64 << 20

type MediaBackgroundArtifactStore struct {
	Media       command.MediaStore
	MediaURLTTL time.Duration
}

func (s MediaBackgroundArtifactStore) StoreBackgroundArtifact(ctx context.Context, artifact core.BackgroundArtifact) (core.GeneratedMediaOutput, error) {
	if s.Media == nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("generated media store is not configured")
	}
	path := strings.TrimSpace(artifact.Path)
	if path == "" {
		return core.GeneratedMediaOutput{}, fmt.Errorf("artifact path is required")
	}
	data, err := readBackgroundArtifactFile(path)
	if err != nil {
		return core.GeneratedMediaOutput{}, err
	}
	mimeType, err := backgroundArtifactMIMEType(artifact.MIMEType, data)
	if err != nil {
		return core.GeneratedMediaOutput{}, err
	}
	mediaID, err := command.NewGeneratedMediaID(time.Now().UTC())
	if err != nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("generate media id: %w", err)
	}
	stored, err := s.Media.StoreGeneratedMedia(ctx, command.StoreMediaInput{
		MediaID:  mediaID,
		Bytes:    data,
		MIMEType: mimeType,
		Filename: filepath.Base(path),
		TTL:      s.MediaURLTTL,
	})
	if err != nil {
		return core.GeneratedMediaOutput{}, err
	}
	return core.GeneratedMediaOutput{
		MediaID:      mediaID,
		MediaURL:     stored.URL,
		MediaURLKind: stored.URLKind,
		MIMEType:     mimeType,
		ExpiresAt:    stored.ExpiresAt,
	}, nil
}

func readBackgroundArtifactFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBackgroundArtifactBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("artifact is empty")
	}
	if len(data) > maxBackgroundArtifactBytes {
		return nil, fmt.Errorf("artifact exceeds max size")
	}
	return data, nil
}

func backgroundArtifactMIMEType(value string, data []byte) (string, error) {
	mimeType := strings.TrimSpace(value)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return "", fmt.Errorf("parse artifact mime type: %w", err)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if !strings.HasPrefix(mediaType, "image/") {
		return "", fmt.Errorf("artifact mime type must be image/*")
	}
	return mediaType, nil
}
