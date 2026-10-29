// Package mqtt publishes events to an MQTT broker.
//
// Topics under the prefix (default used/<hostname>):
//
//	availability   online | offline (retained; offline is the last will)
//	state          active | present | unused (retained)
//	status         the latest event payload as JSON (retained)
//	event/<type>   every event payload as JSON, filtered by `events`
//
// With homeassistant = true the machine also appears in Home Assistant via
// MQTT discovery, as one device with presence, occupancy and load sensors.
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/KilianSen/used/internal/bus"
	"github.com/KilianSen/used/internal/secret"
)

type Config struct {
	Broker        string   `toml:"broker"`        // tcp://host:1883, ssl://host:8883 (system CAs, e.g. Let's Encrypt), ws://, wss://
	Username      string   `toml:"username"`      // literal, env:NAME or file:/path
	Password      string   `toml:"password"`      // literal, env:NAME or file:/path
	ClientID      string   `toml:"client_id"`     // default used-<machine id>
	Topic         string   `toml:"topic"`         // prefix, default used/<hostname>
	QoS           byte     `toml:"qos"`           // 0, 1 or 2
	Events        []string `toml:"events"`        // event/<type> filter; empty = all
	HomeAssistant bool     `toml:"homeassistant"` // publish Home Assistant discovery
}

// Device identifies this machine on the broker.
type Device struct{ ID, Hostname, Model string }

type Sink struct {
	cfg    Config
	dev    Device
	prefix string
	client paho.Client
	log    *slog.Logger

	mu   sync.Mutex
	last []byte // latest event, republished on reconnect
}

const timeout = 10 * time.Second

func New(cfg Config, dev Device, log *slog.Logger) (*Sink, error) {
	u, err := url.Parse(cfg.Broker)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("mqtt: broker must look like tcp://host:1883, got %q", cfg.Broker)
	}
	if u.Port() == "" { // paho needs one; use the scheme's standard port
		port := map[string]string{"tcp": "1883", "mqtt": "1883", "ssl": "8883", "tls": "8883",
			"mqtts": "8883", "ws": "80", "wss": "443"}[u.Scheme]
		if port == "" {
			return nil, fmt.Errorf("mqtt: unknown broker scheme %q (tcp, ssl, mqtts, ws, wss)", u.Scheme)
		}
		u.Host += ":" + port
		cfg.Broker = u.String()
	}
	if cfg.QoS > 2 {
		return nil, fmt.Errorf("mqtt: qos must be 0, 1 or 2")
	}
	user, err := secret.Resolve(cfg.Username)
	if err != nil {
		return nil, fmt.Errorf("mqtt: username: %w", err)
	}
	pass, err := secret.Resolve(cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("mqtt: password: %w", err)
	}
	s := &Sink{cfg: cfg, dev: dev, prefix: strings.TrimSuffix(cfg.Topic, "/"), log: log.With("mqtt", u.Redacted())}
	if s.prefix == "" {
		s.prefix = "used/" + topicSafe(dev.Hostname)
	}
	id := cfg.ClientID
	if id == "" {
		id = "used-" + dev.ID[:min(12, len(dev.ID))]
	}

	opts := paho.NewClientOptions().AddBroker(cfg.Broker).SetClientID(id).
		SetUsername(user).SetPassword(pass).
		SetCleanSession(true).SetAutoReconnect(true).SetConnectRetry(true).
		SetMaxReconnectInterval(5*time.Minute).SetOrderMatters(true).
		SetWill(s.topic("availability"), "offline", 1, true).
		SetOnConnectHandler(func(paho.Client) { s.connected() }).
		SetConnectionLostHandler(func(_ paho.Client, err error) { s.log.Warn("connection lost", "err", err) })
	s.client = paho.NewClient(opts)
	return s, nil
}

func (s *Sink) topic(name string) string { return s.prefix + "/" + name }

// Run publishes events until ctx ends, then marks the machine offline.
func (s *Sink) Run(ctx context.Context, events <-chan bus.Msg) {
	s.client.Connect() // retries in the background
	defer func() {
		if s.client.IsConnected() {
			s.send("availability", true, []byte("offline"))
		}
		s.client.Disconnect(250)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-events:
			if !ok {
				return
			}
			s.handle(m)
		}
	}
}

