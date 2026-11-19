#!/usr/bin/env bash
# Builds the package handed to Palate and to restaurants:
#
#   bash scripts/package.sh
#
# Writes dist/palate-print-agent-<version>/ and the same as a .zip:
#   palate-agent.exe   the agent, release build (-trimpath, -s -w)
#   mockpalate.exe     the reference server, for the demo and for Palate
#   fakeprinter.exe    the demo's on-screen printers
#   start-demo.ps1     the demo, for Windows without Git Bash or Go
#   README-FIRST.md, docs/SERVER-API.md, docs/TILL-SETUP.md, examples/
#   SHA256SUMS.txt
#
# Needs bash, Go and PowerShell (for the zip).
set -euo pipefail
cd "$(dirname "$0")/.."

version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' agent/agent.go)
name="palate-print-agent-$version"
out="dist/$name"
rm -rf "${out:?}" "dist/$name.zip"
mkdir -p "$out/docs" "$out/examples"

echo "building $name..."
for cmd in palate-agent mockpalate fakeprinter; do
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o "$out/$cmd.exe" "./cmd/$cmd"
done

cp package/README-FIRST.md package/start-demo.ps1 "$out/"
cp docs/SERVER-API.md docs/TILL-SETUP.md "$out/docs/"
cp examples/bill.html examples/bill-80mm-printed.png examples/bill-58mm-printed.png "$out/examples/"
# Windows tools read these better with CRLF line endings.
for f in "$out/README-FIRST.md" "$out/start-demo.ps1" "$out/docs/"*.md "$out/examples/bill.html"; do
  sed -i 's/\r*$/\r/' "$f"
done
(cd "$out" && sha256sum palate-agent.exe mockpalate.exe fakeprinter.exe > SHA256SUMS.txt)

powershell.exe -NoProfile -Command "Compress-Archive -Path '$(cygpath -w "$out")' -DestinationPath '$(cygpath -w "dist/$name.zip")' -Force"

echo
echo "  $out/"
echo "  dist/$name.zip ($(du -h "dist/$name.zip" | cut -f1))"
echo
cat "$out/SHA256SUMS.txt"
