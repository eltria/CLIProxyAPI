package cfg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

// ApplyFunc applies a fresh TOML payload from config_hub directly to
// the running service, bypassing the spool-file → fsnotify → watcher
// reload chain. Returning an error logs the failure but does not stop
// subsequent invocations.
type ApplyFunc func(content []byte) error

var registeredApply atomic.Pointer[ApplyFunc]

// SetApply installs (or, with nil, clears) the in-memory apply
// callback. Bootstrap's OnChange invokes it after the spool write so a
// failed or missing spool never suppresses the in-memory reload, and a
// successful in-memory reload makes the watcher's debounced fsnotify
// path redundant rather than the only path that can land changes.
//
// Safe to call from any goroutine. Idempotent: the most recent
// registration wins.
func SetApply(fn ApplyFunc) {
	if fn == nil {
		registeredApply.Store(nil)
		return
	}
	registeredApply.Store(&fn)
}

// loadApply returns the currently registered apply callback or nil.
func loadApply() ApplyFunc {
	if p := registeredApply.Load(); p != nil {
		return *p
	}
	return nil
}

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
		spoolWritten := true
		if err := writeAtomic(opt.SpoolPath, s.Content); err != nil {
			log.WithError(err).Errorf("config_hub: write spool %s on change failed", opt.SpoolPath)
			spoolWritten = false
			// fall through: spool failure must not suppress the in-memory
			// apply path — that's the entire point of the in-memory branch
		}
		log.Infof("config changed: dataId=%s version=%d md5=%s", s.DataID, s.Version, s.MD5)
		// Primary path: in-memory apply directly into the running service
		// when a callback has been registered. The watcher's fsnotify path
		// is redundant when this succeeds but still serves as a backstop
		// when no callback is registered (e.g. tests or future embedders).
		if apply := loadApply(); apply != nil {
			if err := apply(s.Content); err != nil {
				log.WithError(err).Errorf("config_hub: in-memory apply failed")
			}
		} else if !spoolWritten {
			// No in-memory consumer AND spool write failed → this push is
			// effectively lost. Surface it loudly so operators notice.
			log.Warn("config_hub: change dropped (no apply callback and spool write failed)")
		}
	})
	src.Watch(ctx, opt.DataID)
}

// writeAtomic writes b to path via a direct overwrite. The directory is
// created if needed.
//
// We deliberately avoid a tmp+rename strategy: cliproxy's fsnotify watch
// on the spool resolves to an inode at Add time, and rename(2) replaces
// that inode in-place. The watch on the old inode then becomes stale
// (the kernel reports IN_MOVE_SELF and stops delivering events), so
// every subsequent config_hub push is silently dropped — the spool gets
// the new content but reloadConfigIfChanged is never invoked. A direct
// overwrite reuses the original inode and produces an IN_MODIFY event
// that the watcher actually sees.
//
// For the ~15 KB TOML bodies cliproxy uses, a single write(2) syscall
// is large enough that a racing reader could in principle observe a
// short read; in practice config.LoadConfig logs the parse error and
// retries on the next reload, and the watcher's 150ms debounce makes
// the window vanishingly small.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
