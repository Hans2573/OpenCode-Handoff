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

	"github.com/xiaohang2/opencode-handoff/internal/domain"
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
			directory TEXT NOT NULL,
			project_name TEXT NOT NULL,
			feishu_chat_id TEXT NOT NULL DEFAULT '',
			feishu_message_id TEXT NOT NULL DEFAULT '',
			handoff_type TEXT NOT NULL,
			last_assistant_message_id TEXT NOT NULL,
			last_assistant_text TEXT NOT NULL DEFAULT '',
			error_text TEXT NOT NULL DEFAULT '',
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
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate SQLite: %w", err)
		}
	}
	return nil
}

func (s *SQLite) Create(ctx context.Context, handoff domain.Handoff) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO handoff_records (
			id, session_id, directory, project_name, handoff_type,
			last_assistant_message_id, last_assistant_text, error_text,
			status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		handoff.ID,
		handoff.SessionID,
		handoff.Directory,
		handoff.ProjectName,
		handoff.Type,
		handoff.LastAssistantMessageID,
		handoff.LastAssistantText,
		handoff.ErrorText,
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

func (s *SQLite) ClaimByMessage(ctx context.Context, messageID, resumeMessageID string) (domain.Handoff, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("begin handoff claim: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, status, created_at, resolved_at
		FROM handoff_records
		WHERE feishu_message_id = ?`, messageID)
	handoff, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, ErrNotFound
	}
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("find handoff message mapping: %w", err)
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
		SELECT id, session_id, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, status, created_at, resolved_at
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
	var createdAt int64
	var resolvedAt sql.NullInt64
	err := row.Scan(
		&handoff.ID,
		&handoff.SessionID,
		&handoff.Directory,
		&handoff.ProjectName,
		&handoff.FeishuChatID,
		&handoff.FeishuMessageID,
		&handoff.Type,
		&handoff.LastAssistantMessageID,
		&handoff.LastAssistantText,
		&handoff.ErrorText,
		&handoff.Status,
		&createdAt,
		&resolvedAt,
	)
	if err != nil {
		return domain.Handoff{}, err
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
