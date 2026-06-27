package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexusproxy/internal/config"
	"nexusproxy/internal/gateway"
)

func TestPlaygroundPageRendersSearchUI(t *testing.T) {
	handler := New(gateway.New(testConfig(), gateway.Options{}), "local-dev-token", ".env", 8)
	request := httptest.NewRequest(http.MethodGet, "/playground", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}

	body := response.Body.String()
	for _, expected := range []string{
		"Search Playground",
		"POST /v1/search",
		"id=\"search-form\"",
		"Run Search",
		"/dashboard",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected playground response to contain %q", expected)
		}
	}

	if strings.Contains(body, "local-dev-token") {
		t.Fatal("playground should not embed the configured auth token")
	}
}

func TestDashboardRendersProviderHealthRefresh(t *testing.T) {
	handler := New(gateway.New(testConfig(), gateway.Options{}), "local-dev-token", ".env", 8)
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}

	body := response.Body.String()
	for _, expected := range []string{
		"Provider Health Refresh",
		"/dashboard/refresh-providers",
		"Refresh Provider Health",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected dashboard response to contain %q", expected)
		}
	}
}

func TestRefreshProvidersRejectsInvalidLocalToken(t *testing.T) {
	handler := New(gateway.New(testConfig(), gateway.Options{}), "local-dev-token", ".env", 8)
	form := url.Values{}
	form.Set("nexus_api_key", "wrong")
	request := httptest.NewRequest(http.MethodPost, "/dashboard/refresh-providers", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", response.Code)
	}
	location := response.Header().Get("Location")
	if !strings.Contains(location, "missing+or+invalid+local+NexusProxy+token") {
		t.Fatalf("expected clearer token error in redirect, got %q", location)
	}
}

func TestAddProviderKeyCreatesNumberedProviderSlot(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	searchGateway := gateway.New(testConfig(), gateway.Options{})
	handler := New(searchGateway, "local-dev-token", envPath, 8)

	form := url.Values{}
	form.Set("base_env", "BRAVE_API_KEY")
	form.Set("api_key", "second-brave-key")
	form.Set("nexus_api_key", "local-dev-token")
	request := httptest.NewRequest(http.MethodPost, "/dashboard/provider-key-add", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", response.Code)
	}

	bytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bytes), `BRAVE_API_KEY_2="second-brave-key"`) {
		t.Fatalf("expected numbered key in env file, got:\n%s", string(bytes))
	}

	status := searchGateway.Status()
	if len(status.Providers) != 2 {
		t.Fatalf("expected 2 provider slots, got %d", len(status.Providers))
	}
	found := false
	for _, provider := range status.Providers {
		if provider.ID == "brave-primary-2" && provider.ConfiguredAPIKeyEnv == "BRAVE_API_KEY_2" && provider.Usable {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected usable brave-primary-2 provider, got %#v", status.Providers)
	}
}

func TestConcurrencyLimiterReturnsTooManyRequestsWhenFull(t *testing.T) {
	server := &Server{limiter: make(chan struct{}, 1)}
	server.limiter <- struct{}{}

	handler := server.withConcurrencyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run while limiter is full")
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/search", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", response.Code)
	}
	if response.Header().Get("Retry-After") != "1" {
		t.Fatalf("expected Retry-After header, got %q", response.Header().Get("Retry-After"))
	}
	if !strings.Contains(response.Body.String(), "retry shortly") {
		t.Fatalf("expected busy response body, got %q", response.Body.String())
	}
}

func TestConcurrencyLimiterAllowsHealthCheckWhenFull(t *testing.T) {
	server := &Server{limiter: make(chan struct{}, 1)}
	server.limiter <- struct{}{}
	ran := false

	handler := server.withConcurrencyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if !ran {
		t.Fatal("expected health check to bypass limiter")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func testConfig() config.Config {
	return config.Config{
		Server: config.ServerConfig{
			Host:                  "127.0.0.1",
			Port:                  8787,
			APIKey:                "local-dev-token",
			MaxConcurrentRequests: 8,
		},
		Routing: config.RoutingConfig{
			Policy:            "priority",
			MaxAttempts:       3,
			RetryOnStatuses:   []int{429, 500, 502, 503, 504},
			DefaultCooldownMs: 60000,
		},
		Providers: []config.ProviderConfig{
			{
				ID:            "brave-primary",
				Type:          "brave",
				Priority:      100,
				APIKeyEnv:     "BRAVE_API_KEY",
				DefaultParams: map[string]any{},
				TimeoutMs:     20000,
			},
		},
	}
}
