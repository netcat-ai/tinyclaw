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
	RegisterRoom(ctx context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error)
	CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
	ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error)
	AckCoreDelivery(ctx context.Context, id int64) (core.Delivery, error)
}

type MemorySearchStore interface {
	SearchRoomMemoryByToken(ctx context.Context, token string, input core.MemorySearchInput) ([]core.MemoryItem, error)
}

type Server struct {
	core     CoreStore
	apiToken string
	mux      *http.ServeMux
}

func NewServer(core CoreStore, apiToken string) *Server {
	server := &Server{
		core:     core,
		apiToken: strings.TrimSpace(apiToken),
		mux:      http.NewServeMux(),
	}
	server.RegisterRoutes(server.mux)
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/api/rooms", s.handleRooms)
	mux.HandleFunc("/api/messages", s.handleMessages)
	mux.HandleFunc("/api/deliveries", s.handleListDeliveries)
	mux.HandleFunc("/api/deliveries/", s.handleDeliveryAction)
	mux.HandleFunc("/internal/memory/search", s.handleMemorySearch)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

type registerRoomResponse struct {
	Room         coreRoomResponse         `json:"room"`
	AgentSession coreAgentSessionResponse `json:"agent_session"`
}

type createMessageResponse struct {
	Message   coreMessageResponse `json:"message"`
	Duplicate bool                `json:"duplicate"`
	Triggered bool                `json:"triggered"`
}

type coreRoomResponse struct {
	ID              int64  `json:"id"`
	TenantID        string `json:"tenant_id"`
	Channel         string `json:"channel"`
	ChannelRoomID   string `json:"channel_room_id"`
	ChannelRoomType string `json:"channel_room_type"`
	DisplayName     string `json:"display_name,omitempty"`
	OutboundAlias   string `json:"outbound_alias"`
}

type coreAgentSessionResponse struct {
	ID                     int64           `json:"id"`
	RoomID                 int64           `json:"room_id"`
	AgentKey               string          `json:"agent_key"`
	Enabled                bool            `json:"enabled"`
	TriggerPolicy          json.RawMessage `json:"trigger_policy,omitempty"`
	TriggerMessageID       int64           `json:"trigger_message_id,omitempty"`
	LastProcessedMessageID int64           `json:"last_processed_message_id"`
	CodexSessionID         string          `json:"codex_session_id,omitempty"`
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

type coreDeliveryResponse struct {
	ID                   int64           `json:"id"`
	RoomID               int64           `json:"room_id"`
	AgentSessionID       int64           `json:"agent_session_id"`
	SourceMessageAfterID int64           `json:"source_message_after_id"`
	SourceMessageUntilID int64           `json:"source_message_until_id"`
	Payload              json.RawMessage `json:"payload"`
	Status               int16           `json:"status"`
}

type deliveriesPageResponse struct {
	Deliveries []coreDeliveryResponse `json:"deliveries"`
	NextID     int64                  `json:"next_id"`
}

type memorySearchResponse struct {
	Items []core.MemoryItem `json:"items"`
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
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

	var input core.RegisterRoomInput
	if err := readJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.core.RegisterRoom(r.Context(), input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, registerRoomResponse{
		Room:         roomToResponse(result.Room),
		AgentSession: agentSessionToResponse(result.AgentSession),
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
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

	var input core.CreateMessageInput
	if err := readJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.core.CreateMessage(r.Context(), input)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeAPIError(w, http.StatusNotFound, "room_not_found", err.Error())
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, createMessageResponse{
		Message:   messageToResponse(result.Message),
		Duplicate: result.Duplicate,
		Triggered: result.Triggered,
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

func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	store, ok := s.core.(MemorySearchStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "memory search is unavailable")
		return
	}
	token := bearerToken(r)
	if token == "" {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing memory capability token")
		return
	}
	var input core.MemorySearchInput
	if err := readJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	items, err := store.SearchRoomMemoryByToken(r.Context(), token, input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, memorySearchResponse{Items: items})
}

func (s *Server) authenticateAPIBearer(r *http.Request) bool {
	if strings.TrimSpace(s.apiToken) == "" {
		return false
	}
	token := bearerToken(r)
	return token != "" && token == s.apiToken
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
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
		OutboundAlias:   room.OutboundAlias,
	}
}

func agentSessionToResponse(session core.AgentSession) coreAgentSessionResponse {
	return coreAgentSessionResponse{
		ID:                     session.ID,
		RoomID:                 session.RoomID,
		AgentKey:               session.AgentKey,
		Enabled:                session.Enabled,
		TriggerPolicy:          session.TriggerPolicy,
		TriggerMessageID:       session.TriggerMessageID,
		LastProcessedMessageID: session.LastProcessedMessageID,
		CodexSessionID:         session.CodexSessionID,
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

func deliveryToResponse(delivery core.Delivery) coreDeliveryResponse {
	return coreDeliveryResponse{
		ID:                   delivery.ID,
		RoomID:               delivery.RoomID,
		AgentSessionID:       delivery.AgentSessionID,
		SourceMessageAfterID: delivery.SourceMessageAfterID,
		SourceMessageUntilID: delivery.SourceMessageUntilID,
		Payload:              delivery.Payload,
		Status:               delivery.Status,
	}
}
