package msghub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu sync.Mutex

	nextActorID int64
	nextRoomID  int64
	nextMsgID   int64
	nextInboxID int64
	nextTaskID  int64

	actors       map[int64]Actor
	actorsByKey  map[string]int64
	actorsByAuth map[string]int64

	rooms      map[int64]Room
	roomsByKey map[string]int64

	messages      map[int64]Message
	messagesByKey map[string]int64

	tasks map[int64]ActorTask

	inbox        map[int64]InboxItem
	inboxByActor map[int64][]int64
	inboxUniq    map[string]int64

	subscriptions map[int64]map[int64]bool

	tokens           map[string]CapabilityToken
	replyTokenByItem map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		actors:           map[int64]Actor{},
		actorsByKey:      map[string]int64{},
		actorsByAuth:     map[string]int64{},
		rooms:            map[int64]Room{},
		roomsByKey:       map[string]int64{},
		messages:         map[int64]Message{},
		messagesByKey:    map[string]int64{},
		tasks:            map[int64]ActorTask{},
		inbox:            map[int64]InboxItem{},
		inboxByActor:     map[int64][]int64{},
		inboxUniq:        map[string]int64{},
		subscriptions:    map[int64]map[int64]bool{},
		tokens:           map[string]CapabilityToken{},
		replyTokenByItem: map[string]string{},
	}
}

func (s *MemoryStore) UpsertActor(input Actor) Actor {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	key := strings.TrimSpace(input.ActorKey)
	if key == "" {
		key = fmt.Sprintf("actor-%d", s.nextActorID+1)
	}
	actor := input
	actor.ActorKey = key
	actor.Kind = strings.TrimSpace(actor.Kind)
	actor.Name = strings.TrimSpace(actor.Name)
	actor.Channel = strings.TrimSpace(actor.Channel)
	actor.ProviderAccountID = strings.TrimSpace(actor.ProviderAccountID)
	actor.Token = strings.TrimSpace(actor.Token)
	if actor.Name == "" {
		actor.Name = actor.ActorKey
	}
	if !actor.Enabled {
		actor.Enabled = true
	}

	if id := s.actorsByKey[key]; id > 0 {
		existing := s.actors[id]
		actor.ID = existing.ID
		actor.CreatedAt = existing.CreatedAt
		actor.UpdatedAt = now
		s.actors[id] = actor
		if actor.Token != "" {
			s.actorsByAuth[actor.Token] = id
		}
		return actor
	}

	s.nextActorID++
	actor.ID = s.nextActorID
	actor.CreatedAt = now
	actor.UpdatedAt = now
	s.actors[actor.ID] = actor
	s.actorsByKey[actor.ActorKey] = actor.ID
	if actor.Token != "" {
		s.actorsByAuth[actor.Token] = actor.ID
	}
	return actor
}

func (s *MemoryStore) SubscribeActorToRoom(actorID int64, roomID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subscriptions[roomID] == nil {
		s.subscriptions[roomID] = map[int64]bool{}
	}
	s.subscriptions[roomID][actorID] = true
}

func (s *MemoryStore) Authenticate(_ context.Context, token string) (Actor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.actorsByAuth[strings.TrimSpace(token)]
	if id <= 0 {
		return Actor{}, false, nil
	}
	actor := s.actors[id]
	if !actor.Enabled {
		return Actor{}, false, nil
	}
	return actor, true, nil
}

