package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/auth"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/config"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Re-authenticate with Google Photos (keeps existing config)",
	Long: `login opens a browser window to re-authenticate with Google Photos.

Your existing configuration (watched folder, album name, etc.) is preserved.
Use this when the app reports that it is not authenticated.`,
	RunE: runLogin,
}

func init() {
	loginCmd.Flags().StringVar(&flagCredFile, "creds", "", "Path to OAuth credentials JSON (defaults to saved credentials)")
}

func runLogin(cmd *cobra.Command, args []string) error {
	credFile := flagCredFile
	if credFile == "" {
		cfgDir, err := config.ConfigDir()
		if err != nil {
			return err
		}
		credFile = filepath.Join(cfgDir, "client_credentials.json")
	}

	mgr, err := auth.NewManagerFromFile(credFile)
	if err != nil {
		return fmt.Errorf("loading credentials from %q: %w\n\nRun 'folder-to-gphotos-album setup' if you have not set up the app yet", credFile, err)
	}

	ctx := context.Background()
	fmt.Println("Starting Google OAuth 2.0 flow...")
	if err := mgr.Setup(ctx); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	fmt.Println("Authentication successful! You can now start the daemon.")
	return nil
}
