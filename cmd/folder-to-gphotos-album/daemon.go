package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/auth"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/config"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/uploader"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/watcher"
)

var (
	flagFolder    string
	flagAlbum     string
	flagCredFile  string
	flagBatchSize int
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the folder watcher and upload daemon",
	Long: `daemon watches the configured folder and uploads any new image or video files
to your Google Photos album automatically. Press Ctrl+C to stop gracefully.

Flags override the values stored by 'folder-to-gphotos-album setup'.`,
	RunE: runDaemon,
}

func init() {
	daemonCmd.Flags().StringVar(&flagFolder, "folder", "", "Override watched folder path")
	daemonCmd.Flags().StringVar(&flagAlbum, "album", "", "Override target album name")
	daemonCmd.Flags().StringVar(&flagCredFile, "creds", "", "Path to OAuth credentials JSON (only needed on first run)")
	daemonCmd.Flags().IntVar(&flagBatchSize, "batch-size", 0, "Override batch size (1-50)")

	setupCmd.Flags().StringVar(&flagCredFile, "creds", "", "Path to OAuth credentials JSON")
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Apply CLI flag overrides.
	if flagFolder != "" {
		cfg.WatchedFolder = flagFolder
	}
	if flagAlbum != "" {
		cfg.AlbumName = flagAlbum
	}
	if flagBatchSize > 0 {
		cfg.BatchSize = flagBatchSize
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	credFile := flagCredFile
	if credFile == "" {
		credFile = defaultCredFilePath()
	}

	var mgr *auth.Manager
	if credFile != "" {
		if _, err := os.Stat(credFile); err == nil {
			mgr, err = auth.NewManagerFromFile(credFile)
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
		}
	}

	if mgr == nil {
		return fmt.Errorf("no credentials file found; run 'folder-to-gphotos-album setup' first or use --creds")
	}

	if !mgr.IsAuthenticated() {
		return fmt.Errorf("not authenticated; run 'folder-to-gphotos-album setup' first")
	}

	// Root context with signal cancellation for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("[daemon] received %s — shutting down gracefully...", sig)
		cancel()
	}()

	httpClient, err := mgr.HTTPClient(ctx)
	if err != nil {
		return fmt.Errorf("creating HTTP client: %w", err)
	}

	cfgDir, _ := config.ConfigDir()
	stateFile := filepath.Join(cfgDir, "uploaded.json")

	up, err := uploader.New(ctx, httpClient, cfg.AlbumName, stateFile)
	if err != nil {
		return fmt.Errorf("initializing uploader: %w", err)
	}

	debounceDur := time.Duration(cfg.DebounceDurationMs) * time.Millisecond
	w, err := watcher.New(cfg.WatchedFolder, debounceDur)
	if err != nil {
		return fmt.Errorf("starting watcher: %w", err)
	}

	log.Printf("[daemon] started — watching %q → album %q", cfg.WatchedFolder, cfg.AlbumName)

	// Initial sync: upload any existing files in the folder that haven't been uploaded yet,
	// and remove from the album any files that are tracked but no longer exist on disk.
	if entries, err := os.ReadDir(cfg.WatchedFolder); err == nil {
		var existing []string
		presentPaths := make(map[string]bool)
		for _, e := range entries {
			if !e.IsDir() {
				p := filepath.Join(cfg.WatchedFolder, e.Name())
				existing = append(existing, p)
				presentPaths[p] = true
			}
		}

		// Find tracked paths that are no longer on disk.
		var missing []string
		for _, p := range up.TrackedPaths() {
			if !presentPaths[p] {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			log.Printf("[daemon] initial sync: removing %d files no longer on disk", len(missing))
			if err := up.Remove(ctx, missing); err != nil {
				log.Printf("[daemon] initial sync remove error: %v", err)
			}
		}

		if len(existing) > 0 {
			log.Printf("[daemon] initial sync: found %d files in folder", len(existing))
			if err := up.Upload(ctx, existing); err != nil {
				log.Printf("[daemon] initial sync error: %v", err)
			}
		}
	} else {
		log.Printf("[daemon] initial sync: could not read folder: %v", err)
	}

	go w.Run(ctx)

	// Collect watcher events and batch them by kind.
	uploadBatch := make([]string, 0, cfg.BatchSize)
	removeBatch := make([]string, 0, cfg.BatchSize)

	uploadTimer := time.NewTimer(5 * time.Second)
	uploadTimer.Stop()
	removeTimer := time.NewTimer(5 * time.Second)
	removeTimer.Stop()

	flushUploads := func() {
		if len(uploadBatch) == 0 {
			return
		}
		toUpload := make([]string, len(uploadBatch))
		copy(toUpload, uploadBatch)
		uploadBatch = uploadBatch[:0]
		if err := up.Upload(ctx, toUpload); err != nil {
			log.Printf("[daemon] upload error: %v", err)
		}
	}

	flushRemoves := func() {
		if len(removeBatch) == 0 {
			return
		}
		toRemove := make([]string, len(removeBatch))
		copy(toRemove, removeBatch)
		removeBatch = removeBatch[:0]
		if err := up.Remove(ctx, toRemove); err != nil {
			log.Printf("[daemon] remove error: %v", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			uploadTimer.Stop()
			removeTimer.Stop()
			flushUploads()
			flushRemoves()
			log.Println("[daemon] shutdown complete.")
			return nil

		case evt, ok := <-w.Events():
			if !ok {
				flushUploads()
				flushRemoves()
				return nil
			}
			switch evt.Kind {
			case watcher.EventUpload:
				uploadBatch = append(uploadBatch, evt.Path)
				if len(uploadBatch) >= cfg.BatchSize {
					uploadTimer.Stop()
					flushUploads()
				} else {
					uploadTimer.Reset(5 * time.Second)
				}
			case watcher.EventRemove:
				removeBatch = append(removeBatch, evt.Path)
				if len(removeBatch) >= cfg.BatchSize {
					removeTimer.Stop()
					flushRemoves()
				} else {
					removeTimer.Reset(5 * time.Second)
				}
			}

		case <-uploadTimer.C:
			flushUploads()

		case <-removeTimer.C:
			flushRemoves()
		}
	}
}

// defaultCredFilePath returns the path to the stored OAuth credentials, if present.
func defaultCredFilePath() string {
	dir, err := config.ConfigDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(dir, "client_credentials.json"),
		filepath.Join(dir, "client_secret.json"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
