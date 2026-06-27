package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nexusproxy/internal/config"
)

type fakeAdapter struct {
	search func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error)
}

func (adapter fakeAdapter) Search(ctx context.Context, request SearchRequest, provider config.ProviderConfig, client *http.Client) (ProviderResponse, error) {
	return adapter.search(request, provider)
}

func TestSearchFallsBackAndCoolsDownRateLimitedProvider(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	cooldownMs := int64(5000)
	cfg := testConfig("priority", []config.ProviderConfig{
		testProvider("primary", "fake429", 100),
		testProvider("secondary", "fakeok", 50),
	})

	searchGateway := New(cfg, Options{
		Now: func() time.Time { return now },
		Adapters: map[string]Adapter{
			"fake429": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				return ProviderResponse{
					OK:     false,
					Status: http.StatusTooManyRequests,
					Error:  "rate limited",
					RateLimit: RateLimit{
						CooldownMs: &cooldownMs,
					},
				}, nil
			}},
			"fakeok": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				return ProviderResponse{
					OK:     true,
					Status: http.StatusOK,
					Results: []SearchResult{{
						Title:    "NexusProxy",
						URL:      "https://example.com",
						Snippet:  "A search gateway.",
						Rank:     1,
						Provider: provider.ID,
					}},
				}, nil
			}},
		},
	})

	response, gatewayErr := searchGateway.Search(context.Background(), map[string]any{
		"query": "nexusproxy",
	})
	if gatewayErr != nil {
		t.Fatalf("Search returned error: %v", gatewayErr)
	}

	if response.Provider != "secondary" {
		t.Fatalf("expected secondary provider, got %q", response.Provider)
	}
	if len(response.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(response.Attempts))
	}
	if response.Attempts[0].Status != http.StatusTooManyRequests {
		t.Fatalf("expected first attempt to be 429, got %d", response.Attempts[0].Status)
	}

	status := searchGateway.Status()
	primary := findProviderStatus(t, status, "primary")
	if primary.Reason != "cooling_down" {
		t.Fatalf("expected primary to cool down, got %q", primary.Reason)
	}
	if primary.CooldownRemainingMs != cooldownMs {
		t.Fatalf("expected cooldown %d ms, got %d", cooldownMs, primary.CooldownRemainingMs)
	}
	if primary.Stats.RateLimited != 1 {
		t.Fatalf("expected primary rate limit count 1, got %d", primary.Stats.RateLimited)
	}
}

func TestRoundRobinRotatesProviders(t *testing.T) {
	cfg := testConfig("round_robin", []config.ProviderConfig{
		testProvider("first", "fakeok", 100),
		testProvider("second", "fakeok", 90),
	})

	searchGateway := New(cfg, Options{
		Adapters: map[string]Adapter{
			"fakeok": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				return ProviderResponse{
					OK:     true,
					Status: http.StatusOK,
					Results: []SearchResult{{
						Title:    provider.ID,
						URL:      "https://example.com/" + provider.ID,
						Rank:     1,
						Provider: provider.ID,
					}},
				}, nil
			}},
		},
	})

	first, err := searchGateway.Search(context.Background(), map[string]any{"query": "alpha"})
	if err != nil {
		t.Fatalf("first search returned error: %v", err)
	}
	second, err := searchGateway.Search(context.Background(), map[string]any{"query": "beta"})
	if err != nil {
		t.Fatalf("second search returned error: %v", err)
	}

	if first.Provider != "first" {
		t.Fatalf("expected first provider, got %q", first.Provider)
	}
	if second.Provider != "second" {
		t.Fatalf("expected second provider, got %q", second.Provider)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	cfg := testConfig("priority", []config.ProviderConfig{
		testProvider("primary", "fakeok", 100),
	})

	searchGateway := New(cfg, Options{
		Adapters: map[string]Adapter{
			"fakeok": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				t.Fatal("adapter should not be called for invalid requests")
				return ProviderResponse{}, nil
			}},
		},
	})

	_, err := searchGateway.Search(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if err.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", err.StatusCode)
	}
}

func TestUpdateProviderKeysMakesMissingProviderReady(t *testing.T) {
	provider := testProvider("primary", "fakeok", 100)
	provider.APIKey = ""
	provider.APIKeyEnv = "FAKE_API_KEY"

	searchGateway := New(testConfig("priority", []config.ProviderConfig{provider}), Options{
		Adapters: map[string]Adapter{
			"fakeok": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				return ProviderResponse{OK: true, Status: http.StatusOK}, nil
			}},
		},
	})

	before := findProviderStatus(t, searchGateway.Status(), "primary")
	if before.Reason != "missing_api_key" {
		t.Fatalf("expected missing_api_key before update, got %q", before.Reason)
	}

	if updated := searchGateway.UpdateProviderKeys(map[string]string{"FAKE_API_KEY": "secret"}); updated != 1 {
		t.Fatalf("expected 1 updated key, got %d", updated)
	}

	after := findProviderStatus(t, searchGateway.Status(), "primary")
	if !after.Usable {
		t.Fatalf("expected provider ready after update, got %q", after.Reason)
	}
}

