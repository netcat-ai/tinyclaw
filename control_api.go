package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type sendJobStore interface {
	EnqueueSendJob(ctx context.Context, recipientAlias, message string) (SendJob, error)
	ClaimNextSendJob(ctx context.Context, deviceID string, lease time.Duration) (*SendJob, error)
	FinishSendJob(ctx context.Context, jobID, deviceID, status, lastError string) (*SendJob, error)
}

type controlAPI struct {
	store sendJobStore
	token string
	lease time.Duration
}

type enqueueSendJobRequest struct {
	RecipientAlias string `json:"recipient_alias"`
	Message        string `json:"message"`
}

type claimSendJobRequest struct {
	DeviceID string `json:"device_id"`
}

type finishSendJobRequest struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
	Error    string `json:"error"`
}

type sendJobResponse struct {
	ID             string     `json:"id"`
	RecipientAlias string     `json:"recipient_alias"`
	Message        string     `json:"message"`
	Status         string     `json:"status"`
	DeviceID       string     `json:"device_id,omitempty"`
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"last_error,omitempty"`
	ClaimDeadline  *time.Time `json:"claim_deadline,omitempty"`
	ClaimedAt      *time.Time `json:"claimed_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func serveControlAPI(ctx context.Context, cfg Config, store *Store) {
	api := &controlAPI{
		store: store,
		token: strings.TrimSpace(cfg.ControlAPIToken),
		lease: time.Duration(cfg.SendJobLeaseSeconds) * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/wecom/send-jobs", api.handleSendJobs)
	mux.HandleFunc("/api/wecom/send-jobs/claim", api.handleClaimSendJob)
	mux.HandleFunc("/api/wecom/send-jobs/", api.handleSendJobResult)

	srv := &http.Server{
		Addr:              cfg.ControlAPIAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if api.token == "" {
		slog.Warn("control api token is empty; control api is unauthenticated", "addr", cfg.ControlAPIAddr)
	}
	slog.Info("control api starting", "addr", cfg.ControlAPIAddr, "lease_seconds", int(api.lease.Seconds()))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("control api failed", "err", err)
	}
}

func (api *controlAPI) handleSendJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !api.authorize(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	var req enqueueSendJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.RecipientAlias = strings.TrimSpace(req.RecipientAlias)
	req.Message = strings.TrimSpace(req.Message)
	if req.RecipientAlias == "" || req.Message == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "recipient_alias and message are required")
		return
	}

	job, err := api.store.EnqueueSendJob(r.Context(), req.RecipientAlias, req.Message)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "enqueue_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusCreated, map[string]any{"job": toSendJobResponse(job)})
}

func (api *controlAPI) handleClaimSendJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !api.authorize(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	var req claimSendJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	if req.DeviceID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "device_id is required")
		return
	}

	job, err := api.store.ClaimNextSendJob(r.Context(), req.DeviceID, api.lease)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "claim_failed", err.Error())
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"job": toSendJobResponse(*job)})
}

func (api *controlAPI) handleSendJobResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !api.authorize(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	jobID := strings.TrimPrefix(r.URL.Path, "/api/wecom/send-jobs/")
	if !strings.HasSuffix(jobID, "/result") {
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown path")
		return
	}
	jobID = strings.TrimSuffix(jobID, "/result")
	jobID = strings.Trim(jobID, "/")
	if jobID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "job id is required")
		return
	}

	var req finishSendJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Status = strings.TrimSpace(req.Status)
	req.Error = strings.TrimSpace(req.Error)
	if req.DeviceID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "device_id is required")
		return
	}
	if req.Status != sendJobStatusSucceeded && req.Status != sendJobStatusFailed {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "status must be succeeded or failed")
		return
	}
	if req.Status == sendJobStatusFailed && req.Error == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "error is required when status=failed")
		return
	}

	job, err := api.store.FinishSendJob(r.Context(), jobID, req.DeviceID, req.Status, req.Error)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "finish_failed", err.Error())
		return
	}
	if job == nil {
		writeAPIError(w, http.StatusConflict, "claim_conflict", "job is not currently claimed by this device")
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"job": toSendJobResponse(*job)})
}

func (api *controlAPI) authorize(r *http.Request) bool {
	if api.token == "" {
		return true
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(authHeader, prefix)) == api.token
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeAPIJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, statusCode int, code, detail string) {
	writeAPIJSON(w, statusCode, map[string]any{
		"error": map[string]string{
			"code":   code,
			"detail": detail,
		},
	})
}

func toSendJobResponse(job SendJob) sendJobResponse {
	return sendJobResponse{
		ID:             job.ID,
		RecipientAlias: job.RecipientAlias,
		Message:        job.Message,
		Status:         job.Status,
		DeviceID:       job.DeviceID,
		Attempts:       job.Attempts,
		LastError:      job.LastError,
		ClaimDeadline:  optionalTime(job.ClaimDeadline),
		ClaimedAt:      optionalTime(job.ClaimedAt),
		CompletedAt:    optionalTime(job.CompletedAt),
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() || value.Equal(time.Unix(0, 0).UTC()) {
		return nil
	}
	ts := value.UTC()
	return &ts
}
