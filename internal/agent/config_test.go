package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withTempHome points ConfigPath at a temp dir for the duration of a test.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestSaveAndLoadConfig(t *testing.T) {
	withTempHome(t)

	cfg := Config{URL: "http://localhost:1234/v1", Model: "m", MaxTokens: 16384}
	backup, err := SaveConfig(cfg)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if backup != "" {
		t.Errorf("first save should not back up, got %q", backup)
	}
	got, found, err := LoadConfigFrom()
	if err != nil || !found {
		t.Fatalf("LoadConfigFrom: found=%v err=%v", found, err)
	}
	if got.Model != "m" || got.MaxTokens != 16384 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	withTempHome(t)
	_, found, err := LoadConfigFrom()
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if found {
		t.Error("found should be false when no file exists")
	}
}

func TestSaveConfigBacksUpUnknownKeys(t *testing.T) {
	home := withTempHome(t)
	p := filepath.Join(home, ".agent", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a config that has a key the struct doesn't model.
	raw := `{"model":"old","// note":"keep me"}`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	backup, err := SaveConfig(Config{Model: "new"})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if backup == "" {
		t.Fatal("expected a backup when unknown keys are present")
	}
	// Backup must retain the comment key.
	bak, _ := os.ReadFile(backup)
	if !containsKey(bak, "// note") {
		t.Error("backup lost the unknown key")
	}
	// Live file is a clean rewrite (no comment key).
	live, _ := os.ReadFile(p)
	if containsKey(live, "// note") {
		t.Error("live file should have dropped the unknown key (it's in .bak)")
	}
}

func TestUnknownConfigKeys(t *testing.T) {
	data := []byte(`{"model":"m","// c":"x","bogus":1}`)
	got := unknownConfigKeys(data)
	if len(got) != 2 || got[0] != "// c" || got[1] != "bogus" {
		t.Errorf("unknownConfigKeys = %v, want [// c bogus]", got)
	}
}

func containsKey(data []byte, key string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
