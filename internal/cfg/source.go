// Package cfg integrates cliproxy with the config_hub config service.
//
// Source pulls the configured TOML from config_hub on demand (Get) and
// watches it via WebSocket (Watch). When the remote dataId changes,
// the registered onChange callback fires with the freshly fetched
// snapshot. Caller is responsible for persisting / applying the bytes —
// this package never touches in-memory cliproxy state directly.
package cfg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

// Snapshot is one fetched copy of a dataId.
type Snapshot struct {
	DataID  string
	Format  string
	Content []byte
	Version int64
	MD5     string
}

// Source talks to a config_hub instance for a single (namespace, group).
// Multiple Sources can coexist if you need different namespaces/groups,
// but for cliproxy we use exactly one.
type Source struct {
	BaseURL   string
	APIKey    string
	Namespace string
	Group     string

	httpClient *http.Client
	dialer     *websocket.Dialer

	mu       sync.Mutex
	onChange func(*Snapshot)
}

// New constructs a Source. baseURL must be the config_hub root (e.g.
// "https://config-hub.zeabur.app"); the trailing slash is normalised.
func New(baseURL, apiKey, ns, group string) *Source {
	return &Source{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		Namespace:  strings.TrimSpace(ns),
		Group:      strings.TrimSpace(group),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		dialer:     websocket.DefaultDialer,
	}
}

// OnChange registers (replacing any prior) the callback fired when a
// watched dataId changes. The callback is invoked from the watch
// goroutine — keep it cheap (e.g. write a file) and let the rest of
// the system react asynchronously.
func (s *Source) OnChange(fn func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

func (s *Source) callOnChange(snap *Snapshot) {
	s.mu.Lock()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb(snap)
	}
}

// Get pulls a single dataId. Errors include 4xx/5xx HTTP responses with
// the body trimmed onto the error message for debuggability.
func (s *Source) Get(ctx context.Context, dataID string) (*Snapshot, error) {
	q := url.Values{}
	q.Set("namespace", s.Namespace)
	q.Set("group", s.Group)
	q.Set("dataId", dataID)
	u := s.BaseURL + "/v1/cs/configs?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("config_hub: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("config_hub: GET %s: %w", dataID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap; spec ≤ 256KiB
	if err != nil {
		return nil, fmt.Errorf("config_hub: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("config_hub: GET %s: HTTP %d: %s", dataID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	v, _ := strconv.ParseInt(resp.Header.Get("X-Config-Version"), 10, 64)
	return &Snapshot{
		DataID:  dataID,
		Format:  resp.Header.Get("X-Config-Format"),
		Content: body,
		Version: v,
		MD5:     resp.Header.Get("X-Config-Md5"),
	}, nil
}

// Watch starts a background goroutine subscribing to the listed dataIds.
// It runs until ctx is cancelled, reconnecting with exponential backoff
// on transport errors. A successful (re)connect immediately re-GETs each
// dataId so the caller is never out of sync after a reconnect.
//
// dataIDs may be a single id (cliproxy's typical case) or several;
// changes to any of them invoke the same OnChange callback.
func (s *Source) Watch(ctx context.Context, dataIDs ...string) {
	if len(dataIDs) == 0 {
		return
	}
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			err := s.watchOnce(ctx, dataIDs)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.WithError(err).Warnf("config_hub ws disconnected, retry in %s", backoff)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
}

// wsURL builds the ws(s) endpoint by swapping http→ws / https→wss.
func (s *Source) wsURL() string {
	u := s.BaseURL + "/v1/ws"
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		return "ws://" + strings.TrimPrefix(u, "http://")
	default:
		return u
	}
}

func (s *Source) watchOnce(ctx context.Context, dataIDs []string) error {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+s.APIKey)

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	conn, _, err := s.dialer.DialContext(dialCtx, s.wsURL(), hdr)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// 服务端 hello: {"op":"hello","heartbeat":30}
	// 我们读一条但不强校验内容（只确认连接还活着）。
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, _, _ = conn.ReadMessage()
	conn.SetReadDeadline(time.Time{}) // 清掉一次性 deadline

	// 订阅
	items := make([]map[string]string, 0, len(dataIDs))
	for _, d := range dataIDs {
		items = append(items, map[string]string{"ns": s.Namespace, "g": s.Group, "d": d})
	}
	if err := conn.WriteJSON(map[string]any{"op": "sub", "items": items}); err != nil {
		return fmt.Errorf("ws sub: %w", err)
	}

	// 重连后立即 re-GET 每个 dataId 校准本地状态
	// （changed 事件不会在断线期间补发，需要主动校准）
	for _, d := range dataIDs {
		if snap, errGet := s.Get(ctx, d); errGet == nil {
			s.callOnChange(snap)
		} else {
			log.WithError(errGet).Warnf("config_hub: post-reconnect resync GET %s failed", d)
		}
	}

	// 心跳 goroutine：双层保险。
	//   - WS protocol-level PingMessage 每 20s 发一次：让中间反向代理识别为
	//     TCP 活动，避免 30s idle timeout 切线（实测有这个问题）。
	//     gorilla/websocket 的 WriteControl 与 Read/Write 并发安全。
	//   - app-level {"op":"ping"} 每 25s 发一次：跟 config_hub 服务器
	//     约定的应用层心跳，按 integration.md §4 协议走。
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		wsTick := time.NewTicker(20 * time.Second)
		appTick := time.NewTicker(25 * time.Second)
		defer wsTick.Stop()
		defer appTick.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-wsTick.C:
				if err := conn.WriteControl(
					websocket.PingMessage,
					nil,
					time.Now().Add(5*time.Second),
				); err != nil {
					return
				}
			case <-appTick.C:
				if err := conn.WriteJSON(map[string]string{"op": "ping"}); err != nil {
					return
				}
			}
		}
	}()

	// 读循环
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}
		var msg struct {
			Op  string `json:"op"`
			D   string `json:"d"`
			MD5 string `json:"md5"`
			V   int64  `json:"v"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("config_hub: ws non-json frame; skipping")
			continue
		}

		switch msg.Op {
		case "changed":
			snap, errGet := s.Get(ctx, msg.D)
			if errGet != nil {
				log.WithError(errGet).Warnf("config_hub: re-GET %s after changed event failed", msg.D)
				continue
			}
			s.callOnChange(snap)
		case "pong", "hello":
			// expected, no-op
		default:
			log.Debugf("config_hub: ws op=%q ignored", msg.Op)
		}
	}
}
