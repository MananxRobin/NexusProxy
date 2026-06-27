package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"nexusproxy/internal/config"
)

type Options struct {
	Client   *http.Client
	Adapters map[string]Adapter
	Now      func() time.Time
}

type SearchGateway struct {
	cfg       config.Config
	client    *http.Client
	now       func() time.Time
	adapters  map[string]Adapter
	providers []*providerState

	mu               sync.Mutex
	roundRobinCursor int
	startedAt        time.Time
	stats            GatewayStats
}

type GatewayStats struct {
	TotalRequests      int64 `json:"total_requests"`
	SuccessfulRequests int64 `json:"successful_requests"`
	FailedRequests     int64 `json:"failed_requests"`
}

type Status struct {
	Name      string           `json:"name"`
	Status    string           `json:"status"`
	StartedAt time.Time        `json:"started_at"`
	Routing   RoutingStatus    `json:"routing"`
	Stats     GatewayStats     `json:"stats"`
	Providers []ProviderStatus `json:"providers"`
}

type RoutingStatus struct {
	Policy      string `json:"policy"`
	MaxAttempts int    `json:"max_attempts"`
}

type ProviderStatus struct {
	ID                   string        `json:"id"`
	Type                 string        `json:"type"`
	Enabled              bool          `json:"enabled"`
	Usable               bool          `json:"usable"`
	Reason               string        `json:"reason"`
	Priority             int           `json:"priority"`
	CooldownUntil        *time.Time    `json:"cooldown_until,omitempty"`
	CooldownRemainingMs  int64         `json:"cooldown_remaining_ms"`
	Quota                ProviderQuota `json:"quota"`
	Stats                ProviderStats `json:"stats"`
	ConfiguredAPIKeyEnv  string        `json:"configured_api_key_env,omitempty"`
	ConfiguredCustomURL  bool          `json:"configured_custom_url"`
	ConfiguredTimeoutMs  int64         `json:"configured_timeout_ms"`
	ConfiguredHasAPIKey  bool          `json:"configured_has_api_key"`
	ConfiguredIsBuiltIn  bool          `json:"configured_is_built_in"`
	ConfiguredIsOverride bool          `json:"configured_is_override"`
}

type ProviderQuota struct {
	Limit     *int              `json:"limit,omitempty"`
	Remaining *int              `json:"remaining,omitempty"`
	ResetAt   *time.Time        `json:"reset_at,omitempty"`
	Windows   []RateLimitWindow `json:"windows,omitempty"`
}

