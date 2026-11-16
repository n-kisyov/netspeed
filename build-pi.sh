#!/usr/bin/env bash
set -euo pipefail

# NetSpeed & NetSpeed CLI Raspberry Pi OS Build Script
# Targets:
#   - Raspberry Pi 3B+ / Pi 4 / Pi 5 (64-bit Raspberry Pi OS -> linux/arm64)
#   - Raspberry Pi 3B+ / Pi 4        (32-bit Raspberry Pi OS -> linux/arm GOARM=7)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

TARGET="auto"
AUTO_INSTALL_GO=0
SKIP_TESTS=0

usage() {
  cat <<'EOF'
NetSpeed Raspberry Pi Build Script

Usage:
  ./build-pi.sh [options]

Options:
  --target <mode>   Build target:
                      auto    Detect Pi OS arch on ARM/ARM64, or build both arm64+armv7 elsewhere (default)
                      native  Build for this device's architecture only
                      arm64   Build for 64-bit Raspberry Pi OS (Pi 3B+, Pi 4, Pi 5)
                      armv7   Build for 32-bit Raspberry Pi OS (Pi 3B+, Pi 4)
                      all     Build both arm64 and armv7 binaries
  --install-go      Automatically install Go if missing or too old without prompting
  --skip-tests      Skip Go unit tests before compiling
  -h, --help        Show this help message
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      TARGET="${2:-}"
      shift 2
      ;;
    --install-go)
      AUTO_INSTALL_GO=1
      shift
      ;;
    --skip-tests)
      SKIP_TESTS=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

log_info()  { printf '\033[1;36m[INFO]\033[0m %s\n' "$*"; }
log_ok()    { printf '\033[1;32m[OK]\033[0m   %s\n' "$*"; }
log_warn()  { printf '\033[1;33m[WARN]\033[0m %s\n' "$*"; }
log_err()   { printf '\033[1;31m[ERR]\033[0m  %s\n' "$*" >&2; }

ensure_path_has_go() {
  if [[ -d /usr/local/go/bin ]] && [[ ":$PATH:" != *":/usr/local/go/bin:"* ]]; then
    export PATH="/usr/local/go/bin:$PATH"
  fi
  if command -v go >/dev/null 2>&1; then
    local gopath_bin
    gopath_bin="$(go env GOPATH 2>/dev/null || true)/bin"
    if [[ -n "$gopath_bin" && "$gopath_bin" != "/bin" && ":$PATH:" != *":$gopath_bin:"* ]]; then
      export PATH="$PATH:$gopath_bin"
    fi
  fi
}

version_ge() {
  # Returns 0 if version $1 >= $2 (numeric dot comparison, e.g. 1.24.1 >= 1.21.0)
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<EOF
$1
EOF
  IFS=. read -r b1 b2 b3 <<EOF
$2
EOF
  a1=${a1:-0}; a2=${a2:-0}; a3=${a3:-0}
  b1=${b1:-0}; b2=${b2:-0}; b3=${b3:-0}
  if [ "$a1" -ne "$b1" ]; then [ "$a1" -gt "$b1" ]; return $?; fi
  if [ "$a2" -ne "$b2" ]; then [ "$a2" -gt "$b2" ]; return $?; fi
  [ "$a3" -ge "$b3" ]
}

detect_go_dist_arch() {
  case "$(uname -m)" in
    aarch64|arm64) echo "linux-arm64" ;;
    armv7l|armv6l|armhf|arm) echo "linux-armv6l" ;;
    x86_64|amd64) echo "linux-amd64" ;;
    *)
      log_err "Unsupported CPU architecture for Go installer: $(uname -m)"
      exit 1
      ;;
  esac
}

run_privileged() {
  if [[ "$(id -u)" -eq 0 ]]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    log_err "Need root or sudo privileges to run: $*"
    exit 1
  fi
}

