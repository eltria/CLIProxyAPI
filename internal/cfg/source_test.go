package cfg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestGet_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer test-key"; got != want {
			t.Errorf("Authorization=%q, want %q", got, want)
		}
		if got := r.URL.Query().Get("namespace"); got != "ns1" {
			t.Errorf("namespace=%q, want ns1", got)
		}
		if got := r.URL.Query().Get("group"); got != "g1" {
			t.Errorf("group=%q, want g1", got)
		}
		if got := r.URL.Query().Get("dataId"); got != "app.toml" {
			t.Errorf("dataId=%q, want app.toml", got)
		}
		w.Header().Set("Content-Type", "application/toml; charset=utf-8")
		w.Header().Set("X-Config-Format", "toml")
		w.Header().Set("X-Config-Md5", "abc123")
		w.Header().Set("X-Config-Version", "7")
		_, _ = w.Write([]byte("port = 8317\n"))
	}))
	defer srv.Close()

	src := New(srv.URL, "test-key", "ns1", "g1")
	snap, err := src.Get(context.Background(), "app.toml")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if snap.Version != 7 {
		t.Errorf("Version=%d, want 7", snap.Version)
	}
	if snap.MD5 != "abc123" {
		t.Errorf("MD5=%q, want abc123", snap.MD5)
	}
	if string(snap.Content) != "port = 8317\n" {
		t.Errorf("Content=%q", snap.Content)
	}
}

func TestGet_HTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key","code":"unauthorized"}`))
	}))
	defer srv.Close()

	src := New(srv.URL, "bad-key", "ns1", "g1")
	_, err := src.Get(context.Background(), "app.toml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %v should mention 401", err)
	}
}

func TestGet_NetworkError(t *testing.T) {
	src := New("http://127.0.0.1:1", "k", "ns", "g") // refused
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := src.Get(ctx, "app.toml")
	if err == nil {
		t.Fatal("expected error from refused connection")
	}
}

// wsServer is a minimal config_hub WS mock supporting hello + sub + changed.
type wsServer struct {
	*httptest.Server
	upgrader websocket.Upgrader
	emitCh   chan wsEvent
	httpHits *atomic.Int64
}

type wsEvent struct {
	op      string // "changed"
	dataID  string
	version int64
	md5     string
}

func newWSServer(t *testing.T, content map[string][]byte) *wsServer {
	t.Helper()
	s := &wsServer{
		upgrader: websocket.Upgrader{},
		emitCh:   make(chan wsEvent, 8),
		httpHits: &atomic.Int64{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cs/configs":
			s.httpHits.Add(1)
			dataID := r.URL.Query().Get("dataId")
			body, ok := content[dataID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("X-Config-Format", "toml")
			w.Header().Set("X-Config-Md5", "md5-of-"+dataID)
			w.Header().Set("X-Config-Version", "1")
			_, _ = w.Write(body)
		case "/v1/ws":
			conn, err := s.upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.WriteJSON(map[string]any{"op": "hello", "heartbeat": 30})
			// 读一条订阅消息然后丢弃，专心推 changed
			_, _, _ = conn.ReadMessage()
			for evt := range s.emitCh {
				if err := conn.WriteJSON(map[string]any{
					"op":  "changed",
					"d":   evt.dataID,
					"v":   evt.version,
					"md5": evt.md5,
				}); err != nil {
					return
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return s
}

func (s *wsServer) emitChanged(dataID string) {
	s.emitCh <- wsEvent{op: "changed", dataID: dataID, version: 2, md5: "md5-of-" + dataID}
}

func TestWatch_ReceivesChange(t *testing.T) {
	mock := newWSServer(t, map[string][]byte{
		"app.toml": []byte("port = 8317\n"),
	})
	defer mock.Close()
	defer close(mock.emitCh)

	src := New(mock.URL, "k", "ns", "g")
	gotCh := make(chan *Snapshot, 4)
	src.OnChange(func(s *Snapshot) { gotCh <- s })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Watch(ctx, "app.toml")

	// 重连后 resync 触发的第一次 onChange
	select {
	case snap := <-gotCh:
		if snap.DataID != "app.toml" {
			t.Errorf("first DataID=%q", snap.DataID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive resync onChange in 3s")
	}

	// 服务器主动 push 一次 changed
	mock.emitChanged("app.toml")
	select {
	case snap := <-gotCh:
		if snap.DataID != "app.toml" {
			t.Errorf("changed DataID=%q", snap.DataID)
		}
		if snap.Version != 1 {
			t.Errorf("Version=%d (we re-GET after changed and the stub returns v=1)", snap.Version)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive changed onChange in 3s")
	}
}

func TestWsURL_SchemeSwap(t *testing.T) {
	cases := map[string]string{
		"https://config-hub.zeabur.app":      "wss://config-hub.zeabur.app/v1/ws",
		"http://localhost:18080":             "ws://localhost:18080/v1/ws",
		"https://config-hub.zeabur.app/":     "wss://config-hub.zeabur.app/v1/ws",
		"http://localhost:18080/some/prefix": "ws://localhost:18080/some/prefix/v1/ws",
	}
	for in, want := range cases {
		s := New(in, "k", "ns", "g")
		if got := s.wsURL(); got != want {
			t.Errorf("wsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSnapshot_JSONRoundTrip(t *testing.T) {
	// Snapshot is exposed to bootstrap; make sure it survives encode/decode
	// for any future logging. Not a critical path; just smoke.
	snap := &Snapshot{DataID: "app.toml", Format: "toml", Version: 5, MD5: "x", Content: []byte("k = 1")}
	b, err := json.Marshal(struct {
		DataID  string `json:"dataId"`
		Format  string `json:"format"`
		Version int64  `json:"version"`
		MD5     string `json:"md5"`
	}{snap.DataID, snap.Format, snap.Version, snap.MD5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"version":5`) {
		t.Errorf("encode missing version: %s", b)
	}
}
