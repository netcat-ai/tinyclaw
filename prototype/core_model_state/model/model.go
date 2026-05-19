package model

import (
	"fmt"
	"slices"
	"time"
)

const (
	DispatchWaiting int64 = 0
	DispatchSkipped int64 = 1

	InvocationStatusQueued    int16 = 0
	InvocationStatusRunning   int16 = 1
	InvocationStatusCompleted int16 = 2
	InvocationStatusFailed    int16 = 3
	InvocationStatusCancelled int16 = 4

	DeliveryStatusPending int16 = 0
	DeliveryStatusAcked   int16 = 1
	DeliveryStatusFailed  int16 = 2

	FirstInvocationID int64 = 1000
)

type Room struct {
	ID              int64
	TenantID        string
	Channel         string
	ChannelRoomID   string
	ChannelRoomType string
	DisplayName     string
}

type Message struct {
	ID              int64
	RoomID          int64
	SourceMessageID string
	SenderID        string
	Text            string
	MessageTime     time.Time
	DispatchState   int64
}

type Invocation struct {
	ID               int64
	RoomID           int64
	Status           int16
	TriggerMessageID int64
	InputSnapshot    string
	OutputSnapshot   string
	Output           string
}

type Delivery struct {
	ID           int64
	Seq          int64
	RoomID       int64
	InvocationID int64
	Payload      string
	Status       int16
}

type State struct {
	Now              time.Time
	Rooms            []Room
	Messages         []Message
	Invocations      []Invocation
	Deliveries       []Delivery
	NextRoomID       int64
	NextMessageID    int64
	NextInvocationID int64
	NextDeliveryID   int64
	NextDeliverySeq  int64
	LastEvent        string
}

func NewState() State {
	return State{
		Now:              time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
		NextRoomID:       1,
		NextMessageID:    1,
		NextInvocationID: FirstInvocationID,
		NextDeliveryID:   1,
		NextDeliverySeq:  1,
		LastEvent:        "prototype initialized",
	}
}

func InboundContext(s State) State {
	return inbound(s, "wecom", "group-ops", "group", "user-a", "context message", false, false)
}

func InboundTrigger(s State) State {
	return inbound(s, "wecom", "group-ops", "group", "user-b", "@agent please handle this", true, false)
}

func InboundSkipped(s State) State {
	return inbound(s, "wecom", "group-ops", "group", "bot-self", "self echo", false, true)
}

func InboundDuplicate(s State) State {
	room := ensureRoom(&s, "wecom", "group-ops", "group")
	if len(s.Messages) == 0 {
		return inbound(s, room.Channel, room.ChannelRoomID, room.ChannelRoomType, "user-a", "duplicate seed", false, false)
	}
	first := s.Messages[0]
	if hasMessage(s, first.RoomID, first.SourceMessageID) {
		s.LastEvent = fmt.Sprintf("duplicate ignored: source_message_id=%s", first.SourceMessageID)
		return s
	}
	return s
}

func CompleteWithOutput(s State) State {
	idx := latestActiveInvocationIndex(s)
	if idx < 0 {
		s.LastEvent = "no active invocation to complete"
		return s
	}
	invocation := &s.Invocations[idx]
	invocation.Status = InvocationStatusCompleted
	invocation.Output = "agent final reply"
	invocation.OutputSnapshot = snapshotOutput("completed", invocation.Output)
	s.Deliveries = append(s.Deliveries, Delivery{
		ID:           s.NextDeliveryID,
		Seq:          s.NextDeliverySeq,
		RoomID:       invocation.RoomID,
		InvocationID: invocation.ID,
		Payload:      invocation.Output,
		Status:       DeliveryStatusPending,
	})
	s.NextDeliveryID++
	s.NextDeliverySeq++
	s.LastEvent = fmt.Sprintf("completed invocation %d and created delivery", invocation.ID)
	return s
}

func CompleteEmpty(s State) State {
	idx := latestActiveInvocationIndex(s)
	if idx < 0 {
		s.LastEvent = "no active invocation to complete"
		return s
	}
	invocation := &s.Invocations[idx]
	invocation.Status = InvocationStatusCompleted
	invocation.Output = ""
	invocation.OutputSnapshot = snapshotOutput("completed", invocation.Output)
	s.LastEvent = fmt.Sprintf("completed invocation %d with zero delivery", invocation.ID)
	return s
}

func FailActive(s State) State {
	idx := latestActiveInvocationIndex(s)
	if idx < 0 {
		s.LastEvent = "no active invocation to fail"
		return s
	}
	invocation := &s.Invocations[idx]
	invocation.Status = InvocationStatusFailed
	invocation.Output = "执行失败，请稍后重试"
	invocation.OutputSnapshot = snapshotOutput("failed", invocation.Output)
	s.Deliveries = append(s.Deliveries, Delivery{
		ID:           s.NextDeliveryID,
		Seq:          s.NextDeliverySeq,
		RoomID:       invocation.RoomID,
		InvocationID: invocation.ID,
		Payload:      invocation.Output,
		Status:       DeliveryStatusPending,
	})
	s.NextDeliveryID++
	s.NextDeliverySeq++
	s.LastEvent = fmt.Sprintf("failed invocation %d and created failure delivery", invocation.ID)
	return s
}

