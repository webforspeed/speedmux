#!/usr/bin/env bash
set -euo pipefail

BINARY_NAME="speedmux"
REPO="webforspeed/speedmux"
BINDIR="$HOME/.local/bin"

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: missing required command: $1" >&2
    exit 1
  fi
}

detect_os() {
  local raw_os
  raw_os="$(uname -s)"
  case "$raw_os" in
    Linux) echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)
      echo "error: unsupported operating system: $raw_os" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  local raw_arch
  raw_arch="$(uname -m)"
  case "$raw_arch" in
    x86_64 | amd64) echo "amd64" ;;
    arm64 | aarch64) echo "arm64" ;;
    *)
      echo "error: unsupported architecture: $raw_arch" >&2
      exit 1
      ;;
  esac
}

resolve_tag() {
  local latest_url tag
  latest_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
  tag="${latest_url##*/}"

  if [[ -z "$tag" ]]; then
    echo "error: could not resolve latest release tag for ${REPO}" >&2
    exit 1
  fi

  echo "$tag"
}

sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return
  fi

  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return
  fi

  echo "error: no SHA256 tool found (need sha256sum or shasum)" >&2
  exit 1
}

main() {
  require_cmd curl
  require_cmd tar
  require_cmd install

  local os arch tag version artifact_name base_url tmpdir archive_path checksums_path expected actual extracted_bin
  os="$(detect_os)"
  arch="$(detect_arch)"
  tag="$(resolve_tag)"
  version="${tag#v}"
  artifact_name="${BINARY_NAME}_${version}_${os}_${arch}.tar.gz"
  base_url="https://github.com/${REPO}/releases/download/${tag}"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT
  archive_path="${tmpdir}/${artifact_name}"
  checksums_path="${tmpdir}/checksums.txt"

  echo "Installing ${BINARY_NAME} ${tag} for ${os}/${arch} from ${REPO}..."
  curl -fsSL "${base_url}/${artifact_name}" -o "$archive_path"
  curl -fsSL "${base_url}/checksums.txt" -o "$checksums_path"

  expected="$(awk -v name="$artifact_name" '$2 == name { print $1 }' "$checksums_path")"
  if [[ -z "$expected" ]]; then
    echo "error: no checksum found for ${artifact_name}" >&2
    exit 1
  fi

  actual="$(sha256_file "$archive_path")"
  if [[ "$actual" != "$expected" ]]; then
    echo "error: checksum mismatch for ${artifact_name}" >&2
    exit 1
  fi

  tar -xzf "$archive_path" -C "$tmpdir"
  extracted_bin="${tmpdir}/${BINARY_NAME}"
  if [[ ! -f "$extracted_bin" ]]; then
    echo "error: extracted archive does not contain ${BINARY_NAME}" >&2
    exit 1
  fi

  mkdir -p "$BINDIR"
  install -m 0755 "$extracted_bin" "${BINDIR}/${BINARY_NAME}"

  echo "Installed to ${BINDIR}/${BINARY_NAME}"
  if [[ ":$PATH:" == *":${BINDIR}:"* ]]; then
    echo "${BINDIR} is already in your PATH."
  else
    echo "Add this to your shell profile to use ${BINARY_NAME} globally:"
    echo "  export PATH=\"${BINDIR}:\$PATH\""
  fi
}

main "$@"
