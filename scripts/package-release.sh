#!/usr/bin/env sh
set -eu

APP_NAME=nexusproxy
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT_DIR/dist"}
PLATFORMS=${PLATFORMS:-"darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"}
GO_BIN=${GO_BIN:-}

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

if [ -z "${VERSION:-}" ]; then
	if command -v git >/dev/null 2>&1; then
		VERSION=$(git -C "$ROOT_DIR" describe --tags --always --dirty 2>/dev/null || printf "dev")
	else
		VERSION=dev
	fi
fi

mkdir -p "$OUT_DIR"
checksums="$OUT_DIR/checksums.txt"
: >"$checksums"

for platform in $PLATFORMS; do
	GOOS=${platform%/*}
	GOARCH=${platform#*/}
	asset="$APP_NAME-$GOOS-$GOARCH.tar.gz"
	stage="$OUT_DIR/.stage-$APP_NAME-$VERSION-$GOOS-$GOARCH-$$"
	package_root="$APP_NAME-$VERSION-$GOOS-$GOARCH"

	mkdir -p "$stage/$package_root/packaging/systemd" "$stage/$package_root/packaging/launchd"
	cp "$ROOT_DIR/config.example.json" "$stage/$package_root/config.example.json"
	cp "$ROOT_DIR/README.md" "$stage/$package_root/README.md"
	cp "$ROOT_DIR/packaging/systemd/nexusproxy.service" "$stage/$package_root/packaging/systemd/nexusproxy.service"
	cp "$ROOT_DIR/packaging/launchd/com.nexusproxy.plist" "$stage/$package_root/packaging/launchd/com.nexusproxy.plist"

	echo "building $asset"
	(
		cd "$ROOT_DIR"
		CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" "$GO_BIN" build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o "$stage/$package_root/$APP_NAME" ./cmd/nexusproxy
	)

	(
		cd "$stage"
		tar -czf "$OUT_DIR/$asset" "$package_root"
	)

	if command -v sha256sum >/dev/null 2>&1; then
		(
			cd "$OUT_DIR"
			sha256sum "$asset" >>"$checksums"
		)
	else
		(
			cd "$OUT_DIR"
			shasum -a 256 "$asset" >>"$checksums"
		)
	fi
done

echo "release artifacts written to $OUT_DIR"
