# agents.md — Agentic Development Guide

> This file is intended for AI coding agents working on this repository.
> It contains architectural decisions, known constraints, patterns used, and
> important context that is not obvious from reading the code alone.

---

## Module identity

| Key | Value |
|---|---|
| Go module | `github.com/trmfreitas/folder-to-gphotos-album` |
| Binary name | `folder-to-gphotos-album` |
| Entry point | `cmd/folder-to-gphotos-album/main.go` |
| Min Go version | 1.21 (uses `any` alias, `os.ReadFile`, generics-compatible toolchain) |

---

## Repository layout

```
cmd/folder-to-gphotos-album/     # CLI layer (Cobra). One file per subcommand.
internal/auth/         # OAuth 2.0 lifecycle (token acquisition, storage, refresh)
internal/config/       # Config struct, load/save, validation
internal/uploader/     # Google Photos REST API client (no generated SDK)
internal/watcher/      # fsnotify wrapper with per-file debounce timers
```

All cross-package dependencies flow **inward** only:
- `cmd` imports `internal/*`
- `internal/auth` imports `internal/config`
- No circular dependencies

---

## Critical design decisions

### No generated Google Photos SDK
`google.golang.org/api/photoslibrary/v1` does **not exist** as a package in the
published `google.golang.org/api` module. All Google Photos API calls are made
directly via `net/http` in `internal/uploader/uploader.go`. Do not attempt to use
a generated client — there is none available as of Go module resolution.

### OAuth 2.0 only — no service accounts
The Google Photos Library API explicitly rejects service account tokens. The auth
flow is:
1. First run: `setup` command → browser → authorization code → token exchange
2. Tokens (access + refresh) stored at `~/.folder-to-gphotos-album/credentials.json` (0600)
3. `auth.Manager.HTTPClient()` creates an `oauth2.TokenSource` that auto-refreshes
   expired access tokens using the stored refresh token

### File watching is top-level only (non-recursive by design)
`fsnotify` is configured on a single directory. Subdirectory events are not
propagated. This is intentional per the project spec. If recursive watching is
needed, iterate over `os.ReadDir` at startup and call `fsnotify.Watcher.Add` for
each subdirectory.

### Debounce pattern
Each file path gets its own `*time.Timer` in a `map[string]*time.Timer` inside
the watcher loop. On `Create`/`Write`/`Rename` events the timer is reset (not
accumulated). The timer fires after `debounceDuration` (default 3 s) and emits
an `Event{Kind: EventUpload, Path: ...}` to the `events` channel. This prevents
uploading half-written files.

On `Remove` events the path is emitted immediately as `Event{Kind: EventRemove, Path: ...}`
(no debounce needed). Any pending upload debounce timer for that path is cancelled,
preventing a race between a remove and a pending upload.

### Duplicate prevention and sync state
`internal/uploader` maintains a persistent state struct with two maps:
- `ByHash map[string]string` — SHA256 hex digest → Google Photos media item ID
- `ByPath map[string]string` — absolute file path → SHA256 hex digest

This is loaded from `~/.folder-to-gphotos-album/uploaded.json` on startup and saved
after every successful batch. The hash-keyed map prevents re-uploading the same file
content even if the file is renamed or moved. The path-keyed map enables deletion sync:
when a file is removed, its path is looked up in `ByPath` to find its hash, then its
media item ID is found in `ByHash` and the item is removed from the album.

### Batch upload flow (two-phase)
1. **Upload bytes** → `POST /v1/uploads` with raw body → returns an opaque upload token
2. **Batch create** → `POST /v1/mediaItems:batchCreate` with up to 50 upload tokens + album ID

Both phases have their own retry loop with exponential backoff (`backoff(attempt)`
returns 1 s, 2 s, 4 s … capped at 30 s).

### Graceful shutdown
`daemon.go` installs a `signal.Notify` handler for `SIGINT`/`SIGTERM` that calls
`cancel()` on the root context. The main select loop checks `ctx.Done()` and calls
`flush()` before returning. The watcher goroutine also exits cleanly when its
context is cancelled, closing the `events` channel which unblocks the main loop.

---

## Key files and their responsibilities

| File | Responsibility |
|---|---|
| `cmd/folder-to-gphotos-album/main.go` | Register Cobra subcommands; `main()` entry |
| `cmd/folder-to-gphotos-album/setup.go` | Interactive wizard: OAuth flow + config init |
| `cmd/folder-to-gphotos-album/daemon.go` | Watcher event loop, batch coalescing, upload dispatch |
| `cmd/folder-to-gphotos-album/config.go` | Read-only config printer + interactive editor |
| `internal/auth/oauth.go` | `Manager` struct: token storage, browser flow, HTTP client factory |
| `internal/auth/browser.go` | `openBrowser(url)` — platform-specific `open`/`xdg-open`/`start` |
| `internal/config/config.go` | `Config` struct, `Load()`, `Save()`, `Validate()`, `ConfigDir()` |
| `internal/uploader/uploader.go` | `Uploader` struct, upload pipeline, album CRUD, state persistence |
| `internal/watcher/watcher.go` | `Watcher` struct, debounce map, `Run(ctx)` goroutine |

