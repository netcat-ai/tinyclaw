package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeJobStore struct {
	getMaxJobSeqFn     func(context.Context, string) (int64, error)
	listJobsSinceSeqFn func(context.Context, string, int64, time.Time) ([]Job, error)
	validateClientFn   func(context.Context, string, string) (bool, error)
	resolveBotIDFn     func(context.Context, string) (string, error)
}

func (f fakeJobStore) GetMaxJobSeq(ctx context.Context, botID string) (int64, error) {
	return f.getMaxJobSeqFn(ctx, botID)
}

func (f fakeJobStore) ListJobsSinceSeq(ctx context.Context, botID string, afterSeq int64, cutoff time.Time) ([]Job, error) {
	return f.listJobsSinceSeqFn(ctx, botID, afterSeq, cutoff)
}

func (f fakeJobStore) ValidateAppClient(ctx context.Context, clientID, clientSecret string) (bool, error) {
	return f.validateClientFn(ctx, clientID, clientSecret)
}

func (f fakeJobStore) ResolveBotIDByAppClient(ctx context.Context, clientID string) (string, error) {
	return f.resolveBotIDFn(ctx, clientID)
}

func TestHandleListJobsRequiresBasicAuth(t *testing.T) {
	api := &controlAPI{
		store: fakeJobStore{
			getMaxJobSeqFn: func(context.Context, string) (int64, error) { return 0, nil },
			listJobsSinceSeqFn: func(context.Context, string, int64, time.Time) ([]Job, error) {
				return nil, nil
			},
			validateClientFn: func(context.Context, string, string) (bool, error) { return false, nil },
			resolveBotIDFn:   func(context.Context, string) (string, error) { return "", nil },
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
			validateClientFn: func(_ context.Context, clientID, clientSecret string) (bool, error) {
				return clientID == "phone-a" && clientSecret == "secret", nil
			},
			resolveBotIDFn: func(_ context.Context, clientID string) (string, error) {
				if clientID != "phone-a" {
					t.Fatalf("clientID = %q, want phone-a", clientID)
				}
				return "moss", nil
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
			validateClientFn: func(_ context.Context, clientID, clientSecret string) (bool, error) {
				return clientID == "phone-a" && clientSecret == "secret", nil
			},
			resolveBotIDFn: func(_ context.Context, clientID string) (string, error) {
				if clientID != "phone-a" {
					t.Fatalf("clientID = %q, want phone-a", clientID)
				}
				return "moss", nil
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
