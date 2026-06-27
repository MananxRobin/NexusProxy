#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
PLATFORMS=${PLATFORMS:-"darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"}
GO_BIN=${GO_BIN:-}
VERSION=${VERSION:-dev}

if [ -z "$GO_BIN" ]; then
	if command -v go >/dev/null 2>&1; then
		GO_BIN=$(command -v go)
	elif [ -x "$HOME/.local/go/bin/go" ]; then
		GO_BIN="$HOME/.local/go/bin/go"
	else
		echo "go was not found. Set GO_BIN=/path/to/go or add go to PATH." >&2
		exit 1
	fi
fi

mkdir -p "$OUT_DIR"

for platform in $PLATFORMS; do
	GOOS=${platform%/*}
	GOARCH=${platform#*/}
	output="$OUT_DIR/nexusproxy-$GOOS-$GOARCH"
	if [ "$GOOS" = "windows" ]; then
		output="$output.exe"
	fi

	echo "building $output"
	(
		cd "$ROOT_DIR"
		CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" "$GO_BIN" build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o "$output" ./cmd/nexusproxy
	)
done

echo "build artifacts written to $OUT_DIR"
