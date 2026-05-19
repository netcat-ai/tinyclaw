package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"tinyclaw/internal/core"
)

type CoreStore interface {
	IngestCoreMessage(ctx context.Context, input core.InboundMessageInput) (core.InboundMessageResult, error)
	CompleteCoreInvocation(ctx context.Context, invocationID int64, input core.CompleteInvocationInput) (core.InvocationResult, error)
	FailCoreInvocation(ctx context.Context, invocationID int64, detail string) (core.InvocationResult, error)
	ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error)
	AckCoreDelivery(ctx context.Context, id int64) (core.Delivery, error)
}

type InvocationScheduler interface {
	ScheduleInvocation(invocationID int64)
}

type Server struct {
	core      CoreStore
	scheduler InvocationScheduler
	apiToken  string
	mux       *http.ServeMux
}

func NewServer(core CoreStore, apiToken string, schedulers ...InvocationScheduler) *Server {
	var scheduler InvocationScheduler
	if len(schedulers) > 0 {
		scheduler = schedulers[0]
	}
	server := &Server{
		core:      core,
		scheduler: scheduler,
		apiToken:  strings.TrimSpace(apiToken),
		mux:       http.NewServeMux(),
	}
	server.RegisterRoutes(server.mux)
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/api/inbound", s.handleInbound)
	mux.HandleFunc("/api/deliveries", s.handleListDeliveries)
	mux.HandleFunc("/api/deliveries/", s.handleDeliveryAction)
	mux.HandleFunc("/api/invocations/", s.handleInvocationAction)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

type inboundResponse struct {
	Room       coreRoomResponse        `json:"room"`
	Message    coreMessageResponse     `json:"message"`
	Invocation *coreInvocationResponse `json:"invocation,omitempty"`
	Duplicate  bool                    `json:"duplicate"`
	Triggered  bool                    `json:"triggered"`
	Appended   bool                    `json:"appended"`
}

type coreRoomResponse struct {
	ID              int64  `json:"id"`
	TenantID        string `json:"tenant_id"`
	Channel         string `json:"channel"`
	ChannelRoomID   string `json:"channel_room_id"`
	ChannelRoomType string `json:"channel_room_type"`
	DisplayName     string `json:"display_name,omitempty"`
}

type coreMessageResponse struct {
	ID              int64           `json:"id"`
	RoomID          int64           `json:"room_id"`
	SourceMessageID string          `json:"source_message_id"`
	SenderID        string          `json:"sender_id"`
	SenderName      string          `json:"sender_name,omitempty"`
	Payload         json.RawMessage `json:"payload"`
	Skipped         bool            `json:"skipped"`
}

type coreInvocationResponse struct {
	ID                int64  `json:"id"`
	RoomID            int64  `json:"room_id"`
	Status            int16  `json:"status"`
	TriggerMessageID  int64  `json:"trigger_message_id,omitempty"`
	StartMessageID    int64  `json:"start_message_id,omitempty"`
	LastSeenMessageID int64  `json:"last_seen_message_id,omitempty"`
	ErrorDetail       string `json:"error_detail,omitempty"`
}

type coreDeliveryResponse struct {
	ID           int64           `json:"id"`
	RoomID       int64           `json:"room_id"`
	InvocationID int64           `json:"invocation_id"`
	Payload      json.RawMessage `json:"payload"`
	Status       int16           `json:"status"`
}

type deliveriesPageResponse struct {
	Deliveries []coreDeliveryResponse `json:"deliveries"`
	NextID     int64                  `json:"next_id"`
}

type invocationActionRequest struct {
	Text   string `json:"text"`
	Detail string `json:"detail"`
}

func (s *Server) handleInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	if s.core == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "core API is unavailable")
		return
	}

	var input core.InboundMessageInput
	if err := readJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.core.IngestCoreMessage(r.Context(), input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if result.Triggered && result.Invocation != nil && s.scheduler != nil {
		s.scheduler.ScheduleInvocation(result.Invocation.ID)
	}

	writeAPIJSON(w, http.StatusOK, inboundResponse{
		Room:       roomToResponse(result.Room),
		Message:    messageToResponse(result.Message),
		Invocation: invocationPtrToResponse(result.Invocation),
		Duplicate:  result.Duplicate,
		Triggered:  result.Triggered,
		Appended:   result.Appended,
	})
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	if !s.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	rawID := strings.TrimSpace(r.URL.Query().Get("id"))
	if rawID == "" {
		rawID = "0"
	}
	afterID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || afterID < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "id must be a non-negative integer")
		return
	}
	deliveries, err := s.core.ListCoreDeliveries(r.Context(), channel, afterID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "list_failed", err.Error())
		return
	}
	response := make([]coreDeliveryResponse, 0, len(deliveries))
	nextID := afterID
	for _, delivery := range deliveries {
		response = append(response, deliveryToResponse(delivery))
		if delivery.ID > nextID {
			nextID = delivery.ID
		}
	}
	writeAPIJSON(w, http.StatusOK, deliveriesPageResponse{Deliveries: response, NextID: nextID})
}

