package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

type fakeDeliveryStore struct {
	deliveries  []json.RawMessage
	latestImage core.Message
	latestErr   error
	latestCalls int
}

func (s *fakeDeliveryStore) CreateCommandDelivery(_ context.Context, _ core.Message, payload json.RawMessage) (*core.Delivery, error) {
	s.deliveries = append(s.deliveries, append(json.RawMessage(nil), payload...))
	return &core.Delivery{ID: int64(len(s.deliveries))}, nil
}

func (s *fakeDeliveryStore) LatestImageMessageBefore(_ context.Context, roomID int64, beforeMessageID int64) (core.Message, error) {
	s.latestCalls++
	if s.latestErr != nil {
		return core.Message{}, s.latestErr
	}
	if s.latestImage.ID == 0 {
		return core.Message{}, fmt.Errorf("not found")
	}
	if s.latestImage.RoomID != roomID {
		return core.Message{}, fmt.Errorf("room mismatch")
	}
	if s.latestImage.ID >= beforeMessageID {
		return core.Message{}, fmt.Errorf("image is not before message")
	}
	return s.latestImage, nil
}

type fakeImageGenerator struct {
	calls int
	input ImageGenerationInput
	err   error
}

func (g *fakeImageGenerator) GenerateImage(_ context.Context, input ImageGenerationInput) (GeneratedImage, error) {
	g.calls++
	g.input = input
	if g.err != nil {
		return GeneratedImage{}, g.err
	}
	return GeneratedImage{Bytes: validGeneratedPNGForTest(), MIMEType: "image/png"}, nil
}

func validGeneratedPNGForTest() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		panic(err)
	}
	return out.Bytes()
}

type fakeMediaStore struct {
	calls int
	input StoreMediaInput
	err   error
}

func (s *fakeMediaStore) StoreGeneratedMedia(_ context.Context, input StoreMediaInput) (StoredMedia, error) {
	s.calls++
	s.input = input
	if s.err != nil {
		return StoredMedia{}, s.err
	}
	return StoredMedia{
		URL:       "https://s3.example/generated.jpg",
		URLKind:   "presigned_s3",
		ExpiresAt: time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
	}, nil
}

type fakeMediaFetcher struct {
	calls   int
	message core.Message
	image   SourceImage
	err     error
}

func (f *fakeMediaFetcher) FetchMessageMedia(_ context.Context, message core.Message) (SourceImage, error) {
	f.calls++
	f.message = message
	if f.err != nil {
		return SourceImage{}, f.err
	}
	return f.image, nil
}

func TestDrawPromptParsesSingleAndMultiLinePrompt(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "single line", body: `{"type":"text","text":" /draw 一朵花 "}`, want: "一朵花"},
		{name: "multi line", body: `{"type":"text","text":"/draw\n一朵花\n水彩"}`, want: "一朵花\n水彩"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := DrawPrompt([]byte(test.body))
			if !ok {
				t.Fatal("ok = false, want true")
			}
			if got != test.want {
				t.Fatalf("prompt = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDrawSourceImagePrompt(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantPrompt string
		wantSource bool
	}{
		{name: "plain draw", prompt: "一朵花", wantPrompt: "一朵花"},
		{name: "marker", prompt: "图生图 赛博朋克风格", wantPrompt: "赛博朋克风格", wantSource: true},
		{name: "reference phrase", prompt: "把上图改成水彩", wantPrompt: "把上图改成水彩", wantSource: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotPrompt, gotSource := drawSourceImagePrompt(test.prompt)
			if gotPrompt != test.wantPrompt || gotSource != test.wantSource {
				t.Fatalf("drawSourceImagePrompt = (%q, %v), want (%q, %v)", gotPrompt, gotSource, test.wantPrompt, test.wantSource)
			}
		})
	}
}

func TestDrawPromptIgnoresNonTextPayload(t *testing.T) {
	if _, ok := DrawPrompt([]byte(`{"type":"image","text":"/draw flower"}`)); ok {
		t.Fatal("ok = true, want false")
	}
}

