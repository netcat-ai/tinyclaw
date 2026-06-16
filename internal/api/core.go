package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"tinyclaw/internal/core"
	"tinyclaw/internal/ingest"
)

type CoreStore interface {
	RegisterRoom(ctx context.Context, input core.RegisterRoomInput) (core.RegisterRoomResult, error)
	CreateMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
	ListCoreDeliveries(ctx context.Context, channel string, afterID int64) ([]core.Delivery, error)
	AckCoreDelivery(ctx context.Context, id int64) (core.Delivery, error)
}

type MediaStore interface {
	GetCoreMessageByID(ctx context.Context, id int64) (core.Message, error)
}

type APIClientStore interface {
	AuthenticateAPIClient(ctx context.Context, clientID string, secret string) (core.APIClient, error)
}

type AdminStore interface {
	APIClientStore
	ListAdminRooms(ctx context.Context, limit int) ([]core.AdminRoomSummary, error)
	GetAdminRoomTimeline(ctx context.Context, roomID int64, beforeMessageID int64, limit int) (core.AdminRoomTimeline, error)
	ListAdminRoomMemory(ctx context.Context, input core.AdminMemoryListInput) ([]core.MemoryItem, error)
	ListAgents(ctx context.Context) ([]core.Agent, error)
	GetAgent(ctx context.Context, id int64) (core.Agent, error)
	CreateAgent(ctx context.Context, input core.UpsertAgentInput) (core.Agent, error)
	UpdateAgent(ctx context.Context, id int64, input core.UpsertAgentInput) (core.Agent, error)
}

type MemorySearchStore interface {
	SearchRoomMemoryByToken(ctx context.Context, token string, input core.MemorySearchInput) ([]core.MemoryItem, error)
}

type CommandHandler interface {
	HandleMessage(ctx context.Context, message core.Message) bool
}

type MessageIngestor interface {
	IngestMessage(ctx context.Context, input core.CreateMessageInput) (core.CreateMessageResult, error)
}

type Server struct {
	core         CoreStore
	messages     MessageIngestor
	apiToken     string
	adminSecret  string
	mediaBaseURL string
	mediaToken   string
	mux          *http.ServeMux
}

func NewServer(core CoreStore, apiToken string) *Server {
	return NewServerWithCommandHandler(core, nil, apiToken)
}

func NewServerWithCommandHandler(core CoreStore, commands CommandHandler, apiToken string, adminSecret ...string) *Server {
	server := &Server{
		core:         core,
		messages:     ingest.NewMessageIngestor(core, commands),
		apiToken:     strings.TrimSpace(apiToken),
		adminSecret:  strings.TrimSpace(firstString(adminSecret)),
		mediaBaseURL: "http://127.0.0.1:36080",
		mux:          http.NewServeMux(),
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
	mux.HandleFunc("/admin/api/rooms", s.handleAdminRooms)
	mux.HandleFunc("/admin/api/rooms/", s.handleAdminRoomAction)
	mux.HandleFunc("/admin/api/agents", s.handleAdminAgents)
	mux.HandleFunc("/admin/api/agents/", s.handleAdminAgentAction)
	mux.HandleFunc("/admin/api/deliveries/", s.handleAdminDeliveryAction)
	mux.HandleFunc("/internal/memory/search", s.handleMemorySearch)
	mux.HandleFunc("/internal/media", s.handleInternalMedia)
}

func (s *Server) SetMediaRedirectConfig(baseURL string, token string) {
	if strings.TrimSpace(baseURL) != "" {
		s.mediaBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	s.mediaToken = strings.TrimSpace(token)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleInternalMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	store, ok := s.core.(MediaStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "media API is unavailable")
		return
	}
	msgID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("msgid")), 10, 64)
	if err != nil || msgID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_msgid", "msgid must be an internal message id")
		return
	}
	message, err := store.GetCoreMessageByID(r.Context(), msgID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "message_not_found", "message not found")
		return
	}
	var body struct {
		WOCInstance string `json:"woc_instance"`
		WOCRoomID   string `json:"woc_room_id"`
		WOCType     string `json:"woc_type"`
		Content     string `json:"content"`
		RawText     string `json:"raw_text"`
	}
	_ = json.Unmarshal(message.Body, &body)
	instanceID := strings.TrimSpace(body.WOCInstance)
	roomID := strings.TrimSpace(firstNonEmpty(body.WOCRoomID, message.RoomIDRaw))
	agentMsgID := wocAgentMessageID(message.MsgID, body.RawText, body.Content)
	if strings.TrimSpace(message.Source) != "wechat" || !isWOCMediaMessage(message, body.WOCType, body.RawText, body.Content) {
		writeAPIError(w, http.StatusBadRequest, "unsupported_media", "message is not a WOC image")
		return
	}
	if instanceID == "" || roomID == "" || agentMsgID == "" {
		writeAPIError(w, http.StatusBadRequest, "missing_media_locator", "message does not contain WOC media locator")
		return
	}
	values := make(url.Values)
	values.Set("instanceid", instanceID)
	values.Set("roomid", roomID)
	values.Set("msgid", agentMsgID)
	values.Set("token", s.mediaToken)
	http.Redirect(w, r, s.mediaBaseURL+"/api/agent/media?"+values.Encode(), http.StatusFound)
}

