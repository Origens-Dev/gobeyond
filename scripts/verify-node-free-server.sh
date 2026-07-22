#!/usr/bin/env sh
set -eu

artifact_dir=${1:-dist/server}

if [ ! -d "$artifact_dir" ]; then
  echo "expected production server artifact directory: $artifact_dir" >&2
  exit 1
fi

if [ ! -x "$artifact_dir/gobeyond-server" ]; then
  echo "expected executable Go server: $artifact_dir/gobeyond-server" >&2
  exit 1
fi

if [ -d "$artifact_dir/node_modules" ] || [ -d "$artifact_dir/.pnpm" ]; then
  echo "production server artifact contains a package-manager directory" >&2
  exit 1
fi

if find "$artifact_dir" -type f \( -name '*.ts' -o -name '*.tsx' -o -name '*.mts' -o -name '*.cts' -o -name 'package.json' -o -name 'pnpm-lock.yaml' -o -name 'npm' -o -name 'node' \) -print -quit | grep -q .; then
  echo "production server artifact contains Node/npm or source TypeScript material" >&2
  exit 1
fi

if find "$artifact_dir" -type f -perm -111 -exec file {} \; | grep -Ei 'node|javascript runtime' >/dev/null; then
  echo "production server artifact contains a Node/JavaScript executable" >&2
  exit 1
fi

echo "Node-free production artifact verified: $artifact_dir"
