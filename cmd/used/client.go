package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/KilianSen/used/internal/config"
)

var errNoDaemon = errors.New("daemon not running (start it with `used daemon`)")

// client talks to the daemon's local API over its unix socket.
type client struct{ http *http.Client }

func dial(cfg config.Config) *client {
	sock := cfg.SocketPath()
	return &client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}}
}

func (c *client) get(path string, out any) error        { return c.do("GET", path, nil, out) }
func (c *client) post(path string, body, out any) error { return c.do("POST", path, body, out) }
func (c *client) delete(path string) error              { return c.do("DELETE", path, nil, nil) }

func (c *client) do(method, path string, body, out any) error {
	resp, err := c.send(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *client) send(method, path string, body any) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, "http://used"+path, &buf)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errNoDaemon
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("%s %s: %s %s", method, path, resp.Status, e.Error)
	}
	return resp, nil
}

// stream calls fn for each NDJSON line until the connection ends.
func (c *client) stream(path string, fn func([]byte)) error {
	resp, err := c.send("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}
