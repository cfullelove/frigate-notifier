package config

import (
	"fmt"
	"github.com/example/frigate-notifier/internal/logging"
	"gopkg.in/yaml.v3"
	"os"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	MQTT          MQTT          `yaml:"mqtt"`
	HomeAssistant HomeAssistant `yaml:"home_assistant"`
	Frigate       Frigate       `yaml:"frigate"`
	Gemini        Gemini        `yaml:"gemini"`
	Telegram      Telegram      `yaml:"telegram"`
	Filter        Filter        `yaml:"filter"`
	Processing    Processing    `yaml:"processing"`
	Logging       Logging       `yaml:"logging"`
}
type MQTT struct {
	Broker         string        `yaml:"broker"`
	ClientID       string        `yaml:"client_id"`
	Username       string        `yaml:"username"`
	Password       string        `yaml:"password"`
	Topic          string        `yaml:"topic"`
	QoS            byte          `yaml:"qos"`
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
	ReconnectDelay time.Duration `yaml:"reconnect_delay"`
}
type HomeAssistant struct {
	URL                  string        `yaml:"url"`
	Token                string        `yaml:"token"`
	AlarmEntityID        string        `yaml:"alarm_entity_id"`
	ConnectTimeout       time.Duration `yaml:"connect_timeout"`
	ReconnectDelay       time.Duration `yaml:"reconnect_delay"`
	StateMaxAge          time.Duration `yaml:"state_max_age"`
	StateRefreshInterval time.Duration `yaml:"state_refresh_interval"`
}
type Frigate struct {
	BaseURL        string        `yaml:"base_url"`
	Token          string        `yaml:"token"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	Snapshot       Retry         `yaml:"snapshot"`
	Clip           Clip          `yaml:"clip"`
}
type Retry struct {
	Retries    int           `yaml:"retries"`
	RetryDelay time.Duration `yaml:"retry_delay"`
	Quality    *int          `yaml:"quality"`
}
type Clip struct {
	Source     string        `yaml:"source"`
	Retries    int           `yaml:"retries"`
	RetryDelay time.Duration `yaml:"retry_delay"`
	Timeout    time.Duration `yaml:"timeout"`
}
type Gemini struct {
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Prompt  string        `yaml:"prompt"`
	Timeout time.Duration `yaml:"timeout"`
}
type Telegram struct {
	BotToken string        `yaml:"bot_token"`
	ChatID   string        `yaml:"chat_id"`
	Timeout  time.Duration `yaml:"timeout"`
}
type Filter struct {
	DefaultAction string `yaml:"default_action"`
	Rules         []Rule `yaml:"rules"`
}
type Rule struct {
	Name         string   `yaml:"name"`
	Action       string   `yaml:"action"`
	AlarmStates  []string `yaml:"alarm_states"`
	ProfileNames []string `yaml:"profile_names"`
}
type Processing struct {
	Workers         int           `yaml:"workers"`
	QueueSize       int           `yaml:"queue_size"`
	EventTTL        time.Duration `yaml:"event_ttl"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

var envRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	// Expand scalar nodes rather than the source text. Besides avoiding YAML syntax
	// injection from values such as passwords containing ':' or '#', this still
	// performs expansion before the node is decoded into Config.
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return Config{}, err
	}
	var expand func(*yaml.Node)
	expand = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			n.Value = envRE.ReplaceAllStringFunc(n.Value, func(match string) string {
				name := envRE.FindStringSubmatch(match)[1]
				value, ok := os.LookupEnv(name)
				if !ok {
					return ""
				}
				return value
			})
		}
		for _, child := range n.Content {
			expand(child)
		}
	}
	expand(&node)
	var c Config
	if err := node.Decode(&c); err != nil {
		return c, err
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}
func (c *Config) Validate() error {
	c.Logging.Level = strings.ToLower(strings.TrimSpace(c.Logging.Level))
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if _, ok := logging.Level(c.Logging.Level); !ok {
		return fmt.Errorf("logging.level must be debug, info, warn, or error")
	}
	c.Logging.Format = strings.ToLower(strings.TrimSpace(c.Logging.Format))
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Logging.Format != "json" && c.Logging.Format != "text" {
		return fmt.Errorf("logging.format must be json or text")
	}
	if c.MQTT.Topic == "" {
		c.MQTT.Topic = "frigate_custom_reviews/reviews"
	}
	// Retry fields are optional in YAML; retain documented safe defaults when
	// omitted. (A zero retry count remains a valid explicit choice.)
	if c.Frigate.Snapshot.RetryDelay == 0 {
		c.Frigate.Snapshot.RetryDelay = time.Second
	}
	if c.Frigate.Clip.RetryDelay == 0 {
		c.Frigate.Clip.RetryDelay = 2 * time.Second
	}
	if c.Frigate.Clip.Timeout == 0 {
		c.Frigate.Clip.Timeout = 2 * time.Minute
	}
	c.Frigate.Clip.Source = strings.ToLower(strings.TrimSpace(c.Frigate.Clip.Source))
	if c.Frigate.Clip.Source == "" {
		c.Frigate.Clip.Source = "clip"
	}
	if c.Frigate.Clip.Source != "clip" && c.Frigate.Clip.Source != "preview" {
		return fmt.Errorf("frigate.clip.source must be clip or preview")
	}
	if c.HomeAssistant.StateRefreshInterval == 0 && c.HomeAssistant.StateMaxAge > 0 {
		c.HomeAssistant.StateRefreshInterval = c.HomeAssistant.StateMaxAge / 2
	}
	c.Frigate.BaseURL = strings.TrimRight(c.Frigate.BaseURL, "/")
	c.HomeAssistant.URL = strings.TrimRight(c.HomeAssistant.URL, "/")
	req := func(v, n string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("missing required %s", n)
		}
		return nil
	}
	for _, x := range []struct{ v, n string }{{c.MQTT.Broker, "mqtt.broker"}, {c.MQTT.ClientID, "mqtt.client_id"}, {c.MQTT.Topic, "mqtt.topic"}, {c.HomeAssistant.URL, "home_assistant.url"}, {c.HomeAssistant.Token, "home_assistant.token"}, {c.HomeAssistant.AlarmEntityID, "home_assistant.alarm_entity_id"}, {c.Frigate.BaseURL, "frigate.base_url"}, {c.Gemini.APIKey, "gemini.api_key"}, {c.Gemini.Model, "gemini.model"}, {c.Gemini.Prompt, "gemini.prompt"}, {c.Telegram.BotToken, "telegram.bot_token"}, {c.Telegram.ChatID, "telegram.chat_id"}} {
		if e := req(x.v, x.n); e != nil {
			return e
		}
	}
	if c.MQTT.QoS > 2 {
		return fmt.Errorf("mqtt.qos must be 0..2")
	}
	for _, item := range []struct {
		name  string
		value time.Duration
	}{
		{"mqtt.connect_timeout", c.MQTT.ConnectTimeout}, {"mqtt.reconnect_delay", c.MQTT.ReconnectDelay},
		{"home_assistant.connect_timeout", c.HomeAssistant.ConnectTimeout}, {"home_assistant.reconnect_delay", c.HomeAssistant.ReconnectDelay}, {"home_assistant.state_max_age", c.HomeAssistant.StateMaxAge}, {"home_assistant.state_refresh_interval", c.HomeAssistant.StateRefreshInterval},
		{"frigate.request_timeout", c.Frigate.RequestTimeout}, {"frigate.snapshot.retry_delay", c.Frigate.Snapshot.RetryDelay}, {"frigate.clip.retry_delay", c.Frigate.Clip.RetryDelay}, {"frigate.clip.timeout", c.Frigate.Clip.Timeout},
		{"gemini.timeout", c.Gemini.Timeout}, {"telegram.timeout", c.Telegram.Timeout}, {"processing.event_ttl", c.Processing.EventTTL}, {"processing.shutdown_timeout", c.Processing.ShutdownTimeout},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be positive", item.name)
		}
	}
	if c.HomeAssistant.StateRefreshInterval >= c.HomeAssistant.StateMaxAge {
		return fmt.Errorf("home_assistant.state_refresh_interval must be less than home_assistant.state_max_age")
	}
	if c.Frigate.Snapshot.Retries < 0 || c.Frigate.Clip.Retries < 0 {
		return fmt.Errorf("frigate retries must not be negative")
	}
	if c.Processing.Workers <= 0 || c.Processing.QueueSize <= 0 {
		return fmt.Errorf("processing workers and queue_size must be positive")
	}
	if c.Frigate.Snapshot.Quality != nil && (*c.Frigate.Snapshot.Quality < 1 || *c.Frigate.Snapshot.Quality > 100) {
		return fmt.Errorf("snapshot quality must be 1..100")
	}
	if !action(c.Filter.DefaultAction) {
		return fmt.Errorf("invalid filter.default_action")
	}
	seen := map[string]bool{}
	for _, r := range c.Filter.Rules {
		if r.Name == "" || seen[r.Name] {
			return fmt.Errorf("filter rule names must be unique and non-empty")
		}
		seen[r.Name] = true
		if !action(r.Action) {
			return fmt.Errorf("invalid action for rule %q", r.Name)
		}
		if len(r.AlarmStates) == 0 && len(r.ProfileNames) == 0 {
			return fmt.Errorf("rule %q has no conditions", r.Name)
		}
	}
	return nil
}
func action(s string) bool { return s == "allow" || s == "deny" }
