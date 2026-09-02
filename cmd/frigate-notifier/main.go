package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/events"
	"github.com/example/frigate-notifier/internal/frigate"
	"github.com/example/frigate-notifier/internal/gemini"
	"github.com/example/frigate-notifier/internal/homeassistant"
	"github.com/example/frigate-notifier/internal/logging"
	"github.com/example/frigate-notifier/internal/mqttclient"
	"github.com/example/frigate-notifier/internal/telegram"
)

func main() {
	path := flag.String("config", "config.yml", "configuration file")
	flag.Parse()
	c, err := config.Load(*path)
	if err != nil {
		panic(err)
	}
	level, _ := logging.Level(c.Logging.Level)
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if c.Logging.Format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	log := slog.New(handler)
	log.Info("service starting", "component", "service", "logging_level", c.Logging.Level, "logging_format", c.Logging.Format, "mqtt_topic", c.MQTT.Topic, "mqtt_qos", c.MQTT.QoS, "workers", c.Processing.Workers, "queue_size", c.Processing.QueueSize, "alarm_entity_id", c.HomeAssistant.AlarmEntityID, "gemini_model", c.Gemini.Model, "filter_rules", len(c.Filter.Rules))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cache := homeassistant.NewCache(c.HomeAssistant.StateMaxAge)
	go homeassistant.New(c.HomeAssistant, cache, log).Run(ctx)
	p := events.New(c, cache, frigate.New(c.Frigate), gemini.New(c.Gemini), telegram.New(c.Telegram), log)
	q := make(chan []byte, c.Processing.QueueSize)
	var wg sync.WaitGroup
	for i := 0; i < c.Processing.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range q {
				m, parseErr := mqttclient.Parse(b)
				if parseErr != nil {
					log.Warn("mqtt message malformed", "component", "mqtt", "error", "invalid review payload")
					continue
				}
				p.Handle(ctx, m)
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
	err = mqttclient.New(c.MQTT, q, log).Run(ctx)
	log.Info("service shutting down", "component", "service")
	cancel()
	close(q)
	workersDone := make(chan struct{})
	go func() { wg.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
	case <-time.After(c.Processing.ShutdownTimeout):
		log.Warn("worker shutdown timed out", "component", "service")
	}
	if err != nil {
		log.Error("mqtt stopped", "component", "mqtt", "error", "mqtt run failed")
	}
	log.Info("service stopped", "component", "service")
}
