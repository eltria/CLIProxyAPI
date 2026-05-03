# config_hub Integration

CLIProxyAPI integrates with a Nacos-compatible **config_hub** server for push-based configuration delivery. This document covers the architecture, the historical bugs that shaped it, how to push updates, and how to verify they took effect.

## When this matters

Set the `CONFIGHUB_URL` family of environment variables (see "Environment" below) and config_hub becomes the source of truth for the on-disk TOML. Every WS-delivered change drives the same in-memory reload pipeline that file edits would, with no process restart.

When the env vars are absent, config_hub is bypassed entirely; cliproxy falls back to a plain `config.toml` on disk and the existing fsnotify-driven reload path. Everything below applies only to the config_hub path.

## Architecture

There are three actors:

1. **config_hub server** — hosts the canonical TOML, exposes Nacos-style HTTP for read / write, and notifies subscribers over WebSocket on change.
2. **`internal/cfg` subscriber** — opens the WS, receives change events, and dispatches them.
3. **cliproxy reload pipeline** — re-renders runtime state (router selectors, retry config, server middleware, auth manager) from a parsed `*config.Config`.

A single config_hub push reaches the running service via two paths:

```
                        config_hub WS push
                                │
                                ▼
                  internal/cfg/bootstrap.go
                       OnChange callback
                       │              │
       ┌───────────────┘              └───────────────┐
       ▼                                              ▼
  writeAtomic spool                            registered ApplyFunc
  (best-effort cache)                          (in-memory primary)
       │                                              │
       ▼                                              ▼
  fsnotify event                            service.ApplyConfigBytes
       │                                              │
       ▼                                              ▼
  watcher.reloadConfig                       config.LoadConfigBytes
       │                                              │
       └───────────────┐              ┌───────────────┘
                       ▼              ▼
              service.applyReloadedConfig
                (single state machine)
```

Both paths converge on `Service.applyReloadedConfig`, which is idempotent. The in-memory path skips the file system entirely (no debounce, no LoadConfig file read) and typically wins by 150–200 ms; the fsnotify path remains as a backstop in case the apply callback hasn't been registered yet (e.g., pre-`Run()`) or the service is embedded by a non-standard host.

## Config flow on a push

1. Operator pushes new TOML via `POST /v1/cs/configs` (see "Pushing updates" below).
2. config_hub stores it, bumps the version, and notifies WS subscribers.
3. `internal/cfg/source.go` receives the WS event and re-fetches the full body (the WS message is a notification, not a payload).
4. `internal/cfg/bootstrap.go` `OnChange` runs:
   - `writeAtomic(spoolPath, snapshot.Content)` — direct overwrite, no tmp+rename (see "fsnotify caveat" below).
   - Logs `config changed: dataId=... version=... md5=...`.
   - If `SetApply` has been called (always true after `cmd.StartService` runs), calls the registered `ApplyFunc` with the raw bytes.
5. `service.ApplyConfigBytes` parses via `config.LoadConfigBytes` (no file IO) and feeds the result to `applyReloadedConfig`.
6. Concurrently, fsnotify catches the spool write; after a 150 ms debounce the watcher's `reloadConfig` parses the spool and feeds the result to the **same** `applyReloadedConfig`. This second invocation is functionally a no-op when the in-memory path already ran.

`applyReloadedConfig` is the only place that:

- Re-creates the auth selector when routing strategy or session-affinity flags changed.
- Calls `applyRetryConfig` / `applyPprofConfig`.
- Calls `Server.UpdateClients(newCfg)`, which drives the router/middleware state machine including the `RemoteManagement.Disable` kill switch and the `secret-key` mount/unmount transitions.
- Updates `s.cfg` under `s.cfgMu`.
- Calls `coreManager.SetConfig` and `rebindExecutors`.

## Pitfalls (historical, codified)

These are non-obvious constraints that earlier revisions of the integration violated. The fixes are in tree but the reasoning lives here.

### fsnotify watch is bound to an inode, not a path

Inotify resolves the watched path to an inode at `Add()` time and reports events on that inode. A `rename(tmp, dest)` replaces `dest`'s inode with `tmp`'s; the original watch goes stale (kernel reports `IN_MOVE_SELF` and stops delivering events on subsequent writes).