type ProviderStats struct {
	Requests         int64      `json:"requests"`
	Successes        int64      `json:"successes"`
	Failures         int64      `json:"failures"`
	RateLimited      int64      `json:"rate_limited"`
	HealthChecks     int64      `json:"health_checks"`
	LastStatus       int        `json:"last_status,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastSuccess      *time.Time `json:"last_success_at,omitempty"`
	LastAttempt      *time.Time `json:"last_attempt_at,omitempty"`
	LastHealthCheck  *time.Time `json:"last_health_check_at,omitempty"`
	LastUsageCredits *int       `json:"last_usage_credits,omitempty"`
	LastProvider     string     `json:"-"`
}

type providerState struct {
	config     config.ProviderConfig
	adapter    Adapter
	isBuiltIn  bool
	isOverride bool

	cooldownUntil *time.Time
	quota         ProviderQuota
	stats         ProviderStats
}

func New(cfg config.Config, options Options) *SearchGateway {
	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	adapters := builtinAdapters()
	for providerType, adapter := range options.Adapters {
		adapters[providerType] = adapter
	}

	providers := make([]*providerState, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		providers = append(providers, newProviderState(provider, adapters, options.Adapters))
	}

	return &SearchGateway{
		cfg:       cfg,
		client:    client,
		now:       now,
		adapters:  adapters,
		providers: providers,
		startedAt: now(),
	}
}

func (gateway *SearchGateway) Search(ctx context.Context, input map[string]any) (SearchResponse, *GatewayError) {
	request, err := normalizeSearchRequest(input)
	if err != nil {
		return SearchResponse{}, &GatewayError{StatusCode: http.StatusBadRequest, Message: err.Error()}
	}

	gateway.mu.Lock()
	gateway.stats.TotalRequests++
	gateway.mu.Unlock()

	attempts := make([]Attempt, 0, gateway.cfg.Routing.MaxAttempts)
	tried := map[string]bool{}
	var lastFailure any

	for attemptIndex := 0; attemptIndex < gateway.cfg.Routing.MaxAttempts; attemptIndex++ {
		provider := gateway.pickProvider(tried)
		if provider == nil {
			break
		}

		tried[provider.config.ID] = true
		attempt := Attempt{
			Provider: provider.config.ID,
			Type:     provider.config.Type,
		}

		gateway.markAttempt(provider)

		response, err := provider.adapter.Search(ctx, request, provider.config, gateway.client)
		if err != nil {
			attempt.Status = 599
			attempt.OK = false
			attempts = append(attempts, attempt)
			lastFailure = gateway.markFailure(provider, 599, err.Error(), nil)
			continue
		}

		attempt.Status = response.Status
		attempt.OK = response.OK
		attempts = append(attempts, attempt)

		if response.OK {
			gateway.markSuccess(provider, response, true)
			return SearchResponse{
				Query:    request.Query,
				Provider: provider.config.ID,
				Results:  response.Results,
				Attempts: attempts,
				Meta: SearchMeta{
					ResultCount:   len(response.Results),
					RoutingPolicy: gateway.cfg.Routing.Policy,
				},
			}, nil
		}

		lastFailure = gateway.markFailure(provider, response.Status, response.Error, &response.RateLimit)
		if response.Status == http.StatusTooManyRequests {
			gateway.cooldownProvider(provider, response.RateLimit)
		}

		if !gateway.cfg.RetryableStatus(response.Status) && response.Status >= 400 && response.Status < 500 {
			continue
		}
	}

	gateway.mu.Lock()
	gateway.stats.FailedRequests++
	statuses := gateway.providerStatusesLocked()
	gateway.mu.Unlock()

	return SearchResponse{}, &GatewayError{
		StatusCode: http.StatusBadGateway,
		Message:    "no provider completed the search request",
		Details: map[string]any{
			"attempts":     attempts,
			"last_failure": lastFailure,
			"providers":    statuses,
		},
	}
}

func (gateway *SearchGateway) Status() Status {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	return Status{
		Name:      "NexusProxy",
		Status:    "ok",
		StartedAt: gateway.startedAt,
		Routing: RoutingStatus{
			Policy:      gateway.cfg.Routing.Policy,
			MaxAttempts: gateway.cfg.Routing.MaxAttempts,
		},
		Stats:     gateway.stats,
		Providers: gateway.providerStatusesLocked(),
	}
}

func (gateway *SearchGateway) UpdateProviderKeys(values map[string]string) int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	updated := 0
	now := gateway.now()
	existing := map[string]*providerState{}
	for _, provider := range gateway.providers {
		existing[provider.config.APIKeyEnv] = provider
		envName := provider.config.APIKeyEnv
		if envName == "" {
			continue
		}

		value := values[envName]
		if value == "" {
			continue
		}

		provider.config.APIKey = value
		if provider.cooldownUntil != nil && provider.cooldownUntil.Before(now) {
			provider.cooldownUntil = nil
		}
		updated++
	}

	for envName, value := range values {
		if value == "" {
			continue
		}
		if _, exists := existing[envName]; exists {
			continue
		}

		for _, provider := range gateway.providers {
			index, ok := config.APIKeyEnvIndex(provider.config.APIKeyEnv, envName)
			if !ok || index < 2 {
				continue
			}

			clone := provider.config
			clone.ID = fmt.Sprintf("%s-%d", provider.config.ID, index)
			clone.APIKeyEnv = envName
			clone.APIKey = value

			if gateway.providerIDExistsLocked(clone.ID) {
				continue
			}

			gateway.providers = append(gateway.providers, newProviderState(clone, gateway.adapters, nil))
			updated++
			break
		}
	}

	return updated
}

func (gateway *SearchGateway) RefreshProviderHealth(ctx context.Context, query string) []ProviderStatus {
	if query == "" {
		query = "nexusproxy provider health check"
	}

	providers := gateway.providerSnapshot()
	request := SearchRequest{
		Query:      query,
		MaxResults: 1,
		Freshness:  "any",
	}

	for _, provider := range providers {
		if providerReason(provider, gateway.now()) != "ready" {
			continue
		}

		gateway.markAttempt(provider)
		response, err := provider.adapter.Search(ctx, request, provider.config, gateway.client)
		if err != nil {
			gateway.markFailure(provider, 599, err.Error(), nil)
			gateway.markHealthCheck(provider)
			continue
		}

		if response.OK {
			gateway.markSuccess(provider, response, false)
		} else {
			gateway.markFailure(provider, response.Status, response.Error, &response.RateLimit)
			if response.Status == http.StatusTooManyRequests {
				gateway.cooldownProvider(provider, response.RateLimit)
			}
		}
		gateway.markHealthCheck(provider)
	}

	return gateway.Status().Providers
}

func (gateway *SearchGateway) providerSnapshot() []*providerState {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	providers := make([]*providerState, len(gateway.providers))
	copy(providers, gateway.providers)
	return providers
}

func (gateway *SearchGateway) pickProvider(tried map[string]bool) *providerState {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	candidates := gateway.availableProvidersLocked(tried)
	if len(candidates) == 0 {
		return nil
	}

	switch gateway.cfg.Routing.Policy {
	case "round_robin":
		sortProviders(candidates)
		provider := candidates[gateway.roundRobinCursor%len(candidates)]
		gateway.roundRobinCursor = (gateway.roundRobinCursor + 1) % int(^uint(0)>>1)
		return provider
	case "quota_aware":
		sort.SliceStable(candidates, func(left, right int) bool {
			leftRemaining := remainingValue(candidates[left].quota.Remaining)
			rightRemaining := remainingValue(candidates[right].quota.Remaining)
			if leftRemaining != rightRemaining {
				return leftRemaining > rightRemaining
			}
			return candidates[left].config.Priority > candidates[right].config.Priority
		})
		return candidates[0]
	default:
		sortProviders(candidates)
		return candidates[0]
	}
}

func (gateway *SearchGateway) availableProvidersLocked(tried map[string]bool) []*providerState {
	now := gateway.now()
	candidates := make([]*providerState, 0, len(gateway.providers))

	for _, provider := range gateway.providers {
		if tried[provider.config.ID] {
			continue
		}
		if providerReason(provider, now) != "ready" {
			continue
		}
		candidates = append(candidates, provider)
	}

	return candidates
}

func (gateway *SearchGateway) markAttempt(provider *providerState) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	now := gateway.now()
	provider.stats.Requests++
	provider.stats.LastAttempt = &now
}

func (gateway *SearchGateway) markSuccess(provider *providerState, response ProviderResponse, countGlobal bool) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	now := gateway.now()
	provider.stats.Successes++
	provider.stats.LastStatus = response.Status
	provider.stats.LastError = ""
	provider.stats.LastSuccess = &now
	provider.stats.LastUsageCredits = response.Usage.Credits
	gateway.updateQuotaLocked(provider, response.RateLimit)
	if countGlobal {
		gateway.stats.SuccessfulRequests++
	}
}

func (gateway *SearchGateway) markFailure(provider *providerState, status int, message string, rateLimit *RateLimit) map[string]any {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	provider.stats.Failures++
	provider.stats.LastStatus = status
	provider.stats.LastError = message
	if status == http.StatusTooManyRequests {
		provider.stats.RateLimited++
	}
	if rateLimit != nil {
		gateway.updateQuotaLocked(provider, *rateLimit)
	}

	return map[string]any{
		"provider": provider.config.ID,
		"status":   status,
		"message":  message,
	}
}

func (gateway *SearchGateway) markHealthCheck(provider *providerState) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	now := gateway.now()
	provider.stats.HealthChecks++
	provider.stats.LastHealthCheck = &now
}

func (gateway *SearchGateway) cooldownProvider(provider *providerState, rateLimit RateLimit) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	cooldown := gateway.cfg.DefaultCooldown()
	if rateLimit.CooldownMs != nil {
		cooldown = time.Duration(*rateLimit.CooldownMs) * time.Millisecond
	}

	until := gateway.now().Add(cooldown)
	provider.cooldownUntil = &until
}

func (gateway *SearchGateway) updateQuotaLocked(provider *providerState, rateLimit RateLimit) {
	if rateLimit.Limit != nil {
		provider.quota.Limit = rateLimit.Limit
	}
	if rateLimit.Remaining != nil {
		provider.quota.Remaining = rateLimit.Remaining
	}
	if rateLimit.ResetAt != nil {
		provider.quota.ResetAt = rateLimit.ResetAt
	}
	if len(rateLimit.Windows) > 0 {
		provider.quota.Windows = rateLimit.Windows
	}
}

func (gateway *SearchGateway) providerStatusesLocked() []ProviderStatus {
	now := gateway.now()
	statuses := make([]ProviderStatus, 0, len(gateway.providers))

	for _, provider := range gateway.providers {
		reason := providerReason(provider, now)
		var cooldownRemainingMs int64
		if provider.cooldownUntil != nil && provider.cooldownUntil.After(now) {
			cooldownRemainingMs = int64(provider.cooldownUntil.Sub(now) / time.Millisecond)
		}

		statuses = append(statuses, ProviderStatus{
			ID:                   provider.config.ID,
			Type:                 provider.config.Type,
			Enabled:              provider.config.IsEnabled(),
			Usable:               reason == "ready",
			Reason:               reason,
			Priority:             provider.config.Priority,
			CooldownUntil:        provider.cooldownUntil,
			CooldownRemainingMs:  cooldownRemainingMs,
			Quota:                provider.quota,
			Stats:                provider.stats,
			ConfiguredAPIKeyEnv:  provider.config.APIKeyEnv,
			ConfiguredCustomURL:  provider.config.Endpoint != "",
			ConfiguredTimeoutMs:  provider.config.TimeoutMs,
			ConfiguredHasAPIKey:  provider.config.APIKey != "",
			ConfiguredIsBuiltIn:  provider.isBuiltIn,
			ConfiguredIsOverride: provider.isOverride,
		})
	}

	return statuses
}

func providerReason(provider *providerState, now time.Time) string {
	switch {
	case !provider.config.IsEnabled():
		return "disabled"
	case provider.adapter == nil:
		return "unknown_provider_type"
	case provider.config.APIKey == "":
		return "missing_api_key"
	case provider.cooldownUntil != nil && provider.cooldownUntil.After(now):
		return "cooling_down"
	default:
		return "ready"
	}
}

func sortProviders(providers []*providerState) {
	sort.SliceStable(providers, func(left, right int) bool {
		return providers[left].config.Priority > providers[right].config.Priority
	})
}

func remainingValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func newProviderState(provider config.ProviderConfig, adapters map[string]Adapter, overrideAdapters map[string]Adapter) *providerState {
	adapter, ok := adapters[provider.Type]
	_, builtIn := builtinAdapters()[provider.Type]
	override := false
	if overrideAdapters != nil {
		_, override = overrideAdapters[provider.Type]
	}
	if !ok {
		adapter = nil
	}

	return &providerState{
		config:     provider,
		adapter:    adapter,
		isBuiltIn:  builtIn,
		isOverride: override,
	}
}

func (gateway *SearchGateway) providerIDExistsLocked(id string) bool {
	for _, provider := range gateway.providers {
		if provider.config.ID == id {
			return true
		}
	}
	return false
}
