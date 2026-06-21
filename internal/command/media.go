package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const defaultMediaURLTTL = 24 * time.Hour

type MediaStore interface {
	StoreGeneratedMedia(ctx context.Context, input StoreMediaInput) (StoredMedia, error)
}

type StoreMediaInput struct {
	MediaID  string
	Bytes    []byte
	MIMEType string
	Filename string
	TTL      time.Duration
}

type StoredMedia struct {
	URL       string
	URLKind   string
	ExpiresAt time.Time
}

func NewGeneratedMediaID(now time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("read random media id suffix: %w", err)
	}
	return "gm_" + now.UTC().Format("20060102") + "_" + strings.ToLower(hex.EncodeToString(suffix[:])), nil
}
