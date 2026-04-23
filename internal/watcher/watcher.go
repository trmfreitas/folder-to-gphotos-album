package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// SupportedExtensions is the set of image/video file extensions the daemon will process.
var SupportedExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".heic": true,
	".heif": true,
	".webp": true,
	".bmp":  true,
	".tiff": true,
	".tif":  true,
	".mp4":  true,
	".mov":  true,
	".avi":  true,
}

// EventKind distinguishes upload-ready events from deletion events.
type EventKind int

const (
	EventUpload EventKind = iota // file created/modified and ready to upload
	EventRemove                  // file deleted from the watched folder
)

// Event represents a file system change detected by the watcher.
type Event struct {
	Path string
	Kind EventKind
}

// Watcher monitors a directory and emits upload-ready file events.
type Watcher struct {
	dir         string
	debounceDur time.Duration
	events      chan Event
	fw          *fsnotify.Watcher
}

// New creates a Watcher for the given directory.
// debounceDur is how long to wait after the last write event before treating a file as ready.
func New(dir string, debounceDur time.Duration) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return nil, err
	}
	return &Watcher{
		dir:         dir,
		debounceDur: debounceDur,
		events:      make(chan Event, 100),
		fw:          fw,
	}, nil
}

// Events returns the channel of upload-ready file events.
func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Run starts the watch loop, blocking until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	defer close(w.events)
	defer w.fw.Close()

	// pending maps file path → debounce timer for upload events.
	pending := make(map[string]*time.Timer)

	// timerFired carries paths whose debounce timer has elapsed.
	// Using a channel ensures all pending-map mutations happen on this goroutine
	// and avoids a data race between the timer goroutine and the Run goroutine.
	timerFired := make(chan string, 100)

	emitUpload := func(path string) {
		if !isSupportedFile(path) {
			return
		}
		if t, ok := pending[path]; ok {
			t.Reset(w.debounceDur)
			return
		}
		pending[path] = time.AfterFunc(w.debounceDur, func() {
			select {
			case timerFired <- path:
			case <-ctx.Done():
			}
		})
	}

	emitRemove := func(path string) {
		if !isSupportedFile(path) {
			return
		}
		// Cancel any pending upload debounce for this path.
		if t, ok := pending[path]; ok {
			t.Stop()
			delete(pending, path)
		}
		select {
		case w.events <- Event{Path: path, Kind: EventRemove}:
		case <-ctx.Done():
		}
	}

	log.Printf("[watcher] watching %s", w.dir)

	for {
		select {
		case <-ctx.Done():
			// Cancel pending timers.
			for _, t := range pending {
				t.Stop()
			}
			return

		case path := <-timerFired:
			delete(pending, path)
			// Verify file still exists and isn't a directory.
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			select {
			case w.events <- Event{Path: path, Kind: EventUpload}:
			case <-ctx.Done():
				return
			}

		case evt, ok := <-w.fw.Events:
			if !ok {
				return
			}
			log.Printf("[watcher] event: %s %s", evt.Op, evt.Name)
			switch {
			case evt.Has(fsnotify.Create) || evt.Has(fsnotify.Write):
				emitUpload(evt.Name)
			case evt.Has(fsnotify.Rename):
				// On macOS, moving a file to Trash or out of the folder fires
				// Rename on the source path. If the file is gone, treat as removal.
				if _, err := os.Stat(evt.Name); os.IsNotExist(err) {
					emitRemove(evt.Name)
				} else {
					emitUpload(evt.Name)
				}
			case evt.Has(fsnotify.Remove):
				emitRemove(evt.Name)
			}

		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			log.Printf("[watcher] error: %v", err)
		}
	}
}

// isSupportedFile reports whether the path has a supported media extension.
// Dotfiles (e.g. .DS_Store) are always excluded.
func isSupportedFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return SupportedExtensions[ext]
}
