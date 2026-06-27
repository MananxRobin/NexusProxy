package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nexusproxy/internal/gateway"
)

type tavilyCompatibleResult struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
	RawContent    *string `json:"raw_content,omitempty"`
	PublishedDate string  `json:"published_date,omitempty"`
}

type tavilyCompatibleResponse struct {
	Query        string                   `json:"query"`
	Answer       *string                  `json:"answer,omitempty"`
	Images       []string                 `json:"images,omitempty"`
	Results      []tavilyCompatibleResult `json:"results"`
	ResponseTime float64                  `json:"response_time"`
}

type braveCompatibleResponse struct {
	Type  string               `json:"type"`
	Query braveCompatibleQuery `json:"query"`
	Web   braveCompatibleWeb   `json:"web"`
}

type braveCompatibleQuery struct {
	Original          string `json:"original"`
	ShowStrictWarning bool   `json:"show_strict_warning"`
	IsNavigational    bool   `json:"is_navigational"`
	IsNewsBreaking    bool   `json:"is_news_breaking"`
	SpellcheckOff     bool   `json:"spellcheck_off"`
	Country           string `json:"country,omitempty"`
}

type braveCompatibleWeb struct {
	Type    string                    `json:"type"`
	Results []braveCompatibleResult   `json:"results"`
	Family  *braveCompatibleWebFamily `json:"family_friendly,omitempty"`
}

type braveCompatibleWebFamily struct {
	FamilyFriendly bool `json:"family_friendly"`
}

type braveCompatibleResult struct {
	Type        string                  `json:"type"`
	Title       string                  `json:"title"`
	URL         string                  `json:"url"`
	Description string                  `json:"description"`
	Age         string                  `json:"age,omitempty"`
	PageAge     string                  `json:"page_age,omitempty"`
	Profile     braveCompatibleProfile  `json:"profile"`
	MetaURL     *braveCompatibleMetaURL `json:"meta_url,omitempty"`
}

type braveCompatibleProfile struct {
	Name     string `json:"name,omitempty"`
	LongName string `json:"long_name,omitempty"`
	URL      string `json:"url,omitempty"`
}

