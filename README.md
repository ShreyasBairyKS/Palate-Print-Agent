# palate-print-agent

The Palate print agent: a Go service on the restaurant's Windows till that
polls Palate's cloud for print jobs and writes each station's ticket to its
printer. The design is in `../Palate print agent design plan.md`.

**Status: M3.**

- **Hostile server:** every hostile mock flag ends in a correct recorded
  outcome (`TestM3EveryHostileFlagEndsInARecordedOutcome`).
- **Hanging printer:** its ticket reaches the backup within 15 s: 3.0 s for
  a connect hang (`TestM3HangingPrinterReachesBackupWithin15s`), 10.0 s for
  a full write stall (`queue.TestHangingPrinterReachesBackupWithin15s`).
- **Silent till:** when a till stops answering, its jobs print on another
  till with a MOVED FROM banner (`TestM3SilentTillsJobsPrintOnAnotherTill`).
- **Crashes (M2):** killing the agent process mid-write never loses a ticket or
  prints one twice unmarked (`TestM2KillMidWrite`).
- **Indian scripts:** a Kannada slip renders identically every time,
  including concurrently (`TestKannadaRendersIdenticallyTwice`).
- **Latency (M1):** a mock order prints within 1 s.

Nothing here should talk to a real printer or a real Palate server until M4
is signed off.

## Live demo

```bash
bash scripts/demo-live.sh
```

This builds everything, starts a mock Palate server, three fake printers and
two tills (two real agents), pairs them, and opens http://127.0.0.1:8080/.
Ctrl-C stops it all.

| Till | Printers |
| --- | --- |
| front-till | Tandoor (its backup on this till: Pass), Pass |
| back-till | Wok |

If the front till goes silent, the server moves its tickets to Wok.

On the page you can:

- **Place orders.** Use the presets (English, Kannada, Hindi, Tamil, accented)
  or type your own. Items are split into one ticket per station.
- **Watch each ticket's outcome live:** queued, printing, then `printed`,
  `written`, `fallback` or `failed`, with the reason, and alerts.
- **See the paper each printer produced,** drawn from the actual ESC/POS
  bytes, with Indian-script lines as the printed images.
- **Break things on purpose:**
  - set a printer to switched off, resetting, hung, dropping or freezing
    mid-ticket, or paper out;
  - cut a till's network, so after 15 s its tickets move to the other till;
  - revoke a till;
  - turn on any hostile server mode.

The demo uses short timings so failures show quickly: a 10 s hold, a 20 s
lease, and a till counts as not answering after 15 s. A till whose network
comes back reconnects on its next retry, within 30 s.

To print Tandoor's tickets on a **real network printer**:

```bash
REAL_PRINTER=192.168.1.50 bash scripts/demo-live.sh
```

## Layout

| Path | Track | What it is |
| --- | --- | --- |
| `job/` | seam | Shared types: `Job`, `Slip`, `Width`, `Outcome` |
| `source/` | seam | The `JobSource` interface |
| `render/` | paper | `Render(slip, width)` at 58/80 mm: text in WPC1252, or raster for Kannada, Devanagari and Tamil; golden tests and PNG previews in `testdata/` |
| `render/fonts/` | paper | Bundled Noto fonts (OFL), embedded in the binary |
| `ledger/` | wire | The printed-ID ledger: Attempting/Done per job, DPAPI-protected, append-only, 7-day retention |
| `source/cloud/` | wire | Long-poll Fetch, Report, printer registration, credential rotation, refusing hostile jobs |
| `pair/` | wire | Redeem a pairing code; the credential is stored with DPAPI at machine scope |
| `internal/dpapi/` | wire | DPAPI at machine scope through crypt32.dll |
| `config/` | wire | The till's local config: server, printers and addresses, timings |
| `printer/` | wire | The `Writer` interface and failure kinds (refused, hang, partial, paper out) |
| `printer/net/` | wire | Raw TCP 9100 writer with a DLE EOT status check |
| `printer/usb/` | wire | Windows spooler writer, datatype RAW |
| `queue/` | wire | One serial queue per printer: ledger, render, write, retry fast failures, failure ladder to the backup, probing down printers, report |
| `agent/` | wire | The poll loop, watchdog, health payload, and backoff with jitter |
| `api/` | wire | Wire format and endpoint paths (**provisional**, see below) |
| `cmd/palate-agent/` | | The agent: `printers`, `pair`, `test-print`, `run`, `version` |
| `internal/escpos/`, `internal/fakeprinter/`, `internal/mockserver/` | rig | Decoder and ticket preview, fake printer with a control API, mock server |
| `internal/mockserver/demo/` | rig | The live demo page, served by the mock server |
| `cmd/escpos-dump/`, `cmd/fakeprinter/`, `cmd/mockpalate/` | rig | Command-line wrappers |
| `arch_test.go` | seam | `render`, `job`, `ledger`, `internal/dpapi` may not import `net`; `queue`, `printer/*`, `config` may not import `net/http` |
| `e2e_test.go` | | The real agent against the mock server and fake printers |
| `crash_test.go` | | Builds `palate-agent`, kills it mid-write, restarts it |
| `m3_test.go` | | Hostile flags, a hanging printer to its backup, a silent till's jobs on another till |
| `scripts/demo-live.sh` | | **Live demo in the browser**: two tills, three printers, break things on purpose |
| `scripts/demo.sh` | | Scripted demo: pair, test print, run, an order with a Kannada dish prints |
| `scripts/demo-protocol.sh` | | M0 walkthrough of the protocol with curl |

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

