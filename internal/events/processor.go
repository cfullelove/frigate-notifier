package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/filter"
	"github.com/example/frigate-notifier/internal/frigate"
	"github.com/example/frigate-notifier/internal/gemini"
	"github.com/example/frigate-notifier/internal/homeassistant"
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
	data    *mqttclient.ReviewData // immutable copy of the accepted new payload
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
	return &Processor{cfg: c, alarm: a, frig: f, ai: g, notify: n, log: l, records: map[string]*record{}}
}

func (p *Processor) Handle(ctx context.Context, m mqttclient.ReviewMessage) {
	if m.Validate() != nil {
		return
	}
	if m.Type == "new" {
		p.new(ctx, m.After)
	} else {
		p.end(ctx, m.After)
	}
}

func copyData(d *mqttclient.ReviewData) *mqttclient.ReviewData {
	v := *d
	v.LinkedEvents = append([]mqttclient.LinkedEvent(nil), d.LinkedEvents...)
	v.Cameras = append([]string(nil), d.Cameras...)
	return &v
}

func (p *Processor) new(ctx context.Context, d *mqttclient.ReviewData) {
	d = copyData(d)
	p.mu.Lock()
	if _, exists := p.records[d.ID]; exists {
		p.mu.Unlock()
		return
	}
	p.records[d.ID] = &record{s: processing, data: d, updated: time.Now()}
	p.mu.Unlock()

	a := p.alarm.Current()
	if !a.Available || !filter.Evaluate(p.cfg.Filter, a.State, d.ProfileName).Allow {
		p.complete(ctx, d.ID, ignored, 0)
		return
	}
	image, mime, err := p.frig.Snapshot(ctx, d.LinkedEvents[0].ID)
	if err != nil {
		p.complete(ctx, d.ID, failed, 0)
		return
	}
	result, err := p.ai.Analyse(ctx, image, mime)
	if err != nil {
		p.complete(ctx, d.ID, failed, 0)
		return
	}
	if !result.HasPerson {
		p.complete(ctx, d.ID, ignored, 0)
		return
	}
	messageID, err := p.notify.SendPhoto(ctx, image, mime, result.Description)
	if err != nil {
		p.complete(ctx, d.ID, failed, 0)
		return
	}
	p.complete(ctx, d.ID, notified, messageID)
}

// complete makes the terminal new transition and atomically claims a pending
// end. Therefore an end racing photo delivery can neither be lost nor upload a
// clip twice.
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
	p.mu.Lock()
	r := p.records[d.ID]
	if r == nil || r.s == ignored || r.s == failed || r.s == clipSending || r.s == clipSent {
		p.mu.Unlock()
		return
	}
	if r.s == processing {
		r.pending = d // latest valid end wins
		p.mu.Unlock()
		return
	}
	// The only remaining state is notified. Claim it before any I/O.
	r.s, r.updated = clipSending, time.Now()
	newData, reply := r.data, r.msg
	p.mu.Unlock()
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
	if camera == "" {
		p.setClipResult(id, false)
		return
	}
	media, err := p.frig.Clip(ctx, camera, end.StartTime.String(), end.EndTime.String())
	if err == nil {
		defer media.Cleanup()
		_, err = p.notify.SendVideo(ctx, media, "Frigate recording: "+camera, reply)
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
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, r := range p.records {
		if r.s != processing && time.Since(r.updated) > p.cfg.Processing.EventTTL {
			delete(p.records, id)
		}
	}
}
