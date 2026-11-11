#!/usr/bin/env bash
# M0 protocol walkthrough: runs the mock server and the fake printer, plays the agent's side of
# the protocol with curl, and prints what each piece saw.
#
#   go build -o bin/ ./cmd/... && bash scripts/demo-protocol.sh
#
# Needs bash, curl and python (for JSON). Uses ports 18080 and 19100.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=$(mktemp -d)
U=http://127.0.0.1:18080
trap 'kill $MOCK $FP 2>/dev/null || true; wait 2>/dev/null || true' EXIT

./bin/mockpalate -addr 127.0.0.1:18080 -seed demo >"$OUT/mock.log" 2>&1 &
MOCK=$!
./bin/fakeprinter -addr 127.0.0.1:19100 -out "$OUT/fake-printer.bin" -dump >"$OUT/printer.out" 2>"$OUT/printer.log" &
FP=$!

for _ in $(seq 50); do
  grep -q "pairing code" "$OUT/mock.log" 2>/dev/null && curl -s -o /dev/null $U/mock/state && break
  sleep 0.1
done

json() { python -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }

echo "== 1. pair the till"
CODE=$(grep -oE '[0-9A-F]{4}-[0-9A-F]{4}' "$OUT/mock.log" | head -1)
CRED=$(curl -sf -X POST $U/v1/pair -d "{\"code\":\"$CODE\",\"agent_name\":\"till-1\"}" | json "['credential']")
echo "code $CODE -> credential ${CRED:0:8}..."
echo -n "reusing the code: "
curl -s -X POST $U/v1/pair -d "{\"code\":\"$CODE\"}"; echo

echo "== 2. report printers"
curl -sf -o /dev/null -w "PUT /v1/printers -> %{http_code}\n" -X PUT $U/v1/printers \
  -H "Authorization: Bearer $CRED" \
  -d '{"printers":[{"id":"p-tandoor","name":"Tandoor","kind":"network","width":80}]}'

echo "== 3. long poll, job confirmed 2 s later"
HEALTH='{"health":{"agent_version":"0.0.1","queue_depth":0,"printers":[{"id":"p-tandoor","state":"up"}]}}'
curl -sf -w "\n(poll held %{time_total}s)\n" -X POST $U/v1/jobs/poll -H "Authorization: Bearer $CRED" -d "$HEALTH" >"$OUT/poll.out" &
POLL=$!
sleep 2
curl -sf -o /dev/null -X POST $U/mock/jobs -d '{"restaurant_id":"demo","jobs":[{"printer_id":"p-tandoor","slip":{"station":"Tandoor","table":"T4","order_number":"1042","items":[{"quantity":2,"name":"Paneer Tikka","notes":"less spicy"}]}}]}'
wait $POLL
cat "$OUT/poll.out"
JOB=$(head -1 "$OUT/poll.out" | json "['jobs'][0]['id']")

echo "== 4. print (hand-made ticket; Render arrives in M1)"
printf '\x1b@\x1ba\x01\x1d!\x11TANDOOR\n\x1d!\x00\x1ba\x00T4  #1042\n\x1bE\x012 x Paneer Tikka\n\x1bE\x00   less spicy\n\x1dVB\x03' >"$OUT/ticket.bin"
python -c "import socket,sys; s=socket.create_connection(('127.0.0.1',19100)); s.sendall(open(sys.argv[1],'rb').read()); s.close()" "$OUT/ticket.bin"
sleep 0.3

echo "== 5. report the outcome, twice"
for i in 1 2; do
  curl -sf -o /dev/null -w "POST outcome -> %{http_code}\n" -X POST "$U/v1/jobs/$JOB/outcome" \
    -H "Authorization: Bearer $CRED" -d '{"kind":"written","printer_id":"p-tandoor"}'
done

echo "== 6. hostile: drop-midway"
curl -sf -o /dev/null -X PUT $U/mock/hostile -d '{"flags":["drop-midway"]}'
curl -sf -o /dev/null -X POST $U/mock/jobs -d '{"restaurant_id":"demo","jobs":[{"printer_id":"p-tandoor","slip":{"station":"Tandoor","order_number":"1043","items":[{"quantity":1,"name":"Naan"}]}}]}'
set +e
curl -s -o /dev/null -w "got %{size_download} bytes, " -X POST $U/v1/jobs/poll -H "Authorization: Bearer $CRED" -d "$HEALTH"
echo "curl exit $? (18 = transfer closed early)"
set -e

echo "== server state"
curl -s $U/mock/state | python -c "
import sys,json
s=json.load(sys.stdin)
for d in s['devices']: print('device', d['id'], d['restaurant'], 'agent', d['health']['agent_version'])
for j in s['jobs']: print(j['id'], j['state'], 'deliveries', j['deliveries'], 'reports', j['reports'], (j.get('outcome') or {}).get('kind',''))
print('hostile', s['hostile'])"

echo "== fake printer saw"
cat "$OUT/printer.log" "$OUT/printer.out"
echo "== logs in $OUT"