The demos need bash, curl and python. `go test -race` needs a 64-bit C
compiler (cgo); the tests pass without it.

To regenerate the render golden files after a deliberate layout change:

```bash
go test ./render -update
```

Review the `.txt` dumps in the diff (every command and line of text) and the
`.png` previews (the raster lines as they will print).

Raster cost per ticket, from `go test ./render -run=^$ -bench=.`:

| Slip | 58 mm | 80 mm |
| --- | --- | --- |
| Latin only | under 0.01 ms, 217 bytes | under 0.01 ms, 265 bytes |
| Kannada (3 items, one wrapping) | 1.3 ms, 11.5 KB | 2.3 ms, 13.8 KB |
| Devanagari (2 items, a note) | 1.5 ms, 10.5 KB | 2.5 ms, 12.6 KB |
| Tamil (2 items) | 0.7 ms, 6.2 KB | 1.3 ms, 9.3 KB |

Rendering is negligible next to the 1 s budget. What still needs measuring on
real printers in M4 is how fast they print a 576-dot raster line, and whether
26-dot text is easy to read in a kitchen.

## Dependencies

| Module | Licence | Used for |
| --- | --- | --- |
| `github.com/go-text/typesetting` | BSD-3-Clause / Unlicense | HarfBuzz shaping for Indian scripts |
| `golang.org/x/image` | BSD-3-Clause | Rasterising glyph outlines |

The fonts are listed in `render/fonts/README.md`.

## Running the agent

The config lives at `%ProgramData%\PalatePrintAgent\agent.json` unless
`-config` says otherwise:

```json
{
  "server": "https://api.palate.example",
  "agent_name": "front-till",
  "printers": [
    {"id": "p-tandoor", "name": "Tandoor", "kind": "network", "address": "192.168.1.50", "width": 80, "backup": "p-pass"},
    {"id": "p-wok", "name": "Wok", "kind": "network", "address": "192.168.1.51:9100", "width": 58},
    {"id": "p-pass", "name": "Pass", "kind": "usb", "printer_name": "EPSON TM-T82 Receipt", "width": 80}
  ],
  "timings": {"stall_timeout": "10s"}
}
```

| Command | What it does |
| --- | --- |
| `palate-agent printers` | Lists the Windows printers this process can see; use one as a usb printer's `printer_name` |
| `palate-agent pair -code K7Q2-M9XD` | Redeems the code, stores the credential (DPAPI) beside the config, registers the printers |
| `palate-agent test-print -printer p-tandoor` | Prints a test slip and says whether the printer confirmed it |
| `palate-agent run` | Polls and prints in the foreground until Ctrl-C, or until the credential is revoked |

Config rules:

- **Printer addresses:** IP literals only, in 10/8, 172.16/12 or 192.168/16,
  with port 9100 by default. Hostnames and public IPs are refused. Loopback is
  also accepted, for the rig.
- **USB printers:** named by their Windows printer name (`printer_name`),
  never an address. The service runs as LocalSystem, which sees only
  printers installed for all users. `run` warns about any it can't see.
- **Server:** must use https. Plain http is allowed only to a loopback address,
  for the mock.
- **Backups:** `backup` names another printer on the same till, used when
  this one is down. A backup on another till is the server's job (see the
  till-not-answering rule below).
- **Files:** the credential and the ledger are stored beside the config by
  default (`credential_file`, `ledger_file`).

| Timing | Default | Meaning |
| --- | --- | --- |
| `connect_timeout` | 3s | No connection by then is a hang |
| `stall_timeout` | 10s | A write with no progress this long is a hang (or partial) |
| `status_timeout` | 500ms | How long to wait for a DLE EOT reply |
| `fast_retries` / `retry_gap` | 2 / 1s | Retries after a refused connection only |
| `delayed_after` | 10m | Older jobs print with a DELAYED banner |
| `probe_every` | 30s | How often a down network printer is checked |

