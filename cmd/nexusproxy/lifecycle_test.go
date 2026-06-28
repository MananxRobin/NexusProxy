package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigPathPrefersInstalledConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NEXUS_CONFIG", "")

	installedConfig := filepath.Join(home, ".config", "nexusproxy", "config.json")
	if err := os.MkdirAll(filepath.Dir(installedConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedConfig, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := defaultConfigPath(); got != installedConfig {
		t.Fatalf("expected installed config path %q, got %q", installedConfig, got)
	}
}

func TestDefaultConfigPathFallsBackToLocalExample(t *testing.T) {
	home := t.TempDir()
	workdir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NEXUS_CONFIG", "")
	chdir(t, workdir)

	if err := os.WriteFile("config.example.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := defaultConfigPath(); got != "config.example.json" {
		t.Fatalf("expected config.example.json, got %q", got)
	}
}

func chdir(t *testing.T, path string) {
	t.Helper()

	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

func TestDefaultEnvPathHonorsOverride(t *testing.T) {
	t.Setenv("NEXUS_ENV_FILE", "/tmp/custom-nexusproxy.env")

	if got := defaultEnvPath("config.example.json"); got != "/tmp/custom-nexusproxy.env" {
		t.Fatalf("expected env override, got %q", got)
	}
}

func TestReadPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexusproxy.pid")
	if err := os.WriteFile(path, []byte("12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pid, err := readPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 12345 {
		t.Fatalf("expected pid 12345, got %d", pid)
	}
}
