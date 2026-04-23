package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/trmfreitas/folder-to-gphotos-album/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "View or edit the daemon configuration",
	Long: `config prints the current configuration. Pass --edit to interactively
update the watched folder, album name, batch size, and debounce duration.`,
	RunE: runConfig,
}

var flagEdit bool

func init() {
	configCmd.Flags().BoolVar(&flagEdit, "edit", false, "Interactively edit the configuration")
}

func runConfig(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if !flagEdit {
		path, _ := config.ConfigPath()
		fmt.Printf("Config file: %s\n\n", path)
		fmt.Printf("watched_folder:       %s\n", cfg.WatchedFolder)
		fmt.Printf("album_name:           %s\n", cfg.AlbumName)
		fmt.Printf("batch_size:           %d\n", cfg.BatchSize)
		fmt.Printf("debounce_duration_ms: %d\n", cfg.DebounceDurationMs)
		return nil
	}

	reader := bufio.NewReader(os.Stdin)

	prompt := func(label, current string) (string, error) {
		fmt.Printf("%s [%s]: ", label, current)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		val := strings.TrimSpace(line)
		if val == "" {
			return current, nil
		}
		return val, nil
	}

	var err2 error

	cfg.WatchedFolder, err2 = prompt("Watched folder", cfg.WatchedFolder)
	if err2 != nil {
		return err2
	}

	cfg.AlbumName, err2 = prompt("Album name", cfg.AlbumName)
	if err2 != nil {
		return err2
	}

	batchStr, err2 := prompt("Batch size (1-50)", strconv.Itoa(cfg.BatchSize))
	if err2 != nil {
		return err2
	}
	if n, err := strconv.Atoi(batchStr); err == nil {
		cfg.BatchSize = n
	}

	debounceStr, err2 := prompt("Debounce duration ms", strconv.Itoa(cfg.DebounceDurationMs))
	if err2 != nil {
		return err2
	}
	if n, err := strconv.Atoi(debounceStr); err == nil {
		cfg.DebounceDurationMs = n
	}

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Println("Configuration saved.")
	return nil
}
