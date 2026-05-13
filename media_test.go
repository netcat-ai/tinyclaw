package main

import (
	"context"
	"errors"
	"testing"

	"tinyclaw/wecom/finance"
)

type fakeMediaLookupStore struct {
	getMessageByIdentityFn func(context.Context, string, string, int64, string) (MessageRecord, bool, error)
}

func (f fakeMediaLookupStore) GetMessageByIdentity(ctx context.Context, tenantID, roomID string, seq int64, msgID string) (MessageRecord, bool, error) {
	return f.getMessageByIdentityFn(ctx, tenantID, roomID, seq, msgID)
}

type fakeMediaSDK struct {
	getMediaDataFn func(string, string) (*finance.MediaData, error)
}

func (f fakeMediaSDK) GetMediaData(indexBuf string, sdkFileID string) (*finance.MediaData, error) {
	return f.getMediaDataFn(indexBuf, sdkFileID)
}

func TestClawmanMediaServiceFetchImage(t *testing.T) {
	service := &clawmanMediaService{
		tenantID: "corp-1",
		store: fakeMediaLookupStore{
			getMessageByIdentityFn: func(_ context.Context, tenantID, roomID string, seq int64, msgID string) (MessageRecord, bool, error) {
				if tenantID != "corp-1" || roomID != "room-1" || seq != 7 || msgID != "msg-7" {
					t.Fatalf("unexpected lookup args: tenant=%q room=%q seq=%d msgid=%q", tenantID, roomID, seq, msgID)
				}
				return MessageRecord{
					Seq:      7,
					TenantID: tenantID,
					MsgID:    msgID,
					RoomID:   roomID,
					Payload:  `{"msgtype":"image","image":{"sdkfileid":"sdk-file-7","url":"https://example.test/demo.png"}}`,
				}, true, nil
			},
		},
		sdk: fakeMediaSDK{
			getMediaDataFn: func(indexBuf, sdkFileID string) (*finance.MediaData, error) {
				if sdkFileID != "sdk-file-7" {
					t.Fatalf("sdk file id = %q, want sdk-file-7", sdkFileID)
				}
				if indexBuf != "" {
					t.Fatalf("indexBuf = %q, want empty", indexBuf)
				}
				return &finance.MediaData{
					Data:     []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
					IsFinish: true,
				}, nil
			},
		},
	}

	blob, err := service.FetchImage(context.Background(), mediaFetchRequest{
		RoomID:    "room-1",
		Seq:       7,
		MsgID:     "msg-7",
		SDKFileID: "sdk-file-7",
	})
	if err != nil {
		t.Fatalf("FetchImage error: %v", err)
	}
	if blob.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", blob.ContentType)
	}
	if blob.FileName != "msg-7.png" {
		t.Fatalf("file name = %q, want msg-7.png", blob.FileName)
	}
	if len(blob.Data) == 0 {
		t.Fatal("media data should not be empty")
	}
}

func TestClawmanMediaServiceRejectsSDKFileIDMismatch(t *testing.T) {
	service := &clawmanMediaService{
		tenantID: "corp-1",
		store: fakeMediaLookupStore{
			getMessageByIdentityFn: func(_ context.Context, _ string, _ string, _ int64, _ string) (MessageRecord, bool, error) {
				return MessageRecord{
					Payload: `{"msgtype":"image","image":{"sdkfileid":"sdk-file-a"}}`,
				}, true, nil
			},
		},
		sdk: fakeMediaSDK{
			getMediaDataFn: func(string, string) (*finance.MediaData, error) {
				t.Fatal("GetMediaData should not be called on mismatch")
				return nil, nil
			},
		},
	}

	_, err := service.FetchImage(context.Background(), mediaFetchRequest{
		RoomID:    "room-1",
		Seq:       7,
		MsgID:     "msg-7",
		SDKFileID: "sdk-file-b",
	})
	if !errors.Is(err, errMediaPayloadMismatch) {
		t.Fatalf("error = %v, want errMediaPayloadMismatch", err)
	}
}
