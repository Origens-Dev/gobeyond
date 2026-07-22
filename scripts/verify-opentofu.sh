#!/usr/bin/env sh
set -eu

directory=${1:-infra/opentofu}

if ! command -v tofu >/dev/null 2>&1; then
  echo "OpenTofu (tofu) is required" >&2
  exit 1
fi

if [ ! -d "$directory" ]; then
  echo "OpenTofu directory does not exist: $directory" >&2
  exit 1
fi

tofu -chdir="$directory" fmt -check -recursive
tofu -chdir="$directory" init -backend=false
tofu -chdir="$directory" validate
