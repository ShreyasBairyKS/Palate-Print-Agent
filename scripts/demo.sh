#!/usr/bin/env bash
# M1 demo: the real agent pairs with the mock server, test-prints, then runs.
# An order confirmed on the mock prints on the fake printer.
#
#   go build -o bin/ ./cmd/... && bash scripts/demo.sh
#
# Needs bash, curl and python. Uses ports 18080 and 19100. Windows only
# (the credential is stored with DPAPI).
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=$(mktemp -d)
WIN_OUT=$(cygpath -w "$OUT" 2>/dev/null || echo "$OUT")
U=http://127.0.0.1:18080
trap 'kill $MOCK $FP ${AGENT:-} 2>/dev/null || true; wait 2>/dev/null || true' EXIT

./bin/mockpalate -addr 127.0.0.1:18080 -hold 25s -seed demo >"$OUT/mock.log" 2>&1 &
MOCK=$!
./bin/fakeprinter -addr 127.0.0.1:19100 -out "$OUT/fake-printer.bin" -dump >"$OUT/printer.out" 2>"$OUT/printer.log" &
FP=$!
for _ in $(seq 50); do
  grep -q "pairing code" "$OUT/mock.log" 2>/dev/null && curl -s -o /dev/null $U/mock/state && break
  sleep 0.1
done

cat >"$OUT/agent.json" <<EOF
{
  "server": "$U",
  "agent_name": "demo-till",
  "printers": [
    {"id": "p-tandoor", "name": "Tandoor", "kind": "network", "address": "127.0.0.1:19100", "width": 80}
  ]
}
EOF
CFG="$WIN_OUT/agent.json"

echo "== 1. pair"
CODE=$(grep -oE '[0-9A-F]{4}-[0-9A-F]{4}' "$OUT/mock.log" | head -1)
./bin/palate-agent pair -config "$CFG" -code "$CODE"
echo -n "credential file starts with: "
head -c 16 "$OUT/credential.bin" | od -An -tx1 | head -1
echo "(DPAPI blob, not the credential)"

echo "== 2. test print"
./bin/palate-agent test-print -config "$CFG" -printer p-tandoor
sleep 0.3

echo "== 3. run, then confirm an order on the mock"
./bin/palate-agent run -config "$CFG" 2>"$OUT/agent.log" &
AGENT=$!
sleep 1
# Sent from a file: on Windows, curl's command-line arguments pass through
# the ANSI code page, which would mangle the accents.
cat >"$OUT/order.json" <<'EOF'
{"restaurant_id":"demo","jobs":[{"printer_id":"p-tandoor","slip":{"station":"Tandoor","table":"T4","order_number":"1042","items":[{"quantity":2,"name":"Paneer Tikka","notes":"less spicy"},{"quantity":1,"name":"Café crème brûlée"}],"notes":"birthday table, bring the cake last"}}]}
EOF
START=$(python -c "import time; print(time.time())")
curl -sf -o /dev/null -X POST $U/mock/jobs -H "Content-Type: application/json" --data-binary "@$WIN_OUT/order.json"
for _ in $(seq 50); do
  curl -s $U/mock/state | grep -q '"outcome"' && break
  sleep 0.05
done
python -c "import time; print('order confirmed -> outcome reported in %.0f ms' % ((time.time()-$START)*1000))"

echo "== server state"
curl -s $U/mock/state | python -c "
import sys,json
s=json.load(sys.stdin)
for d in s['devices']:
    h=d['health']; print('device', d['id'], 'agent', h['agent_version'], 'printers', [(p['id'], p['state']) for p in h.get('printers') or []])
for j in s['jobs']: print(j['id'], j['state'], (j.get('outcome') or {}).get('kind',''), (j.get('outcome') or {}).get('printer_id',''))"

echo "== fake printer saw (test print, then the order)"
cat "$OUT/printer.log"
grep -E 'text|GS V' "$OUT/printer.out"
echo "== agent log"
cat "$OUT/agent.log"
