package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

func (s *SQLite) CreateGoalLoop(ctx context.Context, loop domain.GoalLoop) error {
	allowedDirectories, err := json.Marshal(loop.AllowedDirectories)
	if err != nil {
		return fmt.Errorf("encode Goal Loop allowed directories: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO goal_loops (
			id, name, goal, project_id, project_name, directory, agent_id, agent_name,
			model_provider_id, model_id, model_name, model_variant, session_id,
			attached_session, automation_mode, permission_approval_mode, allowed_directories_json,
			supervisor_model_provider_id, supervisor_model_id, supervisor_model_name,
			supervisor_model_variant, supervisor_session_id, pending_request_id,
			pending_request_type, supervisor_last_message_id, pending_feedback,
			status, require_completion_confirmation, failure_limit,
			consecutive_failures, cycle_count, last_assistant_message_id, last_error,
			retry_at, created_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		loop.ID, loop.Name, loop.Goal, loop.ProjectID, loop.ProjectName, loop.Directory,
		loop.AgentID, loop.AgentName, loop.ModelProviderID, loop.ModelID, loop.ModelName, loop.ModelVariant,
		loop.SessionID, boolInt(loop.AttachedSession), loop.AutomationMode, loop.PermissionApprovalMode, string(allowedDirectories),
		loop.SupervisorModelProviderID, loop.SupervisorModelID, loop.SupervisorModelName,
		loop.SupervisorModelVariant, loop.SupervisorSessionID, loop.PendingRequestID,
		loop.PendingRequestType, loop.SupervisorLastMessageID, loop.PendingFeedback,
		loop.Status, boolInt(loop.RequireCompletionConfirmation),
		loop.FailureLimit, loop.ConsecutiveFailures, loop.CycleCount, loop.LastAssistantMessageID,
		loop.LastError, nullableTime(loop.RetryAt), loop.CreatedAt.UTC().UnixMilli(),
		loop.UpdatedAt.UTC().UnixMilli(), nullableTime(loop.CompletedAt))
	if err != nil {
		return fmt.Errorf("create goal loop: %w", err)
	}
	return nil
}

func (s *SQLite) SaveGoalLoop(ctx context.Context, loop domain.GoalLoop) error {
	allowedDirectories, err := json.Marshal(loop.AllowedDirectories)
	if err != nil {
		return fmt.Errorf("encode Goal Loop allowed directories: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE goal_loops SET
			name = ?, goal = ?, project_id = ?, project_name = ?, directory = ?,
			agent_id = ?, agent_name = ?, model_provider_id = ?, model_id = ?,
			model_name = ?, model_variant = ?, session_id = ?, attached_session = ?,
			automation_mode = ?, permission_approval_mode = ?, allowed_directories_json = ?,
			supervisor_model_provider_id = ?, supervisor_model_id = ?, supervisor_model_name = ?,
			supervisor_model_variant = ?, supervisor_session_id = ?, pending_request_id = ?,
			pending_request_type = ?, supervisor_last_message_id = ?, pending_feedback = ?, status = ?,
			require_completion_confirmation = ?, failure_limit = ?, consecutive_failures = ?,
			cycle_count = ?, last_assistant_message_id = ?, last_error = ?, retry_at = ?,
			updated_at = ?, completed_at = ?
		WHERE id = ?`,
		loop.Name, loop.Goal, loop.ProjectID, loop.ProjectName, loop.Directory,
		loop.AgentID, loop.AgentName, loop.ModelProviderID, loop.ModelID, loop.ModelName, loop.ModelVariant,
		loop.SessionID, boolInt(loop.AttachedSession), loop.AutomationMode, loop.PermissionApprovalMode, string(allowedDirectories),
		loop.SupervisorModelProviderID, loop.SupervisorModelID, loop.SupervisorModelName,
		loop.SupervisorModelVariant, loop.SupervisorSessionID, loop.PendingRequestID,
		loop.PendingRequestType, loop.SupervisorLastMessageID, loop.PendingFeedback, loop.Status,
		boolInt(loop.RequireCompletionConfirmation), loop.FailureLimit, loop.ConsecutiveFailures,
		loop.CycleCount, loop.LastAssistantMessageID, loop.LastError, nullableTime(loop.RetryAt),
		loop.UpdatedAt.UTC().UnixMilli(), nullableTime(loop.CompletedAt), loop.ID)
	if err != nil {
		return fmt.Errorf("save goal loop: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) GetGoalLoop(ctx context.Context, id string) (domain.GoalLoop, error) {
	loop, err := scanGoalLoop(s.db.QueryRowContext(ctx, goalLoopSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GoalLoop{}, ErrNotFound
	}
	if err != nil {
		return domain.GoalLoop{}, fmt.Errorf("get goal loop: %w", err)
	}
	return loop, nil
}

func (s *SQLite) GetGoalLoopBySession(ctx context.Context, sessionID, directory string) (domain.GoalLoop, error) {
	loop, err := scanGoalLoop(s.db.QueryRowContext(ctx, goalLoopSelect+`
		WHERE session_id = ? AND directory = ?
		ORDER BY updated_at DESC LIMIT 1`, sessionID, directory))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GoalLoop{}, ErrNotFound
	}
	if err != nil {
		return domain.GoalLoop{}, fmt.Errorf("get goal loop by session: %w", err)
	}
	return loop, nil
}

func (s *SQLite) GetOpenGoalCompletionHandoff(ctx context.Context, sessionID string) (domain.Handoff, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, session_name, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, question_id, question_json,
			permission_id, permission_json, status, created_at, resolved_at
		FROM handoff_records
		WHERE session_id = ? AND handoff_type = ? AND status = ?
		ORDER BY created_at DESC LIMIT 1`, sessionID, domain.HandoffGoalCompletion, domain.StatusOpen)
	item, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, ErrNotFound
	}
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("get open goal completion handoff: %w", err)
	}
	return item, nil
}