func MarkRunning(s State) State {
	idx := latestActiveInvocationIndex(s)
	if idx < 0 {
		s.LastEvent = "no active invocation to mark running"
		return s
	}
	s.Invocations[idx].Status = InvocationStatusRunning
	s.LastEvent = fmt.Sprintf("invocation %d is running", s.Invocations[idx].ID)
	return s
}

func AckDelivery(s State) State {
	for i := range s.Deliveries {
		if s.Deliveries[i].Status == DeliveryStatusPending {
			s.Deliveries[i].Status = DeliveryStatusAcked
			s.LastEvent = fmt.Sprintf("acked delivery %d", s.Deliveries[i].ID)
			return s
		}
	}
	s.LastEvent = "no pending delivery to ack"
	return s
}

func Tick(s State) State {
	s.Now = s.Now.Add(time.Minute)
	s.LastEvent = "clock advanced by 1 minute"
	return s
}

func inbound(s State, channel, channelRoomID, roomType, senderID, text string, triggers bool, skipped bool) State {
	room := ensureRoom(&s, channel, channelRoomID, roomType)
	sourceMessageID := fmt.Sprintf("%s-%03d", channelRoomID, s.NextMessageID)
	if hasMessage(s, room.ID, sourceMessageID) {
		s.LastEvent = fmt.Sprintf("duplicate ignored: source_message_id=%s", sourceMessageID)
		return s
	}

	dispatchState := DispatchWaiting
	if skipped {
		dispatchState = DispatchSkipped
	}

	activeIdx := latestActiveInvocationIndexForRoom(s, room.ID)
	if activeIdx >= 0 && !skipped {
		dispatchState = s.Invocations[activeIdx].ID
	}

	message := Message{
		ID:              s.NextMessageID,
		RoomID:          room.ID,
		SourceMessageID: sourceMessageID,
		SenderID:        senderID,
		Text:            text,
		MessageTime:     s.Now,
		DispatchState:   dispatchState,
	}
	s.Messages = append(s.Messages, message)
	s.NextMessageID++

	if skipped {
		s.LastEvent = fmt.Sprintf("inserted skipped message %d", message.ID)
		return s
	}
	if activeIdx >= 0 {
		s.LastEvent = fmt.Sprintf("appended message %d to active invocation %d", message.ID, dispatchState)
		return s
	}
	if !triggers {
		s.LastEvent = fmt.Sprintf("inserted waiting context message %d", message.ID)
		return s
	}

	invocationID := s.NextInvocationID
	s.NextInvocationID++
	s.Invocations = append(s.Invocations, Invocation{
		ID:               invocationID,
		RoomID:           room.ID,
		Status:           InvocationStatusQueued,
		TriggerMessageID: message.ID,
		InputSnapshot:    buildInputSnapshot(s, room.ID, invocationID),
	})
	for i := range s.Messages {
		if s.Messages[i].RoomID == room.ID && s.Messages[i].DispatchState == DispatchWaiting {
			s.Messages[i].DispatchState = invocationID
		}
	}
	s.LastEvent = fmt.Sprintf("created invocation %d and bound waiting messages", invocationID)
	return s
}

func buildInputSnapshot(s State, roomID int64, invocationID int64) string {
	messageIDs := make([]int64, 0)
	for _, message := range s.Messages {
		if message.RoomID == roomID && (message.DispatchState == DispatchWaiting || message.DispatchState == invocationID) {
			messageIDs = append(messageIDs, message.ID)
		}
	}
	return fmt.Sprintf(`{"prompt":"default room prompt","message_ids":%v,"memory":{},"attachments":[]}`, messageIDs)
}

func snapshotOutput(status string, output string) string {
	return fmt.Sprintf(`{"status":%q,"final_output":%q}`, status, output)
}

func ensureRoom(s *State, channel, channelRoomID, roomType string) Room {
	for _, room := range s.Rooms {
		if room.Channel == channel && room.ChannelRoomID == channelRoomID {
			return room
		}
	}
	room := Room{
		ID:              s.NextRoomID,
		TenantID:        "default",
		Channel:         channel,
		ChannelRoomID:   channelRoomID,
		ChannelRoomType: roomType,
		DisplayName:     channelRoomID,
	}
	s.NextRoomID++
	s.Rooms = append(s.Rooms, room)
	return room
}

func hasMessage(s State, roomID int64, sourceMessageID string) bool {
	return slices.ContainsFunc(s.Messages, func(message Message) bool {
		return message.RoomID == roomID && message.SourceMessageID == sourceMessageID
	})
}

func latestActiveInvocationIndex(s State) int {
	for i := len(s.Invocations) - 1; i >= 0; i-- {
		if isActive(s.Invocations[i].Status) {
			return i
		}
	}
	return -1
}

func latestActiveInvocationIndexForRoom(s State, roomID int64) int {
	for i := len(s.Invocations) - 1; i >= 0; i-- {
		if s.Invocations[i].RoomID == roomID && isActive(s.Invocations[i].Status) {
			return i
		}
	}
	return -1
}

func isActive(status int16) bool {
	return status == InvocationStatusQueued || status == InvocationStatusRunning
}
