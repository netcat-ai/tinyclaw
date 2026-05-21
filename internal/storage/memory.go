package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinyclaw/internal/core"
)

const (
	defaultMemorySearchLimit = 5
	maxMemorySearchLimit     = 20
	maxMemoryJobAttempts     = 3
	defaultMemoryTokenTTL    = 10 * time.Minute
)

func (s *CoreStore) CreateMemoryCapabilityToken(ctx context.Context, run core.AgentRun, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = defaultMemoryTokenTTL
	}
	token, err := randomMemoryToken()
	if err != nil {
		return "", err
	}
	hash := memoryTokenHash(token)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_capability_tokens (
			token_hash,
			room_id,
			agent_session_id,
			agent_key,
			source_message_after_id,
			source_message_until_id,
			expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, hash, run.RoomID, run.AgentSessionID, run.AgentKey, run.SourceMessageAfterID, run.SourceMessageUntilID, time.Now().UTC().Add(ttl))
	if err != nil {
		return "", fmt.Errorf("create memory capability token: %w", err)
	}
	return token, nil
}

func (s *CoreStore) SearchRoomMemoryByToken(ctx context.Context, token string, input core.MemorySearchInput) ([]core.MemoryItem, error) {
	run, err := s.agentRunForMemoryToken(ctx, token)
	if err != nil {
		return nil, err
	}
	input.RoomID = run.RoomID
	return s.SearchRoomMemory(ctx, input)
}

func (s *CoreStore) SearchRoomMemory(ctx context.Context, input core.MemorySearchInput) ([]core.MemoryItem, error) {
	input.Query = strings.TrimSpace(input.Query)
	if input.RoomID <= 0 {
		return nil, fmt.Errorf("room_id is required")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultMemorySearchLimit
	}
	if limit > maxMemorySearchLimit {
		limit = maxMemorySearchLimit
	}
	if err := validateMemoryTypes(input.Types); err != nil {
		return nil, err
	}

	args := []any{input.RoomID}
	conditions := []string{"room_id = $1"}
	if !input.IncludeInactive {
		args = append(args, core.MemoryStatusActive)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(input.Types) > 0 {
		placeholders := make([]string, 0, len(input.Types))
		for _, typ := range input.Types {
			args = append(args, typ)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		conditions = append(conditions, "type IN ("+strings.Join(placeholders, ", ")+")")
	}
	if input.Query != "" {
		args = append(args, "%"+input.Query+"%")
		conditions = append(conditions, fmt.Sprintf("(key ILIKE $%d OR content ILIKE $%d)", len(args), len(args)))
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, room_id, type, key, content, status, source_message_after_id, source_message_until_id, created_by_agent_session_id, updated_by_agent_session_id, created_at, updated_at
		FROM memory_items
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT $`+fmt.Sprintf("%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("search room memory: %w", err)
	}
	defer rows.Close()
	return scanMemoryItems(rows)
}

func (s *CoreStore) agentRunForMemoryToken(ctx context.Context, token string) (core.AgentRun, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return core.AgentRun{}, fmt.Errorf("memory capability token is required")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT room_id, agent_session_id, agent_key, source_message_after_id, source_message_until_id
		FROM memory_capability_tokens
		WHERE token_hash = $1
		  AND expires_at > NOW()
	`, memoryTokenHash(token))
	var run core.AgentRun
	if err := row.Scan(&run.RoomID, &run.AgentSessionID, &run.AgentKey, &run.SourceMessageAfterID, &run.SourceMessageUntilID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.AgentRun{}, fmt.Errorf("invalid memory capability token")
		}
		return core.AgentRun{}, fmt.Errorf("resolve memory capability token: %w", err)
	}
	return run, nil
}

func randomMemoryToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate memory capability token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func memoryTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *CoreStore) ApplyNextMemoryWriteJob(ctx context.Context) (core.MemoryWriteJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.MemoryWriteJob{}, false, fmt.Errorf("begin memory write job tx: %w", err)
	}
	defer tx.Rollback()

	job, ok, err := claimMemoryWriteJobTx(ctx, tx)
	if err != nil || !ok {
		return job, ok, err
	}
	item, err := applyMemoryWriteJobTx(ctx, tx, job)
	if err != nil {
		status := core.MemoryWriteJobStatusPending
		if job.Attempts >= maxMemoryJobAttempts {
			status = core.MemoryWriteJobStatusFailed
		}
		if markErr := markMemoryWriteJobTx(ctx, tx, job.ID, status, err.Error()); markErr != nil {
			return core.MemoryWriteJob{}, false, markErr
		}
		if status == core.MemoryWriteJobStatusFailed {
			if auditErr := insertMemoryAuditTx(ctx, tx, job, 0, "failed", map[string]any{
				"op":    job.Op,
				"type":  job.Type,
				"key":   job.Key,
				"error": err.Error(),
			}); auditErr != nil {
				return core.MemoryWriteJob{}, false, auditErr
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return core.MemoryWriteJob{}, false, fmt.Errorf("commit failed memory write job: %w", commitErr)
		}
		return job, true, err
	}
	if err := markMemoryWriteJobTx(ctx, tx, job.ID, core.MemoryWriteJobStatusApplied, ""); err != nil {
		return core.MemoryWriteJob{}, false, err
	}
	if err := insertMemoryAuditTx(ctx, tx, job, item.ID, "applied", map[string]any{
		"op":      job.Op,
		"type":    job.Type,
		"key":     job.Key,
		"status":  item.Status,
		"content": item.Content,
	}); err != nil {
		return core.MemoryWriteJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return core.MemoryWriteJob{}, false, fmt.Errorf("commit memory write job: %w", err)
	}
	return job, true, nil
}