func isWOCMediaMessage(message core.Message, wocType string, hints ...string) bool {
	if strings.TrimSpace(message.MsgType) == "image" {
		return true
	}
	if strings.TrimSpace(wocType) != "link" {
		return false
	}
	for _, hint := range hints {
		if strings.Contains(hint, "当前版本不支持展示") {
			return true
		}
	}
	return false
}

var wocLocalIDPattern = regexp.MustCompile(`\blocal_id=(\d+)\b`)

func wocAgentMessageID(msgID string, hints ...string) string {
	for _, hint := range hints {
		if match := wocLocalIDPattern.FindStringSubmatch(hint); len(match) == 2 {
			return match[1]
		}
	}
	msgID = strings.TrimSpace(msgID)
	if after, ok := strings.CutPrefix(msgID, "woc:"); ok {
		return strings.TrimSpace(after)
	}
	return msgID
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
	ID                      int64           `json:"id"`
	RoomID                  int64           `json:"room_id"`
	Enabled                 bool            `json:"enabled"`
	TriggerPolicy           json.RawMessage `json:"trigger_policy,omitempty"`
	PendingTriggerMessageID int64           `json:"pending_trigger_message_id,omitempty"`
	CaughtUpMessageID       int64           `json:"caught_up_message_id"`
	CodexSessionID          string          `json:"codex_session_id,omitempty"`
}

