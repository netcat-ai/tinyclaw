package main

import (
	"fmt"
	"strconv"
	"time"
)

const (
	eventTypeMessageReceived = "message.received"
	eventTypeSubagentTask    = "subagent.task"
	eventTypeSubagentResult  = "subagent.result"
)

type IngressEvent struct {
	EventID     string
	EventType   string
	TenantID    string
	SessionKey  string
	SourceMsgID string
	SenderID    string
	ChatType    string
	ContentType string
	Content     string
	Attachments string
	OccurredAt  time.Time
	TraceID     string
	Raw         string
}

func buildIngressEvent(tenantID string, msg *WeComMessage) (IngressEvent, error) {
	if msg == nil {
		return IngressEvent{}, fmt.Errorf("message is nil")
	}

	session, err := sessionFromMessage(tenantID, msg)
	if err != nil {
		return IngressEvent{}, err
	}

	occurredAt := messageOccurredAt(msg.MsgTime)
	eventID := ingressEventID(msg, occurredAt)

	return IngressEvent{
		EventID:     eventID,
		EventType:   eventTypeMessageReceived,
		TenantID:    session.TenantID,
		SessionKey:  session.SessionKey,
		SourceMsgID: msg.MsgID,
		SenderID:    msg.From,
		ChatType:    session.ChatType,
		ContentType: normalizeContentType(msg.MsgType),
		Content:     msg.RawContent,
		Attachments: "[]",
		OccurredAt:  occurredAt,
		TraceID:     eventID,
		Raw:         msg.RawContent,
	}, nil
}

func ingressEventID(msg *WeComMessage, occurredAt time.Time) string {
	if msg == nil {
		return ""
	}
	if msg.MsgID != "" {
		return "wecom:" + msg.MsgID
	}
	return "wecom:ts:" + strconv.FormatInt(occurredAt.Unix(), 10)
}

func messageOccurredAt(ts int64) time.Time {
	if ts <= 0 {
		return time.Unix(0, 0).UTC()
	}
	if ts >= 1_000_000_000_000 {
		return time.UnixMilli(ts).UTC()
	}
	return time.Unix(ts, 0).UTC()
}

func normalizeContentType(msgType string) string {
	switch msgType {
	case "text":
		return "text"
	case "image", "voice", "video", "emotion":
		return "image"
	case "file":
		return "file"
	case "mixed":
		return "mixed"
	default:
		return "mixed"
	}
}

func streamValues(event IngressEvent) map[string]any {
	return map[string]any{
		"event_id":      event.EventID,
		"event_type":    event.EventType,
		"tenant_id":     event.TenantID,
		"session_key":   event.SessionKey,
		"source_msg_id": event.SourceMsgID,
		"sender_id":     event.SenderID,
		"chat_type":     event.ChatType,
		"content_type":  event.ContentType,
		"content":       event.Content,
		"attachments":   event.Attachments,
		"occurred_at":   event.OccurredAt.Format(time.RFC3339),
		"trace_id":      event.TraceID,
		"raw":           event.Raw,
		"msgid":         event.SourceMsgID,
	}
}