func (s *SQLite) CloseGoalCompletionHandoff(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE handoff_records SET status = ?, resolved_at = ?
		WHERE session_id = ? AND handoff_type = ? AND status = ?`,
		domain.StatusClosed, time.Now().UTC().UnixMilli(), sessionID, domain.HandoffGoalCompletion, domain.StatusOpen)
	if err != nil {
		return fmt.Errorf("close goal completion handoff: %w", err)
	}
	return nil
}

func (s *SQLite) CloseGoalStatusHandoff(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE handoff_records SET status = ?, resolved_at = ? WHERE id = ?`,
		domain.StatusClosed, time.Now().UTC().UnixMilli(), id)
	return err
}

func (s *SQLite) ListGoalLoops(ctx context.Context) ([]domain.GoalLoop, error) {
	rows, err := s.db.QueryContext(ctx, goalLoopSelect+` ORDER BY updated_at DESC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list goal loops: %w", err)
	}
	defer rows.Close()
	var result []domain.GoalLoop
	for rows.Next() {
		loop, err := scanGoalLoop(rows)
		if err != nil {
			return nil, fmt.Errorf("scan goal loop: %w", err)
		}
		result = append(result, loop)
	}
	return result, rows.Err()
}

func (s *SQLite) DeleteGoalLoop(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM goal_loops WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete goal loop: %w", err)
	}
	return expectOne(result)
}

func (s *SQLite) AppendGoalLoopEvent(ctx context.Context, loopID, eventType, message string) error {
	return s.AppendGoalLoopEventDetails(ctx, loopID, eventType, message, nil)
}

func (s *SQLite) AppendGoalLoopEventDetails(ctx context.Context, loopID, eventType, message string, metadata map[string]any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode Goal Loop event metadata: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO goal_loop_events (loop_id, event_type, message, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?)`, loopID, eventType, message, string(encoded), time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("append goal loop event: %w", err)
	}
	return nil
}

