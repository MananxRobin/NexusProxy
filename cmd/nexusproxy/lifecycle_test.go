package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestUninstallKeepsConfigByDefault(t *testing.T) {
	binaryPath, configDir, pidPath := makeUninstallFixture(t)

	var out bytes.Buffer
	err := uninstall(uninstallOptions{
		BinaryPath: binaryPath,
		ConfigDir:  configDir,
		PIDPath:    pidPath,
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(binaryPath); !os.IsNotExist(err) {
		t.Fatalf("expected binary to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".env")); err != nil {
		t.Fatalf("expected config and API keys to remain: %v", err)
	}
	if !strings.Contains(out.String(), "Kept config and API keys") {
		t.Fatalf("expected keep-config message, got:\n%s", out.String())
	}
}

func TestUninstallPurgeCanBeDeclined(t *testing.T) {
	binaryPath, configDir, pidPath := makeUninstallFixture(t)

	var out bytes.Buffer
	err := uninstall(uninstallOptions{
		BinaryPath: binaryPath,
		ConfigDir:  configDir,
		PIDPath:    pidPath,
		Purge:      true,
		In:         strings.NewReader("n\n"),
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(binaryPath); !os.IsNotExist(err) {
		t.Fatalf("expected binary to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".env")); err != nil {
		t.Fatalf("expected declined purge to keep API keys: %v", err)
	}
}

func TestUninstallPurgeYesRemovesConfig(t *testing.T) {
	binaryPath, configDir, pidPath := makeUninstallFixture(t)

	var out bytes.Buffer
	err := uninstall(uninstallOptions{
		BinaryPath: binaryPath,
		ConfigDir:  configDir,
		PIDPath:    pidPath,
		Purge:      true,
		Yes:        true,
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(binaryPath); !os.IsNotExist(err) {
		t.Fatalf("expected binary to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("expected config dir to be removed, stat err: %v", err)
	}
	if !strings.Contains(out.String(), "Removed config and API keys") {
		t.Fatalf("expected purge message, got:\n%s", out.String())
	}
}

func makeUninstallFixture(t *testing.T) (string, string, string) {
	t.Helper()

	root := t.TempDir()
	binaryPath := filepath.Join(root, "bin", "nexusproxy")
	configDir := filepath.Join(root, ".config", "nexusproxy")
	pidPath := filepath.Join(configDir, "nexusproxy.pid")

	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("fake binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte("BRAVE_API_KEY=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return binaryPath, configDir, pidPath
}
