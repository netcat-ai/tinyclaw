package sandbox

type AgentRequest struct {
	MsgID    string         `json:"msgid"`
	RoomID   string         `json:"room_id"`
	TenantID string         `json:"tenant_id"`
	ChatType string         `json:"chat_type"`
	Messages []AgentMessage `json:"messages"`
}

type AgentMessage struct {
	Seq      int64  `json:"seq"`
	MsgID    string `json:"msgid"`
	FromID   string `json:"from_id"`
	FromName string `json:"from_name,omitempty"`
	MsgTime  string `json:"msg_time,omitempty"`
	Payload  string `json:"payload"`
}

type ExecutionResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}