func TestHandlerCreatesEmptyPromptFailureOnly(t *testing.T) {
	store := &fakeDeliveryStore{}
	handler := NewHandler(store, &fakeImageGenerator{}, &fakeMediaStore{})
	handler.Async = false

	if !handler.HandleMessage(context.Background(), core.Message{ID: 1, RoomID: 10, Payload: []byte(`{"type":"text","text":"/draw"}`)}) {
		t.Fatal("handled = false, want true")
	}
	if len(store.deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(store.deliveries))
	}
	payload := decodeDeliveryPayload(t, store.deliveries[0])
	if payload["kind"] != KindCommandFailure || payload["text"] != "请在 /draw 后面描述要画什么" {
		t.Fatalf("payload = %+v, want empty prompt failure", payload)
	}
}

func TestHandlerCreatesPreExecutionConfigurationFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler *Handler
		want    string
	}{
		{
			name: "disabled",
			handler: func() *Handler {
				h := NewHandler(&fakeDeliveryStore{}, &fakeImageGenerator{}, &fakeMediaStore{})
				h.Enabled = false
				return h
			}(),
			want: "画图功能未启用",
		},
		{
			name:    "missing image provider",
			handler: NewHandler(&fakeDeliveryStore{}, nil, &fakeMediaStore{}),
			want:    "画图功能未配置",
		},
		{
			name:    "missing media store",
			handler: NewHandler(&fakeDeliveryStore{}, &fakeImageGenerator{}, nil),
			want:    "画图存储未配置",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.handler.Async = false
			store := test.handler.Store.(*fakeDeliveryStore)
			test.handler.HandleMessage(context.Background(), core.Message{ID: 1, RoomID: 10, Payload: []byte(`{"type":"text","text":"/draw flower"}`)})
			if len(store.deliveries) != 1 {
				t.Fatalf("deliveries = %d, want 1", len(store.deliveries))
			}
			payload := decodeDeliveryPayload(t, store.deliveries[0])
			if payload["kind"] != KindCommandFailure || payload["text"] != test.want {
				t.Fatalf("payload = %+v, want %q", payload, test.want)
			}
		})
	}
}

func TestHandlerCreatesSuccessDeliveries(t *testing.T) {
	store := &fakeDeliveryStore{}
	image := &fakeImageGenerator{}
	media := &fakeMediaStore{}
	handler := NewHandler(store, image, media)
	handler.Async = false

	if !handler.HandleMessage(context.Background(), core.Message{ID: 1, RoomID: 10, Payload: []byte(`{"type":"text","text":"/draw flower"}`)}) {
		t.Fatal("handled = false, want true")
	}
	if image.calls != 1 || image.input.Prompt != "flower" || image.input.Size != defaultDrawImageSize {
		t.Fatalf("image call = %+v calls=%d", image.input, image.calls)
	}
	if media.calls != 1 || media.input.MediaID == "" || media.input.MIMEType != "image/jpeg" {
		t.Fatalf("media call = %+v calls=%d", media.input, media.calls)
	}
	if len(store.deliveries) != 3 {
		t.Fatalf("deliveries = %d, want 3", len(store.deliveries))
	}
	progress := decodeDeliveryPayload(t, store.deliveries[0])
	done := decodeDeliveryPayload(t, store.deliveries[1])
	imagePayload := decodeDeliveryPayload(t, store.deliveries[2])
	if progress["kind"] != KindCommandProgress || progress["text"] != "正在画图..." {
		t.Fatalf("progress = %+v", progress)
	}
	if done["kind"] != KindCommandOutput || done["type"] != "text" {
		t.Fatalf("done = %+v", done)
	}
	if imagePayload["kind"] != KindCommandOutput || imagePayload["type"] != "image" || imagePayload["media_url_kind"] != "presigned_s3" {
		t.Fatalf("image payload = %+v", imagePayload)
	}
	if imagePayload["mime_type"] != "image/jpeg" {
		t.Fatalf("image mime type = %v, want image/jpeg", imagePayload["mime_type"])
	}
}

