package handoff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xiaohang2/opencode-handoff/internal/channel"
	"github.com/xiaohang2/opencode-handoff/internal/domain"
	"github.com/xiaohang2/opencode-handoff/internal/opencode"
	"github.com/xiaohang2/opencode-handoff/internal/store"
)

type EngineOptions struct {
	MaxOutputChars int
	NotifyIdle     bool
	NotifyError    bool
	AllowedUsers   []string
	ChatID         string
}

type Engine struct {
	opencode opencode.Adapter
	channel  channel.Channel
	store    store.Store
	options  EngineOptions
	allowed  map[string]struct{}
	logger   *slog.Logger
}

func NewEngine(
	opencodeClient opencode.Adapter,
	channelClient channel.Channel,
	handoffStore store.Store,
	options EngineOptions,
	logger *slog.Logger,
) *Engine {
	allowed := make(map[string]struct{}, len(options.AllowedUsers))
	for _, user := range options.AllowedUsers {
		allowed[user] = struct{}{}
	}
	return &Engine{
		opencode: opencodeClient,
		channel:  channelClient,
		store:    handoffStore,
		options:  options,
		allowed:  allowed,
		logger:   logger,
	}
}

func (e *Engine) Run(ctx context.Context, signals <-chan Signal) error {
	replies, err := e.channel.Receive(ctx)
	if err != nil {
		return fmt.Errorf("start channel receiver: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case signal, ok := <-signals:
			if !ok {
				return nil
			}
			if err := e.handleSignal(ctx, signal); err != nil && ctx.Err() == nil {
				e.logger.Error("process handoff signal", "session_id", signal.SessionID, "error", err)
			}
		case reply, ok := <-replies:
			if !ok {
				return errors.New("channel receiver stopped")
			}
			if err := e.handleReply(ctx, reply); err != nil && ctx.Err() == nil {
				e.logger.Error("process channel reply", "message_id", reply.MessageID, "error", err)
			}
		}
	}
}

func (e *Engine) handleSignal(ctx context.Context, signal Signal) error {
	if signal.Kind == SignalError && !e.options.NotifyError {
		return nil
	}

	session, err := e.opencode.GetSession(ctx, signal.SessionID, signal.Directory)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}
	if session.ParentID != "" {
		e.logger.Debug("ignore subagent session handoff", "session_id", session.ID, "parent_id", session.ParentID)
		return nil
	}
	messages, err := e.opencode.GetMessages(ctx, signal.SessionID, signal.Directory, 100)
	if err != nil {
		return fmt.Errorf("get session messages: %w", err)
	}
	output, found := opencode.LastAssistantOutput(messages)
	if !found && signal.Kind != SignalError {
		e.logger.Debug("ignore idle session without assistant output", "session_id", signal.SessionID)
		return nil
	}

	errorText := strings.TrimSpace(signal.Error)
	if errorText == "" {
		errorText = output.Error
	}
	if signal.Kind == SignalError && errorText == "" {
		errorText = "OpenCode session error"
	}
	handoffType := domain.HandoffFinished
	if errorText != "" {
		handoffType = domain.HandoffError
		if !e.options.NotifyError {
			return nil
		}
	} else if !e.options.NotifyIdle {
		return nil
	}
	messageID := output.MessageID
	if messageID == "" {
		messageID = "error-without-message"
	}
	handoff := domain.Handoff{
		ID:                     newID(),
		SessionID:              session.ID,
		SessionName:            sessionName(session),
		Directory:              session.Directory,
		ProjectName:            projectName(session),
		Type:                   handoffType,
		LastAssistantMessageID: messageID,
		LastAssistantText:      truncateTail(output.Text, e.options.MaxOutputChars),
		ErrorText:              truncateTail(errorText, e.options.MaxOutputChars),
		Status:                 domain.StatusOpen,
		CreatedAt:              time.Now().UTC(),
	}
	if err := e.store.Create(ctx, handoff); errors.Is(err, store.ErrDuplicate) {
		e.logger.Debug("skip duplicate handoff", "session_id", handoff.SessionID, "message_id", messageID, "type", handoffType)
		return nil
	} else if err != nil {
		return err
	}

	ref, err := e.sendWithRetry(ctx, handoff)
	if err != nil {
		if cleanupErr := e.store.DeleteUnbound(context.WithoutCancel(ctx), handoff.ID); cleanupErr != nil {
			e.logger.Error("clean up undelivered handoff", "handoff_id", handoff.ID, "error", cleanupErr)
		}
		return err
	}
	var bindErr error
	for attempt := 1; attempt <= 3; attempt++ {
		bindErr = e.store.BindMessage(ctx, handoff.ID, ref)
		if bindErr == nil {
			break
		}
	}
	if bindErr != nil {
		return fmt.Errorf("persist channel message mapping: %w", bindErr)
	}
	e.logger.Info("handoff sent", "session_id", handoff.SessionID, "type", handoff.Type, "message_id", ref.MessageID)
	return nil
}

