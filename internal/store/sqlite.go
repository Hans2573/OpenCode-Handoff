package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

type SQLite struct {
	db *sql.DB
}

func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &SQLite{db: db}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS handoff_records (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			session_name TEXT NOT NULL DEFAULT '',
			directory TEXT NOT NULL,
			project_name TEXT NOT NULL,
			feishu_chat_id TEXT NOT NULL DEFAULT '',
			feishu_message_id TEXT NOT NULL DEFAULT '',
			handoff_type TEXT NOT NULL,
			last_assistant_message_id TEXT NOT NULL,
			last_assistant_text TEXT NOT NULL DEFAULT '',
			error_text TEXT NOT NULL DEFAULT '',
			question_id TEXT NOT NULL DEFAULT '',
			question_json TEXT NOT NULL DEFAULT '[]',
			permission_id TEXT NOT NULL DEFAULT '',
			permission_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			resolved_at INTEGER,
			resume_message_id TEXT NOT NULL DEFAULT '',
			UNIQUE(session_id, last_assistant_message_id, handoff_type)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_handoff_feishu_message
			ON handoff_records(feishu_message_id) WHERE feishu_message_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_handoff_status ON handoff_records(status)`,
		`CREATE TABLE IF NOT EXISTS channel_binding (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			chat_id TEXT NOT NULL,
			user_ids TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS channel_reply_receipts (
			message_id TEXT PRIMARY KEY,
			handoff_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (handoff_id) REFERENCES handoff_records(id)
		)`,
		`CREATE TABLE IF NOT EXISTS session_create_receipts (
			message_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS session_model_overrides (
			session_id TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			model_name TEXT NOT NULL DEFAULT '',
			variant TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS recent_models (
			provider_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			model_name TEXT NOT NULL DEFAULT '',
			variant TEXT NOT NULL DEFAULT '',
			used_at INTEGER NOT NULL,
			PRIMARY KEY(provider_id, model_id, variant)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_recent_models_used_at ON recent_models(used_at DESC)`,
		`CREATE TABLE IF NOT EXISTS agent_instances (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			endpoint TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			health TEXT NOT NULL DEFAULT 'unknown',
			last_error TEXT NOT NULL DEFAULT '',
			last_seen_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS channel_instances (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			health TEXT NOT NULL DEFAULT 'unknown',
			last_error TEXT NOT NULL DEFAULT '',
			last_seen_at INTEGER,
			config_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			directory TEXT NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_seen_at INTEGER NOT NULL,
			UNIQUE(agent_id, directory),
			FOREIGN KEY (agent_id) REFERENCES agent_instances(id)
		)`,
		`CREATE TABLE IF NOT EXISTS project_routes (
			project_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(project_id, channel_id),
			FOREIGN KEY (project_id) REFERENCES projects(id),
			FOREIGN KEY (channel_id) REFERENCES channel_instances(id)
		)`,
		`CREATE TABLE IF NOT EXISTS event_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			level TEXT NOT NULL,
			event_type TEXT NOT NULL,
			source TEXT NOT NULL,
			message TEXT NOT NULL,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_event_logs_created_at ON event_logs(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS session_execution_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			directory TEXT NOT NULL,
			project_name TEXT NOT NULL DEFAULT '',
			session_title TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL,
			ended_at INTEGER,
			duration_seconds INTEGER NOT NULL DEFAULT 0,
			end_reason TEXT NOT NULL DEFAULT '',
			UNIQUE(session_id, directory, started_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_execution_runs_ended_at
			ON session_execution_runs(ended_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_session_execution_runs_session
			ON session_execution_runs(session_id, directory, started_at DESC)`,
		`CREATE TABLE IF NOT EXISTS goal_loops (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			goal TEXT NOT NULL,
			project_id TEXT NOT NULL,
			project_name TEXT NOT NULL,
			directory TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			model_provider_id TEXT NOT NULL DEFAULT '',
			model_id TEXT NOT NULL DEFAULT '',
			model_name TEXT NOT NULL DEFAULT '',
			model_variant TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			attached_session INTEGER NOT NULL DEFAULT 0,
			automation_mode TEXT NOT NULL DEFAULT 'manual',
			permission_approval_mode TEXT NOT NULL DEFAULT 'ai',
			allowed_directories_json TEXT NOT NULL DEFAULT '[]',
			supervisor_model_provider_id TEXT NOT NULL DEFAULT '',
			supervisor_model_id TEXT NOT NULL DEFAULT '',
			supervisor_model_name TEXT NOT NULL DEFAULT '',
			supervisor_model_variant TEXT NOT NULL DEFAULT '',
			supervisor_session_id TEXT NOT NULL DEFAULT '',
			pending_request_id TEXT NOT NULL DEFAULT '',
			pending_request_type TEXT NOT NULL DEFAULT '',
			supervisor_last_message_id TEXT NOT NULL DEFAULT '',
			pending_feedback TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			require_completion_confirmation INTEGER NOT NULL DEFAULT 0,
			failure_limit INTEGER NOT NULL DEFAULT 3,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			cycle_count INTEGER NOT NULL DEFAULT 0,
			last_assistant_message_id TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			retry_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			completed_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_goal_loops_status ON goal_loops(status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_goal_loops_project ON goal_loops(project_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS goal_loop_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			loop_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			message TEXT NOT NULL,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL,
			FOREIGN KEY (loop_id) REFERENCES goal_loops(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_goal_loop_events_loop ON goal_loop_events(loop_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate SQLite: %w", err)
		}
	}
	for name, definition := range map[string]string{
		"session_name":    "TEXT NOT NULL DEFAULT ''",
		"question_id":     "TEXT NOT NULL DEFAULT ''",
		"question_json":   "TEXT NOT NULL DEFAULT '[]'",
		"permission_id":   "TEXT NOT NULL DEFAULT ''",
		"permission_json": "TEXT NOT NULL DEFAULT '{}'",
	} {
		if err := s.ensureColumn(ctx, "handoff_records", name, definition); err != nil {
			return err
		}
	}
	for name, definition := range map[string]string{
		"model_provider_id":            "TEXT NOT NULL DEFAULT ''",
		"model_id":                     "TEXT NOT NULL DEFAULT ''",
		"model_name":                   "TEXT NOT NULL DEFAULT ''",
		"model_variant":                "TEXT NOT NULL DEFAULT ''",
		"attached_session":             "INTEGER NOT NULL DEFAULT 0",
		"automation_mode":              "TEXT NOT NULL DEFAULT 'manual'",
		"permission_approval_mode":     "TEXT NOT NULL DEFAULT 'ai'",
		"allowed_directories_json":     "TEXT NOT NULL DEFAULT '[]'",
		"supervisor_model_provider_id": "TEXT NOT NULL DEFAULT ''",
		"supervisor_model_id":          "TEXT NOT NULL DEFAULT ''",
		"supervisor_model_name":        "TEXT NOT NULL DEFAULT ''",
		"supervisor_model_variant":     "TEXT NOT NULL DEFAULT ''",
		"supervisor_session_id":        "TEXT NOT NULL DEFAULT ''",
		"pending_request_id":           "TEXT NOT NULL DEFAULT ''",
		"pending_request_type":         "TEXT NOT NULL DEFAULT ''",
		"supervisor_last_message_id":   "TEXT NOT NULL DEFAULT ''",
		"pending_feedback":             "TEXT NOT NULL DEFAULT ''",
	} {
		if err := s.ensureColumn(ctx, "goal_loops", name, definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "goal_loop_events", "metadata_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	return nil
}

func (s *SQLite) ensureColumn(ctx context.Context, table, name, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect SQLite schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan SQLite schema: %w", err)
		}
		if columnName == name {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close SQLite schema rows: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+name+" "+definition); err != nil {
		return fmt.Errorf("add SQLite column %s: %w", name, err)
	}
	return nil
}

func (s *SQLite) Create(ctx context.Context, handoff domain.Handoff) error {
	questions, err := json.Marshal(handoff.Questions)
	if err != nil {
		return fmt.Errorf("encode handoff questions: %w", err)
	}
	permission, err := json.Marshal(handoff.Permission)
	if err != nil {
		return fmt.Errorf("encode handoff permission: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO handoff_records (
			id, session_id, session_name, directory, project_name, handoff_type,
			last_assistant_message_id, last_assistant_text, error_text,
			question_id, question_json, permission_id, permission_json,
			status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		handoff.ID,
		handoff.SessionID,
		handoff.SessionName,
		handoff.Directory,
		handoff.ProjectName,
		handoff.Type,
		handoff.LastAssistantMessageID,
		handoff.LastAssistantText,
		handoff.ErrorText,
		handoff.QuestionID,
		string(questions),
		handoff.PermissionID,
		string(permission),
		domain.StatusOpen,
		handoff.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return ErrDuplicate
		}
		return fmt.Errorf("create handoff: %w", err)
	}
	return nil
}

func (s *SQLite) BindMessage(ctx context.Context, id string, ref domain.MessageRef) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE handoff_records
		SET feishu_chat_id = ?, feishu_message_id = ?
		WHERE id = ? AND feishu_message_id = ''`, ref.ChatID, ref.MessageID, id)
	if err != nil {
		return fmt.Errorf("bind handoff message: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) DeleteUnbound(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM handoff_records WHERE id = ? AND feishu_message_id = ''`, id)
	if err != nil {
		return fmt.Errorf("delete undelivered handoff: %w", err)
	}
	return nil
}

func (s *SQLite) GetByID(ctx context.Context, id string) (domain.Handoff, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, session_name, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, question_id, question_json,
			permission_id, permission_json,
			status, created_at, resolved_at
		FROM handoff_records
		WHERE id = ?`, id)
	handoff, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, ErrNotFound
	}
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("find handoff by id: %w", err)
	}
	return handoff, nil
}

func (s *SQLite) ClaimByMessage(ctx context.Context, messageID, resumeMessageID string) (domain.Handoff, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("begin handoff claim: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, session_name, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, question_id, question_json,
			permission_id, permission_json,
			status, created_at, resolved_at
		FROM handoff_records
		WHERE feishu_message_id = ?`, messageID)
	handoff, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, ErrNotFound
	}
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("find handoff message mapping: %w", err)
	}
	if (handoff.Type == domain.HandoffQuestion || handoff.Type == domain.HandoffPermission || handoff.Type == domain.HandoffGoalCompletion) && handoff.Status != domain.StatusOpen {
		return domain.Handoff{}, ErrNotFound
	}
	if err := recordReplyReceipt(ctx, tx, resumeMessageID, handoff.ID); err != nil {
		return domain.Handoff{}, err
	}

	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE handoff_records
		SET status = ?, resolved_at = ?, resume_message_id = ?
		WHERE id = ?`,
		domain.StatusResumed, now.UnixMilli(), resumeMessageID, handoff.ID)
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("claim handoff: %w", err)
	}
	if err := expectOne(result); err != nil {
		return domain.Handoff{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Handoff{}, fmt.Errorf("commit handoff claim: %w", err)
	}
	handoff.Status = domain.StatusResumed
	handoff.ResolvedAt = &now
	return handoff, nil
}

func (s *SQLite) ClaimOnlyOpenByChat(ctx context.Context, chatID, resumeMessageID string) (domain.Handoff, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("begin channel handoff claim: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT session_id
		FROM handoff_records
		WHERE feishu_chat_id = ? AND status = ?
		LIMIT 2`, chatID, domain.StatusOpen)
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("list open channel sessions: %w", err)
	}
	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return domain.Handoff{}, fmt.Errorf("scan open channel session: %w", err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Close(); err != nil {
		return domain.Handoff{}, fmt.Errorf("close open channel sessions: %w", err)
	}
	if len(sessionIDs) == 0 {
		return domain.Handoff{}, ErrNotFound
	}
	if len(sessionIDs) > 1 {
		return domain.Handoff{}, ErrAmbiguous
	}

	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, session_name, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, question_id, question_json,
			permission_id, permission_json,
			status, created_at, resolved_at
		FROM handoff_records
		WHERE feishu_chat_id = ? AND session_id = ? AND status = ?
		ORDER BY created_at DESC
		LIMIT 1`, chatID, sessionIDs[0], domain.StatusOpen)
	handoff, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, ErrNotFound
	}
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("find open channel handoff: %w", err)
	}
	if err := recordReplyReceipt(ctx, tx, resumeMessageID, handoff.ID); err != nil {
		return domain.Handoff{}, err
	}

	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE handoff_records
		SET status = ?, resolved_at = ?, resume_message_id = ?
		WHERE id = ? AND status = ?`,
		domain.StatusResumed, now.UnixMilli(), resumeMessageID, handoff.ID, domain.StatusOpen)
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("claim open channel handoff: %w", err)
	}
	if err := expectOne(result); err != nil {
		return domain.Handoff{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Handoff{}, fmt.Errorf("commit channel handoff claim: %w", err)
	}
	handoff.Status = domain.StatusResumed
	handoff.ResolvedAt = &now
	return handoff, nil
}

func (s *SQLite) Reopen(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE handoff_records
		SET status = ?, resolved_at = NULL, resume_message_id = ''
		WHERE id = ? AND status = ?`, domain.StatusOpen, id, domain.StatusResumed)
	if err != nil {
		return fmt.Errorf("reopen handoff: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) CloseResolvedPermissions(ctx context.Context, sessionID string, pendingIDs []string) error {
	query := `
		UPDATE handoff_records
		SET status = ?, resolved_at = ?
		WHERE session_id = ? AND handoff_type = ? AND status = ?`
	args := []any{domain.StatusClosed, time.Now().UTC().UnixMilli(), sessionID, domain.HandoffPermission, domain.StatusOpen}
	if len(pendingIDs) > 0 {
		query += " AND permission_id NOT IN (" + strings.TrimRight(strings.Repeat("?,", len(pendingIDs)), ",") + ")"
		for _, id := range pendingIDs {
			args = append(args, id)
		}
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("close resolved permission handoffs: %w", err)
	}
	return nil
}

func (s *SQLite) ClosePermission(ctx context.Context, permissionID string) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE handoff_records
		SET status = ?, resolved_at = ?
		WHERE permission_id = ? AND handoff_type = ? AND status = ?`,
		domain.StatusClosed, time.Now().UTC().UnixMilli(), permissionID, domain.HandoffPermission, domain.StatusOpen); err != nil {
		return fmt.Errorf("close permission handoff: %w", err)
	}
	return nil
}

func (s *SQLite) ClaimSessionCreate(ctx context.Context, messageID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_create_receipts (message_id, created_at)
		VALUES (?, ?)`, messageID, time.Now().UTC().UnixMilli())
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrDuplicateReply
	}
	return fmt.Errorf("claim session creation: %w", err)
}

func (s *SQLite) CompleteSessionCreate(ctx context.Context, messageID, sessionID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_create_receipts
		SET session_id = ?
		WHERE message_id = ? AND session_id = ''`, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("complete session creation: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) ReleaseSessionCreate(ctx context.Context, messageID string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM session_create_receipts
		WHERE message_id = ? AND session_id = ''`, messageID)
	if err != nil {
		return fmt.Errorf("release session creation: %w", err)
	}
	return nil
}

func (s *SQLite) GetChannelBinding(ctx context.Context) (domain.ChannelBinding, error) {
	var binding domain.ChannelBinding
	var encodedIDs string
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT chat_id, user_ids, created_at
		FROM channel_binding
		WHERE singleton = 1`).Scan(&binding.ChatID, &encodedIDs, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ChannelBinding{}, ErrNotFound
	}
	if err != nil {
		return domain.ChannelBinding{}, fmt.Errorf("get channel binding: %w", err)
	}
	if err := json.Unmarshal([]byte(encodedIDs), &binding.UserIDs); err != nil {
		return domain.ChannelBinding{}, fmt.Errorf("decode channel binding identities: %w", err)
	}
	binding.CreatedAt = time.UnixMilli(createdAt).UTC()
	return binding, nil
}

func (s *SQLite) BindChannel(ctx context.Context, binding domain.ChannelBinding) error {
	encodedIDs, err := json.Marshal(binding.UserIDs)
	if err != nil {
		return fmt.Errorf("encode channel binding identities: %w", err)
	}
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO channel_binding (singleton, chat_id, user_ids, created_at)
		VALUES (1, ?, ?, ?)`, binding.ChatID, string(encodedIDs), binding.CreatedAt.UTC().UnixMilli())
	if err == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return fmt.Errorf("bind channel: %w", err)
	}
	existing, getErr := s.GetChannelBinding(ctx)
	if getErr != nil {
		return getErr
	}
	if existing.ChatID == binding.ChatID && identifiersOverlap(existing.UserIDs, binding.UserIDs) {
		return nil
	}
	return ErrAlreadyBound
}

func (s *SQLite) Close() error {
	return s.db.Close()
}

type scanner interface {
	Scan(...any) error
}

func scanHandoff(row scanner) (domain.Handoff, error) {
	var handoff domain.Handoff
	var questions string
	var permission string
	var createdAt int64
	var resolvedAt sql.NullInt64
	err := row.Scan(
		&handoff.ID,
		&handoff.SessionID,
		&handoff.SessionName,
		&handoff.Directory,
		&handoff.ProjectName,
		&handoff.FeishuChatID,
		&handoff.FeishuMessageID,
		&handoff.Type,
		&handoff.LastAssistantMessageID,
		&handoff.LastAssistantText,
		&handoff.ErrorText,
		&handoff.QuestionID,
		&questions,
		&handoff.PermissionID,
		&permission,
		&handoff.Status,
		&createdAt,
		&resolvedAt,
	)
	if err != nil {
		return domain.Handoff{}, err
	}
	if err := json.Unmarshal([]byte(questions), &handoff.Questions); err != nil {
		return domain.Handoff{}, fmt.Errorf("decode handoff questions: %w", err)
	}
	if err := json.Unmarshal([]byte(permission), &handoff.Permission); err != nil {
		return domain.Handoff{}, fmt.Errorf("decode handoff permission: %w", err)
	}
	handoff.CreatedAt = time.UnixMilli(createdAt).UTC()
	if resolvedAt.Valid {
		value := time.UnixMilli(resolvedAt.Int64).UTC()
		handoff.ResolvedAt = &value
	}
	return handoff, nil
}

func expectOne(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func identifiersOverlap(left, right []string) bool {
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := values[value]; ok && value != "" {
			return true
		}
	}
	return false
}

func recordReplyReceipt(ctx context.Context, tx *sql.Tx, messageID, handoffID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO channel_reply_receipts (message_id, handoff_id, created_at)
		VALUES (?, ?, ?)`, messageID, handoffID, time.Now().UTC().UnixMilli())
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrDuplicateReply
	}
	return fmt.Errorf("record channel reply receipt: %w", err)
}
