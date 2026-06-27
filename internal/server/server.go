package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"nexusproxy/internal/config"
	"nexusproxy/internal/gateway"
)

type Server struct {
	gateway *gateway.SearchGateway
	apiKey  string
	envPath string
	limiter chan struct{}
}

type dashboardData struct {
	gateway.Status
	EnvPath       string
	RequiresToken bool
	KeyGroups     []providerKeyGroup
	Message       string
	Error         string
}

type providerKeyGroup struct {
	Type        string
	BaseEnvName string
	NextEnvName string
	Count       int
}

func New(searchGateway *gateway.SearchGateway, apiKey string, envPath string, maxConcurrentRequests int) http.Handler {
	server := &Server{
		gateway: searchGateway,
		apiKey:  apiKey,
		envPath: envPath,
	}
	if maxConcurrentRequests > 0 {
		server.limiter = make(chan struct{}, maxConcurrentRequests)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.handleHome)
	mux.HandleFunc("/dashboard", server.handleDashboard)
	mux.HandleFunc("/playground", server.handlePlayground)
	mux.HandleFunc("/dashboard/provider-keys", server.handleProviderKeys)
	mux.HandleFunc("/dashboard/provider-key-add", server.handleProviderKeyAdd)
	mux.HandleFunc("/dashboard/refresh-providers", server.handleRefreshProviders)
	mux.HandleFunc("/healthz", server.handleHealth)
	mux.HandleFunc("/search", server.handleTavilyCompatibleSearch)
	mux.HandleFunc("/res/v1/web/search", server.withAuth(server.handleBraveCompatibleSearch))
	mux.HandleFunc("/v1/status", server.withAuth(server.handleStatus))
	mux.HandleFunc("/v1/search", server.withAuth(server.handleSearch))
	return server.withConcurrencyLimit(mux)
}

func (server *Server) withConcurrencyLimit(next http.Handler) http.Handler {
	if server.limiter == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		select {
		case server.limiter <- struct{}{}:
			defer func() {
				<-server.limiter
			}()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "nexusproxy is busy; retry shortly"})
		}
	})
}

func (server *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (server *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (server *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, server.gateway.Status())
}

func (server *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	var input map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON request body"})
		return
	}

	response, gatewayErr := server.gateway.Search(r.Context(), input)
	if gatewayErr != nil {
		writeJSON(w, gatewayErr.StatusCode, gatewayErr)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (server *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	data := dashboardData{
		Status:        server.gateway.Status(),
		EnvPath:       server.envPath,
		RequiresToken: server.apiKey != "",
		KeyGroups:     providerKeyGroups(server.gateway.Status().Providers),
		Message:       r.URL.Query().Get("message"),
		Error:         r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (server *Server) handlePlayground(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	data := dashboardData{
		Status:        server.gateway.Status(),
		EnvPath:       server.envPath,
		RequiresToken: server.apiKey != "",
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := playgroundTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (server *Server) handleProviderKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		server.redirectDashboard(w, r, "", "invalid form submission")
		return
	}

	if server.apiKey != "" && r.FormValue("nexus_api_key") != server.apiKey {
		server.redirectDashboard(w, r, "", "missing or invalid local token")
		return
	}

	values := map[string]string{}
	for _, provider := range server.gateway.Status().Providers {
		envName := provider.ConfiguredAPIKeyEnv
		if envName == "" {
			continue
		}

		value := strings.TrimSpace(r.FormValue("key_" + envName))
		if value != "" {
			values[envName] = value
		}
	}

	if len(values) == 0 {
		server.redirectDashboard(w, r, "", "no keys were entered")
		return
	}

	if err := config.SaveEnvValues(server.envPath, values); err != nil {
		server.redirectDashboard(w, r, "", err.Error())
		return
	}
	if err := config.ApplyEnvValues(values); err != nil {
		server.redirectDashboard(w, r, "", err.Error())
		return
	}

	updated := server.gateway.UpdateProviderKeys(values)
	server.redirectDashboard(w, r, pluralizeKeys(updated)+" saved to "+server.envPath, "")
}

func (server *Server) handleProviderKeyAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		server.redirectDashboard(w, r, "", "invalid form submission")
		return
	}

	if server.apiKey != "" && r.FormValue("nexus_api_key") != server.apiKey {
		server.redirectDashboard(w, r, "", "missing or invalid local token")
		return
	}

	baseEnv := strings.TrimSpace(r.FormValue("base_env"))
	value := strings.TrimSpace(r.FormValue("api_key"))
	if baseEnv == "" || value == "" {
		server.redirectDashboard(w, r, "", "provider and API key are required")
		return
	}

	nextEnv := nextProviderEnvName(baseEnv, server.gateway.Status().Providers)
	if nextEnv == "" {
		server.redirectDashboard(w, r, "", "could not allocate a provider key slot")
		return
	}

	values := map[string]string{nextEnv: value}
	if err := config.SaveEnvValues(server.envPath, values); err != nil {
		server.redirectDashboard(w, r, "", err.Error())
		return
	}
	if err := config.ApplyEnvValues(values); err != nil {
		server.redirectDashboard(w, r, "", err.Error())
		return
	}

	updated := server.gateway.UpdateProviderKeys(values)
	if updated == 0 {
		server.redirectDashboard(w, r, "", "key saved, but no matching provider template was found")
		return
	}

	server.redirectDashboard(w, r, nextEnv+" saved to "+server.envPath, "")
}

func (server *Server) handleRefreshProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		server.redirectDashboard(w, r, "", "invalid form submission")
		return
	}

	if server.apiKey != "" && r.FormValue("nexus_api_key") != server.apiKey {
		server.redirectDashboard(w, r, "", "missing or invalid local NexusProxy token")
		return
	}

	query := strings.TrimSpace(r.FormValue("probe_query"))
	statuses := server.gateway.RefreshProviderHealth(r.Context(), query)

	checked := 0
	for _, provider := range statuses {
		if provider.Stats.LastHealthCheck != nil {
			checked++
		}
	}

	server.redirectDashboard(w, r, fmt.Sprintf("provider health refreshed for %d providers", checked), "")
}

