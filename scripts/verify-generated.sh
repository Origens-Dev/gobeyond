#!/usr/bin/env sh
set -eu

if ! command -v go >/dev/null 2>&1; then
  echo "go is required to verify generated GoBeyond output" >&2
  exit 1
fi

go run ./cmd/gobeyond generate --check
