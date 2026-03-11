package main

type AgentSpec struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Model       string   `json:"model,omitempty"`
	Tools       []string `json:"tools,omitempty"`
}

var AgentRegistry = map[string]AgentSpec{
	"sql-agent": {
		ID:          "sql-agent",
		Name:        "SQL查询Agent",
		Description: "专门处理数据库查询任务",
		Model:       "gpt-3.5-turbo",
		Tools:       []string{"sql_query"},
	},
	"validator-agent": {
		ID:          "validator-agent",
		Name:        "验证Agent",
		Description: "验证和复核其他agent的输出结果",
		Model:       "gpt-3.5-turbo",
		Tools:       []string{},
	},
	"search-agent": {
		ID:          "search-agent",
		Name:        "搜索Agent",
		Description: "执行网络搜索和信息检索",
		Model:       "gpt-4o-mini",
		Tools:       []string{"web_search"},
	},
	"file-agent": {
		ID:          "file-agent",
		Name:        "文件处理Agent",
		Description: "处理文件读取、解析和分析任务",
		Model:       "gpt-4o-mini",
		Tools:       []string{"file_read", "file_parse"},
	},
}

func GetAgentSpec(agentID string) (AgentSpec, bool) {
	spec, ok := AgentRegistry[agentID]
	return spec, ok
}
