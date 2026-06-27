package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFileDoesNotOverrideExistingEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("BRAVE_API_KEY=\"from-file\"\nTAVILY_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BRAVE_API_KEY", "from-shell")
	t.Setenv("TAVILY_API_KEY", "")

	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("BRAVE_API_KEY"); got != "from-shell" {
		t.Fatalf("expected shell value to win, got %q", got)
	}
	if got := os.Getenv("TAVILY_API_KEY"); got != "from-file" {
		t.Fatalf("expected file value, got %q", got)
	}
}

func TestSaveEnvValuesPreservesUnrelatedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	initial := "# local secrets\nBRAVE_API_KEY=\"old\"\nOTHER=value\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveEnvValues(path, map[string]string{
		"BRAVE_API_KEY":  "new",
		"SERPER_API_KEY": "serper-secret",
	}); err != nil {
		t.Fatal(err)
	}

	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(bytes)

	for _, expected := range []string{
		"# local secrets",
		`BRAVE_API_KEY="new"`,
		"OTHER=value",
		`SERPER_API_KEY="serper-secret"`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in saved env file:\n%s", expected, got)
		}
	}
}
