package events

import (
	"context"
	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/frigate"
	"github.com/example/frigate-notifier/internal/gemini"
	"github.com/example/frigate-notifier/internal/homeassistant"
	"github.com/example/frigate-notifier/internal/mqttclient"
	"testing"
	"time"
)

type alarm struct{}

func (alarm) Current() homeassistant.AlarmState {
	return homeassistant.AlarmState{State: "armed", Available: true, UpdatedAt: time.Now()}
}

type fc struct{ clips int }

func (f *fc) Snapshot(context.Context, string) ([]byte, string, error) {
	return []byte("x"), "image/jpeg", nil
}
func (f *fc) Clip(context.Context, string, string, string) (frigate.LocalMedia, error) {
	f.clips++
	return frigate.LocalMedia{}, nil
}

type ai struct{ person bool }

func (a ai) Analyse(context.Context, []byte, string) (gemini.Result, error) {
	return gemini.Result{HasPerson: a.person, Description: "person"}, nil
}

type nt struct{ photos, videos int }

func (n *nt) SendPhoto(context.Context, []byte, string, string) (int64, error) {
	n.photos++
	return 1, nil
}
func (n *nt) SendVideo(context.Context, frigate.LocalMedia, string, int64) (int64, error) {
	n.videos++
	return 2, nil
}

type blockingAI struct {
	started chan struct{}
	release chan struct{}
}

func (a blockingAI) Analyse(context.Context, []byte, string) (gemini.Result, error) {
	close(a.started)
	<-a.release
	return gemini.Result{HasPerson: true, Description: "person"}, nil
}

func TestEndDuringAnalysisSendsOneClip(t *testing.T) {
	c := config.Config{Filter: config.Filter{DefaultAction: "deny", Rules: []config.Rule{{Name: "ok", Action: "allow", AlarmStates: []string{"armed"}}}}, Processing: config.Processing{EventTTL: time.Hour}}
	f, n := &fc{}, &nt{}
	a := blockingAI{started: make(chan struct{}), release: make(chan struct{})}
	p := New(c, alarm{}, f, a, n, nil)
	newData := &mqttclient.ReviewData{ID: "r", ProfileName: "x", LinkedEvents: []mqttclient.LinkedEvent{{ID: "e", Camera: "cam"}}}
	endData := &mqttclient.ReviewData{ID: "r", StartTime: "1.1", EndTime: "2.2"}
	done := make(chan struct{})
	go func() {
		p.Handle(context.Background(), mqttclient.ReviewMessage{Type: "new", After: newData})
		close(done)
	}()
	<-a.started
	p.Handle(context.Background(), mqttclient.ReviewMessage{Type: "end", After: endData})
	close(a.release)
	<-done
	p.Handle(context.Background(), mqttclient.ReviewMessage{Type: "end", After: endData})
	if n.photos != 1 || n.videos != 1 || f.clips != 1 {
		t.Fatalf("photos=%d videos=%d clips=%d", n.photos, n.videos, f.clips)
	}
}

func TestPhotoAndSingleClip(t *testing.T) {
	c := config.Config{Filter: config.Filter{DefaultAction: "deny", Rules: []config.Rule{{Name: "ok", Action: "allow", AlarmStates: []string{"armed"}}}}, Processing: config.Processing{EventTTL: time.Hour}}
	f := &fc{}
	n := &nt{}
	p := New(c, alarm{}, f, ai{true}, n, nil)
	d := &mqttclient.ReviewData{ID: "r", ProfileName: "x", LinkedEvents: []mqttclient.LinkedEvent{{ID: "e", Camera: "cam"}}}
	p.Handle(context.Background(), mqttclient.ReviewMessage{Type: "new", After: d})
	d.EndTime = "2.2"
	d.StartTime = "1.1"
	p.Handle(context.Background(), mqttclient.ReviewMessage{Type: "end", After: d})
	p.Handle(context.Background(), mqttclient.ReviewMessage{Type: "end", After: d})
	if n.photos != 1 || n.videos != 1 || f.clips != 1 {
		t.Fatalf("%d %d %d", n.photos, n.videos, f.clips)
	}
}
