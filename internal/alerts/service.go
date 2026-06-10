package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/storage"
)

// Config is the operator-facing settings for the alerts subsystem.
// Mirrors cfg.alerts.* in monitor.yaml.
type Config struct {
	// Enabled gates the whole subsystem. When false, no rules
	// evaluate, no notifications go out, no goroutines run. Default
	// off — operators must opt in.
	Enabled bool `koanf:"enabled"`

	// EvaluationIntervalSeconds is how often the evaluator ticks.
	// Default 60. Floor is 15s — anything lower is a tight loop on
	// VM that doesn't help.
	EvaluationIntervalSeconds int `koanf:"evaluation_interval_seconds"`

	// NotifyRepeatSeconds is the cooldown before re-paging the same
	// (rule, fingerprint) firing alert. Default 3600 (1h). Set to 0
	// to suppress repeats entirely (notify only on the first fire and
	// on the eventual resolution).
	NotifyRepeatSeconds int `koanf:"notify_repeat_seconds"`

	// VMQueryTimeoutSeconds is the per-rule eval ctx timeout. Default
	// 10. Far above P99 of a healthy VM instant query.
	VMQueryTimeoutSeconds int `koanf:"vm_query_timeout_seconds"`

	// Telegram fan-out config.
	Telegram TelegramConfig `koanf:"telegram"`

	// Rules supplied by the operator. Combined with DefaultRules()
	// unless DisableDefaults is true.
	Rules []Rule `koanf:"rules"`

	// DisableDefaults skips DefaultRules(). Use when an operator
	// wants total control over what fires.
	DisableDefaults bool `koanf:"disable_defaults"`
}

type TelegramConfig struct {
	BotToken           string   `koanf:"bot_token"`
	ChatIDs            []string `koanf:"chat_ids"`
	SendTimeoutSeconds int      `koanf:"send_timeout_seconds"`
}

// Validate ensures the config is sound when alerts are enabled. When
// disabled, all checks are skipped — defaults are fine.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.EvaluationIntervalSeconds < 15 {
		return fmt.Errorf("alerts.evaluation_interval_seconds must be >= 15, got %d",
			c.EvaluationIntervalSeconds)
	}
	if c.VMQueryTimeoutSeconds < 1 {
		return fmt.Errorf("alerts.vm_query_timeout_seconds must be >= 1, got %d",
			c.VMQueryTimeoutSeconds)
	}
	if strings.TrimSpace(c.Telegram.BotToken) == "" {
		return fmt.Errorf("alerts.telegram.bot_token is required when alerts.enabled is true")
	}
	if len(c.Telegram.ChatIDs) == 0 {
		return fmt.Errorf("alerts.telegram.chat_ids must contain at least one entry when alerts.enabled is true")
	}
	if c.Telegram.SendTimeoutSeconds < 1 {
		return fmt.Errorf("alerts.telegram.send_timeout_seconds must be >= 1, got %d",
			c.Telegram.SendTimeoutSeconds)
	}
	for i, r := range c.Rules {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("alerts.rules[%d]: %w", i, err)
		}
	}
	return nil
}

// Service owns the evaluator goroutine. Start kicks it off; Stop
// returns once the in-flight evaluation has finished. Idempotent.
type Service struct {
	evaluator *Evaluator
	interval  time.Duration

	logger   *slog.Logger
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New builds a Service from config + dependencies. Returns nil + nil
// when alerts are disabled — caller treats nil Service as "no-op".
//
// extra notifiers are appended to the Telegram fan-out — used to push
// fire/resolve events to the real-time dashboard alongside Telegram.
func New(cfg Config, vmBaseURL string, store storage.Store, logger *slog.Logger, extra ...Notifier) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, nil
	}

	rules := append([]Rule{}, cfg.Rules...)
	if !cfg.DisableDefaults {
		rules = append(DefaultRules(), rules...)
	}

	vm := NewHTTPVMClient(vmBaseURL, time.Duration(cfg.VMQueryTimeoutSeconds)*time.Second)

	notifiers := make(MultiNotifier, 0, len(cfg.Telegram.ChatIDs)+len(extra))
	for _, id := range cfg.Telegram.ChatIDs {
		notifiers = append(notifiers, NewTelegramClient(
			cfg.Telegram.BotToken, id,
			time.Duration(cfg.Telegram.SendTimeoutSeconds)*time.Second,
		))
	}
	notifiers = append(notifiers, extra...)

	ev := &Evaluator{
		Rules:            rules,
		VM:               vm,
		Store:            store,
		Notifier:         notifiers,
		NotifyRepeatTime: time.Duration(cfg.NotifyRepeatSeconds) * time.Second,
		Logger:           logger,
	}

	return &Service{
		evaluator: ev,
		interval:  time.Duration(cfg.EvaluationIntervalSeconds) * time.Second,
		logger:    logger,
		stop:      make(chan struct{}),
	}, nil
}

// Start launches the evaluation loop. Non-blocking. Safe to call
// multiple times only by the same goroutine; concurrent calls would
// panic on the closed `stop` channel.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Tick once at startup so a freshly-deployed monitor doesn't
		// wait a whole interval before its first evaluation.
		s.evaluator.EvaluateOnce(ctx)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.logger.Info("alerts: stopping (ctx canceled)")
				return
			case <-s.stop:
				s.logger.Info("alerts: stopping (Stop called)")
				return
			case <-t.C:
				s.evaluator.EvaluateOnce(ctx)
			}
		}
	}()
}

// Stop signals the loop to exit and waits for it. Idempotent — the
// close is guarded so repeated calls don't panic on a closed channel.
func (s *Service) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// Rules returns a copy of the rules the evaluator runs. Used by the
// API to render the "configured rules" list on the Alerts page.
func (s *Service) Rules() []Rule {
	out := make([]Rule, len(s.evaluator.Rules))
	copy(out, s.evaluator.Rules)
	return out
}
