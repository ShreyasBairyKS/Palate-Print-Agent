# palate-print-agent

The Palate print agent: a Go service on the restaurant's Windows till that
polls Palate's cloud for print jobs and writes each station's ticket to its
printer, the moment an order is confirmed.

**Status: M4 in progress: the Windows service is done; signed updates are next.**

- **Windows service:** installs under its own low-privilege account, starts
  with Windows, restarts after a failure, and stops cleanly: tickets in hand
  finish before it exits (`agent.TestStopLetsTheTicketInHandFinish`,
  `service.TestStopFinishesTheAgentCleanly`). `scripts/service-smoke.sh`
  checks it end to end on a real PC, as administrator.

- **Hostile server:** every hostile mock flag ends in a correct recorded
  outcome (`TestM3EveryHostileFlagEndsInARecordedOutcome`).
- **Hanging printer:** its ticket reaches the backup within 15 s: 3.0 s for
  a connect hang (`TestM3HangingPrinterReachesBackupWithin15s`), 10.0 s for
  a full write stall (`queue.TestHangingPrinterReachesBackupWithin15s`).
- **Silent till:** when a till stops answering, its jobs print on another
  till with a MOVED FROM banner (`TestM3SilentTillsJobsPrintOnAnotherTill`).
- **Internet outages:** the line goes quiet twice and comes back; the agent
  reconnects on its own, prints everything ordered meanwhile and nothing
  twice (`TestInternetGoesQuietTwiceAndComesBack`; set
  `PALATE_REALTIME_OUTAGE=1` for two real 90-second outages).
- **Newer server:** fields this agent version has never seen are ignored and
  the ticket prints (`TestNewerServerFieldsAreIgnored`, and the
  `newer-server` mock flag).
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
comes back reconnects on its next retry, within about 12 s.

To print Tandoor's tickets on a **real printer** (the rest stay simulated):

```bash
REAL_PRINTER=192.168.1.50 bash scripts/demo-live.sh
```

```bash
REAL_USB_PRINTER="EPSON TM-T82 Receipt" REAL_WIDTH=58 bash scripts/demo-live.sh
```

