package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nexusproxy/internal/config"
)

type Adapter interface {
	Search(ctx context.Context, request SearchRequest, provider config.ProviderConfig, client *http.Client) (ProviderResponse, error)
}

func builtinAdapters() map[string]Adapter {
	return map[string]Adapter{
		"brave":  braveAdapter{},
		"tavily": tavilyAdapter{},
		"serper": serperAdapter{},
	}
}

type braveAdapter struct{}

func (braveAdapter) Search(ctx context.Context, request SearchRequest, provider config.ProviderConfig, client *http.Client) (ProviderResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.search.brave.com/res/v1/web/search"
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ProviderResponse{}, err
	}

	query := parsed.Query()
	query.Set("q", request.Query)
	query.Set("count", fmt.Sprint(request.MaxResults))

	country := providerString(provider.DefaultParams, "country")
	if request.Country != "" {
		country = request.Country
	}
	if country != "" {
		query.Set("country", strings.ToUpper(country))
	}

	language := providerString(provider.DefaultParams, "language")
	if request.Language != "" {
		language = request.Language
	}
	if language != "" {
		query.Set("search_lang", strings.ToLower(language))
	}

	if value, ok := effectiveSafeSearch(request, provider); ok {
		if value {
			query.Set("safesearch", "moderate")
		} else {
			query.Set("safesearch", "off")
		}
	}

	if freshness := braveFreshness(request.Freshness); freshness != "" {
		query.Set("freshness", freshness)
	}

	parsed.RawQuery = query.Encode()

	status, body, headers, err := doRequest(ctx, client, provider, http.MethodGet, parsed.String(), map[string]string{
		"Accept":               "application/json",
		"X-Subscription-Token": provider.APIKey,
	}, nil)
	if err != nil {
		return ProviderResponse{}, err
	}

	rateLimit := extractRateLimit(headers)
	if status < 200 || status >= 300 {
		return ProviderResponse{OK: false, Status: status, RateLimit: rateLimit, Error: errorFromBody(body)}, nil
	}

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Snippet     string `json:"snippet"`
				PageAge     string `json:"page_age"`
				Age         string `json:"age"`
				Profile     struct {
					Name string `json:"name"`
				} `json:"profile"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ProviderResponse{OK: false, Status: http.StatusBadGateway, RateLimit: rateLimit, Error: "failed to parse Brave response"}, nil
	}

	results := make([]SearchResult, 0, len(payload.Web.Results))
	for index, item := range payload.Web.Results {
		snippet := item.Description
		if snippet == "" {
			snippet = item.Snippet
		}
		publishedAt := item.PageAge
		if publishedAt == "" {
			publishedAt = item.Age
		}
		source := item.Profile.Name
		if source == "" {
			source = hostFromURL(item.URL)
		}

		results = append(results, SearchResult{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     snippet,
			PublishedAt: publishedAt,
			Source:      source,
			Rank:        index + 1,
			Provider:    provider.ID,
		})
	}

	return ProviderResponse{OK: true, Status: status, Results: results, RateLimit: rateLimit}, nil
}

type tavilyAdapter struct{}

func (tavilyAdapter) Search(ctx context.Context, request SearchRequest, provider config.ProviderConfig, client *http.Client) (ProviderResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://api.tavily.com/search"
	}

	body := map[string]any{
		"query":               request.Query,
		"max_results":         request.MaxResults,
		"search_depth":        providerStringDefault(provider.DefaultParams, "basic", "searchDepth", "search_depth"),
		"include_answer":      false,
		"include_raw_content": false,
		"include_usage":       true,
	}
	if request.Freshness != "" && request.Freshness != "any" {
		body["topic"] = "news"
	}

	status, responseBody, headers, err := doRequest(ctx, client, provider, http.MethodPost, endpoint, map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + provider.APIKey,
		"Content-Type":  "application/json",
	}, body)
	if err != nil {
		return ProviderResponse{}, err
	}

	rateLimit := extractRateLimit(headers)
	if status < 200 || status >= 300 {
		return ProviderResponse{OK: false, Status: status, RateLimit: rateLimit, Error: errorFromBody(responseBody)}, nil
	}

	var payload struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			Snippet       string `json:"snippet"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
		Usage struct {
			Credits *int `json:"credits"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return ProviderResponse{OK: false, Status: http.StatusBadGateway, RateLimit: rateLimit, Error: "failed to parse Tavily response"}, nil
	}

	results := make([]SearchResult, 0, len(payload.Results))
	for index, item := range payload.Results {
		snippet := item.Content
		if snippet == "" {
			snippet = item.Snippet
		}

		results = append(results, SearchResult{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     snippet,
			PublishedAt: item.PublishedDate,
			Source:      hostFromURL(item.URL),
			Rank:        index + 1,
			Provider:    provider.ID,
		})
	}

	return ProviderResponse{
		OK:        true,
		Status:    status,
		Results:   results,
		RateLimit: rateLimit,
		Usage: ProviderUsage{
			Credits: payload.Usage.Credits,
		},
	}, nil
}

type serperAdapter struct{}

func (serperAdapter) Search(ctx context.Context, request SearchRequest, provider config.ProviderConfig, client *http.Client) (ProviderResponse, error) {
	endpoint := provider.Endpoint
	if endpoint == "" {
		endpoint = "https://google.serper.dev/search"
	}

	body := map[string]any{
		"q":   request.Query,
		"num": request.MaxResults,
	}

	country := providerString(provider.DefaultParams, "country")
	if request.Country != "" {
		country = request.Country
	}
	if country != "" {
		body["gl"] = strings.ToLower(country)
	}

	language := providerString(provider.DefaultParams, "language")
	if request.Language != "" {
		language = request.Language
	}
	if language != "" {
		body["hl"] = strings.ToLower(language)
	}

	status, responseBody, headers, err := doRequest(ctx, client, provider, http.MethodPost, endpoint, map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
		"X-API-KEY":    provider.APIKey,
	}, body)
	if err != nil {
		return ProviderResponse{}, err
	}

	rateLimit := extractRateLimit(headers)
	if status < 200 || status >= 300 {
		return ProviderResponse{OK: false, Status: status, RateLimit: rateLimit, Error: errorFromBody(responseBody)}, nil
	}

	var payload struct {
		Organic []struct {
			Title    string `json:"title"`
			Link     string `json:"link"`
			Snippet  string `json:"snippet"`
			Date     string `json:"date"`
			Source   string `json:"source"`
			Position int    `json:"position"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return ProviderResponse{OK: false, Status: http.StatusBadGateway, RateLimit: rateLimit, Error: "failed to parse Serper response"}, nil
	}

	results := make([]SearchResult, 0, len(payload.Organic))
	for index, item := range payload.Organic {
		rank := item.Position
		if rank == 0 {
			rank = index + 1
		}
		source := item.Source
		if source == "" {
			source = hostFromURL(item.Link)
		}

		results = append(results, SearchResult{
			Title:       item.Title,
			URL:         item.Link,
			Snippet:     item.Snippet,
			PublishedAt: item.Date,
			Source:      source,
			Rank:        rank,
			Provider:    provider.ID,
		})
	}

	return ProviderResponse{OK: true, Status: status, Results: results, RateLimit: rateLimit}, nil
}

