package cfg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

// Options bundles env-derived settings + the spool path that cliproxy's
// existing LoadConfigOptional + fsnotify chain will consume.
type Options struct {
	BaseURL   string
	APIKey    string
	Namespace string
	Group     string
	DataID    string
	SpoolPath string
}

// complete reports whether the minimum required fields are set.
func (o Options) complete() bool {
	return o.BaseURL != "" && o.APIKey != "" && o.Namespace != "" &&
		o.DataID != "" && o.SpoolPath != ""
}

// Bootstrap performs the initial GET (writing the result to SpoolPath if
// successful), then starts a background watch goroutine that reapplies
// updates as they arrive.
//
// Behaviour contract (matches the integration spec):
//
//   - All required fields missing → no-op + info log
//   - GET succeeds → write spool atomically, log "config loaded: ..."
//   - GET fails → log warn, leave existing spool alone (cliproxy falls
//     back to whatever local config.toml is already there)
//   - WS changed event → re-GET, write spool, log "config changed: ..."
//   - Failures never block startup; the function always returns quickly
//     after dispatching the watch goroutine.
//
// The watch goroutine lives until ctx is cancelled (typically the
// server's shutdown ctx), so callers should pass a context whose
// lifetime matches the server.
func Bootstrap(ctx context.Context, opt Options) {
	if !opt.complete() {
		log.Info("config_hub disabled (CONFIGHUB_URL/API_KEY/NAMESPACE/DATAID not all set), using local config only")
		return
	}

	src := New(opt.BaseURL, opt.APIKey, opt.Namespace, opt.Group)

	// initial pull
	snap, err := src.Get(ctx, opt.DataID)
	if err != nil {
		log.WithError(err).Warn("config_hub initial GET failed, falling back to local config")
		// fall through to start watcher anyway — the remote may recover
		// later and we still want changes to land hot
	} else if errW := writeAtomic(opt.SpoolPath, snap.Content); errW != nil {
		log.WithError(errW).Errorf("config_hub: write spool %s failed", opt.SpoolPath)
	} else {
		log.Infof("config loaded: dataId=%s version=%d md5=%s", snap.DataID, snap.Version, snap.MD5)
	}

	// install change handler + start watch
	src.OnChange(func(s *Snapshot) {
		if err := writeAtomic(opt.SpoolPath, s.Content); err != nil {
			log.WithError(err).Errorf("config_hub: write spool %s on change failed", opt.SpoolPath)
			return
		}
		log.Infof("config changed: dataId=%s version=%d md5=%s", s.DataID, s.Version, s.MD5)
		// fsnotify on opt.SpoolPath drives the rest: cliproxy's
		// reloadConfigIfChanged debounces and applies in-memory.
	})
	src.Watch(ctx, opt.DataID)
}

// writeAtomic writes b to path via tmp + rename so concurrent readers
// (cliproxy's fsnotify-watched LoadConfigOptional path) never see a
// half-written file. The directory is created if needed.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".cfghub.tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
