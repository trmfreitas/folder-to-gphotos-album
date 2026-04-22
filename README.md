# folder-to-gphotos-album

A Go daemon that watches a local folder and automatically uploads new photos and videos to a specific Google Photos album. Drop a file into the watched folder and it appears in your album within seconds.

## Features

- **Initial sync on startup** — uploads all files already in the folder and removes from the album any that have been deleted since the last run
- **Real-time folder watching** via [fsnotify](https://github.com/fsnotify/fsnotify) with write-debouncing to handle file copies correctly
- **Deletion sync** — moving or deleting a file from the watched folder removes it from the Google Photos album automatically
- **Automatic album management** — finds the target album by name or creates it if it doesn't exist
- **Duplicate prevention** — SHA256-based tracking persisted to disk; re-running the daemon never re-uploads the same file
- **Batch uploads** — groups files into requests of up to 50 items to minimise API quota usage
- **Exponential backoff** on rate-limit (HTTP 429) and transient network errors
- **Graceful shutdown** — SIGINT/SIGTERM drains the upload queue before exit
- **One-time OAuth 2.0 setup** with refresh tokens stored locally; the daemon runs unattended after that

### Supported file types

`.jpg` `.jpeg` `.png` `.gif` `.heic` `.heif` `.webp` `.bmp` `.tiff` `.tif` `.mp4` `.mov` `.avi`

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Go 1.21+ | `go version` to check |
| Google Cloud project | Free; used only to enable the API and create OAuth credentials |
| Google Photos account | The account the daemon will upload to |

---

## Google Cloud Setup

> This is a one-time step. You only need a client ID and secret — no billing required.

1. Go to [console.cloud.google.com](https://console.cloud.google.com/) and create a new project (e.g. *photos-uploader*).
2. Navigate to **APIs & Services → Library** and enable **Photos Library API**.
3. Navigate to **APIs & Services → OAuth consent screen**:
   - Choose **External**, fill in an app name, and add your Google account as a test user.
4. Navigate to **APIs & Services → Credentials → Create Credentials → OAuth client ID**:
   - Application type: **Desktop app**
   - Download the JSON file (e.g. `client_secret_xxx.json`).

---

## Installation

```bash
git clone https://github.com/trmfreitas/folder-to-gphotos-album.git
cd folder-to-gphotos-album
go build -o folder-to-gphotos-album ./cmd/folder-to-gphotos-album/
```

Optionally move the binary to somewhere on your `PATH`:

```bash
mv folder-to-gphotos-album /usr/local/bin/
```

---

## Usage

### 1. First-time setup

```bash
./folder-to-gphotos-album setup --creds /path/to/client_secret_xxx.json
```

Or without the flag — the wizard will prompt for the path interactively.

The wizard will:
- Copy your OAuth client credentials to `~/.folder-to-gphotos-album/client_credentials.json` (mode `0600`)
- Open your browser for Google account authorisation (one-time)
- Store the OAuth token at `~/.folder-to-gphotos-album/token.json` (mode `0600`)
- Ask which local folder to watch and which Google Photos album to use
- Save settings to `~/.folder-to-gphotos-album/config.json`

### 2. Run the daemon

```bash
./folder-to-gphotos-album daemon
```

Override any configured value with flags:

```bash
./folder-to-gphotos-album daemon \
  --folder ~/Desktop/export \
  --album "Family 2026" \
  --batch-size 25
```

On startup the daemon performs an initial sync:
- Uploads any files in the folder not yet in the album
- Removes from the album any files that were deleted while the daemon was not running

Drop any supported image or video into the watched folder — it will be uploaded within ~3 seconds (debounce window). Delete or move a file out of the folder and it will be removed from the album.

Press **Ctrl+C** to stop; the daemon drains any pending uploads before exiting.

### 3. View or edit configuration

```bash
./folder-to-gphotos-album config
```

---

## Configuration file

Stored at `~/.folder-to-gphotos-album/config.json`:

```json
{
  "watched_folder": "/Users/you/Pictures/export",
  "album_name": "Family 2026",
  "batch_size": 25,
  "debounce_duration_ms": 3000
}
```

| Field | Default | Description |
|---|---|---|
| `watched_folder` | *(required)* | Absolute path to the folder to monitor |
| `album_name` | *(required)* | Exact name of the Google Photos album |
| `batch_size` | `25` | Files per `mediaItems:batchCreate` request (1–50) |
| `debounce_duration_ms` | `3000` | Milliseconds to wait after last write event before uploading |

---

## State & credentials

All data lives under `~/.folder-to-gphotos-album/`:

| File | Purpose |
|---|---|
| `config.json` | Application settings |
| `client_credentials.json` | OAuth client ID and secret from Google Cloud Console (mode `0600`) |
| `token.json` | OAuth access + refresh token (mode `0600`) |
| `uploaded.json` | SHA256 hashes and media item IDs of uploaded files |

To reset authentication only:

```bash
rm ~/.folder-to-gphotos-album/token.json
./folder-to-gphotos-album setup --creds /path/to/client_secret_xxx.json
```

To reset everything:

```bash
rm -rf ~/.folder-to-gphotos-album/
./folder-to-gphotos-album setup
```

---

## Running as a launchd service (macOS)

Create `~/Library/LaunchAgents/com.trmfreitas.folder-to-gphotos-album.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.trmfreitas.folder-to-gphotos-album</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/folder-to-gphotos-album</string>
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/folder-to-gphotos-album.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/folder-to-gphotos-album.log</string>
</dict>
</plist>
```

Then load it:

```bash
launchctl load ~/Library/LaunchAgents/com.trmfreitas.folder-to-gphotos-album.plist
```

---

## Rate limits

The Google Photos Library API allows **10,000 requests per day** per project. Each `batchCreate` request uploads up to 50 files, so a default batch size of 25 supports roughly 250,000 files per day in practice. If you hit the limit, the daemon backs off and retries automatically.

---

## Project structure

```
folder-to-gphotos-album/
├── cmd/
│   └── folder-to-gphotos-album/
│       ├── main.go       # Cobra root command
│       ├── setup.go      # One-time OAuth + config wizard
│       ├── daemon.go     # Folder watcher + upload loop
│       └── config.go     # View/edit config subcommand
├── internal/
│   ├── auth/
│   │   ├── oauth.go      # OAuth 2.0 manager, token storage & refresh
│   │   └── browser.go    # Cross-platform browser launcher
│   ├── config/
│   │   └── config.go     # Config load/save/validate
│   ├── uploader/
│   │   └── uploader.go   # Google Photos REST API client
│   └── watcher/
│       └── watcher.go    # fsnotify wrapper with debounce
├── go.mod
├── go.sum
├── README.md
└── agents.md
```

---

## Dependencies

| Module | Version | Purpose |
|---|---|---|
| `github.com/fsnotify/fsnotify` | v1.9.0 | File system events |
| `golang.org/x/oauth2` | latest | OAuth 2.0 token management |
| `github.com/spf13/cobra` | v1.10+ | CLI framework |

All Google Photos API calls are made directly via `net/http` against the [Photos Library REST API v1](https://developers.google.com/photos/library/reference/rest).

---

## Limitations

- **Top-level folder only** — subdirectories are not watched. This is a design decision; add recursive watcher support if needed.
- **OAuth 2.0 required** — the Google Photos Library API does not support service accounts.
- **App-created media only** — albums and media created by this daemon can be listed/managed; items not created by this app are not visible via the API.
- **Deletion sync** — only files uploaded by this daemon (tracked in `uploaded.json`) can be removed from the album. Files added to the album through other means are not affected.

---

## License

MIT
