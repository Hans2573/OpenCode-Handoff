package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

func (s *SQLite) UpsertSlowOperation(ctx context.Context, item domain.SlowOperation) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_slow_operations (
			root_session_id, source_session_id, directory, message_id, part_id,
			tool, input_preview, input_hash, summary, status, source_title,
			source_agent, source_is_subagent, started_at, ended_at,
			duration_seconds, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(directory, source_session_id, message_id, part_id) DO UPDATE SET
			root_session_id = excluded.root_session_id,
			tool = excluded.tool,
			input_preview = excluded.input_preview,
			input_hash = excluded.input_hash,
			summary = excluded.summary,
			status = excluded.status,
			source_title = excluded.source_title,
			source_agent = excluded.source_agent,
			source_is_subagent = excluded.source_is_subagent,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			duration_seconds = excluded.duration_seconds,
			updated_at = excluded.updated_at
		WHERE session_slow_operations.duration_seconds != excluded.duration_seconds
			OR session_slow_operations.status != excluded.status
			OR session_slow_operations.input_hash != excluded.input_hash
			OR session_slow_operations.summary != excluded.summary
			OR COALESCE(session_slow_operations.ended_at, 0) != COALESCE(excluded.ended_at, 0)`,
		item.RootSessionID, item.SourceSessionID, item.Directory, item.MessageID, item.PartID,
		item.Tool, item.InputPreview, item.InputHash, item.Summary, item.Status, item.SourceTitle,
		item.SourceAgent, boolInt(item.SourceIsSubagent), item.StartedAt.UTC().UnixMilli(),
		nullableTime(item.EndedAt), item.DurationSeconds, item.UpdatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("upsert slow operation: %w", err)
	}
	return nil
}

func (s *SQLite) ListSlowOperations(ctx context.Context, rootSessionID, directory string, limit int) ([]domain.SlowOperation, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, root_session_id, source_session_id,
		directory, message_id, part_id, tool, input_preview, input_hash, summary,
		status, source_title, source_agent, source_is_subagent, started_at,
		ended_at, duration_seconds, updated_at
		FROM session_slow_operations
		WHERE root_session_id = ? AND directory = ?
		ORDER BY duration_seconds DESC, updated_at DESC LIMIT ?`, rootSessionID, directory, limit)
	if err != nil {
		return nil, fmt.Errorf("list slow operations: %w", err)
	}
	defer rows.Close()
	var result []domain.SlowOperation
	for rows.Next() {
		var item domain.SlowOperation
		var sourceIsSubagent int
		var startedAt, updatedAt int64
		var endedAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.RootSessionID, &item.SourceSessionID,
			&item.Directory, &item.MessageID, &item.PartID, &item.Tool,
			&item.InputPreview, &item.InputHash, &item.Summary, &item.Status,
			&item.SourceTitle, &item.SourceAgent, &sourceIsSubagent, &startedAt,
			&endedAt, &item.DurationSeconds, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan slow operation: %w", err)
		}
		item.SourceIsSubagent = sourceIsSubagent != 0
		item.StartedAt = time.UnixMilli(startedAt).UTC()
		item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		if endedAt.Valid {
			item.EndedAt = time.UnixMilli(endedAt.Int64).UTC()
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLite) PruneSlowOperations(ctx context.Context, rootSessionID, directory string, keep int) error {
	if keep < 1 {
		keep = 100
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_slow_operations
		WHERE root_session_id = ? AND directory = ? AND id NOT IN (
			SELECT id FROM session_slow_operations
			WHERE root_session_id = ? AND directory = ?
			ORDER BY duration_seconds DESC, updated_at DESC LIMIT ?
		)`, rootSessionID, directory, rootSessionID, directory, keep)
	if err != nil {
		return fmt.Errorf("prune slow operations for session: %w", err)
	}
	return nil
}

func (s *SQLite) CleanupSlowOperations(ctx context.Context, maxAge time.Duration) error {
	cutoff := time.Now().UTC().Add(-maxAge).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_slow_operations WHERE updated_at < ?`, cutoff); err != nil {
		return fmt.Errorf("cleanup slow operations: %w", err)
	}
	return nil
}
