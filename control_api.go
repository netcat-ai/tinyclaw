package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	store         jobStore
	media         mediaService
	internalToken string
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

type mediaService interface {
	FetchImage(context.Context, mediaFetchRequest) (mediaBlob, error)
}

func serveControlAPI(ctx context.Context, cfg Config, store *Store, media mediaService) {
	api := &controlAPI{
		store:         store,
		media:         media,
		internalToken: strings.TrimSpace(cfg.ClawmanInternalToken),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/wecom/jobs", api.handleListJobs)
	mux.HandleFunc("/internal/media/fetch", api.handleFetchMedia)

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

func (api *controlAPI) handleFetchMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if strings.TrimSpace(api.internalToken) == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "internal media API is disabled")
		return
	}
	if !api.authenticateInternalBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid internal bearer token")
		return
	}
	if api.media == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "media service is unavailable")
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "read request body failed")
		return
	}

	var req mediaFetchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
		return
	}

	blob, err := api.media.FetchImage(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, errMediaMessageNotFound):
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
		case errors.Is(err, errMediaPayloadMismatch), errors.Is(err, errMediaNotDownloadable):
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, errMediaDownloadDisabled):
			writeAPIError(w, http.StatusServiceUnavailable, "disabled", err.Error())
		default:
			writeAPIError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", blob.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(blob.Data)))
	w.Header().Set("X-TinyClaw-File-Name", blob.FileName)
	w.Header().Set("X-TinyClaw-MsgId", req.MsgID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob.Data)
}

func (api *controlAPI) authenticateInternalBearer(r *http.Request) bool {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
	return token != "" && token == api.internalToken
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
