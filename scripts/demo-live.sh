#!/usr/bin/env bash
# Live demo: a mock Palate server, three fake printers and two tills (two real
# agents), all on this machine, driven from a page in the browser.
#
#   bash scripts/demo-live.sh
#
# Then open http://127.0.0.1:8080/ (opened for you unless NO_BROWSER=1).
# Ctrl-C stops everything.
#
#   Front till: Tandoor (backup: Pass), Pass
#   Back till:  Wok
#   If the front till goes silent, the server moves its tickets to Wok.
#
# Options (environment variables):
#   REAL_PRINTER=192.168.1.50   print Tandoor's tickets on a real network printer
#   PORT=8080                   where the page and mock server listen
#   NO_BROWSER=1                don't open the browser
#
# Needs bash, curl and Go. Windows only (credentials and the ledger use DPAPI).
set -euo pipefail
cd "$(dirname "$0")/.."

PORT=${PORT:-8080}
U=http://127.0.0.1:$PORT
OUT=$(mktemp -d)
WIN_OUT=$(cygpath -w "$OUT" 2>/dev/null || echo "$OUT")
PIDS=()
cleanup() {
  echo
  echo "stopping..."
  kill "${PIDS[@]}" 2>/dev/null || true
  wait 2>/dev/null || true
  echo "logs were in $OUT"
}
trap cleanup EXIT INT TERM

echo "building..."
go build -o bin/ ./cmd/...

start() { # name, then the command
  local name=$1; shift
  "$@" >"$OUT/$name.log" 2>&1 &
  PIDS+=($!)
}

# Fake printers, each with a control API for the page.
CONTROLS="p-pass=http://127.0.0.1:19191,p-wok=http://127.0.0.1:19192"
start pass ./bin/fakeprinter -name Pass -addr 127.0.0.1:19101 -control 127.0.0.1:19191 -out ""
start wok ./bin/fakeprinter -name Wok -addr 127.0.0.1:19102 -control 127.0.0.1:19192 -out ""
if [ -n "${REAL_PRINTER:-}" ]; then
  TANDOOR_ADDR=$REAL_PRINTER
  echo "Tandoor tickets go to the real printer at $REAL_PRINTER"
else
  TANDOOR_ADDR=127.0.0.1:19100
  start tandoor ./bin/fakeprinter -name Tandoor -addr 127.0.0.1:19100 -control 127.0.0.1:19190 -out ""
  CONTROLS="p-tandoor=http://127.0.0.1:19190,$CONTROLS"
fi

# The mock server, with short timings so failures show quickly on stage:
# a silent till's tickets move after 15 s.
start mockpalate ./bin/mockpalate -addr 127.0.0.1:$PORT -hold 10s -lease 20s -not-answering 15s \
  -printer-control "$CONTROLS"
for _ in $(seq 100); do curl -sf -o /dev/null $U/mock/state && break; sleep 0.1; done
curl -sf -o /dev/null $U/mock/state || { echo "mock server did not start; see $OUT/mockpalate.log"; exit 1; }

# Two tills.
mkdir -p "$OUT/front" "$OUT/back"
cat >"$OUT/front/agent.json" <<EOF
{
  "server": "$U",
  "agent_name": "front-till",
  "printers": [
    {"id": "p-tandoor", "name": "Tandoor", "kind": "network", "address": "$TANDOOR_ADDR", "width": 80, "backup": "p-pass"},
    {"id": "p-pass", "name": "Pass", "kind": "network", "address": "127.0.0.1:19101", "width": 58}
  ]
}
EOF
cat >"$OUT/back/agent.json" <<EOF
{
  "server": "$U",
  "agent_name": "back-till",
  "printers": [
    {"id": "p-wok", "name": "Wok", "kind": "network", "address": "127.0.0.1:19102", "width": 80}
  ]
}
EOF

pair() { # till folder
  local code
  code=$(curl -sf -X POST $U/mock/pairing-codes -d '{"restaurant_id":"demo"}' | sed -E 's/.*"code":"([^"]+)".*/\1/')
  ./bin/palate-agent pair -config "$WIN_OUT/$1/agent.json" -code "$code"
}
echo "pairing the tills..."
pair front
pair back

# Backups across tills, for when a whole till goes silent.
curl -sf -o /dev/null -X PUT $U/mock/backups -d '{"backups":{"p-tandoor":"p-wok","p-pass":"p-wok"}}'

start front-till ./bin/palate-agent run -config "$WIN_OUT/front/agent.json"
start back-till ./bin/palate-agent run -config "$WIN_OUT/back/agent.json"

echo
echo "  Live demo running: $U/"
echo "  Logs: $OUT"
echo "  Ctrl-C to stop."
echo
if [ -z "${NO_BROWSER:-}" ]; then
  (cmd.exe /c start "" "$U/" >/dev/null 2>&1 || xdg-open "$U/" >/dev/null 2>&1 || true) &
fi
wait
