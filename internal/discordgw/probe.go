package discordgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// Where Probe looks; replaced in tests.
var (
	probeRESTURL = "https://discord.com/api/v10/gateway"
	probeWSURL   = "wss://gateway.discord.gg/?v=10&encoding=json"
)

const (
	probeTimeout = 15 * time.Second
	probeMaxBody = 1 << 16
	opHello      = 10
)

// Probe checks that Discord can be reached from here, through the proxy the
// environment names. It needs no bot token and never identifies: it reads
// the public gateway address over REST, then opens the gateway websocket and
// waits for the hello. The error names the step that failed and never holds
// an address.
func Probe(ctx context.Context) error {
	if err := probeREST(ctx); err != nil {
		return fmt.Errorf("discord rest: %w", noURL(err))
	}
	if err := probeWS(ctx); err != nil {
		return fmt.Errorf("discord gateway: %w", noURL(err))
	}
	return nil
}

// noURL drops the address a *url.Error carries (it can name a proxy).
func noURL(err error) error {
	var ue *url.Error
	for errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	return err
}

func probeREST(ctx context.Context) error {
	hc := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true},
		Timeout:   probeTimeout,
	}
	defer hc.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeRESTURL, nil)
	if err != nil {
		return errors.New("bad address")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
	if err != nil {
		return err
	}
	var v struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(b, &v) != nil || v.URL == "" {
		return errors.New("answer has no gateway url")
	}
	return nil
}

func probeWS(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: probeTimeout}
	conn, resp, err := d.DialContext(ctx, probeWSURL, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return fmt.Errorf("connect: status %d", resp.StatusCode)
		}
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	// A cancelled ctx ends the read by closing the connection.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	conn.SetReadLimit(probeMaxBody)
	_ = conn.SetReadDeadline(time.Now().Add(probeTimeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("no hello: read failed")
	}
	var v struct {
		Op *int `json:"op"`
	}
	if json.Unmarshal(msg, &v) != nil || v.Op == nil || *v.Op != opHello {
		return errors.New("first message is not a hello")
	}
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	return nil
}
