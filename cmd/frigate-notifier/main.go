package main

import (
	"context"
	"flag"
	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/events"
	"github.com/example/frigate-notifier/internal/frigate"
	"github.com/example/frigate-notifier/internal/gemini"
	"github.com/example/frigate-notifier/internal/homeassistant"
	"github.com/example/frigate-notifier/internal/mqttclient"
	"github.com/example/frigate-notifier/internal/telegram"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "config.yml", "configuration file")
	flag.Parse()
	c, e := config.Load(*path)
	if e != nil {
		panic(e)
	}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, nil)
	if c.Logging.Format == "text" {
		h = slog.NewTextHandler(os.Stdout, nil)
	}
	l := slog.New(h)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cache := homeassistant.NewCache(c.HomeAssistant.StateMaxAge)
	go homeassistant.New(c.HomeAssistant, cache, l).Run(ctx)
	p := events.New(c, cache, frigate.New(c.Frigate), gemini.New(c.Gemini), telegram.New(c.Telegram), l)
	q := make(chan []byte, c.Processing.QueueSize)
	var wg sync.WaitGroup
	for i := 0; i < c.Processing.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range q {
				m, err := mqttclient.Parse(b)
				if err == nil {
					p.Handle(ctx, m)
				}
			}
		}()
	}
	go func() {
		interval := c.Processing.EventTTL / 2
		if interval <= 0 {
			interval = time.Minute
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.Cleanup()
			}
		}
	}()
	err := mqttclient.New(c.MQTT, q, l).Run(ctx)
	// Run has stopped accepting MQTT callbacks, so closing the queue cannot race
	// a producer. Cancellation makes in-flight network operations return.
	cancel()
	close(q)
	workersDone := make(chan struct{})
	go func() { wg.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
	case <-time.After(c.Processing.ShutdownTimeout):
		l.Warn("worker shutdown timed out")
	}
	if err != nil {
		l.Error("mqtt stopped", "error", err)
	}
}
