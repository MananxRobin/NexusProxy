package gateway

import "time"

type SearchRequest struct {
	Query      string
	MaxResults int
	Country    string
	Language   string
	Freshness  string
	SafeSearch *bool
}

type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at,omitempty"`
	Source      string `json:"source,omitempty"`
	Rank        int    `json:"rank"`
	Provider    string `json:"provider"`
}

type SearchResponse struct {
	Query    string         `json:"query"`
	Provider string         `json:"provider"`
	Results  []SearchResult `json:"results"`
	Attempts []Attempt      `json:"attempts"`
	Meta     SearchMeta     `json:"meta"`
}

type SearchMeta struct {
	ResultCount   int    `json:"result_count"`
	RoutingPolicy string `json:"routing_policy"`
}

type Attempt struct {
	Provider string `json:"provider"`
	Type     string `json:"type"`
	Status   int    `json:"status"`
	OK       bool   `json:"ok"`
}

type RateLimit struct {
	Limit      *int
	Remaining  *int
	ResetAt    *time.Time
	CooldownMs *int64
	Windows    []RateLimitWindow
}

type RateLimitWindow struct {
	Limit         *int       `json:"limit,omitempty"`
	Remaining     *int       `json:"remaining,omitempty"`
	ResetAt       *time.Time `json:"reset_at,omitempty"`
	ResetAfterSec *int64     `json:"reset_after_sec,omitempty"`
}

type ProviderUsage struct {
	Credits *int `json:"credits,omitempty"`
}

type ProviderResponse struct {
	OK        bool
	Status    int
	Results   []SearchResult
	RateLimit RateLimit
	Usage     ProviderUsage
	Error     string
}

type GatewayError struct {
	StatusCode int    `json:"-"`
	Message    string `json:"message"`
	Details    any    `json:"details,omitempty"`
}

func (err *GatewayError) Error() string {
	return err.Message
}