type coreMessageResponse struct {
	ID              int64           `json:"id"`
	RoomID          int64           `json:"room_id"`
	Source          string          `json:"source"`
	MsgID           string          `json:"msgid"`
	Action          string          `json:"action"`
	FromID          string          `json:"from"`
	ToList          json.RawMessage `json:"tolist"`
	RoomIDRaw       string          `json:"roomid"`
	MsgTime         int64           `json:"msgtime"`
	MsgType         string          `json:"msgtype"`
	Body            json.RawMessage `json:"body"`
	SourceMessageID string          `json:"source_message_id,omitempty"`
	SenderID        string          `json:"sender_id"`
	SenderName      string          `json:"sender_name,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type coreDeliveryResponse struct {
	ID                  int64           `json:"id"`
	RoomID              int64           `json:"room_id"`
	AgentSessionID      int64           `json:"agent_session_id"`
	SourceMessageFromID int64           `json:"source_message_from_id"`
	SourceMessageToID   int64           `json:"source_message_to_id"`
	Payload             json.RawMessage `json:"payload"`
	Status              int16           `json:"status"`
}

type deliveriesPageResponse struct {
	Deliveries []coreDeliveryResponse `json:"deliveries"`
}

type memorySearchResponse struct {
	Items []core.MemoryItem `json:"items"`
}

type adminRoomsResponse struct {
	Rooms []adminRoomSummaryResponse `json:"rooms"`
}

type adminRoomSummaryResponse struct {
	Room                 coreRoomResponse         `json:"room"`
	AgentSession         coreAgentSessionResponse `json:"agent_session,omitempty"`
	PendingDeliveryCount int64                    `json:"pending_delivery_count"`
	LastMessageTime      time.Time                `json:"last_message_time,omitempty"`
	UpdatedAt            time.Time                `json:"updated_at"`
}

type adminTimelineResponse struct {
	Room          coreRoomResponse           `json:"room"`
	AgentSessions []coreAgentSessionResponse `json:"agent_sessions"`
	Messages      []adminMessageResponse     `json:"messages"`
	Deliveries    []adminDeliveryResponse    `json:"deliveries"`
	HasMore       bool                       `json:"has_more"`
}

type adminMessageResponse struct {
	ID              int64           `json:"id"`
	RoomID          int64           `json:"room_id"`
	Source          string          `json:"source"`
	MsgID           string          `json:"msgid"`
	Action          string          `json:"action"`
	FromID          string          `json:"from"`
	ToList          json.RawMessage `json:"tolist"`
	RoomIDRaw       string          `json:"roomid"`
	MsgTime         int64           `json:"msgtime"`
	MsgType         string          `json:"msgtype"`
	Body            json.RawMessage `json:"body"`
	SourceMessageID string          `json:"source_message_id,omitempty"`
	SenderID        string          `json:"sender_id"`
	SenderName      string          `json:"sender_name,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	MessageTime     time.Time       `json:"message_time,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

type adminDeliveryResponse struct {
	coreDeliveryResponse
	CreatedAt time.Time `json:"created_at"`
	AckedAt   time.Time `json:"acked_at,omitempty"`
}

type adminMemoryResponse struct {
	Items []core.MemoryItem `json:"items"`
}

type adminAgentsResponse struct {
	Agents []core.Agent `json:"agents"`
}

type adminAgentResponse struct {
	Agent core.Agent `json:"agent"`
}

type injectMessageRequest struct {
	SenderID             string          `json:"sender_id"`
	SenderName           string          `json:"sender_name"`
	Text                 string          `json:"text"`
	Payload              json.RawMessage `json:"payload"`
	SuppressAgentTrigger bool            `json:"suppress_agent_trigger"`
}

func (s *Server) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	store, ok := s.core.(AdminStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "admin API is unavailable")
		return
	}
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	switch r.Method {
	case http.MethodGet:
		agents, err := store.ListAgents(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "list_failed", err.Error())
			return
		}
		if agents == nil {
			agents = []core.Agent{}
		}
		writeAPIJSON(w, http.StatusOK, adminAgentsResponse{Agents: agents})
	case http.MethodPost:
		var input core.UpsertAgentInput
		if err := readJSONBody(r, &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		agent, err := store.CreateAgent(r.Context(), input)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "create_failed", err.Error())
			return
		}
		writeAPIJSON(w, http.StatusOK, adminAgentResponse{Agent: agent})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Server) handleAdminAgentAction(w http.ResponseWriter, r *http.Request) {
	store, ok := s.core.(AdminStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "admin API is unavailable")
		return
	}
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/agents/"), "/"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown agent")
		return
	}
	switch r.Method {
	case http.MethodGet:
		agent, err := store.GetAgent(r.Context(), id)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeAPIJSON(w, http.StatusOK, adminAgentResponse{Agent: agent})
	case http.MethodPut:
		var input core.UpsertAgentInput
		if err := readJSONBody(r, &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		agent, err := store.UpdateAgent(r.Context(), id, input)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "update_failed", err.Error())
			return
		}
		writeAPIJSON(w, http.StatusOK, adminAgentResponse{Agent: agent})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or PUT")
	}
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAdapter(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API credentials")
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
	if !s.authenticateAdapter(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API credentials")
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
	result, err := s.messages.IngestMessage(r.Context(), input)
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
	if !s.authenticateAdapter(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API credentials")
		return
	}
	channels := parseDeliveryChannels(r.URL.Query().Get("channels"))
	if len(channels) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "channels is required")
		return
	}
	afterID := parsePositiveInt64(firstNonEmpty(r.URL.Query().Get("after_id"), r.URL.Query().Get("id")), 0)
	var deliveries []core.Delivery
	for _, channel := range channels {
		channelDeliveries, err := s.core.ListCoreDeliveries(r.Context(), channel, afterID)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "list_failed", err.Error())
			return
		}
		deliveries = append(deliveries, channelDeliveries...)
	}
	sort.SliceStable(deliveries, func(i, j int) bool {
		return deliveries[i].ID < deliveries[j].ID
	})
	response := make([]coreDeliveryResponse, 0, len(deliveries))
	for _, delivery := range deliveries {
		response = append(response, deliveryToResponse(delivery))
	}
	writeAPIJSON(w, http.StatusOK, deliveriesPageResponse{Deliveries: response})
}

func parseDeliveryChannels(value string) []string {
	seen := map[string]bool{}
	var channels []string
	for _, part := range strings.Split(value, ",") {
		channel := strings.TrimSpace(part)
		if channel == "" || seen[channel] {
			continue
		}
		seen[channel] = true
		channels = append(channels, channel)
	}
	return channels
}