`REAL_USB_PRINTER` takes the printer's Windows name, as `palate-agent
printers` lists it. `REAL_WIDTH` is the paper width in mm (58 or 80; default
80).

## Layout

| Path | Track | What it is |
| --- | --- | --- |
| `job/` | seam | Shared types: `Job`, `Slip`, `Width`, `Outcome` |
| `source/` | seam | The `JobSource` interface |
| `render/` | paper | `Render(slip, width)` at 58/80 mm: text in WPC1252, or raster for Kannada, Devanagari and Tamil; golden tests and PNG previews in `testdata/` |
| `render/fonts/` | paper | Bundled Noto fonts (OFL), embedded in the binary |
| `render/picture.go` | paper | `Picture(banners, image, width)`: a document already drawn as an image, printed as raster bands with the same banners as a ticket |
| `htmlrender/` | paper | Draws an HTML document (a customer bill) at the paper's width with headless Microsoft Edge, locked down: no scripts, network or files |
| `ledger/` | wire | The printed-ID ledger: Attempting/Done per job, DPAPI-protected, append-only, 7-day retention |
| `source/cloud/` | wire | Long-poll Fetch, Report, printer registration, credential rotation, refusing hostile jobs |
| `pair/` | wire | Redeem a pairing code; the credential is stored with DPAPI at machine scope |
| `internal/dpapi/` | wire | DPAPI at machine scope through crypt32.dll |
| `internal/fileacl/` | wire | Locks the credential and ledger files to SYSTEM, Administrators and the agent's account (DPAPI at machine scope alone would let any account on the till decrypt them) |
| `config/` | wire | The till's local config: server, printers and addresses, timings |
| `printer/` | wire | The `Writer` interface and failure kinds (refused, hang, partial, paper out) |
| `printer/net/` | wire | Raw TCP 9100 writer with a DLE EOT status check |
| `printer/usb/` | wire | Windows spooler writer, datatype RAW |
| `queue/` | wire | One serial queue per printer: ledger, render, write, retry fast failures, failure ladder to the backup, probing down printers, report |
| `agent/` | wire | The poll loop, watchdog, health payload, and backoff with jitter |
| `api/` | wire | Wire format and endpoint paths (**provisional**, see below) |
| `cmd/palate-agent/` | | The agent: `printers`, `pair`, `test-print`, `run`, `install`, `status`, `start`, `stop`, `uninstall`, `version` |
| `service/` | | The Windows service: start, clean stop, install with restart-on-failure, status |
| `internal/logfile/` | | The service's log file, rotated by size |
| `internal/escpos/`, `internal/fakeprinter/`, `internal/mockserver/` | rig | Decoder and ticket preview, fake printer with a control API, mock server |
| `internal/mockserver/demo/` | rig | The live demo page, served by the mock server |
| `cmd/escpos-dump/`, `cmd/fakeprinter/`, `cmd/mockpalate/` | rig | Command-line wrappers |
| `arch_test.go` | seam | `render`, `htmlrender`, `job`, `ledger`, `internal/dpapi` may not import `net`; `queue`, `printer/*`, `config` may not import `net/http` |
| `e2e_test.go` | | The real agent against the mock server and fake printers |
| `crash_test.go` | | Builds `palate-agent`, kills it mid-write, restarts it |
| `m3_test.go` | | Hostile flags, a hanging printer to its backup, a silent till's jobs on another till |
| `outage_test.go` | | The internet goes quiet twice and comes back |
| `scripts/demo-live.sh` | | **Live demo in the browser**: two tills, three printers, break things on purpose |
| `scripts/demo.sh` | | Scripted demo: pair, test print, run, an order with a Kannada dish prints |
| `scripts/demo-protocol.sh` | | M0 walkthrough of the protocol with curl |
| `scripts/service-smoke.sh` | | As administrator: installs the real service against the mock, checks printing, stop/start, restart after a kill and the folder's access, then removes it all |
| `scripts/build-release.sh` | | The release build for a till, with size and SHA-256 |
| `examples/bill.html` | | A sample customer bill in HTML that fits both 80 mm and 58 mm paper; `bill-80mm-printed.png` and `bill-58mm-printed.png` are how it printed |

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

The release build for a till (debugging information stripped, about 11 MB,
about 5 MB compressed) goes to `dist/`, with its size and SHA-256:

```bash
bash scripts/build-release.sh
```

To fuzz the renderer or the job reader (both run their seed inputs as part of
`go test`):

```bash
go test ./render -run '^$' -fuzz FuzzRender -fuzztime 60s
```

```bash
go test ./source/cloud -run '^$' -fuzz FuzzParseJob -fuzztime 60s
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
| `golang.org/x/net` (`html` only) | BSD-3-Clause | Parsing HTML documents to strip scripts, frames and links before drawing |
| `golang.org/x/sys` | BSD-3-Clause | The Windows service |

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
| `palate-agent printer list` / `set` / `add` / `remove` | As administrator: changes this till's printers without editing the file or contacting Palate (see below) |
| `palate-agent test-print -printer p-tandoor` | Prints a test slip (bold, accented letters, a Kannada line and the cut, so one print checks what a real printer can get wrong) and says whether the printer confirmed it |
| `palate-agent run` | Polls and prints in the foreground until Ctrl-C (tickets in hand finish first), or until the credential is revoked |
| `palate-agent install` | As administrator: installs the Windows service and starts it if the till is paired (see below) |
| `palate-agent status` | Whether the service is installed and running, its account, and its last log lines; needs no administrator rights |
| `palate-agent start` / `stop` | As administrator. Stop lets the tickets in hand finish (up to 10 s) |
| `palate-agent uninstall` | As administrator: stops and removes the service and the program; keeps the data folder |

### Changing printers on the till

A restaurant can change what only the till knows, without Palate's server
and without editing `agent.json` by hand. From a terminal opened with "Run
as administrator":

```bash
palate-agent printer list
```

```bash
palate-agent printer set p-tandoor -address 192.168.1.60
```

```bash
palate-agent printer set p-tandoor -backup p-pass
```

```bash
palate-agent printer add p-grill -name Grill -address 192.168.1.70 -width 80
```

```bash
palate-agent printer remove p-grill
```

| Allowed here | Flag |
| --- | --- |
| Where a printer is: a new IP address, a replacement printer, network to USB or back | `-address IP[:port]`, `-usb "Windows printer name"` |
| Paper width | `-width 58` or `80` |
| The name printed on BACKUP FOR banners | `-name` |
| The backup printer on this till | `-backup ID`, or `-backup none` |
| Adding or removing a printer | `printer add`, `printer remove` |

Each change is:

1. **Checked** by the same rules as the file: private IPs only, no
   hostnames, width 58 or 80, a backup that exists on this till. Removing
   the last printer, or one another printer uses as its backup, is refused.
2. **Test-printed** on the printer it changes, before anything is saved. If
   the test fails, nothing changes. `-no-test` saves anyway, for a printer
   that is off right now.
3. **Saved in one step,** with the old file kept as `agent.json.previous`
   (copy it back to undo), and written to `logs\config-changes.log` with
   the time and the Windows user.
4. **Applied** by restarting the service, which sends Palate the new printer
   list (ids, names, kinds and widths). `-no-restart` leaves that for later.

Not allowed here, because Palate or pairing owns them:

- **Which dishes a station cooks, and which printer a station or the bill
  prints on.** Those links live in Palate. A new printer gets no jobs until
  Palate links something to it; a removed printer's jobs are recorded as
  failed until Palate is changed too.
- **A printer's id.** Palate's jobs name it, so it can't be renamed; add a
  new printer and remove the old one instead.
- **The server address and the credential.** Moving or replacing a till
  means pairing again with a code from Palate.

### The Windows service

`palate-agent install` (from a terminal opened with "Run as administrator"):

- **Program:** copies itself to `C:\Program Files\PalatePrintAgent\palate-agent.exe`,
  because a service account can't read a user's folders (Desktop, Downloads,
  OneDrive). For the same reason the config, credential and ledger must not
  be in a user's folder; install refuses if they are.
- **Account:** `NT SERVICE\PalatePrintAgent`, a virtual account Windows makes
  for this service alone: no password, few rights. `-account localsystem` is
  the fallback for a USB printer the virtual account can't see.
- **Data folder** (the config's folder, `C:\ProgramData\PalatePrintAgent` by
  default): closed to everyone but SYSTEM, Administrators and the service.
  The credential and ledger files carry their own access list with the same
  accounts plus whoever ran `pair`. After install, run `pair` and
  `test-print` as administrator.
- **Starts** with Windows. **Restarts** after a crash or an error exit: after
  5 s, then 30 s, then 60 s, with the count reset after a day.
- **Stops cleanly:** polling stops at once; tickets already on the till get up
  to 10 s to finish (Windows allows 20 s). Anything unfinished comes back
  after its lease.
- **Log:** `logs\agent.log` in the data folder, rotated at 5 MB, keeping three
  old files. `palate-agent status` shows the last lines.

Setting up a till:

```bash
palate-agent install -start=false
```

```bash
palate-agent pair -code K7Q2-M9XD
```

```bash
palate-agent test-print -printer p-tandoor
```

```bash
palate-agent start
```

Config rules:

- **Printer addresses:** IP literals only, in 10/8, 172.16/12 or 192.168/16,
  with port 9100 by default. Hostnames and public IPs are refused. Loopback is
  also accepted, for the rig.
- **USB printers:** named by their Windows printer name (`printer_name`),
  never an address. The service runs as its own account, which sees only
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
2. Fields the agent doesn't know are ignored, at every level, so a newer
   server can add fields without breaking agents already installed; each is
   logged once. Information the server wants printed goes in `slip.extras`
   (labelled lines such as "Course: 2", up to 10), which this version already
   prints. Jobs carrying a field that tries to steer printing (a printer
   address, host, port or raw printer bytes, at any depth), or a printer id
   that is an address, are refused and reported `failed`. They never reach a
   printer. A job repeated in one response, or delivered again while it is
   still queued, prints once.
3. The ledger is checked:

   | Ledger says | What happens |
   | --- | --- |
   | Done, printed or written | The job is not printed; its stored outcome is reported again |
   | Done, failed | Its failure never reached the server, and the printer may be back (the router was off, say), so it is tried again. If the failed attempt got bytes to the printer, the retry is marked DUPLICATE ("earlier attempt partly printed") |
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
   outcome reported, with retries, in the background: the printer's next
   ticket never waits on the network. If the report is lost, the lease
   expires, the job comes back, and step 3 reports the stored outcome without
   printing. If it comes back while the first report is still being retried,
   the stored outcome is reported again at once.

### Documents: customer bills in HTML

A job can carry a finished document instead of a kitchen slip. Palate owns
the whole design; the agent prints it as it is, on the printer the job
names, with the same ledger, backups, banners and outage recovery as a
ticket.

```json
{"id": "job-2041", "printer_id": "p-bill", "created_at": "2026-11-18T14:12:00Z",
 "document": {"type": "html", "html": "<!doctype html><html>...</html>"}}
```

A job carries a slip or a document, never both. `html` is the only type, up
to 1 MiB; a poll response may be up to 4 MiB.

How it prints:

1. The agent parses the HTML and removes everything that could run code,
   load something or move to another page: `<script>`, frames, `<object>`,
   `<embed>`, `<base>`, `<link>`, every `<meta http-equiv>` (refresh
   included) and every `on…` attribute. Its own Content-Security-Policy goes
   first in `<head>`: no scripts, no network, no files; only inline CSS and
   `data:` images and fonts.
2. Headless Microsoft Edge (already on Windows 10 and 11) draws the page,
   pinned to 576 dots wide for 80 mm paper or 384 for 58 mm (Edge's window
   is wider, because Edge won't open one narrower than about 520 px; the
   picture is cropped to the paper), with a fresh throwaway
   profile, no extensions, and a proxy that goes nowhere, so nothing can
   leave the till even if the policy missed it. Edge listens on no port; it
   is handed a file and writes a screenshot back.
3. The picture is trimmed below its last ink, turned to black and white
   (darker than mid-grey prints), and sent as raster bands of 256 rows,
   after any banners (DUPLICATE, MOVED FROM, BACKUP FOR, DELAYED). Then the
   paper is cut.

What Palate's HTML should be:

- **Self-contained:** CSS in `<style>`, images as `data:` URIs or inline
  `<svg>`. Anything fetched from a URL is blocked and simply missing.
- **No scripts.** They don't run.
- **Sized for the paper:** the page is laid out exactly 576 px wide for
  80 mm and 384 px for 58 mm, and anything wider is cut off on the right.
  Use the full width (no fixed `width` on the body). `<html>` gets the class
  `paper-80` or `paper-58` (and `data-paper-dots`), so the narrow roll can
  have its own rules, such as `html.paper-58 body { font-size: 15px }`, as
  the sample does. Width media queries don't work: Edge's window is wider
  than the paper. Palate also knows each printer's width from
  `PUT /v1/printers`, so it can send a design per width instead.
- **Black on white**, with no page breaks: one long strip, up to 150 cm.
  Grey prints as black or white; photos print poorly.
- **Fonts:** Windows' own fonts cover Latin, Kannada, Hindi and Tamil
  (Nirmala UI). Others can be embedded as `data:` fonts.

`examples/bill.html` is a sample. Drawing takes about a second per
document on this machine (Edge starts fresh each time); documents are drawn
one at a time. `edge_path` in the config points at Edge if it isn't in its
usual place; `html_work_dir` (default `html-work` beside the config) holds
each document's scratch folder until it is printed.

### When something gets stuck

- **The internet drops:** failed attempts are retried after 1, 2, 4, 8, then
  every 10 s (±20% jitter), so the agent is back within about 12 s of the
  line returning. A connection attempt gives up after 10 s, and TCP
  keepalives notice within about 20 s that a held poll's connection has
  died. Jobs ordered meanwhile wait on the server and print on reconnection;
  a till quiet for over 60 s has its jobs moved to backups on another till,
  if there is one (below).
- **The poll loop:** the watchdog abandons a poll that outlives 55 s (the
  hold time plus 30 s) and starts a fresh one. The restart count rides in
  the health payload as `watchdog_restarts`.
- **A till stops answering:** this is the server's rule; the mock implements
  it. No poll for `2 x hold + 10 s` (60 s) and the till counts as not
  answering.
  - Its queued jobs move to their printers' backups on an answering till,
    and print there with "MOVED FROM TANDOOR / check for duplicate".
  - A job whose lease is still live is never moved.
  - With no reachable backup, the job waits for its own till, with an alert,
    and prints when the till comes back (DELAYED if over 10 minutes old). So
    a short outage at a one-till restaurant loses nothing. Only a job still
    waiting after 30 minutes is recorded as failed.
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
    back to the server, to be added to the backend API.
  - USB spooler job status: the spooler accepts jobs for an unplugged printer.
  - Small tickets to a stalled network printer show as `written`, because
    socket buffers accept them. A big one shows as `partial`.
- **M4:**
  - Signed updates with rollback, and the signed installer.
  - Raster speed and legibility on real printers.
- **Owner to confirm:**
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
| `POST /mock/jobs` | `{"restaurant_id":"demo","jobs":[{"printer_id":"p1","slip":{...}}]}`, or a document: `{"printer_id":"p1","document":{"type":"html","html":"..."}}` |
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
| `newer-server` | Adds fields this agent has never seen, at every level | Ignores them | `printed`, one copy on paper |

## Provisional: what changes when the backend API is specified

The endpoint paths, JSON shapes, pairing-code format and error codes in
`api/` are a working design. Once the backend API is specified, it wins on
any difference.

Only `api/`, `source/cloud/`, `pair/redeem.go` and `internal/mockserver/`
know the wire format. Reconciling should not touch the queue, the renderer
or the printer writers.

The module path is `github.com/ShreyasBairyKS/Palate-Print-Agent`, matching
the GitHub repository.