Earlier `writeAtomic` used tmp+rename for crash-safety. The result was that **every config_hub push silently failed to trigger reload** — the spool got the new content but `reloadConfigIfChanged` was never invoked. The "config changed" log fired (it logs *after* the spool write returns) without any subsequent "config file changed, reloading" log, and behaviour diffs only ever applied after a process restart.

`writeAtomic` now uses a direct `os.WriteFile` to keep the original inode. The atomicity loss for ~15 KB TOML bodies is academic — `LoadConfig` retries on parse error and the watcher's 150 ms debounce absorbs torn reads in practice.

If you ever need to reintroduce atomicity, watch the **parent directory** instead of the file and filter by base name; rename-into-dir produces an `IN_MOVED_TO` event the watcher would catch.

### diff list is for logging, not gating

`internal/watcher/diff/config_diff.go::BuildConfigChangeDetails` enumerates changed fields and returns a string list. **It does not gate reload.** `reloadConfig` runs unconditionally when fsnotify (or in-memory apply) fires; the diff list only feeds a debug log.

This is easy to misread as "if a field isn't in the diff, it won't hot-reload." Earlier `RemoteManagement.Disable` wasn't in the diff list, which was misdiagnosed as the cause of disable=true not taking effect — the real cause was the fsnotify rename bug above. The Disable entry was added anyway because the omission still produced misleading logs ("no material config field changes detected" while the kill switch flipped), but it isn't load-bearing.

When adding a new config field, you only need to add it to `BuildConfigChangeDetails` if you want it to show up in the human-readable change log. The hot-reload itself is automatic via `applyReloadedConfig`.

### Spool file is a cache, not the source of truth

When config_hub is enabled, `redirectForConfigHub` (in `cmd/server/main.go`) points `configFilePath` at `config.confighub.toml` instead of `config.toml`. This avoids collision with read-only Zeabur Config Editor mounts on the original `config.toml` path.

The spool file is best-effort. A failed spool write **must not** suppress hot reload — `OnChange` continues to the in-memory apply even when `writeAtomic` errors. The spool exists for two reasons only:

1. Cold-boot fallback: if config_hub is unreachable on startup, `LoadConfigOptional` reads whatever the spool last had.
2. Inspection: `cat /CLIProxyAPI/config.confighub.toml` shows what the running server thinks it has.

### bcrypt write-back is gated on a non-empty `configFile`

`internal/config/config.go::parseConfigData` hashes plaintext `[remote-management].secret-key` on every load. The hash is written back to disk via `updateTOMLScalarInPlace` **only when the caller passes a non-empty `configFile`**.

`LoadConfigOptional` passes the spool path (write-back active). `LoadConfigBytes` (used by the in-memory apply path) passes `""` (write-back skipped). This avoids two failure modes:

- Pushing plaintext via config_hub on every change → hash → write spool → fsnotify → re-parse hash (no-op via `looksLikeBcrypt`) — the in-memory path doesn't loop because it never writes.
- Hash on load → spool diverges from config_hub source → next config_hub push overwrites with plaintext again — operator never sees the canonical hashed form.

**Recommendation**: push the bcrypt hash directly into config_hub, not plaintext. `looksLikeBcrypt` (matches `$2a$/$2b$/$2y$` prefix) short-circuits both the hashing and the write-back, eliminating round-trip churn.

## Pushing updates

The config_hub write API is Nacos-compatible:

```bash
curl -fsS -X POST \
  -H "Authorization: Bearer ${CONFIGHUB_API_KEY}" \
  --data-urlencode "dataId=${CONFIGHUB_DATAID}" \
  --data-urlencode "group=${CONFIGHUB_GROUP}" \
  --data-urlencode "namespace=${CONFIGHUB_NAMESPACE}" \
  --data-urlencode "type=toml" \
  --data-urlencode "content@/path/to/new.toml" \
  "${CONFIGHUB_URL}/v1/cs/configs"
```

Response shape:

```json
{ "id": "<uuid>", "md5": "<32 hex>", "op": "update", "version": <int> }
```

Notes:

