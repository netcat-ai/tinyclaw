package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"tinyclaw/internal/core"
)

func (s *CoreStore) AuthenticateAPIClient(ctx context.Context, clientID string, secret string) (core.APIClient, error) {
	clientID = strings.TrimSpace(clientID)
	secret = strings.TrimSpace(secret)
	if clientID == "" || secret == "" {
		return core.APIClient{}, fmt.Errorf("client credentials are required")
	}
	var client core.APIClient
	var secretHash string
	var permissions []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, client_id, client_secret_hash, name, enabled, permissions, created_at, updated_at
		FROM api_clients
		WHERE client_id = $1
	`, clientID).Scan(
		&client.ID,
		&client.ClientID,
		&secretHash,
		&client.Name,
		&client.Enabled,
		&permissions,
		&client.CreatedAt,
		&client.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.APIClient{}, fmt.Errorf("invalid client credentials")
		}
		return core.APIClient{}, fmt.Errorf("authenticate api client: %w", err)
	}
	if !client.Enabled {
		return core.APIClient{}, fmt.Errorf("api client is disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(secret)); err != nil {
		return core.APIClient{}, fmt.Errorf("invalid client credentials")
	}
	if err := json.Unmarshal(permissions, &client.Permissions); err != nil {
		return core.APIClient{}, fmt.Errorf("decode api client permissions: %w", err)
	}
	return client, nil
}