func doRequest(ctx context.Context, client *http.Client, provider config.ProviderConfig, method string, endpoint string, headers map[string]string, body any) (int, []byte, http.Header, error) {
	requestCtx, cancel := context.WithTimeout(ctx, provider.Timeout())
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}

	return resp.StatusCode, data, resp.Header, nil
}

func extractRateLimit(headers http.Header) RateLimit {
	retryAfter := firstHeader(headers, "Retry-After")
	reset := firstHeader(headers, "X-RateLimit-Reset", "X-Rate-Limit-Reset", "RateLimit-Reset")
	limits := headerInts(headers, "X-RateLimit-Limit", "X-Rate-Limit-Limit", "RateLimit-Limit")
	remainingValues := headerInts(headers, "X-RateLimit-Remaining", "X-Rate-Limit-Remaining", "RateLimit-Remaining")
	resetValues := headerInt64s(headers, "X-RateLimit-Reset", "X-Rate-Limit-Reset", "RateLimit-Reset")
	limit := lastIntPtr(limits)
	remaining := lastIntPtr(remainingValues)

	var resetAt *time.Time
	if parsed := parseResetAt(reset); !parsed.IsZero() {
		resetAt = &parsed
	}

	windows := buildRateLimitWindows(limits, remainingValues, resetValues)
	var cooldownMs *int64
	if duration := parseRetryAfter(retryAfter); duration > 0 {
		value := int64(duration / time.Millisecond)
		cooldownMs = &value
	} else if cooldown := shortestResetDuration(windows); cooldown > 0 {
		value := int64(cooldown / time.Millisecond)
		cooldownMs = &value
	}

	return RateLimit{
		Limit:      limit,
		Remaining:  remaining,
		ResetAt:    resetAt,
		CooldownMs: cooldownMs,
		Windows:    windows,
	}
}

func errorFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var payload struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Message != "" {
			return payload.Message
		}
		if payload.Error != nil {
			return fmt.Sprint(payload.Error)
		}
	}

	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		return text[:300]
	}
	return text
}

func providerString(params map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := params[key]; ok && value != nil {
			return fmt.Sprint(value)
		}
	}

	return ""
}

func providerStringDefault(params map[string]any, fallback string, keys ...string) string {
	value := providerString(params, keys...)
	if value == "" {
		return fallback
	}
	return value
}

func effectiveSafeSearch(request SearchRequest, provider config.ProviderConfig) (bool, bool) {
	if request.SafeSearch != nil {
		return *request.SafeSearch, true
	}

	if value, ok := provider.DefaultParams["safeSearch"]; ok {
		if typed, ok := value.(bool); ok {
			return typed, true
		}
	}

	if value, ok := provider.DefaultParams["safe_search"]; ok {
		if typed, ok := value.(bool); ok {
			return typed, true
		}
	}

	return false, false
}

func braveFreshness(freshness string) string {
	switch freshness {
	case "day":
		return "pd"
	case "week":
		return "pw"
	case "month":
		return "pm"
	case "year":
		return "py"
	default:
		return ""
	}
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := headers.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func headerInts(headers http.Header, names ...string) []int {
	raw := firstHeader(headers, names...)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil {
			values = append(values, value)
		}
	}
	return values
}

func headerInt64s(headers http.Header, names ...string) []int64 {
	raw := firstHeader(headers, names...)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]int64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil {
			values = append(values, value)
		}
	}
	return values
}

func lastIntPtr(values []int) *int {
	if len(values) == 0 {
		return nil
	}
	value := values[len(values)-1]
	return &value
}

func buildRateLimitWindows(limits []int, remainingValues []int, resetValues []int64) []RateLimitWindow {
	maxLength := max(len(limits), len(remainingValues), len(resetValues))
	if maxLength == 0 {
		return nil
	}

	now := time.Now()
	windows := make([]RateLimitWindow, 0, maxLength)
	for index := 0; index < maxLength; index++ {
		window := RateLimitWindow{}
		if index < len(limits) {
			value := limits[index]
			window.Limit = &value
		}
		if index < len(remainingValues) {
			value := remainingValues[index]
			window.Remaining = &value
		}
		if index < len(resetValues) {
			value := resetValues[index]
			window.ResetAfterSec = &value
			resetAt := now.Add(time.Duration(value) * time.Second)
			window.ResetAt = &resetAt
		}
		windows = append(windows, window)
	}

	return windows
}

func shortestResetDuration(windows []RateLimitWindow) time.Duration {
	var shortest time.Duration
	for _, window := range windows {
		if window.ResetAt == nil {
			continue
		}
		duration := time.Until(*window.ResetAt)
		if duration <= 0 {
			continue
		}
		if shortest == 0 || duration < shortest {
			shortest = duration
		}
	}
	return shortest
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}

	var seconds int64
	if _, err := fmt.Sscanf(value, "%d", &seconds); err == nil {
		return time.Duration(seconds) * time.Second
	}

	if parsed, err := http.ParseTime(value); err == nil {
		return time.Until(parsed)
	}

	return 0
}

func parseResetAt(value string) time.Time {
	if value == "" {
		return time.Time{}
	}

	var number int64
	if _, err := fmt.Sscanf(value, "%d", &number); err == nil {
		if number > 1_000_000_000_000 {
			return time.UnixMilli(number)
		}
		if number > 1_000_000_000 {
			return time.Unix(number, 0)
		}
		return time.Now().Add(time.Duration(number) * time.Second)
	}

	if parsed, err := http.ParseTime(value); err == nil {
		return parsed
	}

	return time.Time{}
}

func hostFromURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}

	return strings.TrimPrefix(parsed.Hostname(), "www.")
}
