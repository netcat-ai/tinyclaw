package telemetry

import (
	"encoding/json"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	roomRegistrations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_room_registrations_total",
		Help: "Total room registration attempts.",
	}, []string{"channel", "result"})

	messageIngestions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_message_ingestions_total",
		Help: "Total message ingestion attempts.",
	}, []string{"source", "msgtype", "result", "triggered"})

	agentRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_agent_runs_total",
		Help: "Total agent run outcomes.",
	}, []string{"result"})

	deliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_deliveries_total",
		Help: "Total delivery outcomes.",
	}, []string{"kind", "result"})

	acks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_delivery_acks_total",
		Help: "Total delivery ack attempts.",
	}, []string{"result"})

	memoryWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tinyclaw_memory_writes_total",
		Help: "Total memory write job outcomes.",
	}, []string{"op", "result"})
)

func init() {
	prometheus.MustRegister(
		roomRegistrations,
		messageIngestions,
		agentRuns,
		deliveries,
		acks,
		memoryWrites,
	)
}

func IncRoomRegistration(channel string, result string) {
	roomRegistrations.WithLabelValues(label(channel, "unknown"), label(result, "unknown")).Inc()
}

func IncMessageIngestion(source string, msgType string, result string, triggered bool) {
	messageIngestions.WithLabelValues(label(source, "unknown"), label(msgType, "unknown"), label(result, "unknown"), boolLabel(triggered)).Inc()
}

func IncAgentRun(result string) {
	agentRuns.WithLabelValues(label(result, "unknown")).Inc()
}

func IncDelivery(kind string, result string) {
	deliveries.WithLabelValues(label(kind, "unknown"), label(result, "unknown")).Inc()
}

func IncDeliveryAck(result string) {
	acks.WithLabelValues(label(result, "unknown")).Inc()
}

func IncMemoryWrite(op string, result string) {
	memoryWrites.WithLabelValues(label(op, "unknown"), label(result, "unknown")).Inc()
}

func PayloadKind(payload json.RawMessage) string {
	var values map[string]any
	if err := json.Unmarshal(payload, &values); err != nil {
		return "unknown"
	}
	if kind, ok := values["kind"].(string); ok && strings.TrimSpace(kind) != "" {
		return strings.TrimSpace(kind)
	}
	if typ, ok := values["type"].(string); ok && strings.TrimSpace(typ) != "" {
		return strings.TrimSpace(typ)
	}
	return "unknown"
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func label(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