func (s *Server) handleDeliveryAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAdapter(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API credentials")
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

func (s *Server) handleAdminRooms(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	switch r.Method {
	case http.MethodGet:
		store, ok := s.core.(AdminStore)
		if !ok {
			writeAPIError(w, http.StatusServiceUnavailable, "disabled", "admin API is unavailable")
			return
		}
		rooms, err := store.ListAdminRooms(r.Context(), parsePositiveInt(r.URL.Query().Get("limit"), 100))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "list_failed", err.Error())
			return
		}
		response := make([]adminRoomSummaryResponse, 0, len(rooms))
		for _, room := range rooms {
			response = append(response, adminRoomSummaryToResponse(room))
		}
		writeAPIJSON(w, http.StatusOK, adminRoomsResponse{Rooms: response})
	case http.MethodPost:
		s.handleAdminRegisterRoom(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Server) handleAdminRegisterRoom(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleAdminRoomAction(w http.ResponseWriter, r *http.Request) {
	roomID, action, ok := parseAdminRoomActionPath(strings.TrimPrefix(r.URL.Path, "/admin/api/rooms/"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown room action")
		return
	}
	switch action {
	case "timeline":
		s.handleAdminRoomTimeline(w, r, roomID)
	case "memory":
		s.handleAdminRoomMemory(w, r, roomID)
	case "messages:inject":
		s.handleAdminInjectMessage(w, r, roomID)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "unknown room action")
	}
}

func (s *Server) handleAdminRoomTimeline(w http.ResponseWriter, r *http.Request, roomID int64) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	store, ok := s.core.(AdminStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "admin API is unavailable")
		return
	}
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	timeline, err := store.GetAdminRoomTimeline(
		r.Context(),
		roomID,
		parsePositiveInt64(r.URL.Query().Get("before_message_id"), 0),
		parsePositiveInt(r.URL.Query().Get("limit"), 100),
	)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "timeline_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, adminTimelineToResponse(timeline))
}

func (s *Server) handleAdminRoomMemory(w http.ResponseWriter, r *http.Request, roomID int64) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	store, ok := s.core.(AdminStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disabled", "admin API is unavailable")
		return
	}
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	items, err := store.ListAdminRoomMemory(r.Context(), core.AdminMemoryListInput{
		RoomID: roomID,
		Status: r.URL.Query().Get("status"),
		Types:  parseCSVQuery(r.URL.Query().Get("types")),
		Limit:  parsePositiveInt(r.URL.Query().Get("limit"), 100),
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "memory_failed", err.Error())
		return
	}
	if items == nil {
		items = []core.MemoryItem{}
	}
	writeAPIJSON(w, http.StatusOK, adminMemoryResponse{Items: items})
}

func (s *Server) handleAdminInjectMessage(w http.ResponseWriter, r *http.Request, roomID int64) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	var input injectMessageRequest
	if err := readJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body := input.Payload
	if len(body) == 0 {
		body = mustMarshalJSON(map[string]string{
			"content": strings.TrimSpace(input.Text),
		})
	}
	result, err := s.messages.IngestMessage(r.Context(), core.CreateMessageInput{
		RoomID:               roomID,
		Source:               "admin",
		MsgID:                "admin:" + uuid.NewString(),
		Action:               "send",
		FromID:               firstNonEmpty(strings.TrimSpace(input.SenderID), "admin"),
		MsgTime:              time.Now().UTC().Unix(),
		MsgType:              "text",
		Body:                 body,
		SuppressAgentTrigger: input.SuppressAgentTrigger,
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeAPIError(w, http.StatusNotFound, "room_not_found", err.Error())
			return
		}
		writeAPIError(w, http.StatusBadRequest, "inject_failed", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, createMessageResponse{
		Message:   messageToResponse(result.Message),
		Duplicate: result.Duplicate,
		Triggered: result.Triggered,
	})
}

func (s *Server) handleAdminDeliveryAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !s.authenticateAdmin(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin credentials")
		return
	}
	id, action, ok := parseIDActionPath(strings.TrimPrefix(r.URL.Path, "/admin/api/deliveries/"))
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

func (s *Server) authenticateAdapter(r *http.Request) bool {
	if s.authenticateAPIBearer(r) {
		return true
	}
	return s.authenticateBasicPermission(r, core.APIClientPermissionAdapter) ||
		s.authenticateBasicPermission(r, core.APIClientPermissionAdmin)
}

func (s *Server) authenticateAdmin(r *http.Request) bool {
	return s.authenticateBasicPermission(r, core.APIClientPermissionAdmin)
}

func (s *Server) authenticateBasicPermission(r *http.Request, permission string) bool {
	clientID, secret, ok := r.BasicAuth()
	if !ok {
		return false
	}
	if s.authenticateBuiltInAdmin(clientID, secret, permission) {
		return true
	}
	store, ok := s.core.(APIClientStore)
	if !ok {
		return false
	}
	client, err := store.AuthenticateAPIClient(r.Context(), clientID, secret)
	if err != nil {
		return false
	}
	return client.HasPermission(permission)
}

