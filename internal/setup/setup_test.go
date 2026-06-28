package setup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexusproxy/internal/config"
	"nexusproxy/internal/gateway"
)

func TestProviderGroupsFriendlyNamesAndCustomProviders(t *testing.T) {
	cfg := config.Config{
		Providers: []config.ProviderConfig{
			{ID: "brave-primary", Type: "brave", APIKeyEnv: "SETUP_GROUP_BRAVE_API_KEY"},
			{ID: "custom-primary", Type: "my-custom-search", APIKeyEnv: "SETUP_GROUP_CUSTOM_API_KEY"},
		},
	}

	groups := providerGroups(cfg, "all")
	if len(groups) != 2 {
		t.Fatalf("expected 2 provider groups, got %d", len(groups))
	}

	names := map[string]string{}
	for _, group := range groups {
		names[group.BaseEnvName] = group.Name
	}
	if names["SETUP_GROUP_BRAVE_API_KEY"] != "Brave Search" {
		t.Fatalf("expected Brave Search name, got %q", names["SETUP_GROUP_BRAVE_API_KEY"])
	}
	if names["SETUP_GROUP_CUSTOM_API_KEY"] != "My Custom Search" {
		t.Fatalf("expected custom display name, got %q", names["SETUP_GROUP_CUSTOM_API_KEY"])
	}
}

