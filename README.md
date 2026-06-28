# NexusProxy

NexusProxy is a self-hosted, rate-limit-aware search API gateway for agents, scripts, and RAG pipelines.

The v1 target is deliberately narrow: expose one local normalized search endpoint and route it across provider adapters such as Brave Search, Tavily, and Serper. The default deployment is a tiny static Go binary that can run as a local sidecar for tools like OpenCode, Hermes, or other agent runtimes. Docker is optional.

## Quick Start

Install from the latest GitHub Release:

```sh
curl -fsSL https://raw.githubusercontent.com/mananxrobin/NexusProxy/main/scripts/install.sh | sh
```

Install a pinned version:

```sh
curl -fsSL https://raw.githubusercontent.com/mananxrobin/NexusProxy/main/scripts/install.sh | NEXUSPROXY_VERSION=v0.1.0 sh
```

If the repository is published somewhere else, set `NEXUSPROXY_REPO` too:

```sh
curl -fsSL https://raw.githubusercontent.com/OWNER/REPO/main/scripts/install.sh | NEXUSPROXY_REPO=OWNER/REPO sh
```

The installer places the binary at `$HOME/.local/bin/nexusproxy`, creates `$HOME/.config/nexusproxy/config.json`, and creates a private `$HOME/.config/nexusproxy/.env` for provider keys.

From a local checkout, the same installer builds from source:

```sh
./scripts/install.sh
```

Run it:

```sh
NEXUS_ENV_FILE="$HOME/.config/nexusproxy/.env" \
  "$HOME/.local/bin/nexusproxy" --config "$HOME/.config/nexusproxy/config.json"
```

For development, you can run directly from source:

```sh
go run ./cmd/nexusproxy --config config.example.json
```

The default server listens on `http://127.0.0.1:8787`.

## Releases

Release packages are static Go binaries plus the starter config and service templates. Build them locally with:

```sh
VERSION=v0.1.0 ./scripts/package-release.sh
```

This writes platform tarballs and `checksums.txt` to `dist/`. The curl installer expects these GitHub Release assets:

- `nexusproxy-darwin-arm64.tar.gz`
- `nexusproxy-darwin-amd64.tar.gz`
- `nexusproxy-linux-arm64.tar.gz`
- `nexusproxy-linux-amd64.tar.gz`
- `checksums.txt`

To publish through GitHub Actions:

```sh
git tag v0.1.0
git push origin v0.1.0
```

## Provider Keys

The easiest setup path is the terminal wizard:

```sh
nexusproxy setup --config "$HOME/.config/nexusproxy/config.json"
```

From a source checkout:

```sh
go run ./cmd/nexusproxy -- setup --config config.example.json
```

Useful setup flags:

- `--env-file <path>`: save keys somewhere other than the config-adjacent `.env`.
- `--provider brave`: configure only one provider type. Use `brave`, `tavily`, `serper`, or a configured custom provider type.
- `--test`: test keys immediately after saving.
- `--no-test`: save keys without spending a provider probe request.

The setup command shows friendly provider names, hides typed API keys in real terminals, saves secrets to `.env`, and creates numbered keys like `BRAVE_API_KEY_2` when you add another account for the same provider.

You can also open `http://127.0.0.1:8787/dashboard` and paste provider keys into the **Provider API Keys** panel. NexusProxy saves them to `.env`, hot-loads them into the running gateway, and keeps `.env` ignored by git.

If `server.apiKey` is configured, the dashboard key form asks for that local token before saving secrets.

Use **Add Another Provider Key** to add multiple accounts for the same provider. For example, a second Brave key is saved as `BRAVE_API_KEY_2`, a third as `BRAVE_API_KEY_3`, and each one becomes a separate routeable provider slot such as `brave-primary-2`.

Use **Refresh Provider Health** on the dashboard to run one 1-result probe per ready provider. This updates cooldowns, status, Brave-style rate-limit windows from response headers, and Tavily's last reported credit usage. It spends a tiny real request because most providers do not expose a separate free quota-status endpoint.

You can still set keys manually:

```sh
export BRAVE_API_KEY="..."
export BRAVE_API_KEY_2="..."
export TAVILY_API_KEY="..."
export SERPER_API_KEY="..."
```

## Visual Test

After at least one provider is ready, open `http://127.0.0.1:8787/playground` to run a browser test. The playground has a search bar, calls `POST /v1/search`, renders clickable result links, and shows the provider attempts used for each request.

## Search

```sh
curl -sS http://127.0.0.1:8787/v1/search \
  -H 'Authorization: Bearer local-dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "best vector databases for rag",
    "max_results": 5,
    "country": "US",
    "language": "en",
    "freshness": "month",
    "safe_search": true
  }'
```

Response shape:

```json
{
  "query": "best vector databases for rag",
  "provider": "brave-primary",
  "results": [
    {
      "title": "Example result",
      "url": "https://example.com",
      "snippet": "Normalized snippet",
      "published_at": "2026-06-01",
      "source": "example.com",
      "rank": 1,
      "provider": "brave-primary"
    }
  ],
  "attempts": [
    {
      "provider": "brave-primary",
      "type": "brave",
      "status": 200,
      "ok": true
    }
  ],
  "meta": {
    "result_count": 1,
    "routing_policy": "priority"
  }
}
```