func (e *Engine) sendWithRetry(ctx context.Context, handoff domain.Handoff) (domain.MessageRef, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ref, err := e.channel.SendHandoff(ctx, handoff)
		if err == nil {
			return ref, nil
		}
		lastErr = err
		if attempt == 3 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.MessageRef{}, ctx.Err()
		case <-timer.C:
		}
	}
	return domain.MessageRef{}, fmt.Errorf("send handoff after retries: %w", lastErr)
}

func (e *Engine) handleReply(ctx context.Context, reply domain.UserReply) error {
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return nil
	}
	allowed, err := e.isAllowed(ctx, reply)
	if err != nil {
		return fmt.Errorf("authorize channel reply: %w", err)
	}
	if !allowed {
		e.logger.Warn("ignore reply from unauthorized user", "sender_id", reply.SenderID)
		return nil
	}

	var handoff domain.Handoff
	if reply.ParentMessageID != "" {
		handoff, err = e.store.ClaimByMessage(ctx, reply.ParentMessageID, reply.MessageID)
	} else {
		handoff, err = e.store.ClaimOnlyOpenByChat(ctx, reply.ChatID, reply.MessageID)
	}
	if errors.Is(err, store.ErrAmbiguous) {
		if replyErr := e.channel.Reply(ctx, reply.MessageID, "当前有多个 OpenCode Session 等待输入，请引用回复对应的 Handoff 通知。"); replyErr != nil {
			return fmt.Errorf("explain ambiguous handoff route: %w", replyErr)
		}
		return nil
	}
	if errors.Is(err, store.ErrDuplicateReply) {
		e.logger.Debug("ignore duplicate channel reply", "message_id", reply.MessageID)
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		e.logger.Debug("ignore reply without an open handoff", "parent_message_id", reply.ParentMessageID)
		return nil
	}
	if err != nil {
		return err
	}
	if err := e.opencode.SendPrompt(ctx, handoff.SessionID, handoff.Directory, text); err != nil {
		if reopenErr := e.store.Reopen(context.WithoutCancel(ctx), handoff.ID); reopenErr != nil {
			e.logger.Error("reopen handoff after prompt failure", "handoff_id", handoff.ID, "error", reopenErr)
		}
		if replyErr := e.channel.Reply(ctx, reply.MessageID, "发送到 OpenCode Session 失败，请检查服务日志后重新发送。"); replyErr != nil {
			e.logger.Warn("report session resume failure in channel", "session_id", handoff.SessionID, "error", replyErr)
		}
		return fmt.Errorf("resume OpenCode session: %w", err)
	}
	e.logger.Info("session resumed", "session_id", handoff.SessionID, "sender_id", reply.SenderID)
	if err := e.channel.Reply(ctx, reply.MessageID, "已发送到 OpenCode Session，任务正在继续。"); err != nil {
		e.logger.Warn("confirm session resume in channel", "session_id", handoff.SessionID, "error", err)
	}
	return nil
}

func (e *Engine) isAllowed(ctx context.Context, reply domain.UserReply) (bool, error) {
	identifiers := append([]string{reply.SenderID}, reply.SenderIDs...)
	if len(e.allowed) > 0 {
		matched := false
		for _, identifier := range identifiers {
			if _, ok := e.allowed[identifier]; ok && identifier != "" {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}

	binding, err := e.store.GetChannelBinding(ctx)
	if err == nil {
		return binding.ChatID == reply.ChatID && identifiersOverlap(binding.UserIDs, identifiers), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if e.options.ChatID == "" || len(e.allowed) == 0 {
		return false, nil
	}
	return reply.ChatID == e.options.ChatID, nil
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

func projectName(session opencode.Session) string {
	directory := filepath.Clean(session.Directory)
	name := filepath.Base(directory)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = strings.TrimSpace(session.Title)
	}
	if name == "" {
		name = session.ID
	}
	return name
}

func sessionName(session opencode.Session) string {
	if name := strings.TrimSpace(session.Title); name != "" {
		return name
	}
	return "Untitled Session"
}

func truncateTail(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return "...\n" + string(runes[len(runes)-maxRunes:])
}

func newID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("hof_%d", time.Now().UnixNano())
	}
	return "hof_" + hex.EncodeToString(buffer)
}
