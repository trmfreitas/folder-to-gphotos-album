package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", cfg.BatchSize, DefaultBatchSize)
	}
	if cfg.DebounceDurationMs != DefaultDebounceDurMs {
		t.Errorf("DebounceDurationMs = %d, want %d", cfg.DebounceDurationMs, DefaultDebounceDurMs)
	}
	if cfg.WatchedFolder != "" {
		t.Errorf("WatchedFolder should be empty by default")
	}
	if cfg.AlbumName != "" {
		t.Errorf("AlbumName should be empty by default")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	// Override the config path by writing directly.
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &Config{
		WatchedFolder:      dir,
		AlbumName:          "Test Album",
		BatchSize:          10,
		DebounceDurationMs: 500,
	}

	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := loadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.WatchedFolder != cfg.WatchedFolder {
		t.Errorf("WatchedFolder = %q, want %q", loaded.WatchedFolder, cfg.WatchedFolder)
	}
	if loaded.AlbumName != cfg.AlbumName {
		t.Errorf("AlbumName = %q, want %q", loaded.AlbumName, cfg.AlbumName)
	}
	if loaded.BatchSize != cfg.BatchSize {
		t.Errorf("BatchSize = %d, want %d", loaded.BatchSize, cfg.BatchSize)
	}
	if loaded.DebounceDurationMs != cfg.DebounceDurationMs {
		t.Errorf("DebounceDurationMs = %d, want %d", loaded.DebounceDurationMs, cfg.DebounceDurationMs)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := loadFromPath("/nonexistent/path/config.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want default %d", cfg.BatchSize, DefaultBatchSize)
	}
}

func TestLoadInvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte("not json {{{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadFromPath(cfgPath)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestLoadClampsOutOfRangeBatchSize(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	raw := `{"watched_folder":"/tmp","album_name":"x","batch_size":999,"debounce_duration_ms":100}`
	if err := os.WriteFile(cfgPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFromPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("expected out-of-range batch_size clamped to %d, got %d", DefaultBatchSize, cfg.BatchSize)
	}
}

func TestLoadClampsZeroDebounce(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	raw := `{"watched_folder":"/tmp","album_name":"x","batch_size":10,"debounce_duration_ms":0}`
	if err := os.WriteFile(cfgPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFromPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DebounceDurationMs != DefaultDebounceDurMs {
		t.Errorf("expected zero debounce clamped to %d, got %d", DefaultDebounceDurMs, cfg.DebounceDurationMs)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     Config{WatchedFolder: dir, AlbumName: "album"},
			wantErr: false,
		},
		{
			name:    "missing folder",
			cfg:     Config{AlbumName: "album"},
			wantErr: true,
		},
		{
			name:    "missing album",
			cfg:     Config{WatchedFolder: dir},
			wantErr: true,
		},
		{
			name:    "nonexistent folder",
			cfg:     Config{WatchedFolder: "/nonexistent/path/xyz", AlbumName: "album"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateFileNotDir(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notadir.txt")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WatchedFolder: f, AlbumName: "album"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when watched_folder is a file, not a directory")
	}
}

// ---- ConfigDir / ConfigPath / Load / Save ----

func TestConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	want := filepath.Join(dir, DefaultConfigDir)
	if got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	want := filepath.Join(dir, DefaultConfigDir, DefaultConfigFile)
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestLoad_ReturnsDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", cfg.BatchSize, DefaultBatchSize)
	}
	if cfg.DebounceDurationMs != DefaultDebounceDurMs {
		t.Errorf("DebounceDurationMs = %d, want %d", cfg.DebounceDurationMs, DefaultDebounceDurMs)
	}
}

func TestLoad_ReadsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgDir := filepath.Join(dir, DefaultConfigDir)
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"watched_folder":"/tmp","album_name":"LoadTest","batch_size":12,"debounce_duration_ms":750}`
	if err := os.WriteFile(filepath.Join(cfgDir, DefaultConfigFile), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AlbumName != "LoadTest" {
		t.Errorf("AlbumName = %q, want LoadTest", cfg.AlbumName)
	}
	if cfg.BatchSize != 12 {
		t.Errorf("BatchSize = %d, want 12", cfg.BatchSize)
	}
	if cfg.DebounceDurationMs != 750 {
		t.Errorf("DebounceDurationMs = %d, want 750", cfg.DebounceDurationMs)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgDir := filepath.Join(dir, DefaultConfigDir)
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, DefaultConfigFile), []byte("not json {{"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid JSON config file")
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := &Config{
		WatchedFolder:      "/tmp",
		AlbumName:          "SaveTest",
		BatchSize:          18,
		DebounceDurationMs: 1200,
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if loaded.AlbumName != cfg.AlbumName {
		t.Errorf("AlbumName = %q, want %q", loaded.AlbumName, cfg.AlbumName)
	}
	if loaded.BatchSize != cfg.BatchSize {
		t.Errorf("BatchSize = %d, want %d", loaded.BatchSize, cfg.BatchSize)
	}
	if loaded.DebounceDurationMs != cfg.DebounceDurationMs {
		t.Errorf("DebounceDurationMs = %d, want %d", loaded.DebounceDurationMs, cfg.DebounceDurationMs)
	}
}

// loadFromPath is the same logic as Load() but pointed at an explicit path,
// so tests don't touch the real home directory.
func loadFromPath(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.BatchSize <= 0 || cfg.BatchSize > 50 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.DebounceDurationMs <= 0 {
		cfg.DebounceDurationMs = DefaultDebounceDurMs
	}
	return cfg, nil
}
