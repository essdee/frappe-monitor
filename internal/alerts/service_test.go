package alerts

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeRules_UserRuleOverridesBundledRuleByName(t *testing.T) {
	override := Rule{
		Name: "site_unhealthy", Expr: "custom_site_health == 0",
		Severity: "warning", FingerprintLabels: []string{"server", "site"},
	}
	custom := Rule{Name: "custom", Expr: "custom_metric > 1", Severity: "info"}
	rules := mergeRules([]Rule{override, custom}, false)

	byName := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byName[rule.Name] = rule
	}
	require.Equal(t, override, byName["site_unhealthy"])
	require.Equal(t, custom, byName["custom"])
	require.Len(t, rules, len(DefaultRules())+1)
}

func TestDefaultRules_AreValidAndUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for _, rule := range DefaultRules() {
		require.NoError(t, rule.Validate(), rule.Name)
		_, exists := seen[rule.Name]
		require.False(t, exists, "duplicate bundled rule %q", rule.Name)
		seen[rule.Name] = struct{}{}
	}
	for _, required := range []string{
		"cpu_saturated", "memory_pressure", "swap_pressure",
		"site_response_slow", "web_worker_shortage",
		"site_request_p95_slow", "site_http_5xx_rate_high", "site_job_wait_p95_high",
		"site_database_query_slow",
	} {
		_, ok := seen[required]
		require.True(t, ok, "missing bundled rule %q", required)
	}
}

func TestConfigValidate_RejectsDuplicateChatsAndRules(t *testing.T) {
	base := Config{
		Enabled: true, EvaluationIntervalSeconds: 60, NotifyRepeatSeconds: 0,
		VMQueryTimeoutSeconds: 5,
		Telegram:              TelegramConfig{BotToken: "123456789:abcdefghijklmnopqrstuvwxyz_123456", ChatIDs: []string{"1"}, SendTimeoutSeconds: 5},
	}

	dupChat := base
	dupChat.Telegram.ChatIDs = []string{"1", "1"}
	require.ErrorContains(t, dupChat.Validate(), "duplicate")

	dupRule := base
	dupRule.Rules = []Rule{{Name: "x", Expr: "up"}, {Name: "x", Expr: "down"}}
	require.ErrorContains(t, dupRule.Validate(), "duplicate")
}

func TestConfigValidate_RejectsMalformedTelegramCredentials(t *testing.T) {
	base := Config{
		Enabled: true, EvaluationIntervalSeconds: 60, NotifyRepeatSeconds: 0,
		VMQueryTimeoutSeconds: 5,
		Telegram: TelegramConfig{
			BotToken: "123456789:abcdefghijklmnopqrstuvwxyz_123456",
			ChatIDs:  []string{"-1001234567890"}, SendTimeoutSeconds: 5,
		},
	}
	require.NoError(t, base.Validate())

	badToken := base
	badToken.Telegram.BotToken = "dasda"
	require.ErrorContains(t, badToken.Validate(), "bot-token format")

	badChat := base
	badChat.Telegram.ChatIDs = []string{"qweq"}
	require.ErrorContains(t, badChat.Validate(), "numeric Telegram chat ID")
}
