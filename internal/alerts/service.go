package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
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

	// QueryLookback is derived from the scheduler interval by main. It is
	// not a separate YAML knob: one cadence must define both collection and
	// query freshness semantics.
	QueryLookback time.Duration

	// Timezone controls presentation in outbound notifications only. Alert
	// evaluation and incident storage remain UTC.
	Timezone string
}

type TelegramConfig struct {
	BotToken           string   `koanf:"bot_token"`
	ChatIDs            []string `koanf:"chat_ids"`
	SendTimeoutSeconds int      `koanf:"send_timeout_seconds"`
}

var (
	telegramBotTokenPattern = regexp.MustCompile(`^[0-9]{6,}:[A-Za-z0-9_-]{20,}$`)
	telegramChatIDPattern   = regexp.MustCompile(`^-?[0-9]+$`)
)

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
	if c.NotifyRepeatSeconds < 0 {
		return fmt.Errorf("alerts.notify_repeat_seconds must be >= 0, got %d", c.NotifyRepeatSeconds)
	}
	botToken := strings.TrimSpace(c.Telegram.BotToken)
	if botToken == "" {
		return fmt.Errorf("alerts.telegram.bot_token is required when alerts.enabled is true")
	}
	if !telegramBotTokenPattern.MatchString(botToken) {
		return fmt.Errorf("alerts.telegram.bot_token has an invalid Telegram bot-token format")
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
	seenNames := make(map[string]struct{}, len(c.Rules))
	for _, rule := range c.Rules {
		if _, exists := seenNames[rule.Name]; exists {
			return fmt.Errorf("duplicate user alert rule name %q", rule.Name)
		}
		seenNames[rule.Name] = struct{}{}
	}
	seenChats := make(map[string]struct{}, len(c.Telegram.ChatIDs))
	for i, chatID := range c.Telegram.ChatIDs {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			return fmt.Errorf("alerts.telegram.chat_ids[%d] is empty", i)
		}
		if !telegramChatIDPattern.MatchString(chatID) {
			return fmt.Errorf("alerts.telegram.chat_ids[%d] must be a numeric Telegram chat ID, got %q", i, chatID)
		}
		if _, exists := seenChats[chatID]; exists {
			return fmt.Errorf("alerts.telegram.chat_ids contains duplicate %q", chatID)
		}
		seenChats[chatID] = struct{}{}
	}
	return nil
}

// Service owns the evaluator goroutine. Start kicks it off; Stop
// returns once the in-flight evaluation has finished. Idempotent.
type Service struct {
	evaluator *Evaluator
	interval  time.Duration

	logger    *slog.Logger
	stop      chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

// New builds a Service from config + dependencies. Returns nil + nil
// when alerts are disabled — caller treats nil Service as "no-op".
func New(cfg Config, vmBaseURL string, store storage.Store, logger *slog.Logger) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, nil
	}

	rules := EffectiveRules(cfg)
	seenRules := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if err := rule.Validate(); err != nil {
			return nil, err
		}
		if _, exists := seenRules[rule.Name]; exists {
			return nil, fmt.Errorf("duplicate alert rule name %q", rule.Name)
		}
		seenRules[rule.Name] = struct{}{}
	}

	vm := NewHTTPVMClient(vmBaseURL, time.Duration(cfg.VMQueryTimeoutSeconds)*time.Second)
	if cfg.QueryLookback > 0 {
		vm.Lookback = cfg.QueryLookback
	}

	notifiers := make([]Notifier, 0, len(cfg.Telegram.ChatIDs))
	location := time.UTC
	if cfg.Timezone != "" {
		var err error
		location, err = time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("alerts timezone %q: %w", cfg.Timezone, err)
		}
	}
	for _, id := range cfg.Telegram.ChatIDs {
		client := NewTelegramClient(
			cfg.Telegram.BotToken, id,
			time.Duration(cfg.Telegram.SendTimeoutSeconds)*time.Second,
		)
		client.Location = location
		notifiers = append(notifiers, client)
	}

	ev := &Evaluator{
		Rules:            rules,
		VM:               vm,
		Store:            store,
		Notifier:         NewMultiNotifier(notifiers...),
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

// EffectiveRules returns the rule set an enabled evaluator would use. The API
// uses it while notifications are disabled so operators can still review the
// bundled and configured detection policy before enabling delivery.
func EffectiveRules(cfg Config) []Rule {
	return mergeRules(cfg.Rules, cfg.DisableDefaults)
}

// Start launches the evaluation loop. Non-blocking and idempotent; startOnce
// makes concurrent calls safe.
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
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
	})
}

// Stop signals the loop to exit and waits for it. Safe to call once.
func (s *Service) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// mergeRules lets an operator replace a bundled rule by using the same name,
// while preserving deterministic default order and appending new rules.
func mergeRules(user []Rule, disableDefaults bool) []Rule {
	if disableDefaults {
		return append([]Rule(nil), user...)
	}
	overrides := make(map[string]Rule, len(user))
	for _, rule := range user {
		overrides[rule.Name] = rule
	}
	defaults := DefaultRules()
	out := make([]Rule, 0, len(defaults)+len(user))
	for _, rule := range defaults {
		if override, ok := overrides[rule.Name]; ok {
			out = append(out, override)
			delete(overrides, rule.Name)
		} else {
			out = append(out, rule)
		}
	}
	for _, rule := range user {
		if _, ok := overrides[rule.Name]; ok {
			out = append(out, rule)
			delete(overrides, rule.Name)
		}
	}
	return out
}

// Rules returns a copy of the rules the evaluator runs. Used by the
// API to render the "configured rules" list on the Alerts page.
func (s *Service) Rules() []Rule {
	out := make([]Rule, len(s.evaluator.Rules))
	copy(out, s.evaluator.Rules)
	return out
}
