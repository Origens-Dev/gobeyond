#!/usr/bin/env sh
set -eu

if ! command -v go >/dev/null 2>&1; then
  echo "go is required to verify generated GoBeyond output" >&2
  exit 1
fi

# Release/toolchain changes legitimately change generated build IDs. Validate
# regeneration and repeatability, while forbidding changes to authored tracked
# input. The default website is examples/seo-site; only its compiler output is
# excluded from the source comparison.
before="$(mktemp)"
after="$(mktemp)"
trap 'rm -f "$before" "$after"' EXIT HUP INT TERM
git diff --binary HEAD -- . ':(exclude)examples/seo-site/generated/**' > "$before"
go run ./cmd/gobeyond generate
go run ./cmd/gobeyond generate --check
git diff --binary HEAD -- . ':(exclude)examples/seo-site/generated/**' > "$after"
if ! cmp -s "$before" "$after"; then
  echo "generation unexpectedly changed authored tracked source" >&2
  diff -u "$before" "$after" || true
  exit 1
fi
