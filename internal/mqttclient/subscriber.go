package mqttclient

import (
	"context"
	"log/slog"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/logging"
)

type Subscriber struct {
	client mqtt.Client
	cfg    config.MQTT
	log    *slog.Logger
	queue  chan<- []byte
}

func New(c config.MQTT, q chan<- []byte, l *slog.Logger) *Subscriber {
	s := &Subscriber{cfg: c, log: logging.Default(l), queue: q}
	o := mqtt.NewClientOptions().AddBroker(c.Broker).SetClientID(c.ClientID).SetUsername(c.Username).SetPassword(c.Password).SetConnectTimeout(c.ConnectTimeout).SetAutoReconnect(true).SetConnectRetryInterval(c.ReconnectDelay)
	o.SetConnectionLostHandler(func(_ mqtt.Client, _ error) {
		s.log.Warn("mqtt disconnected", "component", "mqtt", "error", "connection lost")
	})
	o.SetOnConnectHandler(func(client mqtt.Client) {
		s.log.Info("mqtt connected", "component", "mqtt")
		t := client.Subscribe(s.cfg.Topic, s.cfg.QoS, s.handle)
		if t.Wait() && t.Error() != nil {
			s.log.Error("mqtt subscription failed", "component", "mqtt", "topic", s.cfg.Topic, "error", "subscribe failed")
			return
		}
		s.log.Info("mqtt subscribed", "component", "mqtt", "topic", s.cfg.Topic, "qos", s.cfg.QoS)
	})
	s.client = mqtt.NewClient(o)
	return s
}

func (s *Subscriber) handle(_ mqtt.Client, m mqtt.Message) {
	b := append([]byte(nil), m.Payload()...)
	select {
	case s.queue <- b:
	default:
		s.log.Warn("mqtt queue full; dropped message", "component", "mqtt", "topic", m.Topic(), "queue_capacity", cap(s.queue))
	}
}

func (s *Subscriber) Run(ctx context.Context) error {
	t := s.client.Connect()
	if t.Wait() && t.Error() != nil {
		s.log.Error("mqtt connect failed", "component", "mqtt", "error", "connect failed")
		return t.Error()
	}
	<-ctx.Done()
	s.log.Info("mqtt disconnecting", "component", "mqtt")
	s.client.Disconnect(uint(time.Second / time.Millisecond))
	s.log.Info("mqtt disconnected", "component", "mqtt")
	return nil
}