func (s *Server) authenticateBuiltInAdmin(clientID string, secret string, permission string) bool {
	if permission != core.APIClientPermissionAdmin {
		return false
	}
	return strings.TrimSpace(s.adminSecret) != "" &&
		strings.TrimSpace(clientID) == "admin" &&
		strings.TrimSpace(secret) == s.adminSecret
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}

func readJSONBody(r *http.Request, target any) error {
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

func parseAdminRoomActionPath(path string) (int64, string, bool) {
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

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parsePositiveInt64(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseCSVQuery(value string) []string {
	var values []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func mustMarshalJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
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
		ID:                      session.ID,
		RoomID:                  session.RoomID,
		Enabled:                 session.Enabled,
		TriggerPolicy:           session.TriggerPolicy,
		PendingTriggerMessageID: session.PendingTriggerMessageID,
		CaughtUpMessageID:       session.CaughtUpMessageID,
		CodexSessionID:          session.CodexSessionID,
	}
}

func messageToResponse(message core.Message) coreMessageResponse {
	return coreMessageResponse{
		ID:              message.ID,
		RoomID:          message.RoomID,
		Source:          message.Source,
		MsgID:           message.MsgID,
		Action:          message.Action,
		FromID:          message.FromID,
		ToList:          message.ToList,
		RoomIDRaw:       message.RoomIDRaw,
		MsgTime:         message.MsgTime,
		MsgType:         message.MsgType,
		Body:            message.Body,
		SourceMessageID: message.MsgID,
		SenderID:        message.SenderID,
		SenderName:      message.SenderName,
		Payload:         message.Body,
	}
}

func deliveryToResponse(delivery core.Delivery) coreDeliveryResponse {
	return coreDeliveryResponse{
		ID:                  delivery.ID,
		RoomID:              delivery.RoomID,
		AgentSessionID:      delivery.AgentSessionID,
		SourceMessageFromID: delivery.SourceMessageFromID,
		SourceMessageToID:   delivery.SourceMessageToID,
		Payload:             delivery.Payload,
		Status:              delivery.Status,
	}
}

func adminRoomSummaryToResponse(summary core.AdminRoomSummary) adminRoomSummaryResponse {
	return adminRoomSummaryResponse{
		Room:                 roomToResponse(summary.Room),
		AgentSession:         agentSessionToResponse(summary.AgentSession),
		PendingDeliveryCount: summary.PendingDeliveryCount,
		LastMessageTime:      summary.LastMessageTime,
		UpdatedAt:            summary.Room.UpdatedAt,
	}
}

func adminTimelineToResponse(timeline core.AdminRoomTimeline) adminTimelineResponse {
	sessions := make([]coreAgentSessionResponse, 0, len(timeline.AgentSessions))
	for _, session := range timeline.AgentSessions {
		sessions = append(sessions, agentSessionToResponse(session))
	}
	messages := make([]adminMessageResponse, 0, len(timeline.Messages))
	for _, message := range timeline.Messages {
		messages = append(messages, adminMessageToResponse(message))
	}
	deliveries := make([]adminDeliveryResponse, 0, len(timeline.Deliveries))
	for _, delivery := range timeline.Deliveries {
		deliveries = append(deliveries, adminDeliveryToResponse(delivery))
	}
	return adminTimelineResponse{
		Room:          roomToResponse(timeline.Room),
		AgentSessions: sessions,
		Messages:      messages,
		Deliveries:    deliveries,
		HasMore:       timeline.HasMore,
	}
}

func adminMessageToResponse(message core.Message) adminMessageResponse {
	return adminMessageResponse{
		ID:              message.ID,
		RoomID:          message.RoomID,
		Source:          message.Source,
		MsgID:           message.MsgID,
		Action:          message.Action,
		FromID:          message.FromID,
		ToList:          message.ToList,
		RoomIDRaw:       message.RoomIDRaw,
		MsgTime:         message.MsgTime,
		MsgType:         message.MsgType,
		Body:            message.Body,
		SourceMessageID: message.MsgID,
		SenderID:        message.SenderID,
		SenderName:      message.SenderName,
		Payload:         message.Body,
		MessageTime:     message.MessageTime,
		CreatedAt:       message.CreatedAt,
	}
}

func adminDeliveryToResponse(delivery core.Delivery) adminDeliveryResponse {
	return adminDeliveryResponse{
		coreDeliveryResponse: deliveryToResponse(delivery),
		CreatedAt:            delivery.CreatedAt,
		AckedAt:              delivery.AckedAt,
	}
}