func TestRunFirstRunSavesMissingKeysAndNumberedExtras(t *testing.T) {
	configPath, envPath := writeSetupConfig(t, "FIRST_RUN", []setupProvider{
		{ID: "brave-primary", Type: "brave", Env: "SETUP_FIRST_BRAVE_API_KEY"},
		{ID: "tavily-primary", Type: "tavily", Env: "SETUP_FIRST_TAVILY_API_KEY"},
	})
	unsetEnv(t, "SETUP_FIRST_BRAVE_API_KEY", "SETUP_FIRST_BRAVE_API_KEY_2", "SETUP_FIRST_TAVILY_API_KEY")

	var out bytes.Buffer
	secrets := secretSequence(t, "brave-key", "second-brave-key", "tavily-key")
	input := strings.NewReader("y\nn\nn\n")

	err := Run(context.Background(), Options{
		ConfigPath:   configPath,
		EnvPath:      envPath,
		TestMode:     TestNever,
		In:           input,
		Out:          &out,
		Err:          &bytes.Buffer{},
		SecretReader: secrets,
		HealthCheck:  failingHealthCheck(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env := string(envBytes)
	for _, expected := range []string{
		`SETUP_FIRST_BRAVE_API_KEY="brave-key"`,
		`SETUP_FIRST_BRAVE_API_KEY_2="second-brave-key"`,
		`SETUP_FIRST_TAVILY_API_KEY="tavily-key"`,
	} {
		if !strings.Contains(env, expected) {
			t.Fatalf("expected env file to contain %q, got:\n%s", expected, env)
		}
	}
	if !strings.Contains(out.String(), "Saved 3 keys") {
		t.Fatalf("expected save summary, got:\n%s", out.String())
	}
}

func TestRunKeepsExistingSavedKeyWhenBlank(t *testing.T) {
	configPath, envPath := writeSetupConfig(t, "EXISTING", []setupProvider{
		{ID: "brave-primary", Type: "brave", Env: "SETUP_EXISTING_BRAVE_API_KEY"},
	})
	unsetEnv(t, "SETUP_EXISTING_BRAVE_API_KEY")
	if err := os.WriteFile(envPath, []byte(`SETUP_EXISTING_BRAVE_API_KEY="existing-key"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var secretCalls int
	err := Run(context.Background(), Options{
		ConfigPath: configPath,
		EnvPath:    envPath,
		TestMode:   TestNever,
		In:         strings.NewReader("n\n"),
		Out:        &bytes.Buffer{},
		Err:        &bytes.Buffer{},
		SecretReader: func(prompt string) (string, error) {
			secretCalls++
			return "", nil
		},
		HealthCheck: failingHealthCheck(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if secretCalls != 0 {
		t.Fatalf("expected no secret prompt for saved key, got %d", secretCalls)
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envBytes), `SETUP_EXISTING_BRAVE_API_KEY="existing-key"`) {
		t.Fatalf("expected existing key to remain, got:\n%s", string(envBytes))
	}
}

func TestRunProviderFilterOnlyPromptsSelectedProvider(t *testing.T) {
	configPath, envPath := writeSetupConfig(t, "FILTER", []setupProvider{
		{ID: "brave-primary", Type: "brave", Env: "SETUP_FILTER_BRAVE_API_KEY"},
		{ID: "tavily-primary", Type: "tavily", Env: "SETUP_FILTER_TAVILY_API_KEY"},
	})
	unsetEnv(t, "SETUP_FILTER_BRAVE_API_KEY", "SETUP_FILTER_TAVILY_API_KEY")

	err := Run(context.Background(), Options{
		ConfigPath:   configPath,
		EnvPath:      envPath,
		Provider:     "brave",
		TestMode:     TestNever,
		In:           strings.NewReader("n\n"),
		Out:          &bytes.Buffer{},
		Err:          &bytes.Buffer{},
		SecretReader: secretSequence(t, "brave-only-key"),
		HealthCheck:  failingHealthCheck(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env := string(envBytes)
	if !strings.Contains(env, `SETUP_FILTER_BRAVE_API_KEY="brave-only-key"`) {
		t.Fatalf("expected brave key in env file, got:\n%s", env)
	}
	if strings.Contains(env, "SETUP_FILTER_TAVILY_API_KEY") {
		t.Fatalf("did not expect tavily key in env file, got:\n%s", env)
	}
}

func TestRunNoTestDoesNotCallHealthCheck(t *testing.T) {
	configPath, envPath := writeSetupConfig(t, "NO_TEST", []setupProvider{
		{ID: "brave-primary", Type: "brave", Env: "SETUP_NO_TEST_BRAVE_API_KEY"},
	})
	unsetEnv(t, "SETUP_NO_TEST_BRAVE_API_KEY")

	called := false
	err := Run(context.Background(), Options{
		ConfigPath:   configPath,
		EnvPath:      envPath,
		TestMode:     TestNever,
		In:           strings.NewReader("n\n"),
		Out:          &bytes.Buffer{},
		Err:          &bytes.Buffer{},
		SecretReader: secretSequence(t, "brave-key"),
		HealthCheck: func(ctx context.Context, cfg config.Config) ([]gateway.ProviderStatus, error) {
			called = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("expected --no-test mode to skip health check")
	}
}

type setupProvider struct {
	ID   string
	Type string
	Env  string
}

func writeSetupConfig(t *testing.T, name string, providers []setupProvider) (string, string) {
	t.Helper()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	envPath := filepath.Join(dir, ".env")

	var builder strings.Builder
	builder.WriteString(`{"server":{"host":"127.0.0.1","port":8787,"apiKey":"local-dev-token"},"routing":{"policy":"priority","maxAttempts":3},"providers":[`)
	for index, provider := range providers {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(fmt.Sprintf(`{"id":%q,"type":%q,"enabled":true,"priority":100,"apiKeyEnv":%q}`, provider.ID+"-"+name, provider.Type, provider.Env))
	}
	builder.WriteString("]}")

	if err := os.WriteFile(configPath, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, envPath
}

func secretSequence(t *testing.T, values ...string) func(string) (string, error) {
	t.Helper()
	index := 0
	return func(prompt string) (string, error) {
		if index >= len(values) {
			t.Fatalf("unexpected secret prompt %q", prompt)
		}
		value := values[index]
		index++
		return value, nil
	}
}

func failingHealthCheck(t *testing.T) func(context.Context, config.Config) ([]gateway.ProviderStatus, error) {
	t.Helper()
	return func(ctx context.Context, cfg config.Config) ([]gateway.ProviderStatus, error) {
		t.Fatal("health check should not run")
		return nil, nil
	}
}

func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		previous, hadPrevious := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if hadPrevious {
				_ = os.Setenv(name, previous)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}
