#!/usr/bin/env sh
set -eu

APP_NAME=nexusproxy
DEFAULT_REPO=mananambaliya7010/NexusProxy

REPO=${NEXUSPROXY_REPO:-$DEFAULT_REPO}
VERSION=${NEXUSPROXY_VERSION:-latest}
DOWNLOAD_BASE_URL=${NEXUSPROXY_DOWNLOAD_BASE_URL:-}
INSTALL_SOURCE=${NEXUSPROXY_INSTALL_SOURCE:-auto}
PREFIX=${PREFIX:-"$HOME/.local"}
BIN_DIR=${BIN_DIR:-"$PREFIX/bin"}
CONFIG_DIR=${CONFIG_DIR:-"$HOME/.config/nexusproxy"}
CONFIG_FILE=${CONFIG_FILE:-"$CONFIG_DIR/config.json"}
ENV_FILE=${ENV_FILE:-"$CONFIG_DIR/.env"}
GO_BIN=${GO_BIN:-}

ROOT_DIR=""
case "$0" in
*/*)
	ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
	;;
esac

BINARY_SOURCE=${NEXUSPROXY_BINARY:-}
CONFIG_SOURCE=""
TMP_DIR=""

cleanup() {
	if [ "${NEXUSPROXY_KEEP_TMP:-}" = "1" ]; then
		return
	fi
	if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
		rm -rf "$TMP_DIR"
	fi
}
trap cleanup EXIT INT TERM

has_local_checkout() {
	[ -n "$ROOT_DIR" ] && [ -f "$ROOT_DIR/go.mod" ] && [ -f "$ROOT_DIR/config.example.json" ] && [ -d "$ROOT_DIR/cmd/nexusproxy" ]
}

detect_go() {
	if [ -n "$GO_BIN" ]; then
		return
	fi
	if command -v go >/dev/null 2>&1; then
		GO_BIN=$(command -v go)
	elif [ -x "$HOME/.local/go/bin/go" ]; then
		GO_BIN="$HOME/.local/go/bin/go"
	else
		echo "go was not found. Set GO_BIN=/path/to/go, add go to PATH, or install from a GitHub Release." >&2
		exit 1
	fi
}

detect_platform() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	arch=$(uname -m)

	case "$os" in
	darwin | linux)
		;;
	*)
		echo "unsupported OS: $os" >&2
		exit 1
		;;
	esac

	case "$arch" in
	x86_64 | amd64)
		arch=amd64
		;;
	arm64 | aarch64)
		arch=arm64
		;;
	*)
		echo "unsupported architecture: $arch" >&2
		exit 1
		;;
	esac

	printf "%s/%s" "$os" "$arch"
}

download() {
	url=$1
	destination=$2

	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$destination"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$destination" "$url"
	else
		echo "curl or wget is required to download release artifacts." >&2
		exit 1
	fi
}

release_base_url() {
	if [ -n "$DOWNLOAD_BASE_URL" ]; then
		printf "%s" "$DOWNLOAD_BASE_URL"
		return
	fi

	if [ "$VERSION" = "latest" ]; then
		printf "https://github.com/%s/releases/latest/download" "$REPO"
	else
		printf "https://github.com/%s/releases/download/%s" "$REPO" "$VERSION"
	fi
}

verify_checksum() {
	archive=$1
	asset=$2
	checksums=$3

	if [ "${NEXUSPROXY_SKIP_CHECKSUM:-}" = "1" ]; then
		echo "checksum verification skipped"
		return
	fi

	line=$(grep "[[:space:]]$asset\$" "$checksums" || true)
	if [ -z "$line" ]; then
		echo "checksum for $asset was not found in checksums.txt" >&2
		exit 1
	fi

	expected=$(printf "%s\n" "$line" | awk '{print $1}')
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$archive" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "$archive" | awk '{print $1}')
	else
		echo "sha256sum or shasum is required for checksum verification. Set NEXUSPROXY_SKIP_CHECKSUM=1 to skip." >&2
		exit 1
	fi

	if [ "$expected" != "$actual" ]; then
		echo "checksum mismatch for $asset" >&2
		exit 1
	fi
}

local_version() {
	if has_local_checkout && command -v git >/dev/null 2>&1; then
		git -C "$ROOT_DIR" describe --tags --always --dirty 2>/dev/null || printf "dev"
	else
		printf "dev"
	fi
}

install_from_source() {
	detect_go
	build_version=${NEXUSPROXY_BUILD_VERSION:-$(local_version)}
	BINARY_SOURCE="$BIN_DIR/$APP_NAME"
	CONFIG_SOURCE="$ROOT_DIR/config.example.json"

	(
		cd "$ROOT_DIR"
		CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags="-s -w -X main.version=$build_version" -o "$BINARY_SOURCE" ./cmd/nexusproxy
	)
}

install_from_release() {
	platform=$(detect_platform)
	os=${platform%/*}
	arch=${platform#*/}
	asset="$APP_NAME-$os-$arch.tar.gz"
	base_url=$(release_base_url)

	TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nexusproxy.XXXXXX")
	archive="$TMP_DIR/$asset"
	checksums="$TMP_DIR/checksums.txt"

	echo "downloading $asset from $REPO ($VERSION)"
	download "$base_url/$asset" "$archive"
	download "$base_url/checksums.txt" "$checksums"
	verify_checksum "$archive" "$asset" "$checksums"

	tar -xzf "$archive" -C "$TMP_DIR"

	for candidate in "$TMP_DIR"/*/"$APP_NAME" "$TMP_DIR/$APP_NAME"; do
		if [ -f "$candidate" ]; then
			BINARY_SOURCE=$candidate
			break
		fi
	done

	for candidate in "$TMP_DIR"/*/config.example.json "$TMP_DIR/config.example.json"; do
		if [ -f "$candidate" ]; then
			CONFIG_SOURCE=$candidate
			break
		fi
	done

	if [ -z "$BINARY_SOURCE" ]; then
		echo "release archive did not contain a $APP_NAME binary" >&2
		exit 1
	fi
}