install_golang() {
  local dist_arch go_ver fallback_ver tarball tmp_tgz url
  dist_arch="$(detect_go_dist_arch)"
  fallback_ver="go1.24.1"

  log_info "Detecting latest Go release for ${dist_arch}..."
  if command -v curl >/dev/null 2>&1; then
    go_ver="$(curl -fsSL https://go.dev/VERSION?m=text 2>/dev/null | head -n 1 | tr -d '\r\n' || true)"
  elif command -v wget >/dev/null 2>&1; then
    go_ver="$(wget -qO- https://go.dev/VERSION?m=text 2>/dev/null | head -n 1 | tr -d '\r\n' || true)"
  else
    go_ver=""
  fi

  if [[ ! "$go_ver" =~ ^go[0-9]+\.[0-9]+ ]]; then
    log_warn "Could not query go.dev/VERSION, falling back to ${fallback_ver}"
    go_ver="$fallback_ver"
  fi

  tarball="${go_ver}.${dist_arch}.tar.gz"
  url="https://go.dev/dl/${tarball}"
  tmp_tgz="$(mktemp "/tmp/${tarball}.XXXXXX")"
  trap 'rm -f "$tmp_tgz"' RETURN

  log_info "Downloading ${url}..."
  local dl_ok=0
  if command -v curl >/dev/null 2>&1; then
    if curl -fsSL -o "$tmp_tgz" "$url"; then dl_ok=1; fi
  elif command -v wget >/dev/null 2>&1; then
    if wget -O "$tmp_tgz" "$url"; then dl_ok=1; fi
  fi

  if [[ "$dl_ok" -eq 1 ]]; then
    log_info "Installing ${go_ver} into /usr/local/go..."
    run_privileged rm -rf /usr/local/go
    run_privileged tar -C /usr/local -xzf "$tmp_tgz"
    export PATH="/usr/local/go/bin:$PATH"

    if [[ -w "$HOME/.profile" ]] || [[ ! -e "$HOME/.profile" ]]; then
      if ! grep -qs '/usr/local/go/bin' "$HOME/.profile" 2>/dev/null; then
        printf '\nexport PATH="$PATH:/usr/local/go/bin"\n' >> "$HOME/.profile"
        log_info "Added /usr/local/go/bin to ~/.profile"
      fi
    fi
  else
    log_warn "Official Go tarball download failed; attempting apt-get install golang..."
    run_privileged apt-get update
    run_privileged apt-get install -y golang ca-certificates
  fi

  ensure_path_has_go
  if ! command -v go >/dev/null 2>&1; then
    log_err "Go installation failed or go binary not found in PATH."
    exit 1
  fi
  log_ok "Installed Go: $(go version)"
}

check_and_offer_go() {
  ensure_path_has_go

  local min_bootstrap="1.21.0"
  local need_install=0
  local reason=""

  if ! command -v go >/dev/null 2>&1; then
    need_install=1
    reason="Go (golang) is not currently installed on this system."
  else
    local raw_ver clean_ver
    raw_ver="$(go version | awk '{print $3}')"
    clean_ver="${raw_ver#go}"
    clean_ver="${clean_ver%%[^0-9.]*}"
    if [[ -n "$clean_ver" ]] && ! version_ge "$clean_ver" "$min_bootstrap"; then
      need_install=1
      reason="Installed Go (${raw_ver}) is older than the minimum bootstrap version go${min_bootstrap}."
    else
      log_ok "Detected Go toolchain: $(go version)"
    fi
  fi

  if [[ "$need_install" -eq 1 ]]; then
    log_warn "$reason"
    if [[ "$AUTO_INSTALL_GO" -eq 1 ]]; then
      install_golang
    elif [[ -t 0 ]]; then
      read -r -p "Would you like to download and install Go now? [y/N] " reply
      case "$reply" in
        [yY]|[yY][eE][sS])
          install_golang
          ;;
        *)
          log_err "Go is required to build NetSpeed. Re-run with --install-go or install Go manually."
          exit 1
          ;;
      esac
    else
      log_err "Non-interactive shell and Go is missing/outdated. Pass --install-go to install automatically."
      exit 1
    fi
  fi

  # Allows older bootstrap Go >= 1.21 to automatically fetch the exact toolchain declared in go.mod if needed.
  export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
}

detect_host_pi_target() {
  case "$(uname -m)" in
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv6l|armhf|arm) echo "armv7" ;;
    *) echo "other" ;;
  esac
}