func (s *CoreStore) EnqueueMemoryWriteJobs(ctx context.Context, run core.AgentRun, proposals []core.MemoryWriteProposal) error {
	if len(proposals) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin enqueue memory write jobs tx: %w", err)
	}
	defer tx.Rollback()
	if err := enqueueMemoryWriteJobsTx(ctx, tx, run, proposals); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit enqueue memory write jobs: %w", err)
	}
	return nil
}

func enqueueMemoryWriteJobsTx(ctx context.Context, tx *sql.Tx, run core.AgentRun, proposals []core.MemoryWriteProposal) error {
	for _, proposal := range proposals {
		job, err := memoryWriteJobFromProposal(run, proposal)
		if err != nil {
			if rejectErr := insertRejectedMemoryWriteJobTx(ctx, tx, run, proposal, err); rejectErr != nil {
				return rejectErr
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memory_write_jobs (
				room_id,
				agent_session_id,
				agent_key,
				source_message_after_id,
				source_message_until_id,
				operation_key,
				op,
				type,
				key,
				content,
				status
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (operation_key) DO NOTHING
		`, job.RoomID, job.AgentSessionID, job.AgentKey, job.SourceMessageAfterID, job.SourceMessageUntilID, job.OperationKey, job.Op, job.Type, job.Key, job.Content, core.MemoryWriteJobStatusPending); err != nil {
			return fmt.Errorf("enqueue memory write job: %w", err)
		}
	}
	return nil
}

func insertRejectedMemoryWriteJobTx(ctx context.Context, tx *sql.Tx, run core.AgentRun, proposal core.MemoryWriteProposal, rejectErr error) error {
	op := strings.TrimSpace(proposal.Op)
	typ := strings.TrimSpace(proposal.Type)
	key := strings.TrimSpace(proposal.Key)
	content := strings.TrimSpace(proposal.Content)
	operationKey := rejectedMemoryOperationKey(run, proposal, rejectErr)
	row := tx.QueryRowContext(ctx, `
		INSERT INTO memory_write_jobs (
			room_id,
			agent_session_id,
			agent_key,
			source_message_after_id,
			source_message_until_id,
			operation_key,
			op,
			type,
			key,
			content,
			status,
			last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (operation_key) DO UPDATE
		SET last_error = EXCLUDED.last_error,
		    updated_at = NOW()
		RETURNING id, room_id, agent_session_id, agent_key, source_message_after_id, source_message_until_id, operation_key, op, type, key, content, status, attempts, last_error, created_at, updated_at
	`, run.RoomID, run.AgentSessionID, run.AgentKey, run.SourceMessageAfterID, run.SourceMessageUntilID, operationKey, op, typ, key, content, core.MemoryWriteJobStatusRejected, rejectErr.Error())
	job, err := scanMemoryWriteJob(row)
	if err != nil {
		return fmt.Errorf("insert rejected memory write job: %w", err)
	}
	if err := insertMemoryAuditTx(ctx, tx, job, 0, "rejected", map[string]any{
		"op":      op,
		"type":    typ,
		"key":     key,
		"content": content,
		"error":   rejectErr.Error(),
	}); err != nil {
		return err
	}
	return nil
}

func memoryWriteJobFromProposal(run core.AgentRun, proposal core.MemoryWriteProposal) (core.MemoryWriteJob, error) {
	proposal.Op = strings.TrimSpace(proposal.Op)
	proposal.Type = strings.TrimSpace(proposal.Type)
	proposal.Key = strings.TrimSpace(proposal.Key)
	proposal.Content = strings.TrimSpace(proposal.Content)
	typ, contentRequired, err := memoryTypeForOp(proposal)
	if err != nil {
		return core.MemoryWriteJob{}, err
	}
	if proposal.Key == "" {
		return core.MemoryWriteJob{}, fmt.Errorf("memory key is required")
	}
	if contentRequired && proposal.Content == "" {
		return core.MemoryWriteJob{}, fmt.Errorf("memory content is required")
	}
	job := core.MemoryWriteJob{
		RoomID:               run.RoomID,
		AgentSessionID:       run.AgentSessionID,
		AgentKey:             run.AgentKey,
		SourceMessageAfterID: run.SourceMessageAfterID,
		SourceMessageUntilID: run.SourceMessageUntilID,
		Op:                   proposal.Op,
		Type:                 typ,
		Key:                  proposal.Key,
		Content:              proposal.Content,
	}
	job.OperationKey = memoryOperationKey(job)
	return job, nil
}

func memoryTypeForOp(proposal core.MemoryWriteProposal) (string, bool, error) {
	switch proposal.Op {
	case core.MemoryWriteOpUpsertFact:
		return core.MemoryTypeFact, true, nil
	case core.MemoryWriteOpSetPreference:
		return core.MemoryTypePreference, true, nil
	case core.MemoryWriteOpAddTodo:
		return core.MemoryTypeTodo, true, nil
	case core.MemoryWriteOpCloseTodo:
		return core.MemoryTypeTodo, false, nil
	case core.MemoryWriteOpMarkStale:
		if !isValidMemoryType(proposal.Type) {
			return "", false, fmt.Errorf("mark_stale requires a valid memory type")
		}
		return proposal.Type, false, nil
	default:
		return "", false, fmt.Errorf("unsupported memory op %q", proposal.Op)
	}
}

func memoryOperationKey(job core.MemoryWriteJob) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%s:%s:%s:%s", job.AgentSessionID, job.SourceMessageAfterID, job.SourceMessageUntilID, job.Op, job.Type, job.Key, job.Content)))
	return hex.EncodeToString(sum[:])
}

func rejectedMemoryOperationKey(run core.AgentRun, proposal core.MemoryWriteProposal, err error) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("rejected:%d:%d:%d:%s:%s:%s:%s:%s", run.AgentSessionID, run.SourceMessageAfterID, run.SourceMessageUntilID, proposal.Op, proposal.Type, proposal.Key, proposal.Content, err.Error())))
	return hex.EncodeToString(sum[:])
}

func claimMemoryWriteJobTx(ctx context.Context, tx *sql.Tx) (core.MemoryWriteJob, bool, error) {
	row := tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM memory_write_jobs
			WHERE status = $1
			  AND attempts < $2
			ORDER BY id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE memory_write_jobs j
		SET attempts = attempts + 1,
		    updated_at = NOW()
		FROM candidate
		WHERE j.id = candidate.id
		RETURNING j.id, j.room_id, j.agent_session_id, j.agent_key, j.source_message_after_id, j.source_message_until_id, j.operation_key, j.op, j.type, j.key, j.content, j.status, j.attempts, j.last_error, j.created_at, j.updated_at
	`, core.MemoryWriteJobStatusPending, maxMemoryJobAttempts)
	job, err := scanMemoryWriteJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.MemoryWriteJob{}, false, nil
		}
		return core.MemoryWriteJob{}, false, fmt.Errorf("claim memory write job: %w", err)
	}
	return job, true, nil
}

func applyMemoryWriteJobTx(ctx context.Context, tx *sql.Tx, job core.MemoryWriteJob) (core.MemoryItem, error) {
	switch job.Op {
	case core.MemoryWriteOpUpsertFact, core.MemoryWriteOpSetPreference, core.MemoryWriteOpAddTodo:
		return upsertMemoryItemTx(ctx, tx, job, core.MemoryStatusActive)
	case core.MemoryWriteOpCloseTodo:
		return updateMemoryItemStatusTx(ctx, tx, job, core.MemoryStatusClosed)
	case core.MemoryWriteOpMarkStale:
		return updateMemoryItemStatusTx(ctx, tx, job, core.MemoryStatusStale)
	default:
		return core.MemoryItem{}, fmt.Errorf("unsupported memory op %q", job.Op)
	}
}

func upsertMemoryItemTx(ctx context.Context, tx *sql.Tx, job core.MemoryWriteJob, status string) (core.MemoryItem, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO memory_items (
			room_id,
			type,
			key,
			content,
			status,
			source_message_after_id,
			source_message_until_id,
			created_by_agent_session_id,
			updated_by_agent_session_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (room_id, type, key) DO UPDATE
		SET content = EXCLUDED.content,
		    status = EXCLUDED.status,
		    source_message_after_id = EXCLUDED.source_message_after_id,
		    source_message_until_id = EXCLUDED.source_message_until_id,
		    updated_by_agent_session_id = EXCLUDED.updated_by_agent_session_id,
		    updated_at = NOW()
		RETURNING id, room_id, type, key, content, status, source_message_after_id, source_message_until_id, created_by_agent_session_id, updated_by_agent_session_id, created_at, updated_at
	`, job.RoomID, job.Type, job.Key, job.Content, status, job.SourceMessageAfterID, job.SourceMessageUntilID, job.AgentSessionID)
	return scanMemoryItem(row)
}

func updateMemoryItemStatusTx(ctx context.Context, tx *sql.Tx, job core.MemoryWriteJob, status string) (core.MemoryItem, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE memory_items
		SET status = $4,
		    source_message_after_id = $5,
		    source_message_until_id = $6,
		    updated_by_agent_session_id = $7,
		    updated_at = NOW()
		WHERE room_id = $1
		  AND type = $2
		  AND key = $3
		RETURNING id, room_id, type, key, content, status, source_message_after_id, source_message_until_id, created_by_agent_session_id, updated_by_agent_session_id, created_at, updated_at
	`, job.RoomID, job.Type, job.Key, status, job.SourceMessageAfterID, job.SourceMessageUntilID, job.AgentSessionID)
	item, err := scanMemoryItem(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.MemoryItem{}, fmt.Errorf("memory item not found")
		}
		return core.MemoryItem{}, err
	}
	return item, nil
}

func markMemoryWriteJobTx(ctx context.Context, tx *sql.Tx, id int64, status string, detail string) error {
	var lastError any
	if strings.TrimSpace(detail) != "" {
		lastError = detail
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE memory_write_jobs
		SET status = $2,
		    last_error = $3,
		    updated_at = NOW()
		WHERE id = $1
	`, id, status, lastError)
	if err != nil {
		return fmt.Errorf("mark memory write job: %w", err)
	}
	return nil
}

func insertMemoryAuditTx(ctx context.Context, tx *sql.Tx, job core.MemoryWriteJob, itemID int64, action string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode memory audit payload: %w", err)
	}
	var memoryItemID any
	if itemID > 0 {
		memoryItemID = itemID
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO memory_change_audit (
			memory_item_id,
			memory_write_job_id,
			room_id,
			agent_session_id,
			action,
			payload
		)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, memoryItemID, job.ID, job.RoomID, job.AgentSessionID, action, data)
	if err != nil {
		return fmt.Errorf("insert memory audit: %w", err)
	}
	return nil
}

func validateMemoryTypes(types []string) error {
	for _, typ := range types {
		if !isValidMemoryType(typ) {
			return fmt.Errorf("unsupported memory type %q", typ)
		}
	}
	return nil
}

func isValidMemoryType(typ string) bool {
	switch typ {
	case core.MemoryTypeFact, core.MemoryTypePreference, core.MemoryTypeTodo:
		return true
	default:
		return false
	}
}

func scanMemoryItems(rows *sql.Rows) ([]core.MemoryItem, error) {
	var items []core.MemoryItem
	for rows.Next() {
		item, err := scanMemoryItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory items: %w", err)
	}
	return items, nil
}

func scanMemoryItem(row scanner) (core.MemoryItem, error) {
	var item core.MemoryItem
	var createdBy sql.NullInt64
	var updatedBy sql.NullInt64
	if err := row.Scan(
		&item.ID,
		&item.RoomID,
		&item.Type,
		&item.Key,
		&item.Content,
		&item.Status,
		&item.SourceMessageAfterID,
		&item.SourceMessageUntilID,
		&createdBy,
		&updatedBy,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return core.MemoryItem{}, err
	}
	if createdBy.Valid {
		item.CreatedByAgentSession = createdBy.Int64
	}
	if updatedBy.Valid {
		item.UpdatedByAgentSession = updatedBy.Int64
	}
	return item, nil
}

func scanMemoryWriteJob(row scanner) (core.MemoryWriteJob, error) {
	var job core.MemoryWriteJob
	var lastError sql.NullString
	if err := row.Scan(
		&job.ID,
		&job.RoomID,
		&job.AgentSessionID,
		&job.AgentKey,
		&job.SourceMessageAfterID,
		&job.SourceMessageUntilID,
		&job.OperationKey,
		&job.Op,
		&job.Type,
		&job.Key,
		&job.Content,
		&job.Status,
		&job.Attempts,
		&lastError,
		&job.CreatedAt,
		&job.UpdatedAt,
	); err != nil {
		return core.MemoryWriteJob{}, err
	}
	if lastError.Valid {
		job.LastError = lastError.String
	}
	return job, nil
}
