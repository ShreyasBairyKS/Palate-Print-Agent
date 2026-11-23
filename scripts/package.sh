#!/usr/bin/env bash
# Builds the package handed to Palate and to restaurants:
#
#   bash scripts/package.sh
#
# Writes dist/palate-print-agent-<version>/ and the same as a .zip (Windows),
# and dist/palate-print-agent-<version>-<linux|macos>-<arch>.tar.gz for Linux
# (amd64, arm64, armv7) and macOS (amd64, arm64).
#
# The Windows folder:
#   palate-agent.exe   the agent, release build (-trimpath, -s -w)
#   mockpalate.exe     the reference server, for the demo and for Palate
#   fakeprinter.exe    the demo's on-screen printers
#   start-demo.ps1     the demo, for Windows without Git Bash or Go
#   HOW-TO-USE.txt, README-FIRST.md, docs/SERVER-API.md, docs/TILL-SETUP.md, examples/
#   SHA256SUMS.txt
#
# Needs bash, Go, Python (for the .tar.gz files) and PowerShell (for the zip).
set -euo pipefail
cd "$(dirname "$0")/.."

version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' agent/agent.go)
name="palate-print-agent-$version"
out="dist/$name"
rm -rf "dist/$name.zip"
# Empty the folder rather than fail when it can't be removed itself (an
# Explorer window or OneDrive sync holding it open).
rm -rf "${out:?}" 2>/dev/null || find "$out" -mindepth 1 -delete
mkdir -p "$out/docs" "$out/examples"

# When it was built: the setup and status windows show it, and an update
# compares it, so two builds of one version can be told apart.
built=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "building $name ($built)..."
for cmd in palate-agent mockpalate fakeprinter; do
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.buildDate=$built" -o "$out/$cmd.exe" "./cmd/$cmd"
done

cp package/HOW-TO-USE.txt package/README-FIRST.md package/start-demo.ps1 "$out/"
cp docs/SERVER-API.md docs/TILL-SETUP.md "$out/docs/"
cp examples/bill.html examples/bill-80mm-printed.png examples/bill-58mm-printed.png "$out/examples/"
# Windows tools read these better with CRLF line endings.
for f in "$out/HOW-TO-USE.txt" "$out/README-FIRST.md" "$out/start-demo.ps1" "$out/docs/"*.md "$out/examples/bill.html"; do
  sed -i 's/\r*$/\r/' "$f"
done
(cd "$out" && sha256sum palate-agent.exe mockpalate.exe fakeprinter.exe > SHA256SUMS.txt)

powershell.exe -NoProfile -Command "Compress-Archive -Path '$(cygpath -w "$out")' -DestinationPath '$(cygpath -w "dist/$name.zip")' -Force"

# Linux and macOS: one .tar.gz each, with the agent, its how-to and (macOS)
# a setup file to double-click. No window there yet: setup and the rest are
# terminal commands. Files keep Unix line endings and modes.
unix=()
for t in linux/amd64 linux/arm64 linux/arm macos/amd64 macos/arm64; do
  os=${t%/*}; arch=${t#*/}
  goos=$os; [ "$os" = macos ] && goos=darwin
  label=$arch; [ "$arch" = arm ] && label=armv7
  u="dist/$name-$os-$label"
  rm -rf "$u.tar.gz"
  rm -rf "${u:?}" 2>/dev/null || find "$u" -mindepth 1 -delete
  mkdir -p "$u"
  GOOS=$goos GOARCH=$arch GOARM=7 CGO_ENABLED=0     go build -trimpath -ldflags "-s -w -X main.buildDate=$built" -o "$u/palate-agent" ./cmd/palate-agent
  cp package/unix/HOW-TO-USE.txt "$u/"
  if [ "$os" = macos ]; then cp "package/unix/Set up Palate print agent.command" "$u/"; fi
  for f in "$u/HOW-TO-USE.txt" "$u/"*.command; do [ -f "$f" ] && sed -i 's/$//' "$f"; done
  (cd "$u" && sha256sum palate-agent > SHA256SUMS.txt)
  python scripts/tarball.py "$u" "$u.tar.gz" >/dev/null
  unix+=("$u.tar.gz")
done

echo
echo "  $out/"
echo "  dist/$name.zip ($(du -h "dist/$name.zip" | cut -f1))"
for f in "${unix[@]}"; do echo "  $f ($(du -h "$f" | cut -f1))"; done
echo
cat "$out/SHA256SUMS.txt"
for f in "${unix[@]}"; do echo "$(cut -d' ' -f1 "${f%.tar.gz}/SHA256SUMS.txt")  $(basename "${f%.tar.gz}")/palate-agent"; done
