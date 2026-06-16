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

type ImageReferenceStore interface {
	LatestImageMessageBefore(ctx context.Context, roomID int64, beforeMessageID int64) (core.Message, error)
}

type SourceMediaFetcher interface {
	FetchMessageMedia(ctx context.Context, message core.Message) (SourceImage, error)
}

type ImageGenerationInput struct {
	Prompt       string
	Size         string
	SourceImages []SourceImage
}

type SourceImage struct {
	Bytes    []byte
	MIMEType string
	Filename string
}

type GeneratedImage struct {
	Bytes    []byte
	MIMEType string
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

type Handler struct {
	Store          DeliveryStore
	ReferenceStore ImageReferenceStore
	Image          ImageGenerator
	Media          MediaStore
	MediaFetcher   SourceMediaFetcher
	Enabled        bool
	ImageSize      string
	MediaURLTTL    time.Duration
	Async          bool
}

func NewHandler(store DeliveryStore, image ImageGenerator, media MediaStore) *Handler {
	handler := &Handler{
		Store:        store,
		Image:        image,
		Media:        media,
		MediaFetcher: HTTPMediaFetcher{},
		Enabled:      true,
		ImageSize:    defaultDrawImageSize,
		MediaURLTTL:  defaultMediaURLTTL,
		Async:        true,
	}
	if referenceStore, ok := store.(ImageReferenceStore); ok {
		handler.ReferenceStore = referenceStore
	}
	return handler
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
		Type    string `json:"type"`
		Text    any    `json:"text"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", false
	}
	if parsed.Type != "" && parsed.Type != "text" {
		return "", false
	}
	text := strings.TrimSpace(parsed.Content)
	switch value := parsed.Text.(type) {
	case string:
		if text == "" {
			text = strings.TrimSpace(value)
		}
	case map[string]any:
		if text == "" {
			if content, ok := value["content"].(string); ok {
				text = strings.TrimSpace(content)
			}
		}
	}
	if !strings.HasPrefix(text, "/draw") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "/draw")), true
}

func (h *Handler) run(ctx context.Context, message core.Message, prompt string) {
	if h == nil || h.Store == nil {
		return
	}
	prompt, sourceImageRequested := drawSourceImagePrompt(prompt)
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
	sourceImages, ok := h.sourceImages(ctx, message, sourceImageRequested)
	if !ok {
		return
	}
	image, err := h.Image.GenerateImage(ctx, ImageGenerationInput{Prompt: prompt, Size: size, SourceImages: sourceImages})
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

func drawSourceImagePrompt(prompt string) (string, bool) {
	text := strings.TrimSpace(prompt)
	for _, marker := range []string{"图生图", "基于上图", "参考上图", "编辑上图", "修改上图"} {
		if text == marker {
			return "", true
		}
		if strings.HasPrefix(text, marker) {
			return strings.TrimSpace(strings.TrimPrefix(text, marker)), true
		}
	}
	for _, phrase := range []string{"上图", "这张图", "这个图", "原图", "参考图"} {
		if strings.Contains(text, phrase) {
			return text, true
		}
	}
	return text, false
}

func (h *Handler) sourceImages(ctx context.Context, message core.Message, requested bool) ([]SourceImage, bool) {
	if !requested {
		return nil, true
	}
	if h.ReferenceStore == nil {
		h.createTextDelivery(ctx, message, KindCommandFailure, "图生图功能未配置")
		return nil, false
	}
	if h.MediaFetcher == nil {
		h.createTextDelivery(ctx, message, KindCommandFailure, "图片下载未配置")
		return nil, false
	}
	sourceMessage, err := h.ReferenceStore.LatestImageMessageBefore(ctx, message.RoomID, message.ID)
	if err != nil {
		h.createTextDelivery(ctx, message, KindCommandFailure, "没有找到可编辑的图片")
		return nil, false
	}
	image, err := h.MediaFetcher.FetchMessageMedia(ctx, sourceMessage)
	if err != nil {
		slog.Error("fetch source image failed", "message_id", message.ID, "source_message_id", sourceMessage.ID, "err", err)
		h.createTextDelivery(ctx, message, KindCommandFailure, "图片暂时无法下载，请稍后再试")
		return nil, false
	}
	if len(image.Bytes) == 0 {
		h.createTextDelivery(ctx, message, KindCommandFailure, "图片内容为空")
		return nil, false
	}
	return []SourceImage{image}, true
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

func (h *Handler) createFileDelivery(ctx context.Context, message core.Message, mediaID string, mimeType string, filename string, media StoredMedia) {
	payload := mustJSON(map[string]any{
		"kind":           KindCommandOutput,
		"type":           "file",
		"media_id":       mediaID,
		"media_url":      media.URL,
		"media_url_kind": media.URLKind,
		"mime_type":      mimeType,
		"filename":       filename,
		"expires_at":     media.ExpiresAt.UTC().Format(time.RFC3339),
	})
	if _, err := h.Store.CreateCommandDelivery(ctx, message, payload); err != nil {
		slog.Error("create command file delivery failed", "message_id", message.ID, "media_id", mediaID, "err", err)
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
