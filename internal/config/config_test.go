package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAllowsUnsetOptionalTokenAndUsesRetryDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yml")
	s := `mqtt: {broker: tcp://x, client_id: x, qos: 1, connect_timeout: 1s, reconnect_delay: 1s}
home_assistant: {url: http://x, token: token, alarm_entity_id: x, connect_timeout: 1s, reconnect_delay: 1s, state_max_age: 1m}
frigate: {base_url: http://x, token: "${OPTIONAL_FRIGATE_TOKEN}", request_timeout: 1s}
gemini: {api_key: key, model: x, prompt: x, timeout: 1s}
telegram: {bot_token: token, chat_id: x, timeout: 1s}
filter: {default_action: deny}
processing: {workers: 1, queue_size: 1, event_ttl: 1h, shutdown_timeout: 1s}`
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTT.Topic != "frigate_custom_reviews/reviews" || c.Frigate.Clip.Timeout <= 0 || c.Frigate.Snapshot.RetryDelay <= 0 || c.HomeAssistant.StateRefreshInterval != 30*time.Second {
		t.Fatalf("defaults were not applied: %#v", c)
	}
}

func TestValidateLoggingLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		c := Config{Logging: Logging{Level: level}}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "missing required") {
			t.Fatalf("level %q did not pass logging validation: %v", level, err)
		}
	}
	c := Config{Logging: Logging{Level: "verbose"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "logging.level") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestLoadExpandsEnvironment(t *testing.T) {
	t.Setenv("TOKEN", "secret-value")
	p := filepath.Join(t.TempDir(), "c.yml")
	s := `mqtt: {broker: tcp://x, client_id: x, topic: x, qos: 1, connect_timeout: 1s, reconnect_delay: 1s}
home_assistant: {url: http://x, token: "${TOKEN}", alarm_entity_id: x, connect_timeout: 1s, reconnect_delay: 1s, state_max_age: 1m}
frigate: {base_url: http://x, request_timeout: 1s}
gemini: {api_key: "${TOKEN}", model: x, prompt: x, timeout: 1s}
telegram: {bot_token: "${TOKEN}", chat_id: x, timeout: 1s}
filter: {default_action: deny}
processing: {workers: 1, queue_size: 1, event_ttl: 1h, shutdown_timeout: 1s}`
	os.WriteFile(p, []byte(s), 0600)
	c, e := Load(p)
	if e != nil || c.Gemini.APIKey != "secret-value" {
		t.Fatal(e)
	}
	os.WriteFile(p, []byte(strings.Replace(s, "${TOKEN}", "${MISSING}", 1)), 0600)
	_, e = Load(p)
	if e == nil || strings.Contains(e.Error(), "secret-value") {
		t.Fatal(e)
	}
}
