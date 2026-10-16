#!/bin/sh
# Install the PAI binary from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/Carudy/pai/main/install.sh | sh
#
# Environment:
#   PAI_VERSION      release tag to install (e.g. v0.8.5); default: the latest
#   PAI_INSTALL_DIR  directory to install into (default: $HOME/.local/bin)
#
# xh is used when available, otherwise curl. Only linux/amd64 and darwin/arm64
# binaries are published; anywhere else, build from source:
# go install github.com/Carudy/pai/cmd/pai@latest

set -eu

REPO="Carudy/pai"
BIN="pai"
RELEASES="https://github.com/${REPO}/releases"
VERSION="${PAI_VERSION:-}"
INSTALL_DIR="${PAI_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}"

err() {
	printf 'install: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
Install the $BIN binary from the latest GitHub release.

Usage: $0

Environment:
  PAI_VERSION      tag to install (e.g. v0.8.5); default is the latest release
  PAI_INSTALL_DIR  directory to install into (default: \$HOME/.local/bin)

Published targets: linux/amd64, darwin/arm64.
EOF
}

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
"") ;;
*)
	err "unexpected argument: $1 (try --help)"
	;;
esac

# Prefer xh; fall back to curl.
if command -v xh >/dev/null 2>&1; then
	HTTP=xh
elif command -v curl >/dev/null 2>&1; then
	HTTP=curl
else
	err "need xh or curl to download"
fi

# headers URL — response headers on stdout. Never fails: a 3xx is expected here,
# and an empty result is reported by the caller.
headers() {
	if [ "$HTTP" = xh ]; then
		xh --print=h GET "$1" 2>/dev/null || true
	else
		curl -fsSI "$1" 2>/dev/null || true
	fi
}

# download URL FILE — write the response body to FILE, failing on an HTTP error.
# xh's download chatter goes to stderr and is shown only when the fetch fails.
download() {
	if [ "$HTTP" = xh ]; then
		if ! xh_err="$(xh --check-status --follow --download --output "$2" GET "$1" 2>&1 >/dev/null)"; then
			printf '%s\n' "$xh_err" >&2
			err "download failed: $1"
		fi
	else
		curl -fsSL -o "$2" "$1" || err "download failed: $1"
	fi
}

os="$(uname -s)"
case "$os" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) err "unsupported OS '$os'; build from source: go install github.com/${REPO}/cmd/${BIN}@latest" ;;
esac

arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) err "unsupported architecture '$arch'" ;;
esac

case "${os}/${arch}" in
linux/amd64 | darwin/arm64) ;;
*) err "no prebuilt binary for ${os}/${arch}; build from source: go install github.com/${REPO}/cmd/${BIN}@latest" ;;
esac

# The /releases/latest redirect ends at /tag/<version>; this avoids the API and
# its unauthenticated rate limit.
if [ -z "$VERSION" ]; then
	VERSION="$(headers "${RELEASES}/latest" | tr -d '\r' | sed -n 's#^[Ll]ocation:.*/tag/\(.*\)$#\1#p' | head -n1)"
	[ -n "$VERSION" ] || err "could not determine the latest release version (set PAI_VERSION to pin one)"
fi

base="${RELEASES}/download/${VERSION}"
asset="${BIN}_${VERSION#v}_${os}_${arch}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

download "$base/$asset" "$tmp/$asset"
download "$base/checksums.txt" "$tmp/checksums.txt"

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		err "need sha256sum or shasum to verify the download"
	fi
}

# checksums.txt is `sha256sum ./*` output, so the filename has a "./" prefix.
expected="$(awk -v f="$asset" '$2 == f || $2 == "./" f { print $1; exit }' "$tmp/checksums.txt")"
[ -n "$expected" ] || err "no checksum entry for $asset in checksums.txt"
[ "$(sha256 "$tmp/$asset")" = "$expected" ] || err "checksum mismatch for $asset (the download may be corrupt)"

mkdir -p "$INSTALL_DIR" || err "cannot create $INSTALL_DIR"
if command -v install >/dev/null 2>&1; then
	install -m 0755 "$tmp/$asset" "$INSTALL_DIR/$BIN"
else
	cp "$tmp/$asset" "$INSTALL_DIR/$BIN"
	chmod 0755 "$INSTALL_DIR/$BIN"
fi

printf 'Installed %s %s to %s\n' "$BIN" "$VERSION" "$INSTALL_DIR/$BIN"
case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	printf 'Note: %s is not on your PATH. Add it, e.g.:\n  export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR"
	;;
esac
