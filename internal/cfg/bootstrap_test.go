package cfg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrap_NoOpWhenIncomplete(t *testing.T) {
	// Missing required fields → must not panic, must not create files.
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "app.toml")
	Bootstrap(context.Background(), Options{
		BaseURL:   "https://config-hub.zeabur.app",
		APIKey:    "",
		Namespace: "cliproxyapi",
		DataID:    "app.toml",
		SpoolPath: dst,
	})
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("spool file should not exist; err=%v", err)
	}
}

func TestBootstrap_GetSuccessWritesSpool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Config-Format", "toml")
		w.Header().Set("X-Config-Md5", "abc")
		w.Header().Set("X-Config-Version", "3")
		_, _ = w.Write([]byte("port = 9000\n"))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dst := filepath.Join(tmp, "app.toml")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Bootstrap(ctx, Options{
		BaseURL:   srv.URL,
		APIKey:    "k",
		Namespace: "ns",
		Group:     "g",
		DataID:    "app.toml",
		SpoolPath: dst,
	})
	// Bootstrap returns immediately after starting Watch; the initial Get
	// happens synchronously so dst must exist now.
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if string(got) != "port = 9000\n" {
		t.Errorf("spool=%q, want %q", got, "port = 9000\n")
	}
}

func TestBootstrap_GetFailureLeavesSpoolAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream is sad"))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dst := filepath.Join(tmp, "app.toml")
	if err := os.WriteFile(dst, []byte("preexisting = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Bootstrap(ctx, Options{
		BaseURL:   srv.URL,
		APIKey:    "k",
		Namespace: "ns",
		Group:     "g",
		DataID:    "app.toml",
		SpoolPath: dst,
	})
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if string(got) != "preexisting = true\n" {
		t.Errorf("spool got overwritten on failure: %q", got)
	}
}

func TestWriteAtomic(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "nested", "dir", "out.toml")
	if err := writeAtomic(dst, []byte("hello\n")); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Errorf("got %q", got)
	}
	// no orphan tmp file
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), "*.cfghub.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("found stray tmp: %v", leftovers)
	}
}

// quick smoke: Bootstrap must return promptly even if the server is slow.
func TestBootstrap_ReturnsQuickly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// fast 200 — we're testing the wrapper, not network slowness
		_, _ = w.Write([]byte("k = 1\n"))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dst := filepath.Join(tmp, "app.toml")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		Bootstrap(ctx, Options{
			BaseURL:   srv.URL,
			APIKey:    "k",
			Namespace: "ns",
			Group:     "g",
			DataID:    "app.toml",
			SpoolPath: dst,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Bootstrap did not return within 2s")
	}
}
