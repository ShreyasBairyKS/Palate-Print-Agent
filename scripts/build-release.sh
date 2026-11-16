#!/usr/bin/env bash
# Release build of the agent: what goes to a till.
#
#   bash scripts/build-release.sh
#
# Writes dist/palate-agent.exe, built for 64-bit Windows with debugging
# information stripped (-s -w) and local file paths removed (-trimpath).
# Behaviour is identical to a normal build; the file is about a quarter
# smaller. Prints the size and SHA-256, to record with the release.
#
# Needs bash and Go.
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" -o dist/palate-agent.exe ./cmd/palate-agent

size=$(wc -c < dist/palate-agent.exe)
sum=$(sha256sum dist/palate-agent.exe | cut -d' ' -f1)
zipped=$(gzip -9 -c dist/palate-agent.exe | wc -c)
version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' agent/agent.go)

echo "built dist/palate-agent.exe"
echo "  version:     $version"
echo "  size:        $(awk -v b="$size" 'BEGIN { printf "%.1f MB", b / 1048576 }')"
echo "  compressed:  $(awk -v b="$zipped" 'BEGIN { printf "%.1f MB (roughly the download)", b / 1048576 }')"
echo "  sha256:      $sum"
