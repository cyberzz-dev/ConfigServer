#!/usr/bin/env bash
# build.sh — Linux/macOS build script for ConfigServer
#
# Usage:
#   ./build.sh                        # build everything (WebUI + all binaries)
#   ./build.sh allinone               # build WebUI + allinone
#   ./build.sh configserver           # build configserver (no WebUI needed)
#   ./build.sh admin                  # build WebUI + admin
#   ./build.sh webui                  # build WebUI only
#   SKIP_WEBUI=1 ./build.sh           # skip npm build (use existing dist/)
#   VERSION=1.0.0 ./build.sh          # embed version string

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${1:-all}"
VERSION="${VERSION:-dev}"
SKIP_WEBUI="${SKIP_WEBUI:-0}"
GOOS_TARGET="${GOOS_TARGET:-linux}"
GOARCH_TARGET="${GOARCH_TARGET:-amd64}"

# ── colors ───────────────────────────────────────────────────────────────────

CYAN='\033[0;36m'
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

step() { echo -e "${CYAN}==> $1${NC}"; }
ok()   { echo -e "${GREEN}    $1${NC}"; }
fail() { echo -e "${RED}    ERROR: $1${NC}" >&2; exit 1; }

# ── webui ────────────────────────────────────────────────────────────────────

build_webui() {
    step "Building React WebUI"
    command -v npm >/dev/null 2>&1 || fail "npm not found. Install Node.js >= 18."
    cd "$SCRIPT_DIR/webui"
    npm install --prefer-offline
    npm run build
    cd "$SCRIPT_DIR"
    ok "WebUI -> webui/dist/"
}

# ── go binaries ───────────────────────────────────────────────────────────────

build_go() {
    local cmd_name="$1"
    local bin_name="$2"
    step "Building $cmd_name"
    CGO_ENABLED=0 GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET" \
        go build \
            -trimpath \
            -ldflags "-s -w -X main.Version=${VERSION}" \
            -o "$SCRIPT_DIR/bin/$bin_name" \
            "./cmd/$cmd_name/"
    ok "bin/$bin_name"
}

# ── main ──────────────────────────────────────────────────────────────────────

mkdir -p "$SCRIPT_DIR/bin"
cd "$SCRIPT_DIR"

need_webui=0
if [[ "$SKIP_WEBUI" == "0" ]] && [[ "$TARGET" =~ ^(all|allinone|admin)$ ]]; then
    need_webui=1
fi

if [[ "$need_webui" == "1" ]]; then
    build_webui
fi

case "$TARGET" in
    webui)
        # already built above
        ;;
    allinone)
        build_go allinone allinone
        ;;
    configserver)
        build_go configserver configserver
        ;;
    admin)
        build_go admin admin
        ;;
    all)
        build_go allinone    allinone
        build_go configserver configserver
        build_go admin       admin
        ;;
    *)
        fail "Unknown target: '$TARGET'. Valid targets: all  allinone  configserver  admin  webui"
        ;;
esac

echo ""
ok "Build complete. Output: $SCRIPT_DIR/bin/"
ls -lh "$SCRIPT_DIR/bin/" 2>/dev/null || true
