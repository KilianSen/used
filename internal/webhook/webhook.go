// Package webhook delivers events to HTTP endpoints, signed and retried.
//
// Signature: X-Used-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>
// Receivers should reject timestamps older than a few minutes.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/KilianSen/used/internal/bus"
	"github.com/KilianSen/used/internal/secret"
)

type Config struct {
	URL     string            `toml:"url"`
	Secret  string            `toml:"secret"`  // literal, env:NAME or file:/path
	Events  []string          `toml:"events"`  // empty = all
	Headers map[string]string `toml:"headers"` // values support env:/file: too
}

type Sender struct {
	cfg     Config
	secret  []byte
	headers map[string]string
	client  *http.Client
	log     *slog.Logger
}

func New(cfg Config, log *slog.Logger) (*Sender, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("webhook: url is required")
	}
	key, err := secret.Resolve(cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("webhook %s: secret: %w", cfg.URL, err)
	}
	s := &Sender{cfg: cfg, secret: []byte(key), headers: map[string]string{},
		client: &http.Client{Timeout: 10 * time.Second}, log: log.With("webhook", cfg.URL)}
	for k, v := range cfg.Headers {
		if s.headers[k], err = secret.Resolve(v); err != nil {
			return nil, fmt.Errorf("webhook %s: header %s: %w", cfg.URL, k, err)
		}
	}
	return s, nil
}

// Run delivers events in order until ctx ends. A failing endpoint is retried
// with backoff; events arriving meanwhile wait in the subscription buffer.
func (s *Sender) Run(ctx context.Context, events <-chan bus.Msg) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-events:
			if !ok {
				return
			}
			if bus.Wants(s.cfg.Events, m.Type) {
				s.deliver(ctx, m)
			}
		}
	}
}

func (s *Sender) deliver(ctx context.Context, m bus.Msg) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		err := s.post(ctx, m)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		wait := backoff + rand.N(backoff/2+1)
		s.log.Warn("delivery failed", "event", m.Type, "attempt", attempt, "retry_in", wait.Round(time.Millisecond), "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, 5*time.Minute)
	}
}

func (s *Sender) post(ctx context.Context, m bus.Msg) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(m.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "used")
	req.Header.Set("X-Used-Event", m.Type)
	req.Header.Set("X-Used-Delivery", m.ID)
	if len(s.secret) > 0 {
		req.Header.Set("X-Used-Signature", Sign(s.secret, time.Now(), m.Body))
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}

// Sign produces the X-Used-Signature header value.
func Sign(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
