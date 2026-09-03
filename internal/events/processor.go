package events

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/filter"
	"github.com/example/frigate-notifier/internal/frigate"
	"github.com/example/frigate-notifier/internal/gemini"
	"github.com/example/frigate-notifier/internal/homeassistant"
	"github.com/example/frigate-notifier/internal/logging"
	"github.com/example/frigate-notifier/internal/mqttclient"
	"github.com/example/frigate-notifier/internal/telegram"
)

type status int

const (
	processing status = iota
	ignored
	notified
	clipSending
	clipSent
	failed
)

type record struct {
	s       status
	data    *mqttclient.ReviewData
	pending *mqttclient.ReviewData
	msg     int64
	updated time.Time
}

type Processor struct {
	cfg   config.Config
	alarm homeassistant.Provider
	frig  interface {
		Snapshot(context.Context, string) ([]byte, string, error)
		Clip(context.Context, string, string, string) (frigate.LocalMedia, error)
	}
	ai      gemini.Analyser
	notify  telegram.Notifier
	log     *slog.Logger
	mu      sync.Mutex
	records map[string]*record
}

func New(c config.Config, a homeassistant.Provider, f interface {
	Snapshot(context.Context, string) ([]byte, string, error)
	Clip(context.Context, string, string, string) (frigate.LocalMedia, error)
}, g gemini.Analyser, n telegram.Notifier, l *slog.Logger) *Processor {
	return &Processor{cfg: c, alarm: a, frig: f, ai: g, notify: n, log: logging.Default(l), records: map[string]*record{}}
}

func attrs(d *mqttclient.ReviewData, messageType string) []any {
	values := []any{"component", "processor", "review_id", d.ID, "message_type", messageType, "profile_name", d.ProfileName}
	if len(d.LinkedEvents) > 0 {
		values = append(values, "linked_event_id", d.LinkedEvents[0].ID)
	}
	return values
}

func safeDescription(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:509]) + "..."
	}
	return value
}

func (p *Processor) Handle(ctx context.Context, m mqttclient.ReviewMessage) {
	if err := m.Validate(); err != nil {
		p.log.Warn("review message ignored", "component", "processor", "message_type", m.Type, "error", err.Error())
		return
	}
	if m.Type == "new" {
		p.new(ctx, m.After)
		return
	}
	p.end(ctx, m.After)
}

func copyData(d *mqttclient.ReviewData) *mqttclient.ReviewData {
	v := *d
	v.LinkedEvents = append([]mqttclient.LinkedEvent(nil), d.LinkedEvents...)
	v.Cameras = append([]string(nil), d.Cameras...)
	return &v
}

func (p *Processor) new(ctx context.Context, d *mqttclient.ReviewData) {
	d = copyData(d)
	fields := attrs(d, "new")
	p.mu.Lock()
	if _, exists := p.records[d.ID]; exists {
		p.mu.Unlock()
		p.log.Info("duplicate new review ignored", fields...)
		return
	}
	p.records[d.ID] = &record{s: processing, data: d, updated: time.Now()}
	p.mu.Unlock()
	p.log.Info("new review processing", fields...)
	a := p.alarm.Current()
	if !a.Available {
		p.log.Warn("alarm state unavailable; review denied", append(fields, "alarm_state", a.State)...)
		p.complete(ctx, d.ID, ignored, 0)
		return
	}
	decision := filter.Evaluate(p.cfg.Filter, a.State, d.ProfileName)
	p.log.Info("filter decision", append(fields, "alarm_state", a.State, "filter_rule", decision.Rule, "filter_action", map[bool]string{true: "allow", false: "deny"}[decision.Allow])...)
	if !decision.Allow {
		p.complete(ctx, d.ID, ignored, 0)
		return
	}
	started := time.Now()
	image, mime, err := p.frig.Snapshot(ctx, d.LinkedEvents[0].ID)
	if err != nil {
		p.log.Error("snapshot fetch failed", append(fields, "error", err.Error(), "duration_ms", time.Since(started).Milliseconds())...)
		p.complete(ctx, d.ID, failed, 0)
		return
	}
	p.log.Info("snapshot fetched", append(fields, "mime_type", mime, "duration_ms", time.Since(started).Milliseconds())...)
	started = time.Now()
	result, err := p.ai.Analyse(ctx, image, mime)
	if err != nil {
		p.log.Error("gemini analysis failed", append(fields, "error", err.Error(), "duration_ms", time.Since(started).Milliseconds())...)
		p.complete(ctx, d.ID, failed, 0)
		return
	}
	p.log.Info("gemini analysis completed", append(fields, "has_person", result.HasPerson, "description", safeDescription(result.Description), "duration_ms", time.Since(started).Milliseconds())...)
	if !result.HasPerson {
		p.log.Info("false positive; notification suppressed", fields...)
		p.complete(ctx, d.ID, ignored, 0)
		return
	}
	started = time.Now()
	messageID, err := p.notify.SendPhoto(ctx, image, mime, result.Description)
	if err != nil {
		p.log.Error("telegram photo failed", append(fields, "error", err.Error(), "duration_ms", time.Since(started).Milliseconds())...)
		p.complete(ctx, d.ID, failed, 0)
		return
	}
	p.log.Info("telegram photo sent", append(fields, "telegram_message_id", messageID, "duration_ms", time.Since(started).Milliseconds())...)
	p.complete(ctx, d.ID, notified, messageID)
}

