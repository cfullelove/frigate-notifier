package homeassistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/logging"
	"github.com/gorilla/websocket"
)

type Client struct {
	cfg   config.HomeAssistant
	cache *Cache
	log   *slog.Logger
}

func New(c config.HomeAssistant, cache *Cache, l *slog.Logger) *Client {
	return &Client{c, cache, logging.Default(l)}
}

func (c *Client) Run(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		attempt++
		c.log.Info("home assistant connecting", "component", "home_assistant", "attempt", attempt)
		c.connect(ctx, attempt)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.ReconnectDelay):
		}
	}
}

func (c *Client) connect(ctx context.Context, attempt int) {
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		c.log.Error("home assistant URL invalid", "component", "home_assistant", "attempt", attempt, "error", "invalid URL")
		return
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/websocket"
	d := websocket.Dialer{HandshakeTimeout: c.cfg.ConnectTimeout}
	ws, _, err := d.DialContext(ctx, u.String(), http.Header{})
	if err != nil {
		c.log.Warn("home assistant connect failed", "component", "home_assistant", "attempt", attempt, "error", "websocket dial failed")
		return
	}
	c.log.Info("home assistant connected", "component", "home_assistant", "attempt", attempt)
	defer func() {
		_ = ws.Close()
		c.log.Info("home assistant disconnected", "component", "home_assistant", "attempt", attempt)
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-done:
		}
	}()
	var msg map[string]json.RawMessage
	if err := ws.ReadJSON(&msg); err != nil {
		c.log.Warn("home assistant websocket read failed", "component", "home_assistant", "attempt", attempt, "operation", "auth_required", "error", "read failed")
		return
	}
	if string(msg["type"]) != `"auth_required"` {
		c.log.Warn("home assistant protocol error", "component", "home_assistant", "attempt", attempt, "operation", "auth_required", "error", "unexpected message")
		return
	}
	if err := ws.WriteJSON(map[string]any{"type": "auth", "access_token": c.cfg.Token}); err != nil {
		c.log.Warn("home assistant websocket write failed", "component", "home_assistant", "attempt", attempt, "operation", "auth", "error", "write failed")
		return
	}
	if err := ws.ReadJSON(&msg); err != nil {
		c.log.Warn("home assistant websocket read failed", "component", "home_assistant", "attempt", attempt, "operation", "auth_result", "error", "read failed")
		return
	}
	if string(msg["type"]) != `"auth_ok"` {
		c.log.Warn("home assistant authentication failed", "component", "home_assistant", "attempt", attempt, "error", "authentication rejected")
		return
	}
	c.log.Info("home assistant authenticated", "component", "home_assistant", "attempt", attempt)
	type message struct {
		Type    string `json:"type"`
		ID      int    `json:"id"`
		Success bool   `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result []struct {
			EntityID string `json:"entity_id"`
			State    string `json:"state"`
		} `json:"result"`
		Event struct {
			Data struct {
				EntityID string `json:"entity_id"`
				NewState struct {
					State string `json:"state"`
				} `json:"new_state"`
			} `json:"data"`
		} `json:"event"`
	}
	type readResult struct {
		message message
		err     error
	}
	getStates := func(id int) error {
		return ws.WriteJSON(map[string]any{"id": id, "type": "get_states"})
	}
	if err := getStates(1); err != nil {
		c.log.Warn("home assistant websocket write failed", "component", "home_assistant", "attempt", attempt, "operation", "get_states", "error", "write failed")
		return
	}
	if err := ws.WriteJSON(map[string]any{"id": 2, "type": "subscribe_events", "event_type": "state_changed"}); err != nil {
		c.log.Warn("home assistant websocket write failed", "component", "home_assistant", "attempt", attempt, "operation", "subscribe_events", "error", "write failed")
		return
	}
	messages := make(chan readResult, 1)
	go func() {
		for {
			var x message
			if err := ws.ReadJSON(&x); err != nil {
				select {
				case messages <- readResult{err: err}:
				case <-done:
				}
				return
			}
			select {
			case messages <- readResult{message: x}:
			case <-done:
				return
			}
		}
	}()
	ticker := time.NewTicker(c.cfg.StateRefreshInterval)
	defer ticker.Stop()
	nextID := 3
	refreshPending := false
	for {
		select {
		case result := <-messages:
			if result.err != nil {
				c.log.Warn("home assistant websocket read failed", "component", "home_assistant", "attempt", attempt, "operation", "events", "error", "read failed")
				return
			}
			x := result.message
			if x.Type == "result" && (x.ID == 1 || refreshPending && x.ID == nextID-1) {
				refreshPending = false
				if !x.Success {
					c.log.Warn("home assistant get_states failed", "component", "home_assistant", "attempt", attempt, "error_code", x.Error.Code, "error_message", x.Error.Message)
					return
				}
				found := false
				for _, s := range x.Result {
					if s.EntityID == c.cfg.AlarmEntityID {
						found = true
						c.cache.Set(s.State)
						if x.ID == 1 {
							c.log.Info("home assistant initial alarm state loaded", "component", "home_assistant", "alarm_state", s.State)
						} else {
							c.log.Debug("home assistant alarm state refreshed", "component", "home_assistant", "alarm_state", s.State)
						}
					}
				}
				if !found {
					c.cache.Set("")
				}
			}
			if x.Type == "result" && x.ID == 2 {
				if !x.Success {
					c.log.Warn("home assistant state subscription failed", "component", "home_assistant", "attempt", attempt, "error_code", x.Error.Code, "error_message", x.Error.Message)
					return
				}
				c.log.Info("home assistant state subscription active", "component", "home_assistant", "attempt", attempt)
			}
			if x.Type == "event" && x.Event.Data.EntityID == c.cfg.AlarmEntityID {
				c.cache.Set(x.Event.Data.NewState.State)
				c.log.Info("home assistant alarm state changed", "component", "home_assistant", "alarm_state", x.Event.Data.NewState.State)
			}
		case <-ticker.C:
			if refreshPending {
				continue
			}
			if err := getStates(nextID); err != nil {
				c.log.Warn("home assistant websocket write failed", "component", "home_assistant", "attempt", attempt, "operation", "refresh_get_states", "error", "write failed")
				return
			}
			refreshPending = true
			nextID++
		}
	}
}