func (server *Server) redirectDashboard(w http.ResponseWriter, r *http.Request, message string, errorMessage string) {
	query := url.Values{}
	if message != "" {
		query.Set("message", message)
	}
	if errorMessage != "" {
		query.Set("error", errorMessage)
	}

	location := "/dashboard"
	if encoded := query.Encode(); encoded != "" {
		location += "?" + encoded
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (server *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if server.apiKey == "" {
			next(w, r)
			return
		}

		if server.requestAuthToken(r) != server.apiKey {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "missing or invalid local NexusProxy token"})
			return
		}

		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func pluralizeKeys(count int) string {
	if count == 1 {
		return "1 key"
	}
	return fmt.Sprintf("%d keys", count)
}

func providerKeyGroups(providers []gateway.ProviderStatus) []providerKeyGroup {
	byBase := map[string]providerKeyGroup{}
	order := []string{}

	for _, provider := range providers {
		envName := provider.ConfiguredAPIKeyEnv
		if envName == "" {
			continue
		}

		baseEnv := baseProviderEnvName(envName)
		group, exists := byBase[baseEnv]
		if !exists {
			group = providerKeyGroup{
				Type:        provider.Type,
				BaseEnvName: baseEnv,
			}
			order = append(order, baseEnv)
		}
		group.Count++
		byBase[baseEnv] = group
	}

	groups := make([]providerKeyGroup, 0, len(order))
	for _, baseEnv := range order {
		group := byBase[baseEnv]
		group.NextEnvName = nextProviderEnvName(baseEnv, providers)
		groups = append(groups, group)
	}

	return groups
}

func nextProviderEnvName(baseEnv string, providers []gateway.ProviderStatus) string {
	next := 2
	for _, provider := range providers {
		envName := provider.ConfiguredAPIKeyEnv
		if baseProviderEnvName(envName) != baseEnv {
			continue
		}
		index := providerEnvIndex(baseEnv, envName)
		if index >= next {
			next = index + 1
		}
	}
	return fmt.Sprintf("%s_%d", baseEnv, next)
}

func baseProviderEnvName(envName string) string {
	index := strings.LastIndex(envName, "_")
	if index < 0 {
		return envName
	}

	suffix := envName[index+1:]
	if suffix == "" {
		return envName
	}

	for _, char := range suffix {
		if char < '0' || char > '9' {
			return envName
		}
	}

	return envName[:index]
}

func providerEnvIndex(baseEnv string, envName string) int {
	if envName == baseEnv {
		return 1
	}
	prefix := baseEnv + "_"
	if !strings.HasPrefix(envName, prefix) {
		return 0
	}

	var index int
	if _, err := fmt.Sscanf(strings.TrimPrefix(envName, prefix), "%d", &index); err != nil {
		return 0
	}
	return index
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>NexusProxy</title>
  <style>
    :root {
      color-scheme: light dark;
      --bg: #f7f8fb;
      --fg: #16181d;
      --muted: #5f6673;
      --line: #d9dee8;
      --ready: #0b7f43;
      --warn: #9a5b00;
      --bad: #b42318;
      --panel: #ffffff;
      --accent: #1f6feb;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg: #101114;
        --fg: #f2f4f8;
        --muted: #a8afbd;
        --line: #2c313a;
        --panel: #181b20;
      }
    }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--fg);
      font: 14px/1.5 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    main {
      max-width: 1120px;
      margin: 0 auto;
      padding: 28px 20px 40px;
    }
    header {
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 20px;
      margin-bottom: 24px;
    }
    h1 {
      font-size: 28px;
      margin: 0 0 4px;
      letter-spacing: 0;
    }
    .muted {
      color: var(--muted);
    }
    .summary {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 12px;
      margin-bottom: 20px;
    }
    .metric, table, .panel {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
    }
    .metric {
      padding: 14px 16px;
    }
    .metric strong {
      display: block;
      font-size: 24px;
      line-height: 1.2;
    }
    table {
      width: 100%;
      border-collapse: separate;
      border-spacing: 0;
      overflow: hidden;
    }
    th, td {
      padding: 11px 12px;
      text-align: left;
      border-bottom: 1px solid var(--line);
      vertical-align: top;
      white-space: nowrap;
    }
    th {
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
      text-transform: uppercase;
    }
    tr:last-child td {
      border-bottom: 0;
    }
    .panel {
      margin: 0 0 20px;
      padding: 16px;
    }
    .panel h2 {
      font-size: 16px;
      margin: 0 0 12px;
      letter-spacing: 0;
    }
    .keys-grid {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 12px;
      margin-bottom: 12px;
    }
    label {
      display: block;
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
      margin-bottom: 5px;
      text-transform: uppercase;
    }
    input, select {
      width: 100%;
      box-sizing: border-box;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: transparent;
      color: var(--fg);
      font: inherit;
      padding: 9px 10px;
    }
    .actions {
      display: flex;
      align-items: center;
      gap: 12px;
      justify-content: space-between;
      flex-wrap: wrap;
    }
    button, .button-link {
      border: 1px solid var(--accent);
      border-radius: 6px;
      background: var(--accent);
      color: #fff;
      cursor: pointer;
      display: inline-block;
      font: inherit;
      font-weight: 700;
      padding: 9px 12px;
      text-decoration: none;
    }
    .button-link {
      background: transparent;
      color: var(--accent);
    }
    .banner {
      border-radius: 6px;
      margin: 0 0 16px;
      padding: 10px 12px;
    }
    .banner.ok {
      background: color-mix(in srgb, var(--ready) 14%, transparent);
      color: var(--ready);
    }
    .banner.error {
      background: color-mix(in srgb, var(--bad) 14%, transparent);
      color: var(--bad);
    }
    .ready { color: var(--ready); font-weight: 700; }
    .warn { color: var(--warn); font-weight: 700; }
    .bad { color: var(--bad); font-weight: 700; }
    code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
    }
    @media (max-width: 760px) {
      header {
        display: block;
      }
      .summary {
        grid-template-columns: repeat(2, minmax(0, 1fr));
      }
      .keys-grid {
        grid-template-columns: 1fr;
      }
      table {
        display: block;
        overflow-x: auto;
      }
    }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <h1>NexusProxy</h1>
        <div class="muted">Local search gateway · routing: <code>{{.Routing.Policy}}</code></div>
      </div>
      <div class="actions">
        <a class="button-link" href="/playground">Search Playground</a>
        <a class="button-link" href="/dashboard">Refresh</a>
      </div>
    </header>

    {{if .Message}}<div class="banner ok">{{.Message}}</div>{{end}}
    {{if .Error}}<div class="banner error">{{.Error}}</div>{{end}}

    <section class="summary" aria-label="Gateway metrics">
      <div class="metric"><span class="muted">Total</span><strong>{{.Stats.TotalRequests}}</strong></div>
      <div class="metric"><span class="muted">Succeeded</span><strong>{{.Stats.SuccessfulRequests}}</strong></div>
      <div class="metric"><span class="muted">Failed</span><strong>{{.Stats.FailedRequests}}</strong></div>
      <div class="metric"><span class="muted">Providers</span><strong>{{len .Providers}}</strong></div>
    </section>

    <section class="panel">
      <h2>Provider API Keys</h2>
      <form method="post" action="/dashboard/provider-keys" autocomplete="off">
        <div class="keys-grid">
          {{range .Providers}}
            {{if .ConfiguredAPIKeyEnv}}
            <div>
              <label for="key_{{.ConfiguredAPIKeyEnv}}">{{.Type}} · {{.ConfiguredAPIKeyEnv}}</label>
              <input id="key_{{.ConfiguredAPIKeyEnv}}" name="key_{{.ConfiguredAPIKeyEnv}}" type="password" placeholder="{{if .ConfiguredHasAPIKey}}Saved{{else}}Paste key{{end}}">
            </div>
            {{end}}
          {{end}}
          {{if .RequiresToken}}
          <div>
            <label for="nexus_api_key">NexusProxy token</label>
            <input id="nexus_api_key" name="nexus_api_key" type="password" placeholder="local token" required>
          </div>
          {{end}}
        </div>
        <div class="actions">
          <button type="submit">Save Keys</button>
          <span class="muted">Saved to <code>{{.EnvPath}}</code>. Existing keys can stay blank.</span>
        </div>
      </form>
    </section>

    <section class="panel">
      <h2>Add Another Provider Key</h2>
      <form method="post" action="/dashboard/provider-key-add" autocomplete="off">
        <div class="keys-grid">
          <div>
            <label for="base_env">Provider</label>
            <select id="base_env" name="base_env" required>
              {{range .KeyGroups}}
              <option value="{{.BaseEnvName}}">{{.Type}} · next {{.NextEnvName}}</option>
              {{end}}
            </select>
          </div>
          <div>
            <label for="extra_api_key">API key</label>
            <input id="extra_api_key" name="api_key" type="password" placeholder="Paste another account key" required>
          </div>
          {{if .RequiresToken}}
          <div>
            <label for="add_nexus_api_key">NexusProxy token</label>
            <input id="add_nexus_api_key" name="nexus_api_key" type="password" placeholder="local token" required>
          </div>
          {{end}}
        </div>
        <div class="actions">
          <button type="submit">Add Key</button>
          <span class="muted">Creates numbered keys like <code>BRAVE_API_KEY_2</code> and routes them as separate provider slots.</span>
        </div>
      </form>
    </section>

    <section class="panel">
      <h2>Provider Health Refresh</h2>
      <form method="post" action="/dashboard/refresh-providers" autocomplete="off">
        <div class="keys-grid">
          <div>
            <label for="probe_query">Probe query</label>
            <input id="probe_query" name="probe_query" value="nexusproxy provider health check">
          </div>
          {{if .RequiresToken}}
          <div>
            <label for="refresh_nexus_api_key">NexusProxy token</label>
            <input id="refresh_nexus_api_key" name="nexus_api_key" type="password" placeholder="local token" required>
          </div>
          {{end}}
        </div>
        <div class="actions">
          <button type="submit">Refresh Provider Health</button>
          <span class="muted">Runs one 1-result probe per ready provider to update real headers, cooldowns, and usage.</span>
        </div>
      </form>
    </section>

    <table>
      <thead>
        <tr>
          <th>Provider</th>
          <th>State</th>
          <th>Priority</th>
          <th>Quota</th>
          <th>Cooldown</th>
          <th>Requests</th>
          <th>Last Status</th>
          <th>Last Error</th>
        </tr>
      </thead>
      <tbody>
        {{range .Providers}}
        <tr>
          <td><strong>{{.ID}}</strong><br><span class="muted">{{.Type}}</span></td>
          <td>
            {{if .Usable}}<span class="ready">ready</span>{{else if eq .Reason "cooling_down"}}<span class="warn">{{.Reason}}</span>{{else}}<span class="bad">{{.Reason}}</span>{{end}}
          </td>
          <td>{{.Priority}}</td>
          <td>
            {{if .Quota.Windows}}
              {{range .Quota.Windows}}
                {{if .Remaining}}{{.Remaining}} remaining{{else}}unknown remaining{{end}}{{if .Limit}} / {{.Limit}}{{end}}
                {{if .ResetAt}}<br><span class="muted">resets {{.ResetAt.Format "15:04:05 MST"}}</span>{{end}}
                <br>
              {{end}}
            {{else}}
              {{if .Quota.Remaining}}{{.Quota.Remaining}} remaining{{else}}unknown{{end}}
              {{if .Quota.ResetAt}}<br><span class="muted">resets {{.Quota.ResetAt.Format "15:04:05 MST"}}</span>{{end}}
            {{end}}
            {{if .Stats.LastUsageCredits}}<span class="muted">last usage: {{.Stats.LastUsageCredits}} credits</span>{{end}}
          </td>
          <td>{{.CooldownRemainingMs}} ms</td>
          <td>{{.Stats.Requests}} / {{.Stats.Successes}} ok / {{.Stats.Failures}} fail{{if .Stats.HealthChecks}}<br><span class="muted">{{.Stats.HealthChecks}} health checks</span>{{end}}</td>
          <td>{{if .Stats.LastStatus}}{{.Stats.LastStatus}}{{else}}none{{end}}</td>
          <td>{{if .Stats.LastError}}{{.Stats.LastError}}{{else}}<span class="muted">none</span>{{end}}</td>
        </tr>
        {{end}}
      </tbody>
    </table>
  </main>