func (p *Processor) complete(ctx context.Context, id string, next status, messageID int64) {
	p.mu.Lock()
	r := p.records[id]
	if r == nil || r.s != processing {
		p.mu.Unlock()
		return
	}
	r.s, r.msg, r.updated = next, messageID, time.Now()
	pending := r.pending
	r.pending = nil
	if next == notified && pending != nil {
		r.s = clipSending
		data, reply := r.data, r.msg
		p.mu.Unlock()
		p.sendClip(ctx, id, data, pending, reply)
		return
	}
	p.mu.Unlock()
}

func (p *Processor) end(ctx context.Context, d *mqttclient.ReviewData) {
	d = copyData(d)
	fields := attrs(d, "end")
	p.mu.Lock()
	r := p.records[d.ID]
	if r == nil || r.s == ignored || r.s == failed || r.s == clipSending || r.s == clipSent {
		p.mu.Unlock()
		p.log.Info("end review ignored", fields...)
		return
	}
	if r.s == processing {
		r.pending = d
		p.mu.Unlock()
		p.log.Info("end review deferred pending notification", fields...)
		return
	}
	r.s, r.updated = clipSending, time.Now()
	newData, reply := r.data, r.msg
	p.mu.Unlock()
	p.log.Info("end review sending video", fields...)
	p.sendClip(ctx, d.ID, newData, d, reply)
}

func cameraFor(end, initial *mqttclient.ReviewData) string {
	if len(end.LinkedEvents) > 0 && end.LinkedEvents[0].Camera != "" {
		return end.LinkedEvents[0].Camera
	}
	if len(end.Cameras) > 0 {
		return end.Cameras[0]
	}
	if len(initial.LinkedEvents) > 0 && initial.LinkedEvents[0].Camera != "" {
		return initial.LinkedEvents[0].Camera
	}
	if len(initial.Cameras) > 0 {
		return initial.Cameras[0]
	}
	return ""
}

func (p *Processor) sendClip(ctx context.Context, id string, initial, end *mqttclient.ReviewData, reply int64) {
	camera := cameraFor(end, initial)
	fields := append(attrs(end, "end"), "camera", camera, "media_source", p.cfg.Frigate.Clip.Source)
	if camera == "" {
		p.log.Warn("clip skipped; camera unavailable", fields...)
		p.setClipResult(id, false)
		return
	}
	started := time.Now()
	media, err := p.frig.Clip(ctx, camera, end.StartTime.String(), end.EndTime.String())
	if err != nil {
		p.log.Error("video fetch failed", append(fields, "error", err.Error(), "duration_ms", time.Since(started).Milliseconds())...)
		p.setClipResult(id, false)
		return
	}
	defer media.Cleanup()
	source := p.cfg.Frigate.Clip.Source
	if source == "" {
		source = "clip"
	}
	_, err = p.notify.SendVideo(ctx, media, "Frigate "+source+": "+camera, reply)
	if err != nil {
		p.log.Error("telegram video failed", append(fields, "error", err.Error(), "duration_ms", time.Since(started).Milliseconds())...)
	} else {
		p.log.Info("telegram video sent", append(fields, "duration_ms", time.Since(started).Milliseconds())...)
	}
	p.setClipResult(id, err == nil)
}

func (p *Processor) setClipResult(id string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := p.records[id]; r != nil && r.s == clipSending {
		if ok {
			r.s = clipSent
		} else {
			r.s = failed
		}
		r.updated = time.Now()
	}
}

func (p *Processor) Cleanup() {
	removed := 0
	p.mu.Lock()
	for id, r := range p.records {
		if r.s != processing && time.Since(r.updated) > p.cfg.Processing.EventTTL {
			delete(p.records, id)
			removed++
		}
	}
	p.mu.Unlock()
	if removed > 0 {
		p.log.Info("event tracker cleanup", "component", "processor", "removed", removed)
	}
}
