package mqttclient

import (
	"context"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/example/frigate-notifier/internal/config"
	"log/slog"
	"time"
)

type Subscriber struct {
	client mqtt.Client
	cfg    config.MQTT
	log    *slog.Logger
	queue  chan<- []byte
}

func New(c config.MQTT, q chan<- []byte, l *slog.Logger) *Subscriber {
	o := mqtt.NewClientOptions().AddBroker(c.Broker).SetClientID(c.ClientID).SetUsername(c.Username).SetPassword(c.Password).SetConnectTimeout(c.ConnectTimeout).SetAutoReconnect(true).SetConnectRetryInterval(c.ReconnectDelay)
	return &Subscriber{mqtt.NewClient(o), c, l, q}
}
func (s *Subscriber) Run(ctx context.Context) error {
	if t := s.client.Connect(); t.Wait() && t.Error() != nil {
		return t.Error()
	}
	t := s.client.Subscribe(s.cfg.Topic, s.cfg.QoS, func(_ mqtt.Client, m mqtt.Message) {
		b := append([]byte(nil), m.Payload()...)
		select {
		case s.queue <- b:
		default:
			s.log.Error("mqtt queue full; dropped message")
		}
	})
	if t.Wait() && t.Error() != nil {
		return t.Error()
	}
	<-ctx.Done()
	s.client.Disconnect(uint(time.Second / time.Millisecond))
	return nil
}