func (s *SQLite) ListGoalLoopEvents(ctx context.Context, loopID string, limit int) ([]domain.GoalLoopEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, loop_id, event_type, message, metadata_json, created_at
		FROM goal_loop_events WHERE loop_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, loopID, limit)
	if err != nil {
		return nil, fmt.Errorf("list goal loop events: %w", err)
	}
	defer rows.Close()
	var result []domain.GoalLoopEvent
	for rows.Next() {
		var item domain.GoalLoopEvent
		var createdAt int64
		var metadataJSON string
		if err := rows.Scan(&item.ID, &item.LoopID, &item.Type, &item.Message, &metadataJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan goal loop event: %w", err)
		}
		item.CreatedAt = time.UnixMilli(createdAt).UTC()
		_ = json.Unmarshal([]byte(metadataJSON), &item.Metadata)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLite) CloseHandoffRequest(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE handoff_records SET status = ?, resolved_at = ?
		WHERE status = ? AND (question_id = ? OR permission_id = ?)`,
		domain.StatusClosed, time.Now().UTC().UnixMilli(), domain.StatusOpen, requestID, requestID)
	if err != nil {
		return fmt.Errorf("close handoff request: %w", err)
	}
	return nil
}

func (s *SQLite) GetOpenHandoffByRequest(ctx context.Context, requestID string) (domain.Handoff, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, session_name, directory, project_name, feishu_chat_id,
			feishu_message_id, handoff_type, last_assistant_message_id,
			last_assistant_text, error_text, question_id, question_json,
			permission_id, permission_json, status, created_at, resolved_at
		FROM handoff_records
		WHERE status = ? AND (question_id = ? OR permission_id = ?)
		ORDER BY created_at DESC LIMIT 1`, domain.StatusOpen, requestID, requestID)
	item, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, ErrNotFound
	}
	if err != nil {
		return domain.Handoff{}, fmt.Errorf("get open handoff request: %w", err)
	}
	return item, nil
}

const goalLoopSelect = `
	SELECT id, name, goal, project_id, project_name, directory, agent_id, agent_name,
		model_provider_id, model_id, model_name, model_variant, session_id,
		attached_session, automation_mode, permission_approval_mode, allowed_directories_json,
		supervisor_model_provider_id, supervisor_model_id, supervisor_model_name,
		supervisor_model_variant, supervisor_session_id, pending_request_id,
		pending_request_type, supervisor_last_message_id, pending_feedback,
		status, require_completion_confirmation, failure_limit,
		consecutive_failures, cycle_count, last_assistant_message_id, last_error,
		retry_at, created_at, updated_at, completed_at
	FROM goal_loops`

type rowScanner interface {
	Scan(...any) error
}

func scanGoalLoop(row rowScanner) (domain.GoalLoop, error) {
	var loop domain.GoalLoop
	var requireConfirmation int
	var attachedSession int
	var allowedDirectoriesJSON string
	var retryAt, completedAt sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(
		&loop.ID, &loop.Name, &loop.Goal, &loop.ProjectID, &loop.ProjectName, &loop.Directory,
		&loop.AgentID, &loop.AgentName, &loop.ModelProviderID, &loop.ModelID, &loop.ModelName, &loop.ModelVariant,
		&loop.SessionID, &attachedSession, &loop.AutomationMode, &loop.PermissionApprovalMode, &allowedDirectoriesJSON,
		&loop.SupervisorModelProviderID, &loop.SupervisorModelID, &loop.SupervisorModelName,
		&loop.SupervisorModelVariant, &loop.SupervisorSessionID, &loop.PendingRequestID,
		&loop.PendingRequestType, &loop.SupervisorLastMessageID, &loop.PendingFeedback,
		&loop.Status, &requireConfirmation,
		&loop.FailureLimit, &loop.ConsecutiveFailures, &loop.CycleCount,
		&loop.LastAssistantMessageID, &loop.LastError, &retryAt, &createdAt, &updatedAt, &completedAt,
	)
	if err != nil {
		return domain.GoalLoop{}, err
	}
	loop.RequireCompletionConfirmation = requireConfirmation != 0
	loop.AttachedSession = attachedSession != 0
	_ = json.Unmarshal([]byte(allowedDirectoriesJSON), &loop.AllowedDirectories)
	if loop.AutomationMode == "" {
		loop.AutomationMode = domain.GoalLoopManual
	}
	if loop.PermissionApprovalMode == "" {
		loop.PermissionApprovalMode = domain.GoalPermissionAI
	}
	loop.CreatedAt = time.UnixMilli(createdAt).UTC()
	loop.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	if retryAt.Valid {
		loop.RetryAt = time.UnixMilli(retryAt.Int64).UTC()
	}
	if completedAt.Valid {
		loop.CompletedAt = time.UnixMilli(completedAt.Int64).UTC()
	}
	return loop, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixMilli()
}
