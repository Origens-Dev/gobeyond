#!/usr/bin/env sh
set -eu

if rg -uuu \
  --glob '!node_modules/**' \
  --glob '!.git/**' \
  --glob '!dist/**' \
  --glob '!scripts/verify-public-boundaries.sh' \
  'github\.com/gobeyond-dev/gobeyond|gobeyond-dev|github\.com/holbrookab/gobeyond-internal' \
  .; then
  echo "obsolete or private GoBeyond dependency found" >&2
  exit 1
fi