func (s *Server) handleDeliveryAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	id, action, ok := parseIDActionPath(strings.TrimPrefix(r.URL.Path, "/api/deliveries/"))
	if !ok || action != "ack" {
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown delivery action")
		return
	}
	delivery, err := s.core.AckCoreDelivery(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "ack_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, deliveryToResponse(delivery))
}

func (s *Server) handleInvocationAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	id, action, ok := parseIDActionPath(strings.TrimPrefix(r.URL.Path, "/api/invocations/"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown invocation action")
		return
	}
	var req invocationActionRequest
	if err := readJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var result core.InvocationResult
	var err error
	switch action {
	case "complete":
		result, err = s.core.CompleteCoreInvocation(r.Context(), id, core.CompleteInvocationInput{
			Text: req.Text,
		})
	case "fail":
		result, err = s.core.FailCoreInvocation(r.Context(), id, req.Detail)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown invocation action")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invocation_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"invocation": invocationToResponse(result.Invocation),
		"delivery":   deliveryPtrToResponse(result.Delivery),
	})
}

func (s *Server) authenticateAPIBearer(r *http.Request) bool {
	if strings.TrimSpace(s.apiToken) == "" {
		return false
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
	return token != "" && token == s.apiToken
}

func readJSONBody(r *http.Request, target any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func parseIDActionPath(path string) (int64, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, parts[1], true
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

func roomToResponse(room core.Room) coreRoomResponse {
	return coreRoomResponse{
		ID:              room.ID,
		TenantID:        room.TenantID,
		Channel:         room.Channel,
		ChannelRoomID:   room.ChannelRoomID,
		ChannelRoomType: room.ChannelRoomType,
		DisplayName:     room.DisplayName,
	}
}

func messageToResponse(message core.Message) coreMessageResponse {
	return coreMessageResponse{
		ID:              message.ID,
		RoomID:          message.RoomID,
		SourceMessageID: message.SourceMessageID,
		SenderID:        message.SenderID,
		SenderName:      message.SenderName,
		Payload:         message.Payload,
		Skipped:         message.Skipped,
	}
}

func invocationPtrToResponse(invocation *core.Invocation) *coreInvocationResponse {
	if invocation == nil {
		return nil
	}
	response := invocationToResponse(*invocation)
	return &response
}

func invocationToResponse(invocation core.Invocation) coreInvocationResponse {
	return coreInvocationResponse{
		ID:                invocation.ID,
		RoomID:            invocation.RoomID,
		Status:            invocation.Status,
		TriggerMessageID:  invocation.TriggerMessageID,
		StartMessageID:    invocation.StartMessageID,
		LastSeenMessageID: invocation.LastSeenMessageID,
		ErrorDetail:       invocation.ErrorDetail,
	}
}

func deliveryPtrToResponse(delivery *core.Delivery) *coreDeliveryResponse {
	if delivery == nil {
		return nil
	}
	response := deliveryToResponse(*delivery)
	return &response
}

func deliveryToResponse(delivery core.Delivery) coreDeliveryResponse {
	return coreDeliveryResponse{
		ID:           delivery.ID,
		RoomID:       delivery.RoomID,
		InvocationID: delivery.InvocationID,
		Payload:      delivery.Payload,
		Status:       delivery.Status,
	}
}
