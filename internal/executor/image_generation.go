package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tinyclaw/internal/command"
	"tinyclaw/internal/core"
)

const (
	defaultAgentImageSize         = "1024x1024"
	defaultAgentGeneratedMediaTTL = 24 * time.Hour
	maxAgentSourceImages          = 4
)

type ImageGenerationTool interface {
	GenerateAgentImage(ctx context.Context, run AgentRunRequest, request core.ImageGenerationRequest) (core.GeneratedMediaOutput, error)
}

type AgentImageSourceMessageStore interface {
	GetCoreMessageByID(ctx context.Context, id int64) (core.Message, error)
}

type AgentImageTool struct {
	Image              command.ImageGenerator
	Media              command.MediaStore
	MediaFetcher       command.SourceMediaFetcher
	SourceMessageStore AgentImageSourceMessageStore
	ImageSize          string
	MediaURLTTL        time.Duration
}

func (t AgentImageTool) GenerateAgentImage(ctx context.Context, run AgentRunRequest, request core.ImageGenerationRequest) (core.GeneratedMediaOutput, error) {
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return core.GeneratedMediaOutput{}, fmt.Errorf("image prompt is required")
	}
	if t.Image == nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("image generation is not configured")
	}
	if t.Media == nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("generated media store is not configured")
	}
	sourceImages, err := t.sourceImages(ctx, run, request.SourceMessageIDs)
	if err != nil {
		return core.GeneratedMediaOutput{}, err
	}
	size := strings.TrimSpace(request.Size)
	if size == "" {
		size = strings.TrimSpace(t.ImageSize)
	}
	if size == "" {
		size = defaultAgentImageSize
	}
	mediaID, err := command.NewGeneratedMediaID(time.Now().UTC())
	if err != nil {
		return core.GeneratedMediaOutput{}, err
	}
	image, err := t.Image.GenerateImage(ctx, command.ImageGenerationInput{
		Prompt:       prompt,
		Size:         size,
		SourceImages: sourceImages,
	})
	if err != nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("generate image: %w", err)
	}
	image, err = command.NormalizeGeneratedImageToJPEG(image)
	if err != nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("normalize generated image: %w", err)
	}
	mimeType := image.MIMEType
	ttl := t.MediaURLTTL
	if ttl <= 0 {
		ttl = defaultAgentGeneratedMediaTTL
	}
	stored, err := t.Media.StoreGeneratedMedia(ctx, command.StoreMediaInput{
		MediaID:  mediaID,
		Bytes:    image.Bytes,
		MIMEType: mimeType,
		TTL:      ttl,
	})
	if err != nil {
		return core.GeneratedMediaOutput{}, fmt.Errorf("store generated media: %w", err)
	}
	return core.GeneratedMediaOutput{
		MediaID:      mediaID,
		MediaURL:     stored.URL,
		MediaURLKind: stored.URLKind,
		MIMEType:     mimeType,
		ExpiresAt:    stored.ExpiresAt,
	}, nil
}

func (t AgentImageTool) sourceImages(ctx context.Context, run AgentRunRequest, ids []int64) ([]command.SourceImage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxAgentSourceImages {
		return nil, fmt.Errorf("too many source images: %d", len(ids))
	}
	if t.MediaFetcher == nil {
		return nil, fmt.Errorf("source image fetcher is not configured")
	}
	imagesByID := make(map[int64]core.Message, len(run.ContextMessages))
	for _, message := range run.ContextMessages {
		if message.ID > 0 && isAgentSourceImageMessage(message) {
			imagesByID[message.ID] = message
		}
	}
	sourceImages := make([]command.SourceImage, 0, len(ids))
	for _, id := range ids {
		message, err := t.sourceImageMessage(ctx, run, imagesByID, id)
		if err != nil {
			return nil, err
		}
		image, err := t.MediaFetcher.FetchMessageMedia(ctx, message)
		if err != nil {
			return nil, fmt.Errorf("fetch source image %d: %w", id, err)
		}
		if len(image.Bytes) == 0 {
			return nil, fmt.Errorf("source image %d is empty", id)
		}
		sourceImages = append(sourceImages, image)
	}
	return sourceImages, nil
}

func (t AgentImageTool) sourceImageMessage(ctx context.Context, run AgentRunRequest, contextImages map[int64]core.Message, id int64) (core.Message, error) {
	if message, ok := contextImages[id]; ok {
		return message, nil
	}
	if t.SourceMessageStore == nil {
		return core.Message{}, fmt.Errorf("source image message %d is not in the current agent context", id)
	}
	message, err := t.SourceMessageStore.GetCoreMessageByID(ctx, id)
	if err != nil {
		return core.Message{}, fmt.Errorf("get source image message %d: %w", id, err)
	}
	if message.RoomID != run.AgentRun.RoomID {
		return core.Message{}, fmt.Errorf("source image message %d is not in the current room", id)
	}
	if !isAgentSourceImageMessage(message) {
		return core.Message{}, fmt.Errorf("source image message %d is not an image or quoted image message", id)
	}
	return message, nil
}

func isAgentSourceImageMessage(message core.Message) bool {
	if isAgentImageType(message.MsgType) {
		return true
	}
	var body struct {
		Quote struct {
			MsgType string `json:"msgtype"`
		} `json:"quote"`
	}
	if len(message.Body) > 0 {
		_ = json.Unmarshal(message.Body, &body)
	}
	return isAgentImageType(body.Quote.MsgType)
}

func isAgentImageType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image", "图片":
		return true
	default:
		return false
	}
}
