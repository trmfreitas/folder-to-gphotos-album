package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "folder-to-gphotos-album",
	Short: "Upload a local folder to a Google Photos album",
	Long: `folder-to-gphotos-album watches a local folder and automatically uploads
new photos and videos to a Google Photos album.

Run 'folder-to-gphotos-album setup' on first use to authenticate and configure.
Run 'folder-to-gphotos-album login' if the daemon reports it is not authenticated.`,
}

func main() {
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(configCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
