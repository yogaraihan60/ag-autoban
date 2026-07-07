#!/usr/bin/env bash
set -euo pipefail

ext="so"
case "$(go env GOOS)" in
  windows) ext="dll" ;;
  darwin) ext="dylib" ;;
esac

ldflags="-s -w"
if [[ -n "${PLUGIN_VERSION:-}" ]]; then
  ldflags="${ldflags} -X 'main.pluginVersion=${PLUGIN_VERSION}'"
fi

CGO_ENABLED="${CGO_ENABLED:-1}" go build -trimpath -ldflags="${ldflags}" -buildmode=c-shared -o "ag-autoban.${ext}" .
if command -v strip >/dev/null 2>&1; then
  strip "ag-autoban.${ext}" 2>/dev/null || true
fi
echo "Built $(pwd)/ag-autoban.${ext}"
