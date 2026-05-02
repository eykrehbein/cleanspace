#!/usr/bin/env sh
# cleanspace installer — downloads a prebuilt binary from GitHub Releases.
set -eu

OWNER="${CLEANSPACE_OWNER:-eykrehbein}"
REPO="cleanspace"
INSTALL_DIR="${CLEANSPACE_INSTALL_DIR:-$HOME/.local/bin}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)   arch="amd64" ;;
  arm64|aarch64)  arch="arm64" ;;
  *) die "unsupported arch: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

version="${CLEANSPACE_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$OWNER/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$version" ] || die "could not resolve latest release from github.com/$OWNER/$REPO"
fi

url="https://github.com/$OWNER/$REPO/releases/download/${version}/${REPO}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

printf '==> Downloading %s %s (%s/%s)\n' "$REPO" "$version" "$os" "$arch"
curl -fSL "$url" -o "$tmp/pkg.tar.gz" || die "download failed: $url"

tar -xzf "$tmp/pkg.tar.gz" -C "$tmp"

mkdir -p "$INSTALL_DIR"
mv "$tmp/$REPO" "$INSTALL_DIR/$REPO"
chmod +x "$INSTALL_DIR/$REPO"

printf '==> Installed to %s/%s\n\n' "$INSTALL_DIR" "$REPO"

case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    printf 'Run: cleanspace\n'
    ;;
  *)
    printf '%s is not on your PATH. Add it to your shell config, or run:\n  %s/%s\n' \
      "$INSTALL_DIR" "$INSTALL_DIR" "$REPO"
    ;;
esac
