package sandbox

type AgentRequest struct {
	Query    string `json:"query"`
	MsgID    string `json:"msgid"`
	RoomID   string `json:"room_id"`
	TenantID string `json:"tenant_id"`
	ChatType string `json:"chat_type"`
}

type ExecutionResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}
