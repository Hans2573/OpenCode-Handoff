package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

const (
	DefaultAgentID              = "opencode-default"
	DefaultChannelID            = "feishu-default"
	projectRoutesOptInPolicyKey = "project_routes_opt_in_v1"
)

func (s *SQLite) EnsureDesktopDefaults(ctx context.Context, endpoint string) error {
	now := time.Now().UTC().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_instances (id, type, name, endpoint, enabled, health, last_seen_at)
		VALUES (?, 'opencode', 'OpenCode', ?, 1, 'unknown', ?)
		ON CONFLICT(id) DO UPDATE SET endpoint = excluded.endpoint`, DefaultAgentID, endpoint, now); err != nil {
		return fmt.Errorf("ensure default agent: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_instances (id, type, name, enabled, health, last_seen_at)
		VALUES (?, 'feishu', '飞书', 1, 'unknown', ?)
		ON CONFLICT(id) DO NOTHING`, DefaultChannelID, now); err != nil {
		return fmt.Errorf("ensure default channel: %w", err)
	}
	return nil
}

// EnsureProjectRoutesOptIn applies the desktop's explicit opt-in policy once.
// It also corrects preview databases created by older builds that automatically
// enabled every imported project. Later calls preserve the user's choices.
func (s *SQLite) EnsureProjectRoutesOptIn(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin project route policy migration: %w", err)
	}
	defer tx.Rollback()
	var value string
	err = tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, projectRoutesOptInPolicyKey).Scan(&value)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read project route policy: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE project_routes SET enabled = 0, updated_at = ?`, now); err != nil {
		return false, fmt.Errorf("reset project routes to opt-in: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, 'true', ?)`, projectRoutesOptInPolicyKey, now); err != nil {
		return false, fmt.Errorf("record project route policy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit project route policy: %w", err)
	}
	return true, nil
}

func (s *SQLite) SyncProjects(ctx context.Context, projects []domain.AgentProject) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project sync: %w", err)
	}
	defer tx.Rollback()
	for _, project := range projects {
		lastSeen := project.LastSeen
		if lastSeen.IsZero() {
			lastSeen = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, agent_id, directory, name, enabled, last_seen_at)
			VALUES (?, ?, ?, ?, 1, ?)
			ON CONFLICT(agent_id, directory) DO UPDATE SET
				name = excluded.name,
				last_seen_at = excluded.last_seen_at`,
			project.ID, project.AgentID, project.Directory, project.Name, lastSeen.UnixMilli()); err != nil {
			return fmt.Errorf("upsert project %s: %w", project.Directory, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO project_routes (project_id, channel_id, enabled, updated_at)
			VALUES (?, ?, 0, ?)
			ON CONFLICT(project_id, channel_id) DO NOTHING`,
			project.ID, DefaultChannelID, time.Now().UTC().UnixMilli()); err != nil {
			return fmt.Errorf("ensure project route %s: %w", project.Directory, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project sync: %w", err)
	}
	return nil
}

func (s *SQLite) ListProjectRoutes(ctx context.Context) ([]domain.ProjectRoute, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.agent_id, p.name, p.directory, r.channel_id, r.enabled, p.last_seen_at
		FROM projects p
		JOIN project_routes r ON r.project_id = p.id
		ORDER BY lower(p.name), lower(p.directory)`)
	if err != nil {
		return nil, fmt.Errorf("list project routes: %w", err)
	}
	defer rows.Close()
	var result []domain.ProjectRoute
	for rows.Next() {
		var item domain.ProjectRoute
		var enabled int
		var lastSeen int64
		if err := rows.Scan(&item.ProjectID, &item.AgentID, &item.Name, &item.Directory, &item.ChannelID, &enabled, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan project route: %w", err)
		}
		item.RouteEnabled = enabled != 0
		item.LastSeen = time.UnixMilli(lastSeen).UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project routes: %w", err)
	}
	return result, nil
}

func (s *SQLite) SetProjectRoute(ctx context.Context, projectID, channelID string, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_routes SET enabled = ?, updated_at = ?
		WHERE project_id = ? AND channel_id = ?`, value, time.Now().UTC().UnixMilli(), projectID, channelID)
	if err != nil {
		return fmt.Errorf("update project route: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get app setting %s: %w", key, err)
	}
	return value, nil
}

func (s *SQLite) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("set app setting %s: %w", key, err)
	}
	return nil
}

