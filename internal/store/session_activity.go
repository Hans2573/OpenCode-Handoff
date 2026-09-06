package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

func (s *SQLite) UpsertSessionActivity(ctx context.Context, item domain.SessionActivitySnapshot) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_activity_snapshots (
			session_id, directory, fingerprint, session_status, level,
			last_activity_at, operation_started_at, operation_type,
			operation_summary, operation_status, source_session_id,
			source_session_title, source_agent, source_is_subagent,
			snoozed_until, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, directory) DO UPDATE SET
			fingerprint = excluded.fingerprint,
			session_status = excluded.session_status,
			level = excluded.level,
			last_activity_at = excluded.last_activity_at,
			operation_started_at = excluded.operation_started_at,
			operation_type = excluded.operation_type,
			operation_summary = excluded.operation_summary,
			operation_status = excluded.operation_status,
			source_session_id = excluded.source_session_id,
			source_session_title = excluded.source_session_title,
			source_agent = excluded.source_agent,
			source_is_subagent = excluded.source_is_subagent,
			snoozed_until = excluded.snoozed_until,
			updated_at = excluded.updated_at`,
		item.SessionID, item.Directory, item.Fingerprint, item.SessionStatus, item.Level,
		item.LastActivityAt.UTC().UnixMilli(), nullableTime(item.OperationStartedAt),
		item.OperationType, item.OperationSummary, item.OperationStatus,
		item.SourceSessionID, item.SourceSessionTitle, item.SourceAgent,
		boolInt(item.SourceIsSubagent), nullableTime(item.SnoozedUntil), item.UpdatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("upsert session activity: %w", err)
	}
	return nil
}

func (s *SQLite) GetSessionActivity(ctx context.Context, sessionID, directory string) (domain.SessionActivitySnapshot, error) {
	item, err := scanSessionActivity(s.db.QueryRowContext(ctx, sessionActivitySelect+` WHERE session_id = ? AND directory = ?`, sessionID, directory))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionActivitySnapshot{}, ErrNotFound
	}
	if err != nil {
		return domain.SessionActivitySnapshot{}, fmt.Errorf("get session activity: %w", err)
	}
	return item, nil
}

func (s *SQLite) ListSessionActivities(ctx context.Context) ([]domain.SessionActivitySnapshot, error) {
	rows, err := s.db.QueryContext(ctx, sessionActivitySelect+` ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list session activities: %w", err)
	}
	defer rows.Close()
	var result []domain.SessionActivitySnapshot
	for rows.Next() {
		item, err := scanSessionActivity(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session activity: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLite) SnoozeSessionActivity(ctx context.Context, sessionID, directory string, until time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE session_activity_snapshots SET level = 'normal', snoozed_until = ?, updated_at = ? WHERE session_id = ? AND directory = ?`, until.UTC().UnixMilli(), time.Now().UTC().UnixMilli(), sessionID, directory)
	if err != nil {
		return fmt.Errorf("snooze session activity: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) TouchSessionActivity(ctx context.Context, sessionID, directory, operationType, summary string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_activity_snapshots (
			session_id, directory, fingerprint, session_status, level,
			last_activity_at, operation_type, operation_summary, operation_status,
			source_session_id, source_agent, updated_at
		) VALUES (?, ?, '', 'busy', 'normal', ?, ?, ?, 'submitted', ?, '主 Agent', ?)
		ON CONFLICT(session_id, directory) DO UPDATE SET
			fingerprint = '', session_status = 'busy', level = 'normal',
			last_activity_at = excluded.last_activity_at,
			operation_started_at = NULL,
			operation_type = excluded.operation_type,
			operation_summary = excluded.operation_summary,
			operation_status = excluded.operation_status,
			source_session_id = excluded.source_session_id,
			source_session_title = '', source_agent = '主 Agent', source_is_subagent = 0,
			snoozed_until = NULL, updated_at = excluded.updated_at`,
		sessionID, directory, at.UTC().UnixMilli(), operationType, summary, sessionID, at.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("touch session activity: %w", err)
	}
	return nil
}

func (s *SQLite) DeleteSessionActivitiesNotIn(ctx context.Context, directory string, sessionIDs []string) error {
	query := `DELETE FROM session_activity_snapshots WHERE directory = ?`
	args := []any{directory}
	if len(sessionIDs) > 0 {
		query += ` AND session_id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(sessionIDs)), ",") + `)`
		for _, sessionID := range sessionIDs {
			args = append(args, sessionID)
		}
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("prune session activities: %w", err)
	}
	return nil
}

const sessionActivitySelect = `SELECT session_id, directory, fingerprint, session_status, level,
	last_activity_at, operation_started_at, operation_type, operation_summary,
	operation_status, source_session_id, source_session_title, source_agent,
	source_is_subagent, snoozed_until, updated_at FROM session_activity_snapshots`

func scanSessionActivity(row rowScanner) (domain.SessionActivitySnapshot, error) {
	var item domain.SessionActivitySnapshot
	var lastActivityAt, updatedAt int64
	var operationStartedAt, snoozedUntil sql.NullInt64
	var sourceIsSubagent int
	err := row.Scan(&item.SessionID, &item.Directory, &item.Fingerprint, &item.SessionStatus,
		&item.Level, &lastActivityAt, &operationStartedAt, &item.OperationType,
		&item.OperationSummary, &item.OperationStatus, &item.SourceSessionID,
		&item.SourceSessionTitle, &item.SourceAgent, &sourceIsSubagent, &snoozedUntil, &updatedAt)
	if err != nil {
		return domain.SessionActivitySnapshot{}, err
	}
	item.LastActivityAt = time.UnixMilli(lastActivityAt).UTC()
	item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	item.SourceIsSubagent = sourceIsSubagent != 0
	if operationStartedAt.Valid {
		item.OperationStartedAt = time.UnixMilli(operationStartedAt.Int64).UTC()
	}
	if snoozedUntil.Valid {
		item.SnoozedUntil = time.UnixMilli(snoozedUntil.Int64).UTC()
	}
	return item, nil
}
