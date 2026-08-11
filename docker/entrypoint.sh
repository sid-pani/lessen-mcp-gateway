#!/bin/sh
set -eu

config_path="${MCP_GATEWAY_CONFIG:-/config/mcp-gateway-config.json}"
if [ ! -e "$config_path" ]; then
  mkdir -p "$(dirname "$config_path")"
  cp /usr/share/mcp-gateway/default-config.json "$config_path"
  chmod 0600 "$config_path" 2>/dev/null || true
fi

exec /usr/local/bin/mcp-gate "$@"
