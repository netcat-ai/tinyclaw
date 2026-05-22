package command

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tinyclaw/internal/core"
)

type fakeDeliveryStore struct {
	deliveries []json.RawMessage
}

func (s *fakeDeliveryStore) CreateCommandDelivery(_ context.Context, _ core.Message, payload json.RawMessage) (*core.Delivery, error) {
	s.deliveries = append(s.deliveries, append(json.RawMessage(nil), payload...))
	return &core.Delivery{ID: int64(len(s.deliveries))}, nil
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
	return GeneratedImage{Bytes: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"}, nil
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
		URL:       "https://s3.example/generated.png",
		URLKind:   "presigned_s3",
		ExpiresAt: time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
	}, nil
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
	if media.calls != 1 || media.input.MediaID == "" || media.input.MIMEType != "image/png" {
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
}

func decodeDeliveryPayload(t *testing.T, data json.RawMessage) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode delivery payload: %v", err)
	}
	return payload
}
