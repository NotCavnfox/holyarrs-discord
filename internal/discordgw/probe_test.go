package discordgw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// probeServers points Probe at two local servers: rest answers the REST
// call, first is the first websocket message. It reports what was seen.
type probeSeen struct {
	rest, ws atomic.Int32
	sent     atomic.Int32 // messages the client sent before closing
	closed   atomic.Bool  // the client closed with a normal close frame
	done     chan struct{}
}

func probeServers(t *testing.T, rest http.HandlerFunc, first string) *probeSeen {
	t.Helper()
	seen := &probeSeen{done: make(chan struct{})}
	rs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.rest.Add(1)
		rest(w, r)
	}))
	up := websocket.Upgrader{}
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.ws.Add(1)
		defer close(seen.done)
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte(first))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				seen.closed.Store(websocket.IsCloseError(err, websocket.CloseNormalClosure))
				return
			}
			seen.sent.Add(1)
		}
	}))
	oldREST, oldWS := probeRESTURL, probeWSURL
	probeRESTURL = rs.URL + "/api/v10/gateway"
	probeWSURL = "ws" + strings.TrimPrefix(ws.URL, "http") + "/?v=10&encoding=json"
	t.Cleanup(func() {
		probeRESTURL, probeWSURL = oldREST, oldWS
		rs.Close()
		ws.Close()
	})
	return seen
}

func restOK(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"url":"wss://gateway.discord.gg"}`))
}

const hello = `{"t":null,"s":null,"op":10,"d":{"heartbeat_interval":41250}}`

func TestProbeOK(t *testing.T) {
	seen := probeServers(t, restOK, hello)
	if err := Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	select {
	case <-seen.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the websocket was not closed")
	}
	if seen.rest.Load() != 1 || seen.ws.Load() != 1 {
		t.Errorf("rest=%d ws=%d", seen.rest.Load(), seen.ws.Load())
	}
	if seen.sent.Load() != 0 {
		t.Errorf("the probe sent %d messages; it must not identify", seen.sent.Load())
	}
	if !seen.closed.Load() {
		t.Error("the probe did not close cleanly")
	}
}

func TestProbeRESTFails(t *testing.T) {
	seen := probeServers(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}, hello)
	err := Probe(context.Background())
	if err == nil || err.Error() != "discord rest: status 500" {
		t.Fatalf("err = %v", err)
	}
	if seen.ws.Load() != 0 {
		t.Error("the gateway was dialled after REST failed")
	}
}

func TestProbeRESTNoURL(t *testing.T) {
	probeServers(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"url":""}`)) }, hello)
	err := Probe(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "discord rest: ") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeNotHello(t *testing.T) {
	probeServers(t, restOK, `{"op":9,"d":false}`)
	err := Probe(context.Background())
	if err == nil || err.Error() != "discord gateway: first message is not a hello" {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeErrorHasNoAddress(t *testing.T) {
	probeServers(t, restOK, hello)
	probeRESTURL = "http://127.0.0.1:1/secret-path"
	err := Probe(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "discord rest: ") || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("err = %v", err)
	}
}