func (s *MemoryStore) AppendTerminalMessage(_ context.Context, actor Actor, input SendMessageRequest) (Message, bool, error) {
	if actor.Kind != ActorKindTerminal {
		return Message{}, false, fmt.Errorf("terminal actor is required")
	}
	if strings.TrimSpace(input.ExternalMessageID) == "" {
		return Message{}, false, fmt.Errorf("external_message_id is required")
	}
	if strings.TrimSpace(input.SenderID) == "" {
		return Message{}, false, fmt.Errorf("sender_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	room, err := s.upsertRoomLocked(actor, input.Room)
	if err != nil {
		return Message{}, false, err
	}
	key := messageKey(room.ID, actor.ID, input.ExternalMessageID)
	if id := s.messagesByKey[key]; id > 0 {
		return s.messages[id], true, nil
	}

	now := time.Now().UTC()
	messageTime := input.MessageTime
	if messageTime.IsZero() {
		messageTime = now
	}
	messageType := strings.TrimSpace(input.MessageType)
	if messageType == "" {
		messageType = "text"
	}
	body := cloneJSON(input.Body)
	s.nextMsgID++
	message := Message{
		ID:                s.nextMsgID,
		RoomID:            room.ID,
		SourceActorID:     actor.ID,
		ExternalMessageID: strings.TrimSpace(input.ExternalMessageID),
		SenderID:          strings.TrimSpace(input.SenderID),
		SenderName:        strings.TrimSpace(input.SenderName),
		MessageTime:       messageTime.UTC(),
		MessageType:       messageType,
		Body:              body,
		CreatedAt:         now,
	}
	s.messages[message.ID] = message
	s.messagesByKey[key] = message.ID
	s.enqueueMessageReceivedLocked(room, message)
	return message, false, nil
}

func (s *MemoryStore) CreateReplyTask(_ context.Context, actor Actor, input SendMessageRequest) (ActorTask, error) {
	if actor.Kind != ActorKindAIAgent {
		return ActorTask{}, fmt.Errorf("aiagent actor is required")
	}
	tokenValue := strings.TrimSpace(input.ContextToken)
	if tokenValue == "" {
		return ActorTask{}, fmt.Errorf("context_token is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.tokens[tokenValue]
	if !ok {
		return ActorTask{}, fmt.Errorf("context_token not found")
	}
	if token.ActorID != actor.ID || token.Action != CapabilityReply {
		return ActorTask{}, fmt.Errorf("context_token is not valid for this actor")
	}
	if token.UsesRemaining <= 0 || time.Now().UTC().After(token.ExpiresAt) {
		return ActorTask{}, fmt.Errorf("context_token expired")
	}
	room := s.rooms[token.RoomID]
	assignee, ok := s.findTerminalForRoomLocked(room)
	if !ok {
		return ActorTask{}, fmt.Errorf("terminal actor not found for room")
	}
	token.UsesRemaining--
	s.tokens[tokenValue] = token

	messageType := strings.TrimSpace(input.MessageType)
	if messageType == "" {
		messageType = "text"
	}
	payload, err := marshalJSON(map[string]any{
		"room":                roomToView(room),
		"message_type":        messageType,
		"body":                json.RawMessage(cloneJSON(input.Body)),
		"reply_to_message_id": token.MessageID,
	})
	if err != nil {
		return ActorTask{}, err
	}
	now := time.Now().UTC()
	s.nextTaskID++
	task := ActorTask{
		ID:               s.nextTaskID,
		RoomID:           room.ID,
		RequesterActorID: actor.ID,
		AssigneeActorID:  assignee.ID,
		TaskType:         TaskTypeSendMessage,
		Payload:          payload,
		Status:           TaskStatusPending,
		CreatedAt:        now,
	}
	s.tasks[task.ID] = task
	s.enqueueLocked(assignee.ID, EventMessageSendRequested, ResourceActorTask, task.ID)
	return task, nil
}

func (s *MemoryStore) ListUpdates(_ context.Context, actor Actor, afterID int64, limit int) ([]UpdateView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var updates []UpdateView
	for _, id := range s.inboxByActor[actor.ID] {
		if id <= afterID {
			continue
		}
		item := s.inbox[id]
		view, err := s.updateViewLocked(item)
		if err != nil {
			return nil, err
		}
		updates = append(updates, view)
		if len(updates) >= limit {
			break
		}
	}
	return updates, nil
}

func (s *MemoryStore) Ack(_ context.Context, actor Actor, input AckRequest) (AckResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	response := AckResponse{
		AckedUpdateIDs: []int64{},
		Tasks:          []ActorTaskView{},
	}
	for _, id := range input.UpdateIDs {
		item, ok := s.inbox[id]
		if !ok || item.ActorID != actor.ID {
			continue
		}
		if item.AckedAt.IsZero() {
			item.AckedAt = now
			s.inbox[id] = item
		}
		response.AckedUpdateIDs = append(response.AckedUpdateIDs, id)
	}
	for _, result := range input.TaskResults {
		task, ok := s.tasks[result.TaskID]
		if !ok {
			return response, fmt.Errorf("task %d not found", result.TaskID)
		}
		if task.AssigneeActorID != actor.ID {
			return response, fmt.Errorf("task %d is not assigned to actor", result.TaskID)
		}
		previousStatus := task.Status
		switch strings.TrimSpace(result.Status) {
		case TaskStatusAcked:
			task.Status = TaskStatusAcked
			task.AckedAt = now
		case TaskStatusFailed:
			task.Status = TaskStatusFailed
			task.AckedAt = now
			task.Payload = payloadWithError(task.Payload, result.Error)
		default:
			return response, fmt.Errorf("unsupported task status %q", result.Status)
		}
		if previousStatus != TaskStatusPending {
			if previousStatus == task.Status {
				response.Tasks = append(response.Tasks, taskToView(task))
				continue
			}
			return response, fmt.Errorf("task %d is already %s", result.TaskID, previousStatus)
		}
		s.tasks[task.ID] = task
		if task.Status == TaskStatusAcked {
			s.enqueueLocked(task.RequesterActorID, EventMessageSent, ResourceActorTask, task.ID)
		} else {
			s.enqueueLocked(task.RequesterActorID, EventMessageFailed, ResourceActorTask, task.ID)
		}
		response.Tasks = append(response.Tasks, taskToView(task))
	}
	return response, nil
}

func (s *MemoryStore) upsertRoomLocked(actor Actor, input RoomInput) (Room, error) {
	tenantID := firstNonEmpty(input.TenantID, DefaultTenantID)
	channel := firstNonEmpty(input.Channel, actor.Channel)
	providerAccountID := firstNonEmpty(input.ProviderAccountID, actor.ProviderAccountID)
	externalRoomID := strings.TrimSpace(input.ExternalRoomID)
	roomType := strings.TrimSpace(input.RoomType)
	if channel == "" {
		return Room{}, fmt.Errorf("room.channel is required")
	}
	if providerAccountID == "" {
		return Room{}, fmt.Errorf("room.provider_account_id is required")
	}
	if externalRoomID == "" {
		return Room{}, fmt.Errorf("room.external_room_id is required")
	}
	if roomType == "" {
		roomType = "direct"
	}
	if roomType != "direct" && roomType != "group" {
		return Room{}, fmt.Errorf("room.room_type must be direct or group")
	}
	key := roomKey(tenantID, channel, providerAccountID, externalRoomID)
	now := time.Now().UTC()
	if id := s.roomsByKey[key]; id > 0 {
		room := s.rooms[id]
		if strings.TrimSpace(input.DisplayName) != "" {
			room.DisplayName = strings.TrimSpace(input.DisplayName)
		}
		if strings.TrimSpace(input.OutboundTarget) != "" {
			room.OutboundTarget = strings.TrimSpace(input.OutboundTarget)
		}
		room.UpdatedAt = now
		s.rooms[id] = room
		return room, nil
	}

	s.nextRoomID++
	room := Room{
		ID:                s.nextRoomID,
		TenantID:          tenantID,
		Channel:           channel,
		ProviderAccountID: providerAccountID,
		ExternalRoomID:    externalRoomID,
		RoomType:          roomType,
		DisplayName:       strings.TrimSpace(input.DisplayName),
		OutboundTarget:    strings.TrimSpace(input.OutboundTarget),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if room.OutboundTarget == "" {
		room.OutboundTarget = room.ExternalRoomID
	}
	s.rooms[room.ID] = room
	s.roomsByKey[key] = room.ID
	return room, nil
}

func (s *MemoryStore) enqueueMessageReceivedLocked(room Room, message Message) {
	recipients := map[int64]bool{}
	for actorID := range s.subscriptions[room.ID] {
		actor := s.actors[actorID]
		if actor.Kind == ActorKindAIAgent && actor.Enabled {
			recipients[actorID] = true
		}
	}
	for _, actor := range s.actors {
		if actor.Kind == ActorKindAIAgent && actor.Enabled && actor.HasPermission(PermissionSubscribeAll) {
			recipients[actor.ID] = true
		}
	}
	for actorID := range recipients {
		token := CapabilityToken{
			Token:         randomToken(),
			ActorID:       actorID,
			RoomID:        room.ID,
			Action:        CapabilityReply,
			MessageID:     message.ID,
			UsesRemaining: 1,
			ExpiresAt:     time.Now().UTC().Add(time.Hour),
			CreatedAt:     time.Now().UTC(),
		}
		s.tokens[token.Token] = token
		s.replyTokenByItem[replyTokenKey(actorID, message.ID)] = token.Token
		s.enqueueLocked(actorID, EventMessageReceived, ResourceMessage, message.ID)
	}
}

func (s *MemoryStore) enqueueLocked(actorID int64, eventType, resourceType string, resourceID int64) {
	if actorID <= 0 {
		return
	}
	key := inboxKey(actorID, eventType, resourceType, resourceID)
	if s.inboxUniq[key] > 0 {
		return
	}
	s.nextInboxID++
	item := InboxItem{
		ID:           s.nextInboxID,
		ActorID:      actorID,
		EventType:    eventType,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		CreatedAt:    time.Now().UTC(),
	}
	s.inbox[item.ID] = item
	s.inboxByActor[actorID] = append(s.inboxByActor[actorID], item.ID)
	s.inboxUniq[key] = item.ID
}

func (s *MemoryStore) updateViewLocked(item InboxItem) (UpdateView, error) {
	payload, err := s.payloadForInboxItemLocked(item)
	if err != nil {
		return UpdateView{}, err
	}
	view := UpdateView{
		ID:           item.ID,
		EventType:    item.EventType,
		ResourceType: item.ResourceType,
		ResourceID:   item.ResourceID,
		Payload:      payload,
		CreatedAt:    item.CreatedAt,
	}
	if !item.AckedAt.IsZero() {
		ackedAt := item.AckedAt
		view.AckedAt = &ackedAt
	}
	return view, nil
}

func (s *MemoryStore) payloadForInboxItemLocked(item InboxItem) (json.RawMessage, error) {
	switch item.ResourceType {
	case ResourceMessage:
		message, ok := s.messages[item.ResourceID]
		if !ok {
			return nil, fmt.Errorf("message %d not found", item.ResourceID)
		}
		room := s.rooms[message.RoomID]
		return marshalJSON(map[string]any{
			"room":          roomToView(room),
			"message":       messageToView(message),
			"context_token": s.replyTokenByItem[replyTokenKey(item.ActorID, message.ID)],
		})
	case ResourceActorTask:
		task, ok := s.tasks[item.ResourceID]
		if !ok {
			return nil, fmt.Errorf("task %d not found", item.ResourceID)
		}
		room := s.rooms[task.RoomID]
		return marshalJSON(map[string]any{
			"room": roomToView(room),
			"task": taskToView(task),
		})
	default:
		return nil, fmt.Errorf("unsupported resource type %q", item.ResourceType)
	}
}

func (s *MemoryStore) findTerminalForRoomLocked(room Room) (Actor, bool) {
	for _, actor := range s.actors {
		if !actor.Enabled || actor.Kind != ActorKindTerminal {
			continue
		}
		if actor.Channel == room.Channel && actor.ProviderAccountID == room.ProviderAccountID {
			return actor, true
		}
	}
	return Actor{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func roomKey(tenantID, channel, providerAccountID, externalRoomID string) string {
	return strings.Join([]string{tenantID, channel, providerAccountID, externalRoomID}, "\x00")
}

func messageKey(roomID int64, actorID int64, externalMessageID string) string {
	return fmt.Sprintf("%d\x00%d\x00%s", roomID, actorID, strings.TrimSpace(externalMessageID))
}

func inboxKey(actorID int64, eventType, resourceType string, resourceID int64) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%d", actorID, eventType, resourceType, resourceID)
}

func replyTokenKey(actorID int64, messageID int64) string {
	return fmt.Sprintf("%d\x00%d", actorID, messageID)
}

func randomToken() string {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}

func marshalJSON(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func payloadWithError(payload json.RawMessage, message string) json.RawMessage {
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		data = map[string]any{}
	}
	data["error"] = strings.TrimSpace(message)
	out, err := json.Marshal(data)
	if err != nil {
		return payload
	}
	return json.RawMessage(out)
}
