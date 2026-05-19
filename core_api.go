package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

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
	DispatchState   int64           `json:"dispatch_state"`
}

type coreInvocationResponse struct {
	ID               int64           `json:"id"`
	RoomID           int64           `json:"room_id"`
	Status           string          `json:"status"`
	TriggerMessageID int64           `json:"trigger_message_id,omitempty"`
	InputSnapshot    json.RawMessage `json:"input_snapshot,omitempty"`
	OutputSnapshot   json.RawMessage `json:"output_snapshot,omitempty"`
}

type coreDeliveryResponse struct {
	ID           int64           `json:"id"`
	Seq          int64           `json:"seq"`
	RoomID       int64           `json:"room_id"`
	InvocationID int64           `json:"invocation_id"`
	Payload      json.RawMessage `json:"payload"`
	Status       string          `json:"status"`
}

type deliveriesPageResponse struct {
	Deliveries []coreDeliveryResponse `json:"deliveries"`
	NextSeq    int64                  `json:"next_seq"`
}

type invocationActionRequest struct {
	Text           string          `json:"text"`
	OutputSnapshot json.RawMessage `json:"output_snapshot"`
	Detail         string          `json:"detail"`
}

func (api *controlAPI) handleInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !api.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	if api.core == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "core API is unavailable")
		return
	}

	var input InboundMessageInput
	if err := readJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := api.core.IngestCoreMessage(r.Context(), input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
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

func (api *controlAPI) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	if !api.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	rawSeq := strings.TrimSpace(r.URL.Query().Get("seq"))
	if rawSeq == "" {
		rawSeq = "0"
	}
	afterSeq, err := strconv.ParseInt(rawSeq, 10, 64)
	if err != nil || afterSeq < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "seq must be a non-negative integer")
		return
	}
	deliveries, err := api.core.ListCoreDeliveries(r.Context(), channel, afterSeq)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "list_failed", err.Error())
		return
	}
	response := make([]coreDeliveryResponse, 0, len(deliveries))
	nextSeq := afterSeq
	for _, delivery := range deliveries {
		response = append(response, deliveryToResponse(delivery))
		if delivery.Seq > nextSeq {
			nextSeq = delivery.Seq
		}
	}
	writeAPIJSON(w, http.StatusOK, deliveriesPageResponse{Deliveries: response, NextSeq: nextSeq})
}

func (api *controlAPI) handleDeliveryAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !api.authenticateAPIBearer(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API bearer token")
		return
	}
	id, action, ok := parseIDActionPath(strings.TrimPrefix(r.URL.Path, "/api/deliveries/"))
	if !ok || action != "ack" {
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown delivery action")
		return
	}
	delivery, err := api.core.AckCoreDelivery(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "ack_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, deliveryToResponse(delivery))
}

func (api *controlAPI) handleInvocationAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !api.authenticateAPIBearer(r) {
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
	var result InvocationResult
	var err error
	switch action {
	case "complete":
		result, err = api.core.CompleteCoreInvocation(r.Context(), id, CompleteInvocationInput{
			Output: req.OutputSnapshot,
			Text:   req.Text,
		})
	case "fail":
		result, err = api.core.FailCoreInvocation(r.Context(), id, req.Detail)
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

func (api *controlAPI) authenticateAPIBearer(r *http.Request) bool {
	if strings.TrimSpace(api.apiToken) == "" {
		return false
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
	return token != "" && token == api.apiToken
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

func roomToResponse(room CoreRoom) coreRoomResponse {
	return coreRoomResponse{
		ID:              room.ID,
		TenantID:        room.TenantID,
		Channel:         room.Channel,
		ChannelRoomID:   room.ChannelRoomID,
		ChannelRoomType: room.ChannelRoomType,
		DisplayName:     room.DisplayName,
	}
}

func messageToResponse(message CoreMessage) coreMessageResponse {
	return coreMessageResponse{
		ID:              message.ID,
		RoomID:          message.RoomID,
		SourceMessageID: message.SourceMessageID,
		SenderID:        message.SenderID,
		SenderName:      message.SenderName,
		Payload:         message.Payload,
		DispatchState:   message.DispatchState,
	}
}

func invocationPtrToResponse(invocation *CoreInvocation) *coreInvocationResponse {
	if invocation == nil {
		return nil
	}
	response := invocationToResponse(*invocation)
	return &response
}

func invocationToResponse(invocation CoreInvocation) coreInvocationResponse {
	return coreInvocationResponse{
		ID:               invocation.ID,
		RoomID:           invocation.RoomID,
		Status:           invocation.Status,
		TriggerMessageID: invocation.TriggerMessageID,
		InputSnapshot:    invocation.InputSnapshot,
		OutputSnapshot:   invocation.OutputSnapshot,
	}
}

func deliveryPtrToResponse(delivery *CoreDelivery) *coreDeliveryResponse {
	if delivery == nil {
		return nil
	}
	response := deliveryToResponse(*delivery)
	return &response
}

func deliveryToResponse(delivery CoreDelivery) coreDeliveryResponse {
	return coreDeliveryResponse{
		ID:           delivery.ID,
		Seq:          delivery.Seq,
		RoomID:       delivery.RoomID,
		InvocationID: delivery.InvocationID,
		Payload:      delivery.Payload,
		Status:       delivery.Status,
	}
}
