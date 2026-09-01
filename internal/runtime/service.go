package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Hans2573/OpenCode-Handoff/internal/channel/feishu"
	"github.com/Hans2573/OpenCode-Handoff/internal/config"
	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/handoff"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

// Service owns the long-running Handoff engine. Both the headless CLI and the
// desktop application use this type so there is only one lifecycle to keep
// correct.
type Service struct {
	engine  *handoff.Engine
	watcher *handoff.Watcher
	channel *feishu.Client

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan error
	running bool
}

// New wires the existing OpenCode and Feishu adapters around a shared store.
// adapter may be supplied by the desktop application to apply project routing;
// the CLI passes nil and keeps its historical behaviour.
func New(ctx context.Context, cfg config.Config, handoffStore store.Store, logger *slog.Logger, adapter opencode.Adapter) (*Service, error) {
	if adapter == nil {
		client, err := opencode.NewClient(opencode.ClientOptions{
			BaseURL:   cfg.OpenCode.BaseURL,
			Directory: cfg.OpenCode.Directory,
			Username:  cfg.OpenCode.Username,
			Password:  cfg.OpenCode.Password,
		})
		if err != nil {
			return nil, fmt.Errorf("create OpenCode client: %w", err)
		}
		adapter = client
	}

	pairingCode, err := PreparePairing(ctx, cfg, handoffStore, logger)
	if err != nil {
		return nil, err
	}
	channelClient := feishu.New(feishu.Options{
		AppID:          cfg.Feishu.AppID,
		AppSecret:      cfg.Feishu.AppSecret,
		ChatID:         cfg.Feishu.ChatID,
		PairingCode:    pairingCode,
		AllowedUsers:   cfg.Security.AllowedUsers,
		BindingStore:   handoffStore,
		MaxOutputChars: cfg.Handoff.MaxOutputChars,
	}, logger)

	watcher := handoff.NewWatcher(adapter, handoff.WatcherOptions{
		SSE:               cfg.Watcher.SSE,
		PollingFallback:   cfg.Watcher.PollingFallback,
		PollingInterval:   cfg.Watcher.PollingInterval.Duration,
		NotifyQuestions:   cfg.Handoff.NotifyQuestion,
		NotifyPermissions: cfg.Handoff.NotifyPermission,
	}, logger)
	engine := handoff.NewEngine(adapter, channelClient, handoffStore, handoff.EngineOptions{
		MaxOutputChars:   cfg.Handoff.MaxOutputChars,
		NotifyIdle:       cfg.Handoff.NotifyIdle,
		NotifyError:      cfg.Handoff.NotifyError,
		NotifyQuestion:   cfg.Handoff.NotifyQuestion,
		NotifyPermission: cfg.Handoff.NotifyPermission,
		AllowedUsers:     cfg.Security.AllowedUsers,
		ChatID:           cfg.Feishu.ChatID,
	}, logger)
	return &Service{engine: engine, watcher: watcher, channel: channelClient}, nil
}

func (s *Service) Start(parent context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("handoff service is already running")
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan error, 1)
	s.running = true
	go func() {
		err := s.engine.Run(ctx, s.watcher.Run(ctx))
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.done <- err
		close(s.done)
	}()
	return nil
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	return <-s.Done()
}

func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) Done() <-chan error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Service) ChannelHealth() feishu.Health {
	if s.channel == nil {
		return feishu.Health{State: "stopped", Message: "飞书监听未启动"}
	}
	return s.channel.Health()
}

// SendHandoff lets the desktop's Goal Loop supervisor use the same connected
// Feishu channel for infrastructure failures that do not originate as an
// OpenCode SSE event.
func (s *Service) SendHandoff(ctx context.Context, item domain.Handoff) (domain.MessageRef, error) {
	if s.channel == nil {
		return domain.MessageRef{}, errors.New("feishu channel is not configured")
	}
	return s.channel.SendHandoff(ctx, item)
}

func (s *Service) ReplyToHandoff(ctx context.Context, messageID, text string) error {
	if s.channel == nil {
		return errors.New("feishu channel is not configured")
	}
	return s.channel.Reply(ctx, messageID, text)
}

func (s *Service) EnsureQuestion(ctx context.Context, directory string, question opencode.QuestionRequest) error {
	if s.engine == nil {
		return errors.New("handoff engine is not configured")
	}
	return s.engine.EnsureQuestion(ctx, directory, question)
}

func (s *Service) EnsurePermission(ctx context.Context, directory string, permission opencode.PermissionRequest) error {
	if s.engine == nil {
		return errors.New("handoff engine is not configured")
	}
	return s.engine.EnsurePermission(ctx, directory, permission)
}

func PreparePairing(ctx context.Context, cfg config.Config, handoffStore store.Store, logger *slog.Logger) (string, error) {
	binding, err := handoffStore.GetChannelBinding(ctx)
	if err == nil {
		if cfg.Feishu.ChatID != "" && cfg.Feishu.ChatID != binding.ChatID {
			return "", fmt.Errorf("configured feishu.chat_id %s differs from stored binding %s", cfg.Feishu.ChatID, binding.ChatID)
		}
		logger.Info("using stored Feishu pairing", "chat_id", binding.ChatID)
		return "", nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if cfg.Feishu.ChatID != "" && len(cfg.Security.AllowedUsers) > 0 {
		return "", nil
	}
	code, err := newPairingCode()
	if err != nil {
		return "", err
	}
	logger.Info("Feishu is not paired; send this command to the bot", "command", "/bind "+code)
	return code, nil
}

func newPairingCode() (string, error) {
	buffer := make([]byte, 5)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate pairing code: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(buffer)), nil
}
