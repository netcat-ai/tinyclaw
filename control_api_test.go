package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeJobStore struct {
	getMaxJobSeqFn     func(context.Context, string) (int64, error)
	listJobsSinceSeqFn func(context.Context, string, int64, time.Time) ([]Job, error)
	authenticateFn     func(context.Context, string, string) (string, bool, error)
}

type fakeMediaService struct {
	fetchImageFn func(context.Context, mediaFetchRequest) (mediaBlob, error)
}

func (f fakeJobStore) GetMaxJobSeq(ctx context.Context, botID string) (int64, error) {
	return f.getMaxJobSeqFn(ctx, botID)
}

func (f fakeJobStore) ListJobsSinceSeq(ctx context.Context, botID string, afterSeq int64, cutoff time.Time) ([]Job, error) {
	return f.listJobsSinceSeqFn(ctx, botID, afterSeq, cutoff)
}

func (f fakeJobStore) AuthenticateAppClient(ctx context.Context, clientID, clientSecret string) (string, bool, error) {
	return f.authenticateFn(ctx, clientID, clientSecret)
}

func (f fakeMediaService) FetchImage(ctx context.Context, req mediaFetchRequest) (mediaBlob, error) {
	return f.fetchImageFn(ctx, req)
}

func TestHandleListJobsRequiresBasicAuth(t *testing.T) {
	api := &controlAPI{
		store: fakeJobStore{
			getMaxJobSeqFn: func(context.Context, string) (int64, error) { return 0, nil },
			listJobsSinceSeqFn: func(context.Context, string, int64, time.Time) ([]Job, error) {
				return nil, nil
			},
			authenticateFn: func(context.Context, string, string) (string, bool, error) {
				return "", false, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/wecom/jobs?seq=0", nil)
	rec := httptest.NewRecorder()

	api.handleListJobs(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleListJobsBootstrapReturnsEmptyAndMaxSeq(t *testing.T) {
	api := &controlAPI{
		store: fakeJobStore{
			getMaxJobSeqFn: func(_ context.Context, botID string) (int64, error) {
				if botID != "moss" {
					t.Fatalf("botID = %q, want moss", botID)
				}
				return 42, nil
			},
			listJobsSinceSeqFn: func(context.Context, string, int64, time.Time) ([]Job, error) {
				t.Fatal("listJobsSinceSeq should not be called during bootstrap")
				return nil, nil
			},
			authenticateFn: func(_ context.Context, clientID, clientSecret string) (string, bool, error) {
				if clientID != "phone-a" {
					t.Fatalf("clientID = %q, want phone-a", clientID)
				}
				if clientSecret != "secret" {
					t.Fatalf("clientSecret = %q, want secret", clientSecret)
				}
				return "moss", true, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/wecom/jobs?seq=0", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("phone-a:secret")))
	rec := httptest.NewRecorder()

	api.handleListJobs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var payload jobsPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Jobs) != 0 {
		t.Fatalf("jobs length = %d, want 0", len(payload.Jobs))
	}
	if payload.NextSeq != 42 {
		t.Fatalf("next_seq = %d, want 42", payload.NextSeq)
	}
}

func TestHandleListJobsReturnsFilteredJobs(t *testing.T) {
	now := time.Now().UTC()
	api := &controlAPI{
		store: fakeJobStore{
			getMaxJobSeqFn: func(_ context.Context, botID string) (int64, error) {
				if botID != "moss" {
					t.Fatalf("botID = %q, want moss", botID)
				}
				return 9, nil
			},
			listJobsSinceSeqFn: func(_ context.Context, botID string, afterSeq int64, cutoff time.Time) ([]Job, error) {
				if botID != "moss" {
					t.Fatalf("botID = %q, want moss", botID)
				}
				if afterSeq != 4 {
					t.Fatalf("afterSeq = %d, want 4", afterSeq)
				}
				if cutoff.After(now) {
					t.Fatalf("cutoff should not be in the future")
				}
				return []Job{
					{
						ID:             "job-1",
						Seq:            7,
						BotID:          "moss",
						RecipientAlias: "小金鱼",
						Message:        "你好呀",
						MaxSeq:         8721,
						CreatedAt:      now,
					},
				}, nil
			},
			authenticateFn: func(_ context.Context, clientID, clientSecret string) (string, bool, error) {
				if clientID != "phone-a" {
					t.Fatalf("clientID = %q, want phone-a", clientID)
				}
				if clientSecret != "secret" {
					t.Fatalf("clientSecret = %q, want secret", clientSecret)
				}
				return "moss", true, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/wecom/jobs?seq=4", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("phone-a:secret")))
	rec := httptest.NewRecorder()

	api.handleListJobs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var payload jobsPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.NextSeq != 9 {
		t.Fatalf("next_seq = %d, want 9", payload.NextSeq)
	}
	if len(payload.Jobs) != 1 {
		t.Fatalf("jobs length = %d, want 1", len(payload.Jobs))
	}
	if payload.Jobs[0].ID != "job-1" || payload.Jobs[0].Seq != 7 || payload.Jobs[0].MaxSeq != 8721 {
		t.Fatalf("unexpected job: %+v", payload.Jobs[0])
	}
}

func TestHandleFetchMediaRequiresBearerToken(t *testing.T) {
	api := &controlAPI{
		internalToken: "secret-token",
		media: fakeMediaService{
			fetchImageFn: func(context.Context, mediaFetchRequest) (mediaBlob, error) {
				t.Fatal("FetchImage should not be called without auth")
				return mediaBlob{}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/media/fetch", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	api.handleFetchMedia(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleFetchMediaReturnsBinaryBody(t *testing.T) {
	api := &controlAPI{
		internalToken: "secret-token",
		media: fakeMediaService{
			fetchImageFn: func(_ context.Context, req mediaFetchRequest) (mediaBlob, error) {
				if req.RoomID != "room-1" || req.Seq != 7 || req.MsgID != "msg-7" || req.SDKFileID != "sdk-7" {
					t.Fatalf("unexpected request: %+v", req)
				}
				return mediaBlob{
					Data:        []byte("PNGDATA"),
					ContentType: "image/png",
					FileName:    "msg-7.png",
				}, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/media/fetch", strings.NewReader(`{"room_id":"room-1","seq":7,"msgid":"msg-7","sdk_file_id":"sdk-7"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	api.handleFetchMedia(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content-type = %q, want image/png", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-TinyClaw-File-Name") != "msg-7.png" {
		t.Fatalf("file name header = %q, want msg-7.png", rec.Header().Get("X-TinyClaw-File-Name"))
	}
	if rec.Body.String() != "PNGDATA" {
		t.Fatalf("body = %q, want PNGDATA", rec.Body.String())
	}
}

func TestHandleFetchMediaMapsNotFound(t *testing.T) {
	api := &controlAPI{
		internalToken: "secret-token",
		media: fakeMediaService{
			fetchImageFn: func(context.Context, mediaFetchRequest) (mediaBlob, error) {
				return mediaBlob{}, errMediaMessageNotFound
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/media/fetch", strings.NewReader(`{"room_id":"room-1","seq":7,"msgid":"msg-7","sdk_file_id":"sdk-7"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	api.handleFetchMedia(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleFetchMediaMapsUnexpectedError(t *testing.T) {
	api := &controlAPI{
		internalToken: "secret-token",
		media: fakeMediaService{
			fetchImageFn: func(context.Context, mediaFetchRequest) (mediaBlob, error) {
				return mediaBlob{}, errors.New("boom")
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/media/fetch", strings.NewReader(`{"room_id":"room-1","seq":7,"msgid":"msg-7","sdk_file_id":"sdk-7"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	api.handleFetchMedia(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