### What happens to a ticket

1. The long poll returns the moment the server queues a job.
2. Jobs carrying an address, raw printer bytes, any unknown field, or a
   printer id that is an address are refused and reported `failed`. They never
   reach a printer. A job repeated in one response, or delivered again while
   it is still queued, prints once.
3. The ledger is checked:

   | Ledger says | What happens |
   | --- | --- |
   | Done | The job is not printed; its stored outcome is reported again |
   | Attempting (the agent died mid-write) | The job prints again with a DUPLICATE banner and the reason `crash-recovery` |
   | Nothing | The job prints normally |

4. The job's printer queue renders it. Each paragraph goes as text if code
   page WPC1252 has every character. Otherwise it is shaped with the bundled
   Noto fonts and sent as a raster image. Text no bundled font covers (for
   example Chinese) is reported `failed` with the reason.
5. The ledger records Attempting and flushes to disk. If the ledger can't be
   written, the job prints anyway and the outcome says a crash could print it
   twice.
6. The writer sends the bytes.

   | Printer | Result | Outcome |
   | --- | --- | --- |
   | Network | Printer confirms the status query | `printed` |
   | Network | No reply | `written` |
   | USB | Spooler took every byte | `written` (the spooler gives no paper status) |
   | Either | Refused | Retried twice, then the ladder (step 7) |
   | Either | Hang, partial write or paper out | No retry; the ladder (step 7) |

7. The failure ladder. A failed printer is marked down. If it has a backup on
   this till, the ticket goes to the backup's own queue and prints there. Only
   the backup's worker writes to it, because most printers drop a second
   connection.
   - **Banner:** the backup copy says "BACKUP FOR TANDOOR".
   - **After a partial write:** the backup copy is also marked DUPLICATE
     ("partial ticket on TANDOOR").
   - **Outcome:** `fallback`, with the reason.
   - **No backup, or the backup fails too:** `failed`, naming both printers.
   - **While a printer is down:** its tickets go straight to the backup, with
     no wait for another timeout. It is probed every 30 s, and a successful
     probe or write brings it back. A down printer with no backup is still
     tried, because it may have recovered.
8. The ledger records Done with the outcome and flushes. Only then is the
   outcome reported, with retries. If the report is lost, the lease expires,
   the job comes back, and step 3 reports the stored outcome without
   printing.

### When something gets stuck

- **The poll loop:** the watchdog abandons a poll that outlives 55 s (the
  hold time plus 30 s) and starts a fresh one. The restart count rides in
  the health payload as `watchdog_restarts`.
- **A till stops answering:** this is the server's rule; the mock implements
  it. No poll for `2 x hold + 10 s` (60 s) and the till counts as not
  answering.
  - Its queued jobs move to their printers' backups on an answering till,
    and print there with "MOVED FROM TANDOOR / check for duplicate".
  - A job whose lease is still live is never moved.
  - With no reachable backup, the server records the job as failed.
  - If the original till reports the job later, the server keeps both
    outcomes and raises a duplicate alert.
- **A job the agent can't accept:** this is a poison job, such as an
  oversized or cut-off response. After 5 deliveries with no outcome, the
  server records it as failed and raises an alert.

## Known gaps (by milestone)

- **Not covered yet:**
  - A printer that dies while its till stays up, when its backup is on
    another till. The agent can only use backups on its own till, and the
    server moves jobs only when a whole till goes silent, so today that
    ticket is a recorded failure. It needs a way for the agent to hand a job
    back to the server (`backend-notes.md`).
  - USB spooler job status: the spooler accepts jobs for an unplugged printer.
  - Small tickets to a stalled network printer show as `written`, because
    socket buffers accept them. A big one shows as `partial`.
- **M4:**
  - The Windows service, the signed MSI and update verification. Today `run`
    is a foreground command; Windows will restart a crashed service.
  - Raster speed and legibility on real printers.
- **Owner to confirm:**
  - LocalSystem as the service account. It's assumed for now and nothing in
    the code depends on it.
  - Reprinting as DUPLICATE (rather than reporting `failed`) after a crash
    mid-write.
  - The timings: lease 60 s, not answering 60 s, probe 30 s, poison job
    after 5 deliveries.

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
| `off` | Stops listening, like a switched-off printer: connections are refused |

`-status ok|paper-out|none` sets the `DLE EOT` answers. `-name Tandoor -control
127.0.0.1:19190` adds a small control API on loopback, used by the demo page:

