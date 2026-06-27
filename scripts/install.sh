#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PREFIX=${PREFIX:-"$HOME/.local"}
BIN_DIR=${BIN_DIR:-"$PREFIX/bin"}
CONFIG_DIR=${CONFIG_DIR:-"$HOME/.config/nexusproxy"}
CONFIG_FILE=${CONFIG_FILE:-"$CONFIG_DIR/config.json"}
ENV_FILE=${ENV_FILE:-"$CONFIG_DIR/.env"}
GO_BIN=${GO_BIN:-}

if [ -z "$GO_BIN" ] && [ -z "${NEXUSPROXY_BINARY:-}" ]; then
	if command -v go >/dev/null 2>&1; then
		GO_BIN=$(command -v go)
	elif [ -x "$HOME/.local/go/bin/go" ]; then
		GO_BIN="$HOME/.local/go/bin/go"
	else
		echo "go was not found. Set GO_BIN=/path/to/go, add go to PATH, or set NEXUSPROXY_BINARY=/path/to/nexusproxy." >&2
		exit 1
	fi
fi

mkdir -p "$BIN_DIR" "$CONFIG_DIR"

if [ -n "${NEXUSPROXY_BINARY:-}" ]; then
	cp "$NEXUSPROXY_BINARY" "$BIN_DIR/nexusproxy"
else
	(
		cd "$ROOT_DIR"
		CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$BIN_DIR/nexusproxy" ./cmd/nexusproxy
	)
fi
chmod 0755 "$BIN_DIR/nexusproxy"

if [ ! -f "$CONFIG_FILE" ]; then
	cp "$ROOT_DIR/config.example.json" "$CONFIG_FILE"
fi

if [ ! -f "$ENV_FILE" ]; then
	touch "$ENV_FILE"
	chmod 0600 "$ENV_FILE"
fi

echo "installed $BIN_DIR/nexusproxy"
echo "config: $CONFIG_FILE"
echo "env:    $ENV_FILE"
echo
echo "run:"
echo "  NEXUS_ENV_FILE=$ENV_FILE $BIN_DIR/nexusproxy --config $CONFIG_FILE"