func TestHandlerCreatesImageEditFromLatestImage(t *testing.T) {
	store := &fakeDeliveryStore{
		latestImage: core.Message{ID: 9, RoomID: 10, MsgType: "image"},
	}
	image := &fakeImageGenerator{}
	media := &fakeMediaStore{}
	fetcher := &fakeMediaFetcher{image: SourceImage{
		Bytes:    []byte{0xff, 0xd8, 0xff},
		MIMEType: "image/jpeg",
		Filename: "source.jpg",
	}}
	handler := NewHandler(store, image, media)
	handler.MediaFetcher = fetcher
	handler.Async = false

	if !handler.HandleMessage(context.Background(), core.Message{ID: 12, RoomID: 10, Payload: []byte(`{"type":"text","text":"/draw 把上图改成水彩风格"}`)}) {
		t.Fatal("handled = false, want true")
	}
	if store.latestCalls != 1 || fetcher.calls != 1 || fetcher.message.ID != 9 {
		t.Fatalf("latestCalls=%d fetchCalls=%d fetchMessage=%+v, want source message 9", store.latestCalls, fetcher.calls, fetcher.message)
	}
	if image.calls != 1 {
		t.Fatalf("image calls = %d, want 1", image.calls)
	}
	if image.input.Prompt != "把上图改成水彩风格" || len(image.input.SourceImages) != 1 {
		t.Fatalf("image input = %+v, want prompt with one source image", image.input)
	}
	if string(image.input.SourceImages[0].Bytes) != "\xff\xd8\xff" || image.input.SourceImages[0].MIMEType != "image/jpeg" {
		t.Fatalf("source image = %+v", image.input.SourceImages[0])
	}
}

func TestHandlerFailsImageEditWhenNoLatestImage(t *testing.T) {
	store := &fakeDeliveryStore{}
	handler := NewHandler(store, &fakeImageGenerator{}, &fakeMediaStore{})
	handler.Async = false

	if !handler.HandleMessage(context.Background(), core.Message{ID: 12, RoomID: 10, Payload: []byte(`{"type":"text","text":"/draw 图生图 水彩"}`)}) {
		t.Fatal("handled = false, want true")
	}
	if len(store.deliveries) != 2 {
		t.Fatalf("deliveries = %d, want progress and failure", len(store.deliveries))
	}
	payload := decodeDeliveryPayload(t, store.deliveries[1])
	if payload["kind"] != KindCommandFailure || payload["text"] != "没有找到可编辑的图片" {
		t.Fatalf("payload = %+v, want no image failure", payload)
	}
}

func TestHandlerCreatesFileDeliveryPayload(t *testing.T) {
	store := &fakeDeliveryStore{}
	handler := NewHandler(store, &fakeImageGenerator{}, &fakeMediaStore{})

	handler.createFileDelivery(context.Background(), core.Message{ID: 1, RoomID: 10}, "gm_1", "video/mp4", "gm_1.mp4", StoredMedia{
		URL:       "https://s3.example/gm_1.mp4",
		URLKind:   "presigned_s3",
		ExpiresAt: time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
	})

	if len(store.deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(store.deliveries))
	}
	payload := decodeDeliveryPayload(t, store.deliveries[0])
	if payload["kind"] != KindCommandOutput || payload["type"] != "file" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload["media_id"] != "gm_1" || payload["media_url"] != "https://s3.example/gm_1.mp4" || payload["mime_type"] != "video/mp4" || payload["filename"] != "gm_1.mp4" {
		t.Fatalf("file payload = %+v", payload)
	}
}

func decodeDeliveryPayload(t *testing.T, data json.RawMessage) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode delivery payload: %v", err)
	}
	return payload
}
