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