build_target() {
  local target="$1"
  local host_pi="$2"
  local goos="linux"
  local goarch=""
  local goarm=""
  local suffix=""

  case "$target" in
    arm64)
      goarch="arm64"
      suffix="linux-arm64"
      log_info "Building Raspberry Pi 64-bit targets (Pi 3B+ / Pi 4 / Pi 5, linux/arm64)..."
      ;;
    armv7)
      goarch="arm"
      goarm="7"
      suffix="linux-armv7"
      log_info "Building Raspberry Pi 32-bit targets (Pi 3B+ / Pi 4, linux/arm GOARM=7)..."
      ;;
    *)
      log_err "Unknown target: $target"
      exit 1
      ;;
  esac

  mkdir -p "$SCRIPT_DIR/bin"

  local cli_out="$SCRIPT_DIR/bin/netspeed-cli-${suffix}"
  local web_out="$SCRIPT_DIR/bin/netspeed-web-${suffix}"

  env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
    go build -trimpath -ldflags="-s -w" -o "$cli_out" ./cmd/netspeed-cli
  chmod +x "$cli_out"

  env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
    go build -trimpath -ldflags="-s -w" -o "$web_out" ./cmd/netspeed-web
  chmod +x "$web_out"

  log_ok "Built $cli_out ($(du -h "$cli_out" | awk '{print $1}'))"
  log_ok "Built $web_out ($(du -h "$web_out" | awk '{print $1}'))"

  if [[ "$target" == "$host_pi" ]] || [[ "$TARGET" == "native" ]]; then
    cp "$cli_out" "$SCRIPT_DIR/bin/netspeed-cli"
    cp "$web_out" "$SCRIPT_DIR/bin/netspeed-web"
    chmod +x "$SCRIPT_DIR/bin/netspeed-cli" "$SCRIPT_DIR/bin/netspeed-web"
    log_ok "Updated default local binaries: bin/netspeed-cli and bin/netspeed-web"
  fi
}

main() {
  check_and_offer_go

  local host_pi
  host_pi="$(detect_host_pi_target)"

  if [[ "$TARGET" == "auto" ]]; then
    if [[ "$host_pi" == "arm64" || "$host_pi" == "armv7" ]]; then
      TARGET="$host_pi"
    else
      TARGET="all"
    fi
  elif [[ "$TARGET" == "native" ]]; then
    if [[ "$host_pi" == "other" ]]; then
      log_err "--target native expects an ARM/ARM64 host (detected $(uname -m)). Use --target arm64, --target armv7, or --target all."
      exit 1
    fi
    TARGET="$host_pi"
  fi

  if [[ "$SKIP_TESTS" -eq 0 ]]; then
    log_info "Running Go test suite..."
    go test -v ./internal/... ./cmd/netspeed-cli/... ./cmd/netspeed-web/...
  else
    log_warn "Skipping Go unit tests (--skip-tests)"
  fi

  case "$TARGET" in
    arm64)
      build_target arm64 "$host_pi"
      ;;
    armv7)
      build_target armv7 "$host_pi"
      ;;
    all)
      build_target arm64 "$host_pi"
      build_target armv7 "$host_pi"
      ;;
    *)
      log_err "Invalid --target '$TARGET'. Expected: auto, native, arm64, armv7, or all."
      exit 1
      ;;
  esac

  echo ""
  log_ok "Raspberry Pi build finished."
  if [[ "$host_pi" == "arm64" || "$host_pi" == "armv7" ]] && [[ -f "$SCRIPT_DIR/bin/netspeed-web" ]]; then
    echo "  CLI binary:        ./bin/netspeed-cli"
    echo "  Web server binary: ./bin/netspeed-web"
    echo "  Start web UI:      ./bin/netspeed-web -addr :8080"
    echo "  Smoke test web:    ./bin/netspeed-web --smoke-test"
  else
    echo "  Built Pi binaries in ./bin/:"
    ls -lh "$SCRIPT_DIR"/bin/netspeed-*-linux-* 2>/dev/null || true
    echo "  Copy the arm64 (64-bit OS) or armv7 (32-bit OS) binaries to your Raspberry Pi and run:"
    echo "    ./netspeed-web -addr :8080"
  fi
}

main "$@"
