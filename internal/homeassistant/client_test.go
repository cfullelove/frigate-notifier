package homeassistant

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/frigate-notifier/internal/config"
	"github.com/gorilla/websocket"
)

func TestClientRefreshesAlarmState(t *testing.T) {
	upgrader := websocket.Upgrader{}
	refreshes := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if err := ws.WriteJSON(map[string]any{"type": "auth_required"}); err != nil {
			return
		}
		var auth map[string]any
		if err := ws.ReadJSON(&auth); err != nil {
			return
		}
		if err := ws.WriteJSON(map[string]any{"type": "auth_ok"}); err != nil {
			return
		}
		for i := 0; i < 2; i++ {
			var request map[string]any
			if err := ws.ReadJSON(&request); err != nil {
				return
			}
			switch request["type"] {
			case "get_states":
				if err := ws.WriteJSON(map[string]any{"type": "result", "id": request["id"], "success": true, "result": []map[string]string{{"entity_id": "alarm_control_panel.home", "state": "armed_home"}}}); err != nil {
					return
				}
			case "subscribe_events":
				if err := ws.WriteJSON(map[string]any{"type": "result", "id": request["id"], "success": true}); err != nil {
					return
				}
			}
		}
		var refresh map[string]any
		if err := ws.ReadJSON(&refresh); err != nil {
			return
		}
		refreshes <- refresh
		_ = ws.WriteJSON(map[string]any{"type": "result", "id": refresh["id"], "success": true, "result": []map[string]string{{"entity_id": "alarm_control_panel.home", "state": "armed_away"}}})
		<-r.Context().Done()
	}))
	defer server.Close()

	cache := NewCache(time.Second)
	client := New(config.HomeAssistant{
		URL:                  server.URL,
		Token:                "token",
		AlarmEntityID:        "alarm_control_panel.home",
		ConnectTimeout:       time.Second,
		ReconnectDelay:       time.Second,
		StateMaxAge:          time.Second,
		StateRefreshInterval: 10 * time.Millisecond,
	}, cache, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	select {
	case refresh := <-refreshes:
		if refresh["type"] != "get_states" || refresh["id"] != float64(3) {
			t.Fatalf("unexpected refresh request: %#v", refresh)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive periodic state refresh")
	}

	deadline := time.After(time.Second)
	for cache.Current().State != "armed_away" {
		select {
		case <-deadline:
			t.Fatalf("refreshed state was not cached: %#v", cache.Current())
		case <-time.After(time.Millisecond):
		}
	}
}
