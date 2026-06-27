package gateway

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

func normalizeSearchRequest(input map[string]any) (SearchRequest, error) {
	query := strings.TrimSpace(firstString(input, "query", "q"))
	if query == "" {
		return SearchRequest{}, errors.New(`missing required field "query"`)
	}

	maxResults := firstInt(input, 10, "max_results", "maxResults")
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 20 {
		maxResults = 20
	}

	freshness := strings.ToLower(strings.TrimSpace(firstString(input, "freshness")))
	switch freshness {
	case "", "any":
		freshness = "any"
	case "day", "week", "month", "year":
	default:
		freshness = "any"
	}

	return SearchRequest{
		Query:      query,
		MaxResults: maxResults,
		Country:    strings.TrimSpace(firstString(input, "country")),
		Language:   strings.TrimSpace(firstString(input, "language")),
		Freshness:  freshness,
		SafeSearch: firstBoolPtr(input, "safe_search", "safeSearch"),
	}, nil
}

func firstString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := input[key]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case string:
			return typed
		default:
			return fmt.Sprint(typed)
		}
	}

	return ""
}

func firstInt(input map[string]any, fallback int, keys ...string) int {
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
			var parsed int
			if _, err := fmt.Sscanf(typed, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}

	return fallback
}

func firstBoolPtr(input map[string]any, keys ...string) *bool {
	for _, key := range keys {
		value, ok := input[key]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case bool:
			return &typed
		case string:
			normalized := strings.ToLower(strings.TrimSpace(typed))
			if normalized == "true" || normalized == "1" || normalized == "yes" {
				value := true
				return &value
			}
			if normalized == "false" || normalized == "0" || normalized == "no" {
				value := false
				return &value
			}
		}
	}

	return nil
}
