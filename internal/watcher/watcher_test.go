package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---- isSupportedFile ----

func TestIsSupportedFile(t *testing.T) {
	supported := []string{
		"photo.jpg", "photo.JPG", "photo.jpeg",
		"image.png", "image.PNG",
		"anim.gif",
		"apple.heic", "apple.heif",
		"modern.webp",
		"bitmap.bmp",
		"scan.tiff", "scan.tif",
		"video.mp4", "video.MP4",
		"clip.mov",
		"old.avi",
	}
	for _, name := range supported {
		if !isSupportedFile(name) {
			t.Errorf("isSupportedFile(%q) = false, want true", name)
		}
	}

	unsupported := []string{
		"doc.pdf", "archive.zip", "readme.txt", "script.sh", "data.json", "noext",
		".DS_Store", ".hidden.jpg", ".localized",
	}
	for _, name := range unsupported {
		if isSupportedFile(name) {
			t.Errorf("isSupportedFile(%q) = true, want false", name)
		}
	}
}

// ---- watcher integration ----

// createWatchedFile writes a file into dir and returns its path.
func createWatchedFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("createWatchedFile: %v", err)
	}
	return path
}

func TestWatcher_EmitsUploadOnCreate(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go w.Run(ctx)

	path := createWatchedFile(t, dir, "photo.jpg", "data")

	select {
	case evt := <-w.Events():
		if evt.Kind != EventUpload {
			t.Errorf("event kind = %v, want EventUpload", evt.Kind)
		}
		if evt.Path != path {
			t.Errorf("event path = %q, want %q", evt.Path, path)
		}
	case <-ctx.Done():
		t.Error("timed out waiting for upload event")
	}
}

func TestWatcher_IgnoresUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go w.Run(ctx)

	createWatchedFile(t, dir, "readme.txt", "ignored")

	select {
	case evt := <-w.Events():
		t.Errorf("unexpected event for unsupported file: %+v", evt)
	case <-ctx.Done():
		// Correct: no event emitted.
	}
}

func TestWatcher_EmitsRemoveOnDelete(t *testing.T) {
	dir := t.TempDir()
	path := createWatchedFile(t, dir, "photo.jpg", "data")

	w, err := New(dir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go w.Run(ctx)

	// Drain any creation event first.
	drainTimeout := time.After(300 * time.Millisecond)
drainLoop:
	for {
		select {
		case <-w.Events():
		case <-drainTimeout:
			break drainLoop
		}
	}

	// Now delete the file.
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	select {
	case evt := <-w.Events():
		if evt.Kind != EventRemove {
			t.Errorf("event kind = %v, want EventRemove", evt.Kind)
		}
		if evt.Path != path {
			t.Errorf("event path = %q, want %q", evt.Path, path)
		}
	case <-ctx.Done():
		t.Error("timed out waiting for remove event")
	}
}

func TestWatcher_RemoveCancelsPendingUpload(t *testing.T) {
	dir := t.TempDir()

	// Long debounce so the upload timer won't fire before we delete.
	w, err := New(dir, 2*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	go w.Run(ctx)

	path := createWatchedFile(t, dir, "photo.jpg", "data")

	// Give the watcher a moment to register the create event but NOT fire the debounce.
	time.Sleep(100 * time.Millisecond)

	// Delete before debounce fires — we expect only EventRemove, not EventUpload.
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	select {
	case evt := <-w.Events():
		if evt.Kind != EventRemove {
			t.Errorf("expected EventRemove after early delete, got %v", evt.Kind)
		}
	case <-ctx.Done():
		t.Error("timed out waiting for remove event")
	}

	// Make sure no upload event follows.
	select {
	case evt, ok := <-w.Events():
		if ok && evt.Kind == EventUpload {
			t.Error("got unexpected EventUpload after file was deleted during debounce")
		}
	case <-time.After(200 * time.Millisecond):
		// Good — no spurious upload event.
	}
}

func TestWatcher_ClosesChannelOnContextCancel(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	cancel()

	select {
	case _, open := <-w.Events():
		if open {
			t.Error("expected events channel to be closed after context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Error("timed out waiting for events channel to close")
	}
}

func TestNew_NonExistentDir(t *testing.T) {
	_, err := New("/nonexistent/directory/that/does/not/exist", 50*time.Millisecond)
	if err == nil {
		t.Error("expected error when watching a non-existent directory")
	}
}

func TestWatcher_DebounceReset(t *testing.T) {
	dir := t.TempDir()

	// Use a longer debounce so the timer doesn't fire between the two writes.
	w, err := New(dir, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go w.Run(ctx)

	// Write the file twice quickly — the debounce timer should reset on the second write.
	path := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Expect exactly one upload event.
	select {
	case evt := <-w.Events():
		if evt.Kind != EventUpload {
			t.Errorf("expected EventUpload, got %v", evt.Kind)
		}
		if evt.Path != path {
			t.Errorf("event path = %q, want %q", evt.Path, path)
		}
	case <-ctx.Done():
		t.Error("timed out waiting for upload event")
	}

	// Ensure no second event follows.
	select {
	case evt, ok := <-w.Events():
		if ok {
			t.Errorf("unexpected second event: %+v", evt)
		}
	case <-time.After(400 * time.Millisecond):
		// Correct: only one event was emitted.
	}
}
