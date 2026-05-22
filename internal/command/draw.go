package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

const (
	KindCommandProgress = "command_progress"
	KindCommandOutput   = "command_output"
	KindCommandFailure  = "command_failure"

	defaultDrawImageSize = "1024x1024"
	defaultMediaURLTTL   = 24 * time.Hour
)

type DeliveryStore interface {
	CreateCommandDelivery(ctx context.Context, message core.Message, payload json.RawMessage) (*core.Delivery, error)
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, input ImageGenerationInput) (GeneratedImage, error)
}

type MediaStore interface {
	StoreGeneratedMedia(ctx context.Context, input StoreMediaInput) (StoredMedia, error)
}

type ImageGenerationInput struct {
	Prompt string
	Size   string
}

type GeneratedImage struct {
	Bytes    []byte
	MIMEType string
}

type StoreMediaInput struct {
	MediaID  string
	Bytes    []byte
	MIMEType string
	TTL      time.Duration
}

type StoredMedia struct {
	URL       string
	URLKind   string
	ExpiresAt time.Time
}

type Handler struct {
	Store       DeliveryStore
	Image       ImageGenerator
	Media       MediaStore
	Enabled     bool
	ImageSize   string
	MediaURLTTL time.Duration
	Async       bool
}

func NewHandler(store DeliveryStore, image ImageGenerator, media MediaStore) *Handler {
	return &Handler{
		Store:       store,
		Image:       image,
		Media:       media,
		Enabled:     true,
		ImageSize:   defaultDrawImageSize,
		MediaURLTTL: defaultMediaURLTTL,
		Async:       true,
	}
}

func (h *Handler) HandleMessage(ctx context.Context, message core.Message) bool {
	prompt, ok := DrawPrompt(message.Payload)
	if !ok {
		return false
	}
	if h.Async {
		go h.run(context.Background(), message, prompt)
		return true
	}
	h.run(ctx, message, prompt)
	return true
}

func IsDrawPayload(payload json.RawMessage) bool {
	_, ok := DrawPrompt(payload)
	return ok
}

func DrawPrompt(payload json.RawMessage) (string, bool) {
	var parsed struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", false
	}
	if parsed.Type != "text" {
		return "", false
	}
	text := strings.TrimSpace(parsed.Text)
	if !strings.HasPrefix(text, "/draw") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "/draw")), true
}

func (h *Handler) run(ctx context.Context, message core.Message, prompt string) {
	if h == nil || h.Store == nil {
		return
	}
	if strings.TrimSpace(prompt) == "" {
		h.createTextDelivery(ctx, message, KindCommandFailure, "请在 /draw 后面描述要画什么")
		return
	}
	if !h.Enabled {
		h.createTextDelivery(ctx, message, KindCommandFailure, "画图功能未启用")
		return
	}
	if h.Image == nil {
		h.createTextDelivery(ctx, message, KindCommandFailure, "画图功能未配置")
		return
	}
	if h.Media == nil {
		h.createTextDelivery(ctx, message, KindCommandFailure, "画图存储未配置")
		return
	}

	mediaID, err := newGeneratedMediaID(time.Now().UTC())
	if err != nil {
		slog.Error("generate media id failed", "message_id", message.ID, "err", err)
		h.createTextDelivery(ctx, message, KindCommandFailure, "画图失败，请稍后再试")
		return
	}
	h.createTextDelivery(ctx, message, KindCommandProgress, "正在画图...")

	size := strings.TrimSpace(h.ImageSize)
	if size == "" {
		size = defaultDrawImageSize
	}
	image, err := h.Image.GenerateImage(ctx, ImageGenerationInput{Prompt: prompt, Size: size})
	if err != nil {
		slog.Error("draw image generation failed", "message_id", message.ID, "media_id", mediaID, "err", err)
		h.createTextDelivery(ctx, message, KindCommandFailure, "画图失败，请稍后再试")
		return
	}
	mimeType := strings.TrimSpace(image.MIMEType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	ttl := h.MediaURLTTL
	if ttl <= 0 {
		ttl = defaultMediaURLTTL
	}
	stored, err := h.Media.StoreGeneratedMedia(ctx, StoreMediaInput{
		MediaID:  mediaID,
		Bytes:    image.Bytes,
		MIMEType: mimeType,
		TTL:      ttl,
	})
	if err != nil {
		slog.Error("store generated media failed", "message_id", message.ID, "media_id", mediaID, "err", err)
		h.createTextDelivery(ctx, message, KindCommandFailure, "画图失败，请稍后再试")
		return
	}
	h.createTextDelivery(ctx, message, KindCommandOutput, "图片已生成："+mediaID)
	h.createImageDelivery(ctx, message, mediaID, mimeType, stored)
}

func (h *Handler) createTextDelivery(ctx context.Context, message core.Message, kind string, text string) {
	payload := mustJSON(map[string]any{
		"kind": kind,
		"type": "text",
		"text": text,
	})
	if _, err := h.Store.CreateCommandDelivery(ctx, message, payload); err != nil {
		slog.Error("create command text delivery failed", "message_id", message.ID, "kind", kind, "err", err)
	}
}

func (h *Handler) createImageDelivery(ctx context.Context, message core.Message, mediaID string, mimeType string, media StoredMedia) {
	payload := mustJSON(map[string]any{
		"kind":           KindCommandOutput,
		"type":           "image",
		"media_id":       mediaID,
		"media_url":      media.URL,
		"media_url_kind": media.URLKind,
		"mime_type":      mimeType,
		"expires_at":     media.ExpiresAt.UTC().Format(time.RFC3339),
	})
	if _, err := h.Store.CreateCommandDelivery(ctx, message, payload); err != nil {
		slog.Error("create command image delivery failed", "message_id", message.ID, "media_id", mediaID, "err", err)
	}
}

func newGeneratedMediaID(now time.Time) (string, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate random media id: %w", err)
	}
	return "gm_" + now.Format("20060102") + "_" + hex.EncodeToString(raw[:]), nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
