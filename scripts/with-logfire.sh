#!/usr/bin/env bash
# Bridge LOGFIRE_TOKEN or .logfire/logfire_credentials.json into the standard
# OTEL_EXPORTER_OTLP_* variables that bskyban reads, then exec the given command.
#
# Usage:
#   LOGFIRE_TOKEN=pylf_v1_... ./scripts/with-logfire.sh ./bskyban feed
#   ./scripts/with-logfire.sh ./bskyban list
set -euo pipefail

if [ "$#" -eq 0 ]; then
    echo "usage: $0 <command> [args...]" >&2
    exit 2
fi

if [ -z "${OTEL_EXPORTER_OTLP_ENDPOINT:-}" ] || [ -z "${OTEL_EXPORTER_OTLP_HEADERS:-}" ]; then
    token="${LOGFIRE_TOKEN:-}"
    endpoint="${LOGFIRE_BASE_URL:-}"
    creds="${LOGFIRE_CREDENTIALS:-.logfire/logfire_credentials.json}"

    if [ -z "$token" ] && [ -f "$creds" ] && command -v python3 >/dev/null 2>&1; then
        endpoint="${endpoint:-$(python3 -c "import json,sys;print(json.load(open(sys.argv[1])).get('logfire_api_url',''))" "$creds" 2>/dev/null || true)}"
        token="$(python3 -c "import json,sys;print(json.load(open(sys.argv[1])).get('token',''))" "$creds" 2>/dev/null || true)"
    fi

    if [ -n "$token" ]; then
        if [ -z "$endpoint" ]; then
            case "$token" in
                pylf_v1_eu_*) endpoint="https://logfire-eu.pydantic.dev" ;;
                *)            endpoint="https://logfire-us.pydantic.dev" ;;
            esac
        fi
        export OTEL_EXPORTER_OTLP_ENDPOINT="$endpoint"
        export OTEL_EXPORTER_OTLP_HEADERS="Authorization=$token"
        export OTEL_SERVICE_NAME="${OTEL_SERVICE_NAME:-bskyban}"
    fi
fi

exec "$@"