type braveCompatibleMetaURL struct {
	Scheme   string `json:"scheme,omitempty"`
	Netloc   string `json:"netloc,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Path     string `json:"path,omitempty"`
}

func (server *Server) handleTavilyCompatibleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	start := time.Now()
	var input map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request body"})
		return
	}

	if !server.isAuthorizedCompatibleRequest(r, input) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid local NexusProxy token"})
		return
	}

	searchInput := map[string]any{
		"query":       compatibleString(input, "query"),
		"max_results": compatibleInt(input, 10, "max_results", "maxResults"),
		"freshness":   tavilyCompatibleFreshness(input),
	}
	if country := compatibleString(input, "country"); country != "" {
		searchInput["country"] = country
	}
	if language := compatibleString(input, "language"); language != "" {
		searchInput["language"] = language
	}
	if safeSearch, ok := compatibleBool(input, "safe_search", "safeSearch"); ok {
		searchInput["safe_search"] = safeSearch
	}

	response, gatewayErr := server.gateway.Search(r.Context(), searchInput)
	if gatewayErr != nil {
		writeCompatibleError(w, gatewayErr)
		return
	}

	includeAnswer, _ := compatibleBool(input, "include_answer", "includeAnswer")
	includeImages, _ := compatibleBool(input, "include_images", "includeImages")
	includeRawContent, _ := compatibleBool(input, "include_raw_content", "includeRawContent")

	output := tavilyCompatibleResponse{
		Query:        response.Query,
		Results:      tavilyCompatibleResults(response.Results, includeRawContent),
		ResponseTime: secondsSince(start),
	}
	if includeAnswer {
		answer := tavilyAnswer(response.Results)
		output.Answer = &answer
	}
	if includeImages {
		output.Images = []string{}
	}

	writeJSON(w, http.StatusOK, output)
}

func (server *Server) handleBraveCompatibleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": `missing required parameter "q"`})
		return
	}

	searchInput := map[string]any{
		"query":       query,
		"max_results": braveCompatibleCount(r.URL.Query()),
		"freshness":   braveCompatibleFreshness(r.URL.Query().Get("freshness")),
	}
	if country := strings.TrimSpace(r.URL.Query().Get("country")); country != "" {
		searchInput["country"] = country
	}
	if language := braveCompatibleLanguage(r.URL.Query()); language != "" {
		searchInput["language"] = language
	}
	if safeSearch, ok := braveCompatibleSafeSearch(r.URL.Query().Get("safesearch")); ok {
		searchInput["safe_search"] = safeSearch
	}

	response, gatewayErr := server.gateway.Search(r.Context(), searchInput)
	if gatewayErr != nil {
		writeCompatibleError(w, gatewayErr)
		return
	}

	output := braveCompatibleResponse{
		Type: "search",
		Query: braveCompatibleQuery{
			Original: query,
			Country:  strings.TrimSpace(r.URL.Query().Get("country")),
		},
		Web: braveCompatibleWeb{
			Type:    "search",
			Results: braveCompatibleResults(response.Results),
		},
	}

	writeJSON(w, http.StatusOK, output)
}

func (server *Server) requestAuthToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-API-Key")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.Header.Get("X-Subscription-Token")); token != "" {
		return token
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= len("Bearer ") && strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):])
	}

	return ""
}

func (server *Server) isAuthorizedCompatibleRequest(r *http.Request, input map[string]any) bool {
	if server.apiKey == "" {
		return true
	}
	if server.requestAuthToken(r) == server.apiKey {
		return true
	}
	return compatibleString(input, "api_key", "apiKey") == server.apiKey
}

func tavilyCompatibleResults(results []gateway.SearchResult, includeRawContent bool) []tavilyCompatibleResult {
	output := make([]tavilyCompatibleResult, 0, len(results))
	total := len(results)
	for index, result := range results {
		item := tavilyCompatibleResult{
			Title:         result.Title,
			URL:           result.URL,
			Content:       result.Snippet,
			Score:         compatibleScore(index, total),
			PublishedDate: result.PublishedAt,
		}
		if includeRawContent {
			rawContent := result.Snippet
			item.RawContent = &rawContent
		}
		output = append(output, item)
	}
	return output
}

func tavilyAnswer(results []gateway.SearchResult) string {
	if len(results) == 0 {
		return ""
	}
	return results[0].Snippet
}

func braveCompatibleResults(results []gateway.SearchResult) []braveCompatibleResult {
	output := make([]braveCompatibleResult, 0, len(results))
	for _, result := range results {
		source := result.Source
		if source == "" {
			source = hostFromCompatibleURL(result.URL)
		}
		output = append(output, braveCompatibleResult{
			Type:        "search_result",
			Title:       result.Title,
			URL:         result.URL,
			Description: result.Snippet,
			Age:         result.PublishedAt,
			PageAge:     result.PublishedAt,
			Profile: braveCompatibleProfile{
				Name:     source,
				LongName: source,
				URL:      result.URL,
			},
			MetaURL: braveCompatibleMeta(result.URL),
		})
	}
	return output
}

func tavilyCompatibleFreshness(input map[string]any) string {
	if freshness := compatibleFreshness(compatibleString(input, "freshness")); freshness != "" {
		return freshness
	}
	return compatibleFreshness(compatibleString(input, "time_range", "timeRange"))
}

func braveCompatibleFreshness(value string) string {
	return compatibleFreshness(value)
}

func compatibleFreshness(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "d", "day", "pd":
		return "day"
	case "w", "week", "pw":
		return "week"
	case "m", "month", "pm":
		return "month"
	case "y", "year", "py":
		return "year"
	default:
		return "any"
	}
}

func braveCompatibleCount(values url.Values) int {
	count := 10
	if raw := strings.TrimSpace(values.Get("count")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			count = parsed
		}
	}
	return count
}

func braveCompatibleLanguage(values url.Values) string {
	if language := strings.TrimSpace(values.Get("search_lang")); language != "" {
		return language
	}
	uiLanguage := strings.TrimSpace(values.Get("ui_lang"))
	if len(uiLanguage) >= 2 {
		return uiLanguage[:2]
	}
	return ""
}

func braveCompatibleSafeSearch(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return false, false
	case "off":
		return false, true
	default:
		return true, true
	}
}

func compatibleScore(index int, total int) float64 {
	if total <= 1 {
		return 1
	}
	score := float64(total-index) / float64(total)
	return math.Round(score*10000) / 10000
}

func compatibleString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := input[key]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		default:
			return strings.TrimSpace(fmt.Sprint(typed))
		}
	}
	return ""
}

func compatibleInt(input map[string]any, fallback int, keys ...string) int {
	for _, key := range keys {
		value, ok := input[key]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				return fallback
			}
			return int(typed)
		case int:
			return typed
		case int64:
			return int(typed)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				return parsed
			}
		}
	}
	return fallback
}

func compatibleBool(input map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := input[key]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			switch strings.ToLower(strings.TrimSpace(typed)) {
			case "true", "1", "yes":
				return true, true
			case "false", "0", "no":
				return false, true
			}
		}
	}
	return false, false
}

func braveCompatibleMeta(rawURL string) *braveCompatibleMetaURL {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return &braveCompatibleMetaURL{
		Scheme:   parsed.Scheme,
		Netloc:   parsed.Host,
		Hostname: parsed.Hostname(),
		Path:     parsed.EscapedPath(),
	}
}

func hostFromCompatibleURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return rawURL
	}
	return parsed.Hostname()
}

func secondsSince(start time.Time) float64 {
	elapsed := time.Since(start).Seconds()
	return math.Round(elapsed*1000) / 1000
}

func writeCompatibleError(w http.ResponseWriter, gatewayErr *gateway.GatewayError) {
	writeJSON(w, gatewayErr.StatusCode, map[string]any{
		"error":   gatewayErr.Message,
		"message": gatewayErr.Message,
		"details": gatewayErr.Details,
	})
}