| Call | What it does |
| --- | --- |
| `GET /state` | Shows the name, address, mode and status |
| `PUT /state` | Changes `{"mode", "status", "stop_after"}` while the printer runs |
| `GET /tickets` | Lists the last 30 tickets, with a preview of the printed lines |
| `DELETE /tickets` | Clears them |

A *connect* timeout can't be faked on loopback, because the OS completes the
handshake. `printer/net` tests inject a dialer that never answers instead.

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

`-printer-control p-tandoor=http://127.0.0.1:19190,...` connects the demo
page (at `/`) to fake printers' control APIs. Loopback addresses only.

Serves on `127.0.0.1:8080` with a 25 s hold and a 60 s lease, and prints a
pairing code for restaurant `demo`. `-not-answering` and `-max-deliveries`
tune the server-side rules.

**Agent endpoints (provisional):**

| Call | Path | Auth |
| --- | --- | --- |
| Redeem pairing code | `POST /v1/pair` | single-use code |
| Report printers | `PUT /v1/printers` | bearer credential |
| Long poll for jobs (carries health) | `POST /v1/jobs/poll` | bearer credential |
| Report outcome | `POST /v1/jobs/{id}/outcome` | bearer credential |

What the mock already enforces:

- **Restaurant scope:** the restaurant comes from the credential. Requests with
  unknown fields are rejected, and another restaurant's job looks like a
  missing one (404).
- **Job delivery:** jobs go only to the device that registered the printer id.
  A lease that expires returns the job to the queue.
- **Repeat reports:** a second report for a job is a no-op. The first outcome
  is kept.
- **Pairing:** codes are single-use, expire after 10 minutes, and are limited
  to 5 failed attempts per minute per address.
- **Rotation:** the new credential arrives in a poll response. The old one
  works until the new one is first used.
- **Till not answering:** a silent till's queued jobs move to backups set with
  `PUT /mock/backups`. A live lease is never moved.
- **Poison jobs:** a job with 5 deliveries and no outcome is recorded as failed
  by the server.
- **Alerts:** moves, server-recorded failures and late duplicate reports are
  listed in `GET /mock/state` under `alerts`.

**Control endpoints** (mock only):

| Call | Body |
| --- | --- |
| `POST /mock/pairing-codes` | `{"restaurant_id":"demo"}` |
| `POST /mock/jobs` | `{"restaurant_id":"demo","jobs":[{"printer_id":"p1","slip":{...}}]}` |
| `GET /mock/state` | Shows devices, health, jobs, leases, outcomes and the active flags |
| `PUT /mock/hostile` | `{"flags":["repeat-id"]}`; an empty list clears them |
| `POST /mock/devices/{id}/revoke` | none |
| `POST /mock/devices/{id}/rotate` | none |
| `POST /mock/devices/{id}/silence` | `{"on": true}`: the till's requests fail as if its network were down, including a poll already held |
| `GET /mock/demo-config` | Lists the printers the page can control, plus the server's timings |
| `GET`, `PUT`, `DELETE /mock/printer-control/{id}/state` or `/tickets` | Passes the request through to that fake printer's control API |
| `PUT /mock/backups` | `{"backups":{"p-tandoor":"p-wok"}}`: printer to backup printer, across tills |

**Hostile flags** (`-hostile a,b`, or `PUT /mock/hostile`). Each ends in the
recorded outcome shown (`TestM3EveryHostileFlagEndsInARecordedOutcome`).

| Flag | What the server does | What the agent does | Recorded outcome |
| --- | --- | --- | --- |
| `ip-in-job` | Adds `printer_address` to each job | Refuses the job | `failed`, by the agent |
| `raw-bytes` | Adds `raw` printer bytes to each job | Refuses the job | `failed`, by the agent |
| `huge-payload` | Pads the poll response to about 50 MB | Stops reading at 1 MiB and backs off | `failed`, by the server after 5 deliveries |
| `drop-midway` | Sends half the response, then closes | Backs off and retries | `failed`, by the server after 5 deliveries |
| `short-lease` | Leases for 2 s, shorter than a slow print | Ignores the redelivery while printing; the ledger stops any reprint | `written`, one copy on paper |
| `repeat-id` | Sends each job twice in one response | Prints it once | `printed`, one copy on paper |

## Provisional: what changes when the missing documents arrive

`backend-notes.md`, `security.md` and the intern brief were not available.
The endpoint paths, JSON shapes, pairing-code format and error codes in
`api/` follow the design plan's illustrative table. `backend-notes.md` wins
on any difference.

Only `api/`, `source/cloud/`, `pair/redeem.go` and `internal/mockserver/`
know the wire format. Reconciling should not touch the queue, the renderer
or the printer writers.

The module path is `github.com/ShreyasBairyKS/Palate-Print-Agent`, matching
the GitHub repository.
