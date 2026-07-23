#!/usr/bin/env sh
set -eu

# Fail if private product/hosting markers or obsolete module paths appear in
# the public tree. Origens-Dev/gobeyond (this repo) is the intended public
# home; ban product hosts, AWS accounts/profiles, and private sibling repos.
if rg -uuu \
  --glob '!node_modules/**' \
  --glob '!.git/**' \
  --glob '!dist/**' \
  --glob '!.terraform/**' \
  --glob '!scripts/verify-public-boundaries.sh' \
  -e 'github\.com/gobeyond-dev/gobeyond' \
  -e 'gobeyond-dev' \
  -e 'github\.com/holbrookab/gobeyond' \
  -e 'github\.com/holbrookab/gobeyond-internal' \
  -e 'github\.com/Origens-Dev/gobeyond-internal' \
  -e 'origens\.dev' \
  -e '(^|[^a-zA-Z])[Oo]rigens([^a-zA-Z./-]|$)' \
  -e '790762402823' \
  -e '605252734348' \
  -e '716414846268' \
  -e 'origens-infra' \
  -e 'origens-staging' \
  -e 'origens-prod' \
  .; then
  echo "obsolete or private GoBeyond dependency / product marker found" >&2
  exit 1
fi