write_fallback_config() {
	cat >"$CONFIG_FILE" <<'CONFIG'
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
CONFIG
}

install_config() {
	if [ -f "$CONFIG_FILE" ]; then
		return
	fi

	if [ -n "$CONFIG_SOURCE" ] && [ -f "$CONFIG_SOURCE" ]; then
		cp "$CONFIG_SOURCE" "$CONFIG_FILE"
	else
		write_fallback_config
	fi
}

mkdir -p "$BIN_DIR" "$CONFIG_DIR"

case "$INSTALL_SOURCE" in
auto)
	if [ -n "$BINARY_SOURCE" ]; then
		:
	elif has_local_checkout; then
		install_from_source
	else
		install_from_release
	fi
	;;
source)
	if ! has_local_checkout; then
		echo "source install requires running this script from a NexusProxy checkout" >&2
		exit 1
	fi
	install_from_source
	;;
release)
	if [ -z "$BINARY_SOURCE" ]; then
		install_from_release
	fi
	;;
*)
	echo "invalid NEXUSPROXY_INSTALL_SOURCE: $INSTALL_SOURCE" >&2
	exit 1
	;;
esac

if [ -n "${NEXUSPROXY_BINARY:-}" ]; then
	BINARY_SOURCE=$NEXUSPROXY_BINARY
fi

if [ "$BINARY_SOURCE" != "$BIN_DIR/$APP_NAME" ]; then
	cp "$BINARY_SOURCE" "$BIN_DIR/$APP_NAME"
fi
chmod 0755 "$BIN_DIR/$APP_NAME"

install_config

if [ ! -f "$ENV_FILE" ]; then
	touch "$ENV_FILE"
	chmod 0600 "$ENV_FILE"
fi

echo "installed $BIN_DIR/$APP_NAME"
"$BIN_DIR/$APP_NAME" --version || true
echo "config: $CONFIG_FILE"
echo "env:    $ENV_FILE"
echo
echo "run:"
echo "  NEXUS_ENV_FILE=$ENV_FILE $BIN_DIR/$APP_NAME --config $CONFIG_FILE"