- Every POST bumps the version, even if the body is byte-identical to the prior revision.
- `Content-Type` must be `application/x-www-form-urlencoded` (curl's `--data-urlencode` does this automatically).
- The legacy raw `Content-Type: text/plain` body returns `400 invalid character '#' looking for beginning of value` — the server is parsing the body as JSON when text/plain is sent, which then fails on TOML's `#` comments.
- `PUT` returns `405 Method Not Allowed`; only `GET` and `POST` are accepted on `/v1/cs/configs`.

## Verifying a push took effect

For any field that changes externally observable behaviour (e.g. `[remote-management].disable`, `secret-key`, `routing.strategy`), the canonical verification flow is:

1. Capture the response from the POST: confirm `version` bumped and `md5` matches your local file's md5.
2. Tail server logs for the OnChange and reload sequence:
   ```
   bootstrap.go:N  config changed: dataId=app.toml version=N md5=...
   server.go:N     management routes (enabled|disabled) via ...     # if mgmt-related
   config_reload.go:N  config file changed, reloading: ...
   config_reload.go:N  config successfully reloaded, ...
   ```
   Two `server clients and configuration updated` lines in close succession are normal — one from the in-memory path, one from the fsnotify backstop.
3. curl the affected endpoint and check the response status / headers / body matches expected.

The in-memory path typically lands behavioural changes within ~200 ms of the POST; the fsnotify path follows ~150 ms later. If you don't see the change after 2 s, look in this order:

- Did the WS push reach cliproxy? (`config changed` log present?)
- Was `SetApply` registered? (Usually yes after `cmd.StartService` finishes Build; pre-Build pushes only have the fsnotify path.)
- Did fsnotify fire? (`config file changed, reloading` log present?) If the push reached cliproxy but neither path produced reload logs, the `writeAtomic` regression is back.
- Did `applyReloadedConfig` see the new field value? Compare the live `/v0/management/config.toml` md5 to the config_hub `md5` you got from the POST.

## Environment

| Variable | Purpose | Required? |
|---|---|---|
| `CONFIGHUB_URL` | Base URL of config_hub server | Yes (else file-based fallback) |
| `CONFIGHUB_API_KEY` | Bearer token for read + write | Yes |
| `CONFIGHUB_NAMESPACE` | Namespace identifier | Yes |
| `CONFIGHUB_GROUP` | Group identifier | Defaults to `prod` |
| `CONFIGHUB_DATAID` | Config entry name | Defaults to `app.toml` |
| `CONFIGHUB_SPOOL_PATH` | Override spool path | Defaults to `<configFilePath>.confighub.toml` |

The spool path defaults work for Zeabur deployments where the original `config.toml` may be a read-only Config Editor mount. Override only when you need to point the spool at a writable volume that doesn't sit next to the original.

## Cold-boot ordering

`cmd/server/main.go` runs the boot sequence:

1. `redirectForConfigHub(originalPath)` → spool path.
2. `seedConfigHubSpool(spool, original)` → ensure the spool exists (copies from original or `config.example.toml`).
3. `bootstrapConfigHub(ctx, spool)` → synchronous initial GET writes the spool, then dispatches a watch goroutine.
4. `LoadConfigOptional(spool, isCloudDeploy)` → parse the (now-populated) spool.
5. `cmd.StartService` → `Build()` then `confighub.SetApply(service.ApplyConfigBytes)` then `Run()`.

Steps 1–4 run synchronously, so `LoadConfigOptional` always sees the latest config_hub content if the initial GET succeeded. On GET failure, `LoadConfigOptional` falls back to whatever was seeded (typically `config.example.toml`), and the WS subscription will overwrite it as soon as the remote becomes reachable.

`SetApply` is registered between `Build()` and `Run()`. WS pushes that arrive in this tiny window land via the fsnotify path only; pushes after `Run()` use both paths.

## Related files

- `internal/cfg/bootstrap.go` — WS subscription, OnChange dispatch, ApplyFunc registry.
- `internal/cfg/source.go` — Nacos-compatible HTTP / WS client.
- `internal/config/config.go` — `LoadConfigOptional` / `LoadConfigBytes` / `parseConfigData`.
- `sdk/cliproxy/service.go` — `applyReloadedConfig`, `ApplyConfigBytes`.
- `internal/watcher/config_reload.go` — fsnotify-driven reload pipeline.
- `internal/watcher/events.go` — fsnotify Add + handleEvent (caveats above apply here).
- `internal/watcher/diff/config_diff.go` — `BuildConfigChangeDetails` (logging only, see pitfalls).
- `cmd/server/main.go` — `redirectForConfigHub`, `seedConfigHubSpool`, `bootstrapConfigHub`.
- `internal/cmd/run.go` — wires `confighub.SetApply` to `service.ApplyConfigBytes`.
