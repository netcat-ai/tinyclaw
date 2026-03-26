package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeSendJobStore struct {
	enqueueFn func(context.Context, string, string) (SendJob, error)
	claimFn   func(context.Context, string, time.Duration) (*SendJob, error)
	finishFn  func(context.Context, string, string, string, string) (*SendJob, error)
}

func (f fakeSendJobStore) EnqueueSendJob(ctx context.Context, recipientAlias, message string) (SendJob, error) {
	return f.enqueueFn(ctx, recipientAlias, message)
}

func (f fakeSendJobStore) ClaimNextSendJob(ctx context.Context, deviceID string, lease time.Duration) (*SendJob, error) {
	return f.claimFn(ctx, deviceID, lease)
}

func (f fakeSendJobStore) FinishSendJob(ctx context.Context, jobID, deviceID, status, lastError string) (*SendJob, error) {
	return f.finishFn(ctx, jobID, deviceID, status, lastError)
}

func TestHandleSendJobsRequiresAuthWhenTokenConfigured(t *testing.T) {
	api := &controlAPI{
		store: fakeSendJobStore{},
		token: "secret",
		lease: 5 * time.Minute,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/wecom/send-jobs", bytes.NewBufferString(`{"recipient_alias":"小金鱼","message":"你好呀"}`))
	rec := httptest.NewRecorder()

	api.handleSendJobs(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleSendJobsEnqueuesJob(t *testing.T) {
	now := time.Now().UTC()
	api := &controlAPI{
		store: fakeSendJobStore{
			enqueueFn: func(_ context.Context, recipientAlias, message string) (SendJob, error) {
				return SendJob{
					ID:             "job-1",
					RecipientAlias: recipientAlias,
					Message:        message,
					Status:         sendJobStatusQueued,
					CreatedAt:      now,
					UpdatedAt:      now,
				}, nil
			},
		},
		token: "",
		lease: 5 * time.Minute,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/wecom/send-jobs", bytes.NewBufferString(`{"recipient_alias":"小金鱼","message":"你好呀"}`))
	rec := httptest.NewRecorder()

	api.handleSendJobs(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	var payload struct {
		Job sendJobResponse `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Job.ID != "job-1" || payload.Job.RecipientAlias != "小金鱼" || payload.Job.Message != "你好呀" {
		t.Fatalf("unexpected job response: %+v", payload.Job)
	}
}

func TestHandleClaimSendJobReturnsNoContentWhenQueueEmpty(t *testing.T) {
	api := &controlAPI{
		store: fakeSendJobStore{
			claimFn: func(_ context.Context, deviceID string, lease time.Duration) (*SendJob, error) {
				if deviceID != "phone-01" {
					t.Fatalf("deviceID = %q, want phone-01", deviceID)
				}
				if lease != 5*time.Minute {
					t.Fatalf("lease = %s, want %s", lease, 5*time.Minute)
				}
				return nil, nil
			},
		},
		lease: 5 * time.Minute,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/wecom/send-jobs/claim", bytes.NewBufferString(`{"device_id":"phone-01"}`))
	rec := httptest.NewRecorder()

	api.handleClaimSendJob(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
