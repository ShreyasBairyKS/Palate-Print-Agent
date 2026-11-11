# palate-print-agent

The Palate print agent: a Go service on the restaurant's Windows till that
polls Palate's cloud for print jobs and writes each station's ticket to its
printer. The design is in `../Palate print agent design plan.md`.

**Status: M0 (test rig and seams).** No agent yet. Nothing here talks to a
real printer or a real Palate server, and nothing should until M4 is signed off.

## Layout

| Path | Track | What it is |
| --- | --- | --- |
| `job/` | seam | Shared types: `Job`, `Slip`, `Width`, `Outcome` |
| `render/` | paper | `Render(slip, width) ([]byte, error)`, a stub until M1 |
| `source/` | seam | The `JobSource` interface |
| `api/` | wire | Wire format and endpoint paths (**provisional**, see below) |
| `internal/escpos/` | rig | ESC/POS decoder behind `escpos-dump` and golden-test diffs |
| `internal/fakeprinter/` | rig | Fake TCP 9100 printer with failure modes |
| `internal/mockserver/` | rig | Mock Palate server with hostile flags |
| `cmd/escpos-dump/`, `cmd/fakeprinter/`, `cmd/mockpalate/` | rig | Command-line wrappers |
| `arch_test.go` | seam | Fails the build if `render` imports `net`, or anything below `JobSource` imports `net/http` |
| `scripts/demo.sh` | rig | The M0 demo, run end to end |

## Build and test

Needs Go 1.27+.

```bash
go build -o bin/ ./cmd/...
```

```bash
go test ./...
```

```bash
bash scripts/demo.sh
```

The demo starts the mock server and the fake printer, plays the agent's side
with curl (pair, report printers, long poll, print, report twice, hostile
drop), and prints what each piece saw. It needs bash, curl and python.

`go test -race` needs a 64-bit C compiler (cgo). Install one (for example
MSYS2's mingw-w64 gcc) to run the race detector; the tests pass without it.

## The rig

### fakeprinter

```bash
bin/fakeprinter -dump
```

Listens on `127.0.0.1:9100`, appends what it keeps to `fake-printer.bin` and,
with `-dump`, decodes each connection as it closes.

| `-mode` | Behaviour |
| --- | --- |
| `normal` | Reads everything; answers `DLE EOT` status queries |
| `refuse` | Resets every connection on accept |
| `discard` | Reads everything, keeps nothing (paper out, never reported) |
| `single` | One connection at a time; a second is accepted and never read |
| `stall` | Accepts, never reads; the writer blocks once buffers fill |
| `midstream` | Reads `-after` bytes, then goes silent with the connection open |
| `drop` | Reads `-after` bytes, then resets (partial write) |

`-status ok|paper-out|none` sets the `DLE EOT` answers.

A *connect* timeout can't be faked on loopback, because the OS completes the
handshake. To test one, dial a blackholed address such as `10.255.255.1:9100`,
or inject a dialer in the writer's tests.

### escpos-dump

```bash
bin/escpos-dump fake-printer.bin
```

Prints the offset, the hex (shortened past 8 bytes) and a human reading of
each command. Unknown sequences are labelled and decoding carries on. `-raw`
prints a plain hexdump.

### mockpalate

```bash
bin/mockpalate -seed demo
```

Serves on `127.0.0.1:8080` with a 25 s hold and a 60 s lease, and prints a
pairing code for restaurant `demo`.

**Agent endpoints (provisional):**

| Call | Path | Auth |
| --- | --- | --- |
| Redeem pairing code | `POST /v1/pair` | single-use code |
| Report printers | `PUT /v1/printers` | bearer credential |
| Long poll for jobs (carries health) | `POST /v1/jobs/poll` | bearer credential |
| Report outcome | `POST /v1/jobs/{id}/outcome` | bearer credential |

What the mock already enforces:

- **Restaurant scope:** the restaurant comes from the credential. Requests with
  unknown fields, such as a `restaurant_id`, are rejected, and another
  restaurant's job looks like a missing one (404).
- **Job delivery:** jobs go only to the device that registered the job's
  printer id. A lease that expires returns the job to the queue.
- **Repeat reports:** a second report for a job is a no-op. The first outcome
  is kept.
- **Pairing:** codes are single-use, expire after 10 minutes, and are limited
  to 5 failed attempts per minute per address.
- **Rotation:** the new credential arrives in a poll response. The old one
  works until the new one is first used, then gets `repair_needed`.

**Control endpoints** (mock only, for tests and people):

| Call | Body |
| --- | --- |
| `POST /mock/pairing-codes` | `{"restaurant_id":"demo"}` |
| `POST /mock/jobs` | `{"restaurant_id":"demo","jobs":[{"printer_id":"p1","slip":{...}}]}` |
| `GET /mock/state` | Shows devices, health, jobs, leases, outcomes and the active flags |
| `PUT /mock/hostile` | `{"flags":["repeat-id"]}`; an empty list clears them |
| `POST /mock/devices/{id}/revoke` | none |
| `POST /mock/devices/{id}/rotate` | none |

**Hostile flags** (`-hostile a,b` at startup, or `PUT /mock/hostile`):

| Flag | What the server does | What the agent must do |
| --- | --- | --- |
| `ip-in-job` | Adds `printer_address` to each job | Refuse the job |
| `raw-bytes` | Adds `raw` printer bytes to each job | Refuse the job |
| `huge-payload` | Pads the poll response to about 50 MB | Stop at `api.MaxResponseBytes` (1 MiB) |
| `drop-midway` | Sends half the response, then closes | Retry; the job is redelivered after the lease |
| `short-lease` | Leases for 2 s | Print once; the ledger stops the redelivery |
| `repeat-id` | Sends each job twice in one response | Print once |

Not in the mock yet:

- the till-not-answering rule and moving jobs to another till (M3)
- the update manifest (M4)

## Provisional: what changes when the missing documents arrive

`backend-notes.md`, `security.md` and the intern brief were not available
when M0 was written. The endpoint paths, JSON shapes, pairing-code format and
error codes in `api/` follow the design plan's illustrative table.
`backend-notes.md` wins on any difference.

Because only `api/`, `internal/mockserver/` and (from M1) `source/cloud`
know the wire format, reconciling should not touch the queue, the renderer or
the printer writers.

## Seams

The two seams from the design plan are in `job/`, `render/` and `source/`.
Both interns review any change to them.

The module path `palate-print-agent` is a placeholder until the repository
has a home.
