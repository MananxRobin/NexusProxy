package server

import (
	"bytes"
	"context"
	"encoding/json"
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

type fakeSearchAdapter struct{}

func (fakeSearchAdapter) Search(ctx context.Context, request gateway.SearchRequest, provider config.ProviderConfig, client *http.Client) (gateway.ProviderResponse, error) {
	results := []gateway.SearchResult{
		{
			Title:       "First result for " + request.Query,
			URL:         "https://example.com/first",
			Snippet:     "First compatible result snippet",
			PublishedAt: "2026-06-01",
			Source:      "example.com",
			Rank:        1,
			Provider:    provider.ID,
		},
		{
			Title:    "Second result",
			URL:      "https://example.org/second",
			Snippet:  "Second compatible result snippet",
			Source:   "example.org",
			Rank:     2,
			Provider: provider.ID,
		},
	}

	if request.MaxResults < len(results) {
		results = results[:request.MaxResults]
	}

	return gateway.ProviderResponse{
		OK:      true,
		Status:  http.StatusOK,
		Results: results,
	}, nil
}

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

func TestTavilyCompatibleSearch(t *testing.T) {
	handler := New(testCompatGateway(), "local-dev-token", ".env", 8)
	payload := map[string]any{
		"query":               "open source search",
		"max_results":         2,
		"search_depth":        "basic",
		"time_range":          "month",
		"include_answer":      true,
		"include_raw_content": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer local-dev-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}

	var output struct {
		Query   string `json:"query"`
		Answer  string `json:"answer"`
		Results []struct {
			Title      string  `json:"title"`
			URL        string  `json:"url"`
			Content    string  `json:"content"`
			Score      float64 `json:"score"`
			RawContent string  `json:"raw_content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Query != "open source search" {
		t.Fatalf("expected query to round trip, got %q", output.Query)
	}
	if output.Answer == "" {
		t.Fatal("expected compatibility answer when include_answer is true")
	}
	if len(output.Results) != 2 {
		t.Fatalf("expected 2 Tavily results, got %d", len(output.Results))
	}
	if output.Results[0].Content != "First compatible result snippet" {
		t.Fatalf("expected Tavily content to use snippet, got %q", output.Results[0].Content)
	}
	if output.Results[0].RawContent == "" {
		t.Fatal("expected raw_content when include_raw_content is true")
	}
	if output.Results[0].Score <= output.Results[1].Score {
		t.Fatalf("expected scores to decrease by rank, got %#v", output.Results)
	}
}

func TestTavilyCompatibleSearchAcceptsBodyAPIKey(t *testing.T) {
	handler := New(testCompatGateway(), "local-dev-token", ".env", 8)
	body := strings.NewReader(`{"api_key":"local-dev-token","query":"body auth","max_results":1}`)
	request := httptest.NewRequest(http.MethodPost, "/search", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected body api_key auth to work, got %d: %s", response.Code, response.Body.String())
	}
}

func TestBraveCompatibleSearch(t *testing.T) {
	handler := New(testCompatGateway(), "local-dev-token", ".env", 8)
	request := httptest.NewRequest(http.MethodGet, "/res/v1/web/search?q=open+source&count=2&country=US&search_lang=en&freshness=pm", nil)
	request.Header.Set("X-Subscription-Token", "local-dev-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}

	var output struct {
		Type  string `json:"type"`
		Query struct {
			Original string `json:"original"`
		} `json:"query"`
		Web struct {
			Type    string `json:"type"`
			Results []struct {
				Type        string `json:"type"`
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
				Profile     struct {
					Name string `json:"name"`
				} `json:"profile"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Type != "search" || output.Web.Type != "search" {
		t.Fatalf("expected Brave search types, got %#v", output)
	}
	if output.Query.Original != "open source" {
		t.Fatalf("expected original query, got %q", output.Query.Original)
	}
	if len(output.Web.Results) != 2 {
		t.Fatalf("expected 2 Brave results, got %d", len(output.Web.Results))
	}
	if output.Web.Results[0].Description != "First compatible result snippet" {
		t.Fatalf("expected Brave description to use snippet, got %q", output.Web.Results[0].Description)
	}
	if output.Web.Results[0].Profile.Name != "example.com" {
		t.Fatalf("expected source profile, got %#v", output.Web.Results[0].Profile)
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

func testCompatGateway() *gateway.SearchGateway {
	cfg := testConfig()
	cfg.Providers = []config.ProviderConfig{
		{
			ID:            "fake-primary",
			Type:          "fake",
			Priority:      100,
			APIKey:        "provider-key",
			DefaultParams: map[string]any{},
			TimeoutMs:     20000,
		},
	}

	return gateway.New(cfg, gateway.Options{
		Adapters: map[string]gateway.Adapter{
			"fake": fakeSearchAdapter{},
		},
	})
}