---

## Runtime data locations

All runtime data is stored under `~/.folder-to-gphotos-album/` (mode `0700`):

| File | Created by | Purpose |
|---|---|---|
| `config.json` | `setup` / `config` commands | Watched folder, album name, batch size, debounce ms |
| `credentials.json` | `auth.Manager.saveToken()` | OAuth access + refresh token (mode 0600) |
| `uploaded.json` | `uploader.saveState()` | `ByHash` (hash→mediaItemID) and `ByPath` (path→hash) maps |

`config.ConfigDir()` returns this path, deriving it from `os.UserHomeDir()`.

---

## Dependencies summary

| Module | Used in | Notes |
|---|---|---|
| `github.com/fsnotify/fsnotify` | `internal/watcher` | Cross-platform FS events |
| `golang.org/x/oauth2` | `internal/auth` | Token source + HTTP client |
| `golang.org/x/oauth2/google` | `internal/auth` | Google OAuth endpoint |
| `github.com/spf13/cobra` | `cmd/folder-to-gphotos-album` | CLI subcommand framework |

No generated Google API client library is used. All HTTP calls are hand-written
against the [Google Photos Library API REST reference](https://developers.google.com/photos/library/reference/rest).

---

## API endpoints used

| Method | Endpoint | Purpose |
|---|---|---|
| `GET` | `/v1/albums?pageSize=50` | List existing albums (paginated) |
| `POST` | `/v1/albums` | Create a new album |
| `POST` | `/v1/uploads` | Upload raw file bytes → upload token |
| `POST` | `/v1/mediaItems:batchCreate` | Create media items in album from tokens |
| `POST` | `/v1/albums/{albumId}:batchRemoveMediaItems` | Remove up to 50 media items from album |

Base URL: `https://photoslibrary.googleapis.com/v1`

OAuth scopes requested:
- `https://www.googleapis.com/auth/photoslibrary.appendonly` — upload new items
- `https://www.googleapis.com/auth/photoslibrary.readonly.appcreateddata` — list albums/items created by this app

---

## Known limitations and gotchas

- **`Albums.List` returns only app-created albums** when using `appendonly` scope.
  If the target album was created manually in Google Photos, the API will not find it
  and will create a new one with the same name. Use albums created by the daemon.

- **Upload tokens expire after ~23 hours.** If the daemon uploads bytes but the
  daemon crashes before `batchCreate`, those tokens are lost. The file hash will
  not be in `uploaded.json`, so the next run will re-upload — this is the correct
  and safe behaviour.

- **`batchCreate` partial failures** — the API returns per-item status codes.
  Items with `status.code != 0` are logged and skipped; their hashes are NOT saved,
  so they will be retried on the next event or daemon restart.

- **Large files** — files are read entirely into memory (`os.ReadFile`) before
  uploading. For very large videos, consider switching to streaming uploads.

- **File moved out of watched folder during debounce** — `os.Stat` is called after
  the debounce timer fires. If the file is gone, the event is silently dropped.

---

## Adding a new subcommand

1. Create `cmd/folder-to-gphotos-album/<name>.go` with `package main`
2. Define a `var <name>Cmd = &cobra.Command{...}` and a `run<Name>` function
3. Register it in `cmd/folder-to-gphotos-album/main.go` with `rootCmd.AddCommand(<name>Cmd)`
4. If the command needs auth, call `auth.NewManagerFromFile(credFile)` then
   `mgr.HTTPClient(ctx)` — see `daemon.go` for the full pattern

---

## Build & test

```bash
# Build
go build -o folder-to-gphotos-album ./cmd/folder-to-gphotos-album/

# Build for Linux from macOS
GOOS=linux GOARCH=amd64 go build -o folder-to-gphotos-album-linux ./cmd/folder-to-gphotos-album/

# Vet
go vet ./...

# No tests yet — integration tests require live Google OAuth credentials.
# When adding unit tests, mock the HTTP client in uploader_test.go using
# net/http/httptest.NewServer to simulate the Photos API.
```

---

## Environment for local development

The daemon does **not** read environment variables; all configuration is file-based.
However, during development you can override via flags:

```bash
./folder-to-gphotos-album daemon \
  --creds ~/Downloads/client_secret.json \
  --folder /tmp/test-photos \
  --album "Test Album Dev"
```

The `--creds` flag lets you point to the raw OAuth client JSON without running
`setup` first, useful for CI or fresh environments.
