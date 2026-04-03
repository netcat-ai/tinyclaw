package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const jobRetentionWindow = 10 * time.Minute

type jobStore interface {
	GetMaxJobSeq(ctx context.Context, botID string) (int64, error)
	ListJobsSinceSeq(ctx context.Context, botID string, afterSeq int64, cutoff time.Time) ([]Job, error)
	AuthenticateAppClient(ctx context.Context, clientID, clientSecret string) (string, bool, error)
}

type controlAPI struct {
	store jobStore
}

type jobResponse struct {
	ID             string `json:"id"`
	Seq            int64  `json:"seq"`
	RecipientAlias string `json:"recipient_alias"`
	Message        string `json:"message"`
	MaxSeq         int64  `json:"max_seq"`
}

type jobsPageResponse struct {
	Jobs    []jobResponse `json:"jobs"`
	NextSeq int64         `json:"next_seq"`
}

func serveControlAPI(ctx context.Context, cfg Config, store *Store) {
	api := &controlAPI{store: store}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/wecom/jobs", api.handleListJobs)

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

	slog.Info("control api starting", "addr", cfg.ControlAPIAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("control api failed", "err", err)
	}
}

func (api *controlAPI) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	botID, ok, err := api.authenticateClient(r)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "auth_failed", err.Error())
		return
	}
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid client credentials")
		return
	}

	rawSeq := strings.TrimSpace(r.URL.Query().Get("seq"))
	if rawSeq == "" {
		rawSeq = "0"
	}
	afterSeq, err := strconv.ParseInt(rawSeq, 10, 64)
	if err != nil || afterSeq < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "seq must be a non-negative integer")
		return
	}

	maxSeq, err := api.store.GetMaxJobSeq(r.Context(), botID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}

	if afterSeq == 0 {
		writeAPIJSON(w, http.StatusOK, jobsPageResponse{
			Jobs:    []jobResponse{},
			NextSeq: maxSeq,
		})
		return
	}

	jobs, err := api.store.ListJobsSinceSeq(r.Context(), botID, afterSeq, time.Now().UTC().Add(-jobRetentionWindow))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}

	nextSeq := afterSeq
	if maxSeq > nextSeq {
		nextSeq = maxSeq
	}

	responseJobs := make([]jobResponse, 0, len(jobs))
	for _, job := range jobs {
		responseJobs = append(responseJobs, jobResponse{
			ID:             job.ID,
			Seq:            job.Seq,
			RecipientAlias: job.RecipientAlias,
			Message:        job.Message,
			MaxSeq:         job.MaxSeq,
		})
	}

	writeAPIJSON(w, http.StatusOK, jobsPageResponse{
		Jobs:    responseJobs,
		NextSeq: nextSeq,
	})
}

func (api *controlAPI) authenticateClient(r *http.Request) (string, bool, error) {
	clientID, clientSecret, ok := r.BasicAuth()
	if !ok {
		return "", false, nil
	}
	return api.store.AuthenticateAppClient(r.Context(), strings.TrimSpace(clientID), strings.TrimSpace(clientSecret))
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