func TestUpdateProviderKeysAddsNumberedProviderSlot(t *testing.T) {
	base := testProvider("brave-primary", "fakeok", 100)
	base.APIKeyEnv = "BRAVE_API_KEY"

	searchGateway := New(testConfig("priority", []config.ProviderConfig{base}), Options{
		Adapters: map[string]Adapter{
			"fakeok": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				return ProviderResponse{OK: true, Status: http.StatusOK}, nil
			}},
		},
	})

	if updated := searchGateway.UpdateProviderKeys(map[string]string{"BRAVE_API_KEY_2": "second"}); updated != 1 {
		t.Fatalf("expected 1 updated key, got %d", updated)
	}

	status := searchGateway.Status()
	if len(status.Providers) != 2 {
		t.Fatalf("expected 2 provider slots, got %d", len(status.Providers))
	}

	extra := findProviderStatus(t, status, "brave-primary-2")
	if extra.ConfiguredAPIKeyEnv != "BRAVE_API_KEY_2" {
		t.Fatalf("expected extra env BRAVE_API_KEY_2, got %q", extra.ConfiguredAPIKeyEnv)
	}
	if !extra.Usable {
		t.Fatalf("expected extra provider to be usable, got %q", extra.Reason)
	}
}

func TestRefreshProviderHealthUpdatesQuotaAndUsage(t *testing.T) {
	resetAt := time.Now().Add(time.Minute)
	limit := 100
	remaining := 42
	credits := 1
	cfg := testConfig("priority", []config.ProviderConfig{
		testProvider("primary", "fakeok", 100),
	})

	searchGateway := New(cfg, Options{
		Adapters: map[string]Adapter{
			"fakeok": fakeAdapter{search: func(request SearchRequest, provider config.ProviderConfig) (ProviderResponse, error) {
				if request.MaxResults != 1 {
					t.Fatalf("expected health probe to request one result, got %d", request.MaxResults)
				}
				return ProviderResponse{
					OK:     true,
					Status: http.StatusOK,
					RateLimit: RateLimit{
						Limit:     &limit,
						Remaining: &remaining,
						ResetAt:   &resetAt,
						Windows: []RateLimitWindow{{
							Limit:     &limit,
							Remaining: &remaining,
							ResetAt:   &resetAt,
						}},
					},
					Usage: ProviderUsage{
						Credits: &credits,
					},
				}, nil
			}},
		},
	})

	statuses := searchGateway.RefreshProviderHealth(context.Background(), "health")
	if len(statuses) != 1 {
		t.Fatalf("expected one provider status, got %d", len(statuses))
	}

	provider := statuses[0]
	if provider.Stats.HealthChecks != 1 {
		t.Fatalf("expected one health check, got %d", provider.Stats.HealthChecks)
	}
	if provider.Stats.LastUsageCredits == nil || *provider.Stats.LastUsageCredits != credits {
		t.Fatalf("expected last usage credits %d, got %#v", credits, provider.Stats.LastUsageCredits)
	}
	if provider.Quota.Remaining == nil || *provider.Quota.Remaining != remaining {
		t.Fatalf("expected remaining quota %d, got %#v", remaining, provider.Quota.Remaining)
	}
	if len(provider.Quota.Windows) != 1 {
		t.Fatalf("expected one quota window, got %d", len(provider.Quota.Windows))
	}
	if searchGateway.Status().Stats.SuccessfulRequests != 0 {
		t.Fatal("health checks should not increment global successful request count")
	}
}

func TestExtractRateLimitParsesMultipleWindows(t *testing.T) {
	response := httptest.NewRecorder()
	response.Header().Set("X-RateLimit-Limit", "1, 2000")
	response.Header().Set("X-RateLimit-Remaining", "0, 1999")
	response.Header().Set("X-RateLimit-Reset", "1, 2592000")

	rateLimit := extractRateLimit(response.Header())
	if len(rateLimit.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(rateLimit.Windows))
	}
	if rateLimit.Remaining == nil || *rateLimit.Remaining != 1999 {
		t.Fatalf("expected legacy remaining to use broadest window, got %#v", rateLimit.Remaining)
	}
	if rateLimit.CooldownMs == nil || *rateLimit.CooldownMs <= 0 || *rateLimit.CooldownMs > 2000 {
		t.Fatalf("expected cooldown near shortest reset window, got %#v", rateLimit.CooldownMs)
	}
}

func testConfig(policy string, providers []config.ProviderConfig) config.Config {
	return config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 8787,
		},
		Routing: config.RoutingConfig{
			Policy:            policy,
			MaxAttempts:       3,
			RetryOnStatuses:   []int{429, 500, 502, 503, 504},
			DefaultCooldownMs: 60000,
		},
		Providers: providers,
	}
}

func testProvider(id string, providerType string, priority int) config.ProviderConfig {
	return config.ProviderConfig{
		ID:            id,
		Type:          providerType,
		Priority:      priority,
		APIKey:        "test-key",
		DefaultParams: map[string]any{},
		TimeoutMs:     1000,
	}
}

func findProviderStatus(t *testing.T, status Status, id string) ProviderStatus {
	t.Helper()

	for _, provider := range status.Providers {
		if provider.ID == id {
			return provider
		}
	}

	t.Fatalf("provider %q not found", id)
	return ProviderStatus{}
}