</body>
</html>`))

var playgroundTemplate = template.Must(template.New("playground").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>NexusProxy Playground</title>
  <style>
    :root {
      color-scheme: light dark;
      --bg: #f7f8fb;
      --fg: #16181d;
      --muted: #5f6673;
      --line: #d9dee8;
      --ready: #0b7f43;
      --warn: #9a5b00;
      --bad: #b42318;
      --panel: #ffffff;
      --accent: #1f6feb;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg: #101114;
        --fg: #f2f4f8;
        --muted: #a8afbd;
        --line: #2c313a;
        --panel: #181b20;
      }
    }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--fg);
      font: 14px/1.5 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    main {
      max-width: 1120px;
      margin: 0 auto;
      padding: 28px 20px 44px;
    }
    header {
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 20px;
      margin-bottom: 22px;
    }
    h1 {
      font-size: 28px;
      margin: 0 0 4px;
      letter-spacing: 0;
    }
    h2 {
      font-size: 16px;
      margin: 0 0 12px;
      letter-spacing: 0;
    }
    .muted { color: var(--muted); }
    .panel, .result-card {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
    }
    .panel {
      margin-bottom: 18px;
      padding: 16px;
    }
    .search-grid {
      display: grid;
      grid-template-columns: minmax(220px, 1fr) 120px 130px 130px;
      gap: 12px;
      align-items: end;
    }
    label {
      display: block;
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
      margin-bottom: 5px;
      text-transform: uppercase;
    }
    input, select {
      width: 100%;
      box-sizing: border-box;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: transparent;
      color: var(--fg);
      font: inherit;
      padding: 9px 10px;
    }
    button, .button-link {
      border: 1px solid var(--accent);
      border-radius: 6px;
      background: var(--accent);
      color: #fff;
      cursor: pointer;
      display: inline-block;
      font: inherit;
      font-weight: 700;
      padding: 9px 12px;
      text-decoration: none;
    }
    button:disabled {
      cursor: wait;
      opacity: 0.65;
    }
    .button-link {
      background: transparent;
      color: var(--accent);
    }
    .actions {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: 10px;
      margin-top: 12px;
    }
    .status-line {
      border-radius: 6px;
      margin-bottom: 16px;
      padding: 10px 12px;
    }
    .status-line.ok {
      background: color-mix(in srgb, var(--ready) 14%, transparent);
      color: var(--ready);
    }
    .status-line.error {
      background: color-mix(in srgb, var(--bad) 14%, transparent);
      color: var(--bad);
    }
    .result-list {
      display: grid;
      gap: 12px;
      margin-top: 12px;
    }
    .result-card {
      padding: 14px 16px;
    }
    .result-card a {
      color: var(--accent);
      font-size: 16px;
      font-weight: 700;
      text-decoration: none;
    }
    .result-card a:hover {
      text-decoration: underline;
    }
    .result-meta {
      color: var(--muted);
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
      margin-top: 8px;
      font-size: 12px;
    }
    .attempts {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 8px;
    }
    .attempt {
      border: 1px solid var(--line);
      border-radius: 999px;
      padding: 4px 9px;
      font-size: 12px;
    }
    .attempt.ok { color: var(--ready); }
    .attempt.fail { color: var(--bad); }
    pre {
      background: color-mix(in srgb, var(--line) 35%, transparent);
      border-radius: 8px;
      overflow-x: auto;
      padding: 12px;
      white-space: pre-wrap;
    }
    code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
    }
    .ready { color: var(--ready); font-weight: 700; }
    .bad { color: var(--bad); font-weight: 700; }
    @media (max-width: 880px) {
      header { display: block; }
      .search-grid { grid-template-columns: 1fr 1fr; }
    }
    @media (max-width: 560px) {
      .search-grid { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <h1>Search Playground</h1>
        <div class="muted">Runs real browser requests against <code>POST /v1/search</code></div>
      </div>
      <div class="actions">
        <a class="button-link" href="/dashboard">Dashboard</a>
        <a class="button-link" href="/v1/status" target="_blank" rel="noreferrer">Status JSON</a>
      </div>
    </header>

    <section class="panel">
      <h2>Visual Search Test</h2>
      <form id="search-form">
        <div class="search-grid">
          <div>
            <label for="query">Search query</label>
            <input id="query" name="query" value="best vector databases for rag" required>
          </div>
          <div>
            <label for="max-results">Max results</label>
            <select id="max-results" name="max_results">
              <option>3</option>
              <option selected>5</option>
              <option>10</option>
              <option>15</option>
              <option>20</option>
            </select>
          </div>
          <div>
            <label for="country">Country</label>
            <input id="country" name="country" value="US">
          </div>
          <div>
            <label for="language">Language</label>
            <input id="language" name="language" value="en">
          </div>
        </div>
        <div class="search-grid" style="margin-top: 12px;">
          <div>
            <label for="token">NexusProxy token</label>
            <input id="token" name="token" type="password" placeholder="{{if .RequiresToken}}local token required{{else}}optional{{end}}">
          </div>
          <div>
            <label for="freshness">Freshness</label>
            <select id="freshness" name="freshness">
              <option selected>any</option>
              <option>day</option>
              <option>week</option>
              <option>month</option>
              <option>year</option>
            </select>
          </div>
          <div>
            <label for="safe-search">Safe search</label>
            <select id="safe-search" name="safe_search">
              <option value="true" selected>on</option>
              <option value="false">off</option>
            </select>
          </div>
          <div>
            <button id="submit-button" type="submit">Run Search</button>
          </div>
        </div>
      </form>
    </section>

    <section class="panel">
      <h2>Provider Readiness</h2>
      <div class="attempts">
        {{range .Providers}}
          <span class="attempt {{if .Usable}}ok{{else}}fail{{end}}">{{.ID}} · {{.Reason}}</span>
        {{end}}
      </div>
    </section>

    <div id="status-line" class="status-line" hidden></div>
    <section id="summary" class="panel" hidden></section>
    <section id="results" class="result-list" aria-live="polite"></section>
    <section class="panel" id="raw-panel" hidden>
      <h2>Raw Response</h2>
      <pre><code id="raw-json"></code></pre>
    </section>
  </main>

  <script>
    const form = document.getElementById('search-form');
    const button = document.getElementById('submit-button');
    const statusLine = document.getElementById('status-line');
    const summary = document.getElementById('summary');
    const results = document.getElementById('results');
    const rawPanel = document.getElementById('raw-panel');
    const rawJson = document.getElementById('raw-json');

    form.addEventListener('submit', async (event) => {
      event.preventDefault();

      const formData = new FormData(form);
      const payload = {
        query: formData.get('query'),
        max_results: Number(formData.get('max_results')),
        country: formData.get('country'),
        language: formData.get('language'),
        freshness: formData.get('freshness'),
        safe_search: formData.get('safe_search') === 'true'
      };

      button.disabled = true;
      button.textContent = 'Searching...';
      renderStatus('Running search through NexusProxy...', 'ok');
      summary.hidden = true;
      results.replaceChildren();
      rawPanel.hidden = true;

      try {
        const headers = { 'Content-Type': 'application/json' };
        const token = String(formData.get('token') || '').trim();
        if (token) headers.Authorization = 'Bearer ' + token;

        const response = await fetch('/v1/search', {
          method: 'POST',
          headers,
          body: JSON.stringify(payload)
        });
        const data = await response.json();
        rawJson.textContent = JSON.stringify(data, null, 2);
        rawPanel.hidden = false;

        if (!response.ok) {
          renderStatus('Search failed: ' + (data.message || response.statusText), 'error');
          renderErrorDetails(data);
          return;
        }

        renderStatus('Search succeeded through ' + data.provider + '.', 'ok');
        renderSummary(data);
        renderResults(data.results || []);
      } catch (error) {
        renderStatus('Search failed: ' + error.message, 'error');
      } finally {
        button.disabled = false;
        button.textContent = 'Run Search';
      }
    });

    function renderStatus(message, type) {
      statusLine.hidden = false;
      statusLine.className = 'status-line ' + type;
      statusLine.textContent = message;
    }

    function renderSummary(data) {
      const attempts = data.attempts || [];
      summary.hidden = false;
      summary.innerHTML = '<h2>Run Summary</h2>' +
        '<div class="muted">Provider <code>' + escapeHtml(data.provider) + '</code> returned ' +
        Number(data.meta?.result_count || 0) + ' results.</div>' +
        '<div class="attempts">' + attempts.map((attempt) => {
          const cls = attempt.ok ? 'ok' : 'fail';
          return '<span class="attempt ' + cls + '">' +
            escapeHtml(attempt.provider) + ' · HTTP ' + Number(attempt.status || 0) +
            '</span>';
        }).join('') + '</div>';
    }

    function renderResults(items) {
      results.replaceChildren();
      if (items.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'panel muted';
        empty.textContent = 'No results returned.';
        results.appendChild(empty);
        return;
      }

      for (const item of items) {
        const card = document.createElement('article');
        card.className = 'result-card';

        const link = document.createElement('a');
        link.href = item.url || '#';
        link.target = '_blank';
        link.rel = 'noreferrer';
        link.textContent = item.title || item.url || 'Untitled result';
        card.appendChild(link);

        const snippet = document.createElement('p');
        snippet.textContent = item.snippet || '';
        card.appendChild(snippet);

        const meta = document.createElement('div');
        meta.className = 'result-meta';
        meta.textContent = [
          'rank ' + item.rank,
          item.provider,
          item.source,
          item.published_at
        ].filter(Boolean).join(' · ');
        card.appendChild(meta);

        results.appendChild(card);
      }
    }

    function renderErrorDetails(data) {
      summary.hidden = false;
      const details = data.details || {};
      const providers = details.providers || [];
      summary.innerHTML = '<h2>Error Details</h2>' +
        '<div class="muted">' + escapeHtml(data.message || 'Request failed') + '</div>' +
        '<div class="attempts">' + providers.map((provider) => {
          const cls = provider.usable ? 'ok' : 'fail';
          return '<span class="attempt ' + cls + '">' +
            escapeHtml(provider.id) + ' · ' + escapeHtml(provider.reason) +
            '</span>';
        }).join('') + '</div>';
    }

    function escapeHtml(value) {
      return String(value ?? '').replace(/[&<>"']/g, (char) => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#39;'
      }[char]));
    }
  </script>
</body>
</html>`))
