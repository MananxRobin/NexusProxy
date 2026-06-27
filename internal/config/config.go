package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server    ServerConfig     `json:"server"`
	Routing   RoutingConfig    `json:"routing"`
	Providers []ProviderConfig `json:"providers"`
}

type ServerConfig struct {
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	APIKey                string `json:"apiKey"`
	MaxConcurrentRequests int    `json:"maxConcurrentRequests"`
}

type RoutingConfig struct {
	Policy            string `json:"policy"`
	MaxAttempts       int    `json:"maxAttempts"`
	RetryOnStatuses   []int  `json:"retryOnStatuses"`
	DefaultCooldownMs int64  `json:"defaultCooldownMs"`
}

type ProviderConfig struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Enabled       *bool          `json:"enabled,omitempty"`
	Priority      int            `json:"priority"`
	APIKey        string         `json:"apiKey"`
	APIKeyEnv     string         `json:"apiKeyEnv"`
	DefaultParams map[string]any `json:"defaultParams"`
	Endpoint      string         `json:"endpoint"`
	TimeoutMs     int64          `json:"timeoutMs"`
}

func Load(path string) (Config, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(bytes, &cfg); err != nil {
		return Config{}, err
	}

	if err := cfg.Normalize(path); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (cfg *Config) Normalize(source string) error {
	if value := os.Getenv("NEXUS_HOST"); value != "" {
		cfg.Server.Host = value
	} else if cfg.Server.Host == "" {
		cfg.Server.Host = "127.0.0.1"
	}
	if value := os.Getenv("NEXUS_PORT"); value != "" {
		fallback := cfg.Server.Port
		if fallback == 0 {
			fallback = 8787
		}
		cfg.Server.Port = parseInt(value, fallback)
	} else if cfg.Server.Port == 0 {
		cfg.Server.Port = 8787
	}
	if value := os.Getenv("NEXUS_API_KEY"); value != "" {
		cfg.Server.APIKey = value
	}
	if value := os.Getenv("NEXUS_MAX_CONCURRENT_REQUESTS"); value != "" {
		fallback := cfg.Server.MaxConcurrentRequests
		if fallback == 0 {
			fallback = 8
		}
		cfg.Server.MaxConcurrentRequests = parseInt(value, fallback)
	} else if cfg.Server.MaxConcurrentRequests == 0 {
		cfg.Server.MaxConcurrentRequests = 8
	}
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("%s has invalid server.port", source)
	}
	if cfg.Server.MaxConcurrentRequests < 0 {
		return fmt.Errorf("%s has invalid server.maxConcurrentRequests", source)
	}

	if cfg.Routing.Policy == "" {
		cfg.Routing.Policy = "priority"
	}
	if cfg.Routing.Policy != "priority" && cfg.Routing.Policy != "round_robin" && cfg.Routing.Policy != "quota_aware" {
		return fmt.Errorf("%s has invalid routing.policy %q", source, cfg.Routing.Policy)
	}
	if cfg.Routing.MaxAttempts == 0 {
		cfg.Routing.MaxAttempts = 3
	}
	if cfg.Routing.MaxAttempts < 1 {
		return fmt.Errorf("%s has invalid routing.maxAttempts", source)
	}
	if len(cfg.Routing.RetryOnStatuses) == 0 {
		cfg.Routing.RetryOnStatuses = []int{429, 500, 502, 503, 504}
	}
	if cfg.Routing.DefaultCooldownMs == 0 {
		cfg.Routing.DefaultCooldownMs = int64(time.Minute / time.Millisecond)
	}

	if len(cfg.Providers) == 0 {
		return errors.New("config must include at least one provider")
	}

	ids := map[string]bool{}
	normalizedProviders := make([]ProviderConfig, 0, len(cfg.Providers))
	for index := range cfg.Providers {
		provider := &cfg.Providers[index]
		if provider.ID == "" || provider.Type == "" {
			return fmt.Errorf("provider at index %d must include id and type", index)
		}
		if ids[provider.ID] {
			return fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		ids[provider.ID] = true

		if provider.APIKey == "" && provider.APIKeyEnv != "" {
			provider.APIKey = os.Getenv(provider.APIKeyEnv)
		}
		if provider.DefaultParams == nil {
			provider.DefaultParams = map[string]any{}
		}
		if provider.TimeoutMs == 0 {
			provider.TimeoutMs = int64(20 * time.Second / time.Millisecond)
		}
		normalizedProviders = append(normalizedProviders, *provider)

		for _, extraEnv := range ExtraAPIKeyEnvNames(provider.APIKeyEnv) {
			extraKey := os.Getenv(extraEnv)
			if extraKey == "" {
				continue
			}

			index, ok := APIKeyEnvIndex(provider.APIKeyEnv, extraEnv)
			if !ok {
				continue
			}

			clone := *provider
			clone.ID = fmt.Sprintf("%s-%d", provider.ID, index)
			clone.APIKeyEnv = extraEnv
			clone.APIKey = extraKey

			if ids[clone.ID] {
				return fmt.Errorf("duplicate expanded provider id %q", clone.ID)
			}
			ids[clone.ID] = true
			normalizedProviders = append(normalizedProviders, clone)
		}
	}

	cfg.Providers = normalizedProviders
	return nil
}

func (cfg Config) RetryableStatus(status int) bool {
	for _, retryable := range cfg.Routing.RetryOnStatuses {
		if status == retryable {
			return true
		}
	}
	return false
}

func (cfg Config) DefaultCooldown() time.Duration {
	return time.Duration(cfg.Routing.DefaultCooldownMs) * time.Millisecond
}

func (provider ProviderConfig) IsEnabled() bool {
	return provider.Enabled == nil || *provider.Enabled
}

func (provider ProviderConfig) Timeout() time.Duration {
	return time.Duration(provider.TimeoutMs) * time.Millisecond
}

func parseInt(value string, fallback int) int {
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}

	return parsed
}

func ExtraAPIKeyEnvNames(base string) []string {
	if base == "" {
		return nil
	}

	names := []string{}
	prefix := base + "_"
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if !found || !strings.HasPrefix(key, prefix) {
			continue
		}

		index, err := strconv.Atoi(strings.TrimPrefix(key, prefix))
		if err != nil || index < 2 {
			continue
		}
		names = append(names, key)
	}

	sort.Slice(names, func(left, right int) bool {
		leftIndex, _ := APIKeyEnvIndex(base, names[left])
		rightIndex, _ := APIKeyEnvIndex(base, names[right])
		return leftIndex < rightIndex
	})
	return names
}

func APIKeyEnvIndex(base string, candidate string) (int, bool) {
	if base == "" || candidate == base {
		return 1, candidate == base
	}

	prefix := base + "_"
	if !strings.HasPrefix(candidate, prefix) {
		return 0, false
	}

	index, err := strconv.Atoi(strings.TrimPrefix(candidate, prefix))
	if err != nil || index < 2 {
		return 0, false
	}

	return index, true
}

func NextAPIKeyEnvName(base string) string {
	if base == "" {
		return ""
	}

	next := 2
	for _, name := range ExtraAPIKeyEnvNames(base) {
		index, ok := APIKeyEnvIndex(base, name)
		if ok && index >= next {
			next = index + 1
		}
	}

	return fmt.Sprintf("%s_%d", base, next)
}
