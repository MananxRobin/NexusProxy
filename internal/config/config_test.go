package config

import "testing"

func TestNormalizeExpandsNumberedProviderKeys(t *testing.T) {
	t.Setenv("NEXUS_TEST_BRAVE_API_KEY_2", "second-brave-key")
	t.Setenv("NEXUS_TEST_BRAVE_API_KEY_3", "third-brave-key")

	cfg := Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 8787,
		},
		Routing: RoutingConfig{
			Policy:      "priority",
			MaxAttempts: 3,
		},
		Providers: []ProviderConfig{
			{
				ID:            "brave-primary",
				Type:          "brave",
				Priority:      100,
				APIKey:        "first-brave-key",
				APIKeyEnv:     "NEXUS_TEST_BRAVE_API_KEY",
				DefaultParams: map[string]any{},
			},
		},
	}

	if err := cfg.Normalize("test"); err != nil {
		t.Fatal(err)
	}

	if len(cfg.Providers) != 3 {
		t.Fatalf("expected 3 expanded providers, got %d", len(cfg.Providers))
	}

	expected := map[string]string{
		"brave-primary":   "NEXUS_TEST_BRAVE_API_KEY",
		"brave-primary-2": "NEXUS_TEST_BRAVE_API_KEY_2",
		"brave-primary-3": "NEXUS_TEST_BRAVE_API_KEY_3",
	}

	for _, provider := range cfg.Providers {
		envName, ok := expected[provider.ID]
		if !ok {
			t.Fatalf("unexpected provider id %q", provider.ID)
		}
		if provider.APIKeyEnv != envName {
			t.Fatalf("expected %s env %q, got %q", provider.ID, envName, provider.APIKeyEnv)
		}
	}
}

func TestNextAPIKeyEnvName(t *testing.T) {
	t.Setenv("NEXUS_TEST_TAVILY_API_KEY_2", "second")

	if got := NextAPIKeyEnvName("NEXUS_TEST_TAVILY_API_KEY"); got != "NEXUS_TEST_TAVILY_API_KEY_3" {
		t.Fatalf("expected NEXUS_TEST_TAVILY_API_KEY_3, got %q", got)
	}
}

func TestNormalizeDefaultsMaxConcurrentRequests(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{
				ID:        "brave-primary",
				Type:      "brave",
				APIKeyEnv: "BRAVE_API_KEY",
			},
		},
	}

	if err := cfg.Normalize("test"); err != nil {
		t.Fatal(err)
	}

	if cfg.Server.MaxConcurrentRequests != 8 {
		t.Fatalf("expected default max concurrency 8, got %d", cfg.Server.MaxConcurrentRequests)
	}
}

func TestNormalizeRejectsNegativeMaxConcurrentRequests(t *testing.T) {
	cfg := Config{
		Server: ServerConfig{MaxConcurrentRequests: -1},
		Providers: []ProviderConfig{
			{
				ID:        "brave-primary",
				Type:      "brave",
				APIKeyEnv: "BRAVE_API_KEY",
			},
		},
	}

	if err := cfg.Normalize("test"); err == nil {
		t.Fatal("expected negative max concurrency to be rejected")
	}
}