func (s *SQLite) GetPendingSessionModel(ctx context.Context, sessionID string) (domain.SessionModel, error) {
	var model domain.SessionModel
	err := s.db.QueryRowContext(ctx, `
		SELECT provider_id, model_id, model_name, variant
		FROM session_model_overrides WHERE session_id = ?`, sessionID).
		Scan(&model.ProviderID, &model.ModelID, &model.ModelName, &model.Variant)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionModel{}, ErrNotFound
	}
	if err != nil {
		return domain.SessionModel{}, fmt.Errorf("get pending model for session %s: %w", sessionID, err)
	}
	return model, nil
}

func (s *SQLite) SetPendingSessionModel(ctx context.Context, sessionID string, model domain.SessionModel) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_model_overrides (session_id, provider_id, model_id, model_name, variant, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			provider_id = excluded.provider_id,
			model_id = excluded.model_id,
			model_name = excluded.model_name,
			variant = excluded.variant,
			updated_at = excluded.updated_at`,
		sessionID, model.ProviderID, model.ModelID, model.ModelName, model.Variant, time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("set pending model for session %s: %w", sessionID, err)
	}
	return nil
}

func (s *SQLite) ClearPendingSessionModel(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_model_overrides WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear pending model for session %s: %w", sessionID, err)
	}
	return nil
}

func (s *SQLite) RecordRecentModel(ctx context.Context, model domain.SessionModel) error {
	model.ProviderID = strings.TrimSpace(model.ProviderID)
	model.ModelID = strings.TrimSpace(model.ModelID)
	model.ModelName = strings.TrimSpace(model.ModelName)
	model.Variant = strings.TrimSpace(model.Variant)
	if model.ProviderID == "" || model.ModelID == "" {
		return errors.New("recent model requires provider_id and model_id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recent model update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recent_models (provider_id, model_id, model_name, variant, used_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider_id, model_id, variant) DO UPDATE SET
			model_name = excluded.model_name,
			used_at = excluded.used_at`,
		model.ProviderID, model.ModelID, model.ModelName, model.Variant, time.Now().UTC().UnixMilli()); err != nil {
		return fmt.Errorf("record recent model: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recent_models WHERE rowid NOT IN (
		SELECT rowid FROM recent_models ORDER BY used_at DESC, rowid DESC LIMIT 20
	)`); err != nil {
		return fmt.Errorf("trim recent models: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recent model update: %w", err)
	}
	return nil
}

func (s *SQLite) ListRecentModels(ctx context.Context, limit int) ([]domain.SessionModel, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_id, model_id, model_name, variant
		FROM recent_models ORDER BY used_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent models: %w", err)
	}
	defer rows.Close()
	result := make([]domain.SessionModel, 0, limit)
	for rows.Next() {
		var model domain.SessionModel
		if err := rows.Scan(&model.ProviderID, &model.ModelID, &model.ModelName, &model.Variant); err != nil {
			return nil, fmt.Errorf("scan recent model: %w", err)
		}
		result = append(result, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent models: %w", err)
	}
	return result, nil
}

func (s *SQLite) AppendEvent(ctx context.Context, event domain.EventLog) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("encode event metadata: %w", err)
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO event_logs (level, event_type, source, message, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, event.Level, event.Type, event.Source, event.Message, string(metadata), createdAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("append event log: %w", err)
	}
	return nil
}

func (s *SQLite) ListEvents(ctx context.Context, search string, limit int) ([]domain.EventLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, level, event_type, source, message, metadata_json, created_at FROM event_logs`
	var args []any
	if search = strings.TrimSpace(search); search != "" {
		query += ` WHERE lower(message) LIKE ? OR lower(source) LIKE ? OR lower(event_type) LIKE ?`
		pattern := "%" + strings.ToLower(search) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list event logs: %w", err)
	}
	defer rows.Close()
	var result []domain.EventLog
	for rows.Next() {
		var item domain.EventLog
		var metadata string
		var createdAt int64
		if err := rows.Scan(&item.ID, &item.Level, &item.Type, &item.Source, &item.Message, &metadata, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event log: %w", err)
		}
		_ = json.Unmarshal([]byte(metadata), &item.Metadata)
		item.CreatedAt = time.UnixMilli(createdAt).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLite) CleanupEvents(ctx context.Context, maxAge time.Duration, maxRows int) error {
	cutoff := time.Now().UTC().Add(-maxAge).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM event_logs WHERE created_at < ?`, cutoff); err != nil {
		return fmt.Errorf("delete expired event logs: %w", err)
	}
	if maxRows > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM event_logs WHERE id NOT IN (
				SELECT id FROM event_logs ORDER BY created_at DESC, id DESC LIMIT ?
			)`, maxRows); err != nil {
			return fmt.Errorf("trim event logs: %w", err)
		}
	}
	return nil
}

func (s *SQLite) ClearEvents(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM event_logs`); err != nil {
		return fmt.Errorf("clear event logs: %w", err)
	}
	return nil
}
