package homeassistant

import (
	"context"
	"encoding/json"
	"github.com/example/frigate-notifier/internal/config"
	"github.com/gorilla/websocket"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	cfg   config.HomeAssistant
	cache *Cache
	log   *slog.Logger
}

func New(c config.HomeAssistant, cache *Cache, l *slog.Logger) *Client { return &Client{c, cache, l} }
func (c *Client) Run(ctx context.Context) {
	for ctx.Err() == nil {
		c.connect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.ReconnectDelay):
		}
	}
}
func (c *Client) connect(ctx context.Context) {
	u, e := url.Parse(c.cfg.URL)
	if e != nil {
		return
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/websocket"
	d := websocket.Dialer{HandshakeTimeout: c.cfg.ConnectTimeout}
	ws, _, e := d.DialContext(ctx, u.String(), http.Header{})
	if e != nil {
		c.log.Warn("home assistant connect failed", "error", e)
		return
	}
	defer ws.Close()
	// A websocket read is not context-aware. Closing this connection on shutdown
	// unblocks the sole read owner and prevents a reconnect goroutine leak.
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
	if err := ws.ReadJSON(&msg); err != nil || string(msg["type"]) != `"auth_required"` {
		c.log.Warn("home assistant protocol error before authentication")
		return
	}
	if err := ws.WriteJSON(map[string]any{"type": "auth", "access_token": c.cfg.Token}); err != nil {
		return
	}
	if err := ws.ReadJSON(&msg); err != nil || string(msg["type"]) != `"auth_ok"` {
		c.log.Warn("home assistant authentication failed")
		return
	}
	if err := ws.WriteJSON(map[string]any{"id": 1, "type": "get_states"}); err != nil {
		return
	}
	if err := ws.WriteJSON(map[string]any{"id": 2, "type": "subscribe_events", "event_type": "state_changed"}); err != nil {
		return
	}
	for {
		var x struct {
			Type    string `json:"type"`
			ID      int    `json:"id"`
			Success bool   `json:"success"`
			Result  []struct {
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
		if ws.ReadJSON(&x) != nil {
			return
		}
		if x.ID == 1 {
			if !x.Success {
				c.log.Warn("home assistant get_states failed")
				return
			}
			for _, s := range x.Result {
				if s.EntityID == c.cfg.AlarmEntityID {
					c.cache.Set(s.State)
				}
			}
		}
		if x.ID == 2 && !x.Success {
			c.log.Warn("home assistant state subscription failed")
			return
		}
		if x.Type == "event" && x.Event.Data.EntityID == c.cfg.AlarmEntityID {
			c.cache.Set(x.Event.Data.NewState.State)
		}
	}
}
