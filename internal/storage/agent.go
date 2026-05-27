package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tinyclaw/internal/core"
)

func (s *CoreStore) ListAgents(ctx context.Context) ([]core.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, key, display_name, description, prompt, allowed_tools, enabled, created_at, updated_at
		FROM agents
		ORDER BY key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (s *CoreStore) GetAgent(ctx context.Context, id int64) (core.Agent, error) {
	if id <= 0 {
		return core.Agent{}, fmt.Errorf("agent id is required")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, key, display_name, description, prompt, allowed_tools, enabled, created_at, updated_at
		FROM agents
		WHERE id = $1
	`, id)
	agent, err := scanAgent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Agent{}, fmt.Errorf("agent %d not found", id)
		}
		return core.Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return agent, nil
}

func (s *CoreStore) CreateAgent(ctx context.Context, input core.UpsertAgentInput) (core.Agent, error) {
	input, err := normalizeAgentInput(input)
	if err != nil {
		return core.Agent{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agents (key, display_name, description, prompt, allowed_tools, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, key, display_name, description, prompt, allowed_tools, enabled, created_at, updated_at
	`, input.Key, input.DisplayName, nullIfEmpty(input.Description), input.Prompt, input.AllowedTools, input.Enabled)
	agent, err := scanAgent(row)
	if err != nil {
		return core.Agent{}, fmt.Errorf("create agent: %w", err)
	}
	return agent, nil
}

func (s *CoreStore) UpdateAgent(ctx context.Context, id int64, input core.UpsertAgentInput) (core.Agent, error) {
	if id <= 0 {
		return core.Agent{}, fmt.Errorf("agent id is required")
	}
	input, err := normalizeAgentInput(input)
	if err != nil {
		return core.Agent{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE agents
		SET key = $2,
		    display_name = $3,
		    description = $4,
		    prompt = $5,
		    allowed_tools = $6,
		    enabled = $7,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING id, key, display_name, description, prompt, allowed_tools, enabled, created_at, updated_at
	`, id, input.Key, input.DisplayName, nullIfEmpty(input.Description), input.Prompt, input.AllowedTools, input.Enabled)
	agent, err := scanAgent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Agent{}, fmt.Errorf("agent %d not found", id)
		}
		return core.Agent{}, fmt.Errorf("update agent: %w", err)
	}
	return agent, nil
}

func normalizeAgentInput(input core.UpsertAgentInput) (core.UpsertAgentInput, error) {
	input.Key = strings.TrimSpace(input.Key)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	input.Prompt = strings.TrimSpace(input.Prompt)
	if len(input.AllowedTools) == 0 {
		input.AllowedTools = json.RawMessage(`[]`)
	}
	switch {
	case input.Key == "":
		return core.UpsertAgentInput{}, fmt.Errorf("key is required")
	case input.DisplayName == "":
		return core.UpsertAgentInput{}, fmt.Errorf("display_name is required")
	case input.Prompt == "":
		return core.UpsertAgentInput{}, fmt.Errorf("prompt is required")
	case !json.Valid(input.AllowedTools):
		return core.UpsertAgentInput{}, fmt.Errorf("allowed_tools must be valid JSON")
	}
	return input, nil
}

func scanAgents(rows *sql.Rows) ([]core.Agent, error) {
	var agents []core.Agent
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return agents, nil
}

func scanAgent(row scanner) (core.Agent, error) {
	var agent core.Agent
	var description sql.NullString
	if err := row.Scan(
		&agent.ID,
		&agent.Key,
		&agent.DisplayName,
		&description,
		&agent.Prompt,
		&agent.AllowedTools,
		&agent.Enabled,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	); err != nil {
		return core.Agent{}, err
	}
	if description.Valid {
		agent.Description = description.String
	}
	return agent, nil
}