func (s *Sink) handle(m bus.Msg) {
	s.mu.Lock()
	s.last = m.Body
	s.mu.Unlock()
	s.retained(m.Body)
	if bus.Wants(s.cfg.Events, m.Type) {
		s.send("event/"+m.Type, false, m.Body)
	}
}

// retained refreshes the state and status topics from an event payload.
func (s *Sink) retained(body []byte) {
	var ev struct {
		Presence struct{ State string } `json:"presence"`
	}
	if json.Unmarshal(body, &ev) == nil && ev.Presence.State != "" {
		s.send("state", true, []byte(ev.Presence.State))
	}
	s.send("status", true, body)
}

// connected runs on every (re)connect: announce, rediscover, refresh state.
func (s *Sink) connected() {
	s.log.Info("connected", "topic", s.prefix)
	go func() { // paho handlers must not block on publishes
		s.send("availability", true, []byte("online"))
		if s.cfg.HomeAssistant {
			s.publish("homeassistant/device/used_"+s.dev.ID+"/config", true, s.discovery())
		}
		s.mu.Lock()
		last := s.last
		s.mu.Unlock()
		if last != nil {
			s.retained(last)
		}
	}()
}

func (s *Sink) send(name string, retain bool, body []byte) { s.publish(s.topic(name), retain, body) }

// publish sends one message. While disconnected it drops instead of queueing:
// retained topics are refreshed on reconnect, and seq shows lost events.
func (s *Sink) publish(topic string, retain bool, body []byte) {
	if !s.client.IsConnectionOpen() {
		return
	}
	t := s.client.Publish(topic, s.cfg.QoS, retain, body)
	if !t.WaitTimeout(timeout) {
		s.log.Warn("publish timed out", "topic", topic)
	} else if err := t.Error(); err != nil {
		s.log.Warn("publish failed", "topic", topic, "err", err)
	}
}

// discovery is a Home Assistant device-discovery message: one device, several
// sensors, all reading the retained status topic.
func (s *Sink) discovery() []byte {
	type cmp = map[string]any
	uid := "used_" + s.dev.ID
	cmps := map[string]cmp{
		"presence": {"platform": "sensor", "name": "Presence", "device_class": "enum",
			"options": []string{"active", "present", "unused"}, "icon": "mdi:account-clock",
			"value_template": "{{ value_json.presence.state }}"},
		"occupied": {"platform": "binary_sensor", "name": "Occupied", "device_class": "occupancy",
			"value_template": "{{ 'ON' if value_json.presence.state != 'unused' else 'OFF' }}"},
		"idle": {"platform": "sensor", "name": "Idle", "device_class": "duration", "unit_of_measurement": "s",
			"value_template": "{{ value_json.presence.idle_seconds }}"},
		"cpu": {"platform": "sensor", "name": "CPU", "unit_of_measurement": "%", "state_class": "measurement",
			"icon": "mdi:cpu-64-bit", "value_template": "{{ value_json.metrics.total.cpu | round(1) }}"},
		"gpu": {"platform": "sensor", "name": "GPU", "unit_of_measurement": "%", "state_class": "measurement",
			"icon": "mdi:expansion-card", "value_template": "{{ value_json.metrics.total.gpu | round(1) }}"},
		"workload_cpu": {"platform": "sensor", "name": "Workload CPU", "unit_of_measurement": "%",
			"state_class": "measurement", "icon": "mdi:server-network",
			"value_template": "{{ value_json.metrics.workload.cpu | round(1) }}"},
		"workloads": {"platform": "sensor", "name": "Workloads", "state_class": "measurement",
			"icon": "mdi:format-list-bulleted", "value_template": "{{ (value_json.workloads or []) | count }}"},
	}
	for key, c := range cmps {
		c["unique_id"] = uid + "_" + key
		c["state_topic"] = s.topic("status")
		c["availability_topic"] = s.topic("availability")
	}
	b, _ := json.Marshal(map[string]any{
		"device": map[string]any{"identifiers": []string{uid}, "name": s.dev.Hostname,
			"manufacturer": "used", "model": s.dev.Model},
		"origin":     map[string]any{"name": "used", "support_url": "https://github.com/KilianSen/used"},
		"components": cmps,
		"qos":        s.cfg.QoS,
	})
	return b
}

// topicSafe drops characters with meaning in MQTT topics.
func topicSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '+' || r == '#' || r == '/' || r == ' ' || r < 32 {
			return '_'
		}
		return r
	}, strings.ToLower(s))
	if s == "" {
		return "unknown"
	}
	return s
}