## Compatible Provider APIs

NexusProxy also exposes provider-compatible endpoints for agents that can change an API base URL but expect Tavily or Brave-shaped requests.

Tavily-compatible search:

```sh
curl -sS http://127.0.0.1:8787/search \
  -H 'Authorization: Bearer local-dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "best vector databases for rag",
    "max_results": 5,
    "search_depth": "basic",
    "time_range": "month",
    "include_answer": true
  }'
```

Supported auth: `Authorization: Bearer <NEXUS_API_KEY>`, `X-API-Key`, or Tavily-style body field `"api_key": "<NEXUS_API_KEY>"`.

Brave-compatible web search:

```sh
curl -sS 'http://127.0.0.1:8787/res/v1/web/search?q=best+vector+databases+for+rag&count=5&country=US&search_lang=en&freshness=pm' \
  -H 'X-Subscription-Token: local-dev-token'
```

Supported auth: `X-Subscription-Token: <NEXUS_API_KEY>`, `Authorization: Bearer <NEXUS_API_KEY>`, or `X-API-Key`.

These compatibility endpoints still route through NexusProxy's provider fallback layer. Tavily-only extras such as `include_domains`, `exclude_domains`, and raw page extraction are accepted but not fully implemented in v1.

## Endpoints

- `POST /v1/search`: normalized search request.
- `POST /search`: Tavily-compatible search request.
- `GET /res/v1/web/search`: Brave-compatible web search request.
- `GET /v1/status`: JSON gateway/provider health, protected by the local API key when configured.
- `GET /dashboard`: local HTML status dashboard.
- `GET /playground`: local visual search test page.
- `GET /healthz`: unauthenticated liveness probe.

## Lightweight Runtime

NexusProxy is meant to run comfortably on small machines. For a 1 GB RAM / 1 CPU agent box, use the binary install and keep Docker off the critical path.

Useful knobs:

- `server.maxConcurrentRequests`: caps in-flight HTTP requests. Default is `8`. When full, NexusProxy returns `429` with `Retry-After: 1`. `/healthz` bypasses this cap for service liveness checks.
- `NEXUS_MAX_CONCURRENT_REQUESTS`: environment override for the same cap.
- `routing.maxAttempts`: limits provider fallback attempts per search.
- `timeoutMs` per provider: bounds slow upstream calls. If omitted, the default is 20 seconds.

For a very small concurrent sidecar, a good starting point is:

```json
{
  "server": {
    "host": "127.0.0.1",
    "port": 8787,
    "apiKey": "local-dev-token",
    "maxConcurrentRequests": 4
  },
  "routing": {
    "policy": "priority",
    "maxAttempts": 2,
    "retryOnStatuses": [429, 500, 502, 503, 504],
    "defaultCooldownMs": 60000
  },
  "providers": []
}
```

## Run As A Service

Linux user service:

```sh
mkdir -p ~/.config/systemd/user
cp packaging/systemd/nexusproxy.service ~/.config/systemd/user/nexusproxy.service
systemctl --user daemon-reload
systemctl --user enable --now nexusproxy
```

macOS launchd:

```sh
mkdir -p ~/Library/LaunchAgents
cp packaging/launchd/com.nexusproxy.plist ~/Library/LaunchAgents/com.nexusproxy.plist
```

Edit `~/Library/LaunchAgents/com.nexusproxy.plist` and replace `YOUR_USER` with your macOS username, then load it:

```sh
launchctl load ~/Library/LaunchAgents/com.nexusproxy.plist
```

## Routing Policies

- `priority`: try the highest-priority ready provider first.
- `round_robin`: rotate across ready providers.
- `quota_aware`: prefer the provider with the highest known remaining quota, then priority.

When an upstream provider returns `429`, NexusProxy marks that provider as cooling down using `Retry-After`, rate-limit reset headers, or `routing.defaultCooldownMs`.

## Configuration

The current v1 config is JSON to keep the Go service standard-library only. YAML can be added later if we decide config ergonomics matter more than zero dependencies.

Provider keys can be set directly with `apiKey`, but `apiKeyEnv` is recommended so secrets stay outside the config file.

```json
{
  "server": {
    "host": "127.0.0.1",
    "port": 8787,
    "apiKey": "local-dev-token",
    "maxConcurrentRequests": 8
  },
  "routing": {
    "policy": "priority",
    "maxAttempts": 3,
    "retryOnStatuses": [429, 500, 502, 503, 504],
    "defaultCooldownMs": 60000
  },
  "providers": []
}
```

## Docker

```sh
docker build -t nexusproxy .
docker run --rm -p 8787:8787 \
  -e BRAVE_API_KEY \
  -e TAVILY_API_KEY \
  -e SERPER_API_KEY \
  nexusproxy
```
