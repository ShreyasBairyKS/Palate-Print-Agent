#!/usr/bin/env bash
# End-to-end check of the Windows service on this PC. Run it from a Git Bash
# opened with "Run as administrator":
#
#   bash scripts/service-smoke.sh
#
# It installs the real service (under its own account), against a mock
# server and a fake printer on this machine, then checks that:
#   1. a ticket prints through the service
#   2. stop and start work, and a ticket prints after the restart
#   3. when the service process is killed, Windows restarts it by itself
#   4. the data folder is closed to ordinary users
# and finally uninstalls the service and deletes everything it made. It
# refuses to run if the service is already installed, so it never touches a
# real installation.
#
# Uses ports 18090 and 19300 and the folder C:\ProgramData\PalatePrintAgentSmoke.
set -uo pipefail
cd "$(dirname "$0")/.."

PORT=18090
U=http://127.0.0.1:$PORT
DATA_WIN='C:\ProgramData\PalatePrintAgentSmoke'
DATA=$(cygpath -u "$DATA_WIN")
OUT=$(mktemp -d)
AGENT=./bin/palate-agent.exe
FAILED=0
PIDS=()

pass() { echo "  PASS  $*"; }
fail() { echo "  FAIL  $*"; FAILED=1; }

cleanup() {
  echo
  echo "cleaning up..."
  "$AGENT" uninstall >/dev/null 2>&1 || true
  kill "${PIDS[@]}" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$DATA"
  echo "logs kept in $OUT"
  if [ "$FAILED" = 0 ]; then echo "ALL PASSED"; else echo "SOME CHECKS FAILED"; fi
}

net session >/dev/null 2>&1 || { echo "run this from a terminal opened with \"Run as administrator\""; exit 1; }
if "$AGENT" status 2>/dev/null | grep -q "^Palate Print Agent:"; then
  echo "the service is already installed here; this check won't touch a real installation"; exit 1
fi
trap cleanup EXIT

echo "building..."
go build -o bin/ ./cmd/... || exit 1

./bin/fakeprinter.exe -name Kitchen -addr 127.0.0.1:19300 -out "" -dump >"$OUT/printer.log" 2>&1 &
PIDS+=($!)
./bin/mockpalate.exe -addr 127.0.0.1:$PORT -hold 5s >"$OUT/mockpalate.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do curl -sf -o /dev/null $U/mock/state && break; sleep 0.2; done

mkdir -p "$DATA"
cat >"$DATA/agent.json" <<EOF
{
  "server": "$U",
  "agent_name": "smoke-till",
  "printers": [{"id": "p-kitchen", "name": "Kitchen", "kind": "network", "address": "127.0.0.1:19300", "width": 80}]
}
EOF
code=$(curl -sf -X POST $U/mock/pairing-codes -d '{"restaurant_id":"demo"}' | sed -E 's/.*"code":"([^"]+)".*/\1/')
"$AGENT" pair -config "$DATA_WIN\\agent.json" -code "$code" || exit 1

echo
echo "installing the service..."
"$AGENT" install -config "$DATA_WIN\\agent.json" || { fail "install"; exit 1; }

order() { # order number
  curl -sf -o /dev/null -X POST $U/mock/jobs \
    -d "{\"restaurant_id\":\"demo\",\"jobs\":[{\"printer_id\":\"p-kitchen\",\"slip\":{\"station\":\"Kitchen\",\"order_number\":\"$1\",\"items\":[{\"quantity\":1,\"name\":\"Naan\"}]}}]}"
}
printed() { # order number: wait up to 30 s for it on the fake printer
  for _ in $(seq 150); do grep -q "#$1" "$OUT/printer.log" && return 0; sleep 0.2; done
  return 1
}
state() { "$AGENT" status -log 0 2>/dev/null | sed -n 's/^Palate Print Agent: //p'; }
pid() { "$AGENT" status -log 0 2>/dev/null | sed -n 's/^  process: *//p'; }

echo
echo "checks:"
order 9001
if printed 9001; then pass "a ticket printed through the service"; else fail "no ticket printed through the service"; fi

"$AGENT" stop >/dev/null && [ "$(state)" = stopped ] && pass "stop" || fail "stop (state: $(state))"
"$AGENT" start >/dev/null && [ "$(state)" = running ] && pass "start" || fail "start (state: $(state))"
order 9002
if printed 9002; then pass "a ticket printed after the restart"; else fail "no ticket after the restart"; fi

old=$(pid)
taskkill //F //PID "$old" >/dev/null 2>&1
new=""
for _ in $(seq 60); do
  sleep 0.5
  p=$(pid)
  if [ -n "$p" ] && [ "$p" != "$old" ] && [ "$(state)" = running ]; then new=$p; break; fi
done
if [ -n "$new" ]; then pass "killed process $old; Windows restarted the service as process $new"; else fail "the service did not come back after being killed (state: $(state))"; fi
order 9003
if printed 9003; then pass "a ticket printed after the automatic restart"; else fail "no ticket after the automatic restart"; fi

acl=$(icacls "$DATA_WIN" 2>/dev/null)
if echo "$acl" | grep -q "NT SERVICE\\\\PalatePrintAgent" && ! echo "$acl" | grep -qi "BUILTIN\\\\Users"; then
  pass "data folder: open to the service and administrators, not to ordinary users"
else
  fail "data folder access is wrong:"; echo "$acl"
fi
if "$AGENT" status | grep -q "log lines"; then pass "status shows the service log"; else fail "status shows no log"; fi

echo
"$AGENT" status
