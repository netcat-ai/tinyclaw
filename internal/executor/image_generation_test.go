package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"tinyclaw/internal/command"
	"tinyclaw/internal/core"
)

var errFakeMessageNotFound = fmt.Errorf("message not found")

type fakeExecutorImageGenerator struct {
	input command.ImageGenerationInput
}

func (g *fakeExecutorImageGenerator) GenerateImage(_ context.Context, input command.ImageGenerationInput) (command.GeneratedImage, error) {
	g.input = input
	return command.GeneratedImage{Bytes: []byte{1, 2, 3}, MIMEType: "image/png"}, nil
}

type fakeExecutorMediaStore struct {
	input command.StoreMediaInput
}

func (s *fakeExecutorMediaStore) StoreGeneratedMedia(_ context.Context, input command.StoreMediaInput) (command.StoredMedia, error) {
	s.input = input
	return command.StoredMedia{
		URL:       "https://media.example/" + input.MediaID + ".png",
		URLKind:   "presigned_s3",
		ExpiresAt: time.Unix(1700000000, 0).UTC(),
	}, nil
}

type fakeExecutorMediaFetcher struct {
	messageID int64
}

func (f *fakeExecutorMediaFetcher) FetchMessageMedia(_ context.Context, message core.Message) (command.SourceImage, error) {
	f.messageID = message.ID
	return command.SourceImage{Bytes: []byte{4, 5, 6}, MIMEType: "image/jpeg", Filename: "source.jpg"}, nil
}

type fakeExecutorSourceMessageStore struct {
	messages map[int64]core.Message
}

func (s fakeExecutorSourceMessageStore) GetCoreMessageByID(_ context.Context, id int64) (core.Message, error) {
	message, ok := s.messages[id]
	if !ok {
		return core.Message{}, errFakeMessageNotFound
	}
	return message, nil
}

func TestAgentImageToolGeneratesWithSourceImageFromContext(t *testing.T) {
	image := &fakeExecutorImageGenerator{}
	media := &fakeExecutorMediaStore{}
	fetcher := &fakeExecutorMediaFetcher{}
	tool := AgentImageTool{
		Image:        image,
		Media:        media,
		MediaFetcher: fetcher,
		ImageSize:    "1536x1024",
		MediaURLTTL:  time.Hour,
	}

	output, err := tool.GenerateAgentImage(context.Background(), AgentRunRequest{
		ContextMessages: []core.Message{
			{ID: 41, MsgType: "text"},
			{ID: 42, MsgType: "image"},
		},
	}, core.ImageGenerationRequest{
		Prompt:           "改成水彩",
		SourceMessageIDs: []int64{42},
	})
	if err != nil {
		t.Fatalf("GenerateAgentImage error: %v", err)
	}
	if fetcher.messageID != 42 {
		t.Fatalf("fetched message id = %d, want 42", fetcher.messageID)
	}
	if image.input.Prompt != "改成水彩" || image.input.Size != "1536x1024" || len(image.input.SourceImages) != 1 {
		t.Fatalf("image input = %+v", image.input)
	}
	if media.input.MediaID == "" || media.input.TTL != time.Hour {
		t.Fatalf("media input = %+v", media.input)
	}
	if output.MediaID != media.input.MediaID || output.MediaURLKind != "presigned_s3" {
		t.Fatalf("output = %+v", output)
	}
}

func TestAgentImageToolGeneratesWithSourceMessageFromSameRoom(t *testing.T) {
	image := &fakeExecutorImageGenerator{}
	fetcher := &fakeExecutorMediaFetcher{}
	tool := AgentImageTool{
		Image:        image,
		Media:        &fakeExecutorMediaStore{},
		MediaFetcher: fetcher,
		SourceMessageStore: fakeExecutorSourceMessageStore{messages: map[int64]core.Message{
			99: {ID: 99, RoomID: 10, MsgType: "text"},
		}},
	}

	_, err := tool.GenerateAgentImage(context.Background(), AgentRunRequest{
		AgentRun:        core.AgentRun{RoomID: 10, SourceMessageToID: 90},
		ContextMessages: []core.Message{{ID: 42, RoomID: 10, MsgType: "image"}},
	}, core.ImageGenerationRequest{
		Prompt:           "改成水彩",
		SourceMessageIDs: []int64{99},
	})
	if err != nil {
		t.Fatalf("GenerateAgentImage error: %v", err)
	}
	if fetcher.messageID != 99 || len(image.input.SourceImages) != 1 {
		t.Fatalf("same-room source message was not used, fetched=%d input=%+v", fetcher.messageID, image.input)
	}
}

func TestAgentImageToolRejectsSourceImageFromOtherRoom(t *testing.T) {
	tool := AgentImageTool{
		Image:        &fakeExecutorImageGenerator{},
		Media:        &fakeExecutorMediaStore{},
		MediaFetcher: &fakeExecutorMediaFetcher{},
		SourceMessageStore: fakeExecutorSourceMessageStore{messages: map[int64]core.Message{
			99: {ID: 99, RoomID: 11, MsgType: "image"},
		}},
	}

	_, err := tool.GenerateAgentImage(context.Background(), AgentRunRequest{
		AgentRun:        core.AgentRun{RoomID: 10, SourceMessageToID: 120},
		ContextMessages: []core.Message{{ID: 42, RoomID: 10, MsgType: "image"}},
	}, core.ImageGenerationRequest{
		Prompt:           "改成水彩",
		SourceMessageIDs: []int64{99},
	})
	if err == nil {
		t.Fatal("GenerateAgentImage error = nil, want room validation error")
	}
}
