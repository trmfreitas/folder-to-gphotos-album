package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/auth"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/config"
)

func init() {
	setupCmd.Flags().StringVar(&flagCredFile, "creds", "", "Path to OAuth credentials JSON")
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "First-time setup: authenticate with Google and configure the daemon",
	Long: `setup guides you through OAuth 2.0 authentication with Google Photos and
saves your watched folder and album name.

You will need a Google Cloud OAuth 2.0 client credentials JSON file. Download it
from the Google Cloud Console (APIs & Services → Credentials → OAuth 2.0 Client IDs).`,
	RunE: runSetup,
}

func runSetup(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("=== folder-to-gphotos-album setup ===")
	fmt.Println()

	// Locate or prompt for the OAuth credentials file.
	credFile := flagCredFile
	if credFile == "" {
		fmt.Print("Path to OAuth 2.0 client credentials JSON: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		credFile = strings.TrimSpace(line)
	}

	if _, err := os.Stat(credFile); err != nil {
		return fmt.Errorf("credentials file %q not found: %w", credFile, err)
	}

	// Copy credentials to config dir before the OAuth flow so they are always
	// available to the daemon even if setup is interrupted mid-flow.
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	destCred := filepath.Join(cfgDir, "client_credentials.json")
	if credFile != destCred {
		data, err := os.ReadFile(credFile)
		if err != nil {
			return fmt.Errorf("reading credentials file: %w", err)
		}
		if err := os.WriteFile(destCred, data, 0600); err != nil {
			return fmt.Errorf("copying credentials: %w", err)
		}
		fmt.Printf("✓ Credentials saved to %s\n", destCred)
	}

	mgr, err := auth.NewManagerFromFile(credFile)
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}

	ctx := context.Background()
	fmt.Println()
	fmt.Println("Starting Google OAuth 2.0 flow...")
	if err := mgr.Setup(ctx); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Authentication successful!")
	fmt.Println()

	// Load or create config.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	fmt.Print("Folder to watch (absolute path): ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	cfg.WatchedFolder = strings.TrimSpace(line)

	fmt.Print("Google Photos album name: ")
	line, err = reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	cfg.AlbumName = strings.TrimSpace(line)

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Println()
	fmt.Printf("Config saved. Watching %q → album %q\n", cfg.WatchedFolder, cfg.AlbumName)
	fmt.Println("Run 'folder-to-gphotos-album daemon' to start the upload daemon.")
	return nil
}
