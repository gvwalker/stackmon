#!/usr/bin/env bash
# Compatibility entrypoint. Writes Markdown plus a same-stem JSON plan.
# Usage: release-plan.sh <notes-file> [stable-version]
set -euo pipefail
exec python3 "$(dirname "$0")/release.py" plan "$@"
