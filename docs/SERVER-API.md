# Palate print agent: what the server must provide

This is the contract between Palate's server and the print agent that runs on
each restaurant till. Build the server to it and the agent works unchanged.
`mockpalate.exe` (in this package) implements every rule here, so it is the
reference: when this document and the mock disagree, tell us, because one of
them is wrong.

Names and paths are proposals. If Palate's API conventions differ (paths,
field names, error codes), agree the change with us; only the agent's wire
layer changes, nothing else.

## Contents

1. [How it works](#1-how-it-works)
2. [Pairing and credentials](#2-pairing-and-credentials)
3. [The four endpoints](#3-the-four-endpoints)
4. [Jobs: kitchen tickets and bills](#4-jobs-kitchen-tickets-and-bills)
5. [Delivery rules the server must keep](#5-delivery-rules-the-server-must-keep)
6. [Errors](#6-errors)
7. [Health: what the portal can show](#7-health-what-the-portal-can-show)
8. [What the portal needs](#8-what-the-portal-needs)
9. [Security requirements](#9-security-requirements)
10. [Customer bills in HTML](#10-customer-bills-in-html)
11. [Trying it against the mock](#11-trying-it-against-the-mock)

## 1. How it works

- **The agent asks; the server never pushes.** The till opens no port. It
  makes outbound HTTPS calls only.
- **Four endpoints:** pair once, report printers, long-poll for jobs, report
  each job's outcome.
- **The server splits orders.** It decides which station cooks which dish and
  which printer each station uses, then sends one job per printer. The agent
  never sees the menu.
- **Jobs name printers by id.** Printer addresses live only on the till; the
  server never knows them and never sends one.
- **HTTPS only,** TLS 1.2 or newer, with a certificate from a public
  authority. The agent refuses plain http (except to its own machine, for
  the mock) and refuses redirects.
- **A path prefix is fine.** The paths below start at `/v1`. If Palate's
  API lives under a prefix (for example `/api/print-agent/v1/pair`), the
  till's server address is `https://host/api/print-agent`, and the agent adds
  `/v1/...` itself. An address that already ends in `/v1` is refused.
- **JSON in UTF-8.** Times are RFC 3339 (`2026-11-18T14:12:00Z`).
- **Limits:** the agent reads at most 4 MiB from any response, and at most
  1 MiB of HTML per document.

```
 Palate server                             Till (print agent)              Printers
 ─────────────                             ──────────────────              ────────
                 ◀── POST /v1/pair (code) ──    once, when installed
                 ── device_id, credential ──▶
                 ◀── PUT /v1/printers ───────   after pairing and on every start
                 ◀── POST /v1/jobs/poll ─────   always one waiting
 order confirmed ── jobs (at once) ─────────▶   prints ───────────────────▶ Tandoor, Pass…
                 ◀── POST /v1/jobs/{id}/outcome
```

## 2. Pairing and credentials

1. A **logged-in restaurant admin** creates a pairing code in the portal. It
   is single-use and expires after 10 minutes. The mock's codes look like
   `K7Q2-M9XD`; any short, typeable format works.
2. The installer runs `palate-agent pair -code K7Q2-M9XD` on the till. The
   agent sends the code once and gets back a **device id** and a **device
   credential**.
3. Every other call sends `Authorization: Bearer <credential>`.
4. **The restaurant comes from the credential,** never from the request. A
   credential can do exactly this: fetch its own restaurant's jobs for the
   printers its till registered, report their outcomes, register its
   printers, and send health.

**Rotation.** To replace a credential, put `new_credential` in a poll
response. The agent saves it before using it, then uses it on every call.
Keep the old one valid until the first call with the new one (or 7 days,
whichever comes first), so a crash mid-rotation never locks a till out.
Proposed schedule: every 30 days.

**Revocation.** When an admin revokes a till (stolen, reimaged, replaced),
answer its next call with `401` and code `repair_needed`. The agent stops
and logs that it must be paired again. A credential the server doesn't
recognise at all gets `401` with code `unauthorized`, with the same effect.

## 3. The four endpoints

### POST /v1/pair

No credential: the code is the proof.

```json
{"code": "K7Q2-M9XD", "agent_name": "FRONT-TILL"}
```

`agent_name` is the till's name (its Windows computer name by default), for
the portal.

| Answer | When |
| --- | --- |
| `200` `{"device_id": "dev-0001", "credential": "…"}` | The code was valid. Mark it used. |
| `401` `{"code": "invalid_code", …}` | Wrong, used or expired code. |
| `429` `{"code": "rate_limited", …}` | Too many wrong codes: the mock allows 5 failures per minute per address. |

The credential is an opaque string. Make it at least 256 random bits (the
mock uses 64 hex characters).

### PUT /v1/printers

Sent after pairing and every time the agent starts, including after a local
printer change. It **replaces** the till's printer list.

```json
{"printers": [
  {"id": "p-tandoor", "name": "Tandoor", "kind": "network", "width": 80},
  {"id": "p-pass",    "name": "Pass",    "kind": "usb",     "width": 58}
]}
```

| Field | Meaning |
| --- | --- |
| `id` | What jobs must name. Unique on the till; set at installation. |
| `name` | Shown in the portal and printed on BACKUP FOR banners. |
| `kind` | `network` or `usb`. For information only. |
| `width` | Paper width in mm: `58` or `80`. Use it to design bills for the roll. |

Answer `204`. A bad entry gets `400` with code `bad_printer`.

### POST /v1/jobs/poll

The agent always has one poll waiting. **Hold the request open until a job
for this till exists, then answer at once;** if none arrives, answer with no
jobs after about 25 seconds. The agent gives up on a poll after 40 seconds,
so never hold longer than 35.

The request carries the till's health (section 7):

```json
{"health": {"agent_version": "0.4.0-m4", "queue_depth": 0,
            "printers": [{"id": "p-tandoor", "state": "up"}, {"id": "p-pass", "state": "down", "last_error": "paper out"}]}}
```

The answer:

```json
{"jobs": [ …see section 4… ], "lease_seconds": 60}
```

| Field | Meaning |
| --- | --- |
| `jobs` | Jobs for printers this till registered. Empty list when the hold ran out. |
| `lease_seconds` | How long the delivered jobs are leased to this till (section 5). |
| `new_credential` | Optional: the replacement credential (section 2). |

The agent polls again immediately after each answer. On errors it waits
1 second, doubling to at most 10, with ±20% jitter, so a restaurant whose
internet returns is printing again within seconds.

### POST /v1/jobs/{id}/outcome

One report per job, after it is printed or has finally failed:

```json
{"kind": "fallback", "printer_id": "p-pass", "reason": "printed on backup Pass (p-pass): primary Tandoor (p-tandoor): hang: no progress for 10s"}
```

| `kind` | Meaning |
| --- | --- |
| `printed` | On paper, and the printer confirmed it. |
| `written` | Every byte was accepted, but this printer can't confirm (some USB and cheap printers). Probably on paper. |
| `fallback` | Printed on its backup printer, because its own printer was down. `printer_id` says which. |
| `failed` | Not printed. `reason` says why, in plain words. |
| `screen_only` | Reserved for a kitchen screen with no printer. |

| Answer | When |
| --- | --- |
| `204` | Recorded. **A second report for the same job is also `204` and changes nothing:** keep the first. |
| `404` `{"code": "not_found", …}` | Not a job of this restaurant. Another restaurant's job must look exactly like a missing one. |
| `400` `{"code": "bad_outcome", …}` | Unknown `kind`. |

The agent retries a report up to 5 times. If it never gets through, the
lease expires, the job comes back, and the agent reports the stored outcome
**without printing it again**.

## 4. Jobs: kitchen tickets and bills

A job carries **either** a `slip` (a kitchen ticket) **or** a `document` (a
finished document such as the customer bill), never both.

```json
{
  "id": "job-1042-tandoor",
  "printer_id": "p-tandoor",
  "created_at": "2026-11-18T14:12:00Z",
  "slip": {
    "station": "Tandoor",
    "table": "T4",
    "order_number": "1042",
    "placed_at": "2026-11-18T14:11:52Z",
    "items": [
      {"quantity": 2, "name": "Paneer Tikka", "notes": "less spicy"},
      {"quantity": 4, "name": "ಬೆಣ್ಣೆ ನಾನ್"}
    ],
    "notes": "allergy: peanuts",
    "extras": [{"label": "Course", "value": "2"}, {"label": "Guests", "value": "3"}]
  }
}
```

| Job field | Meaning |
| --- | --- |
| `id` | Unique, forever. The agent's duplicate protection is keyed on it: the same id is never printed twice. |
| `printer_id` | One of the ids this till registered. |
| `created_at` | When the order was confirmed. Jobs older than 10 minutes print with a DELAYED banner. |
| `slip` / `document` | What to print. |

| Slip field | Required | Meaning |
| --- | --- | --- |
| `station` | yes | Printed large at the top. |
| `order_number` | yes | Printed as `#1042`. |
| `table` | no | Printed as `Table T4`; empty means `Takeaway`. |
| `placed_at` | yes | Order time, printed in the till's time zone. |
| `items` | yes, at least one | `quantity` (1 or more), `name`, optional `notes`. Names can be English, Kannada, Hindi, Tamil or accented Latin. |
| `notes` | no | An order-level note, printed in bold. |
| `extras` | no | Up to 10 labelled lines. **New information for the ticket goes here,** so agents already installed print it without an update. |
| `duplicate` | no | `true` prints a DUPLICATE banner (for reprints the server asks for). |
| `moved_from` | no | Set by the server when it moves a job (section 5): prints MOVED FROM banner. |

A document job:

```json
{
  "id": "job-1042-bill",
  "printer_id": "p-pass",
  "created_at": "2026-11-18T14:40:00Z",
  "document": {"type": "html", "html": "<!doctype html><html>…</html>"}
}
```

| Document field | Meaning |
| --- | --- |
| `type` | `html`, the only type today. |
| `html` | A self-contained page, at most 1 MiB. See section 10. |
| `duplicate`, `moved_from` | As on a slip. |

**Splitting an order.** For an order with dishes for Tandoor and Wok and a
bill for the customer: three jobs, each with its own id and `printer_id`.
Jobs for one printer print in the order the server sent them.

**What the agent refuses.** A job is refused, reported `failed` with the
reason, and never printed, if it has:

- a field that tries to steer printing, at any depth and in any letter case:
  `printer_address`, `printer_ip`, `printer_host`, `printer_port`, `ip`,
  `ip_address`, `host`, `hostname`, `port`, `raw`, `raw_bytes`, `escpos`,
  `esc_pos`, `printer_commands`;
- a `printer_id` that looks like an address;
- both a slip and a document, a document type other than `html`, empty HTML,
  or more than 1 MiB of HTML;
- a printer id this till doesn't have (reported `failed`, "not configured").

**Fields the agent doesn't know are ignored** at every level (and logged
once), so the server can add fields without breaking installed agents.

## 5. Delivery rules the server must keep

The agent prevents duplicates on its own till. These rules, on the server,
complete the picture:

1. **Lease on delivery.** When a poll delivers a job, lease it to that till
   for `lease_seconds` (60). While the lease is live, don't deliver it again.
2. **Redeliver after the lease.** A job with no outcome when its lease runs
   out goes back in the queue. The agent handles a redelivery correctly:
   printed jobs are reported again, not reprinted.
3. **Keep the first outcome.** Repeated reports are `204` and change nothing.
4. **Poison jobs.** A job delivered 5 times with no outcome is recorded as
   `failed` by the server ("no outcome after 5 deliveries") and not sent
   again.
5. **Till not answering.** A till that hasn't polled for 60 seconds counts as
   down. Move its queued (not leased) jobs to each printer's backup on
   another till, if the restaurant set one, with `moved_from` set to the
   original printer's name, so the paper says "MOVED FROM TANDOOR, check for
   duplicate". **Never move a job whose lease is still live.**
6. **Late reports.** If the first till comes back and reports a job that was
   already moved, record both outcomes and alert the floor: there may be two
   copies.

## 6. Errors

Every non-2xx answer has this body:

```json
{"code": "repair_needed", "message": "credential revoked; re-pair this till"}
```

| Status and code | The agent |
| --- | --- |
| `401 repair_needed` | Stops; logs that the till must be paired again. |
| `401 unauthorized` | The same. |
| `404 not_found` (on an outcome) | Retries the report up to 5 times, then logs it as unreported. |
| `404` without a `not_found` body, on any call | Takes it as a wrong server address (a missing path prefix, or `/v1` twice), says so in the log and retries with backoff. |
| `429`, `5xx`, timeouts, dropped connections | Waits 1 s doubling to 10 s, with jitter, and tries again. |
| Any redirect (`3xx`) | Refuses it; treated as an error, retried with backoff. |
| A response over 4 MiB | Refuses it; retried with backoff. |

## 7. Health: what the portal can show

Every poll carries:

| Field | Meaning |
| --- | --- |
| `agent_version` | e.g. `0.4.0-m4`. |
| `queue_depth` | Tickets on the till waiting to print. |
| `printers[]` | Each printer's `id`, `state` (`up`, `down`, `unknown`) and `last_error`. |
| `last_error` | The agent's last error code, if any. |
| `watchdog_restarts` | Times the agent had to restart a stuck poll. |
| `bad_version` | An update that failed and was rolled back (for later). |

A network printer's `state` stays current between tickets: the agent checks
idle printers once a minute (a connection and a status question, never
printing), so a printer switched off or out of paper shows as `down` before
the next order. USB printers stay `unknown` until they print.

Health never contains ticket contents or customer data. With it the portal
can show, per restaurant: each till online or not (last poll time), each
printer up or down and why ("paper out"), and tickets waiting.

## 8. What the portal needs

For a restaurant admin:

1. **Pairing:** "Add a till" shows a pairing code (section 2).
2. **Tills:** each till's name, last seen, agent version, its printers and
   their state; a **Revoke** button.
3. **Station mapping:** tag each dish with a station; link each station, and
   the customer bill, to one of the printers the tills reported.
4. **Backups across tills (optional):** for a printer, a backup printer on
   another till, used by rule 5.
5. **Job log:** recent jobs with their outcomes and reasons; alerts for moved
   jobs, server-recorded failures and late duplicate reports.

Printer details (addresses, paper width, backups on the same till) are set
on the till, with `palate-agent printer set …`. The portal doesn't edit
them; it sees the result in `PUT /v1/printers`.

## 9. Security requirements

The agent assumes the till may be compromised and limits what a stolen
credential can do. The server must hold up its half:

- Credentials: at least 256 random bits; **store only a hash**; compare in
  constant time; never log them.
- **Restaurant from the credential,** never from the request; another
  restaurant's job looks missing (`404`).
- Deliver a job **only to the till that registered its printer id.**
- Pairing codes: created only by a logged-in restaurant admin; single use;
  10-minute expiry; rate-limited per address and per account.
- Rotation (every 30 days proposed) with the overlap rule; revocation with an
  audit trail.
- **Two tills using one credential** (overlapping polls from different
  addresses): alert and revoke.
- Rate-limit polls and outcome reports per device.
- HTTPS, TLS 1.2+, no redirects on these endpoints.
- **Never put a printer address or raw printer bytes in a job.** The agent
  refuses them anyway.
- Ticket contents: only what the kitchen needs (no customer phone numbers or
  addresses on kitchen tickets; India's DPDP Act 2023).

## 10. Customer bills in HTML

The agent prints the HTML as it is, as a picture. Palate owns the design.

How the agent draws it:

1. It removes scripts, frames, `<object>`, `<embed>`, `<base>`, `<link>`,
   every `<meta http-equiv>` and every `on…` attribute, and adds a policy
   that blocks scripts, network and file access.
2. Hidden Microsoft Edge (on every Windows 10 and 11 till) lays the page out
   **exactly 576 px wide for 80 mm paper, 384 px for 58 mm**, and the result
   is printed in black and white, then cut.

What the HTML must be:

- **Self-contained:** CSS in `<style>`; images as `data:` URIs or inline
  `<svg>`. Anything loaded from a URL is blocked and simply missing.
- **No scripts.** They don't run.
- **Full width,** with no fixed width on the body. Anything wider than the
  paper is cut off on the right.
- **One design for both rolls:** `<html>` carries the class `paper-80` or
  `paper-58` (and `data-paper-dots="576"` or `"384"`), so the narrow roll can
  have its own rules, for example `html.paper-58 body { font-size: 15px }`.
  Width media queries don't see the paper; use these classes. Or send a
  design per width: the server knows each printer's width.
- **Black on white,** no page breaks, at most 150 cm long. Grey becomes black
  or white; photos print poorly.
- **Fonts:** Windows' own fonts cover English, Kannada, Hindi and Tamil
  (`"Nirmala UI"`). Others can be embedded as `data:` fonts.

`examples/bill.html` is a complete sample;
`examples/bill-80mm-printed.png` and `bill-58mm-printed.png` show how it
printed.

## 11. Trying it against the mock

`mockpalate.exe` serves this API on your own machine, plus a few extra
`/mock/…` endpoints that stand in for the portal. `start-demo.ps1` runs the
whole thing (mock, two tills, three on-screen printers) and opens the demo
page; see `README-FIRST.md`.

To run just the mock:

```powershell
.\mockpalate.exe -addr 127.0.0.1:8080 -seed demo
```

It prints a pairing code for the restaurant `demo`. Useful calls:

| Call | What it does |
| --- | --- |
| `POST /mock/pairing-codes` `{"restaurant_id":"demo"}` | A new pairing code (the portal's "Add a till"). |
| `POST /mock/jobs` `{"restaurant_id":"demo","jobs":[{"printer_id":"p-tandoor","slip":{…}}]}` | Confirms an order: queues jobs exactly as the server would. A job may carry `"document": {"type":"html","html":"…"}` instead. |
| `GET /mock/state` | Everything the server knows: tills, health, jobs, leases, outcomes, alerts. |
| `PUT /mock/backups` `{"backups":{"p-tandoor":"p-wok"}}` | Backup printers across tills (rule 5). |
| `POST /mock/devices/{id}/revoke` | Revokes a till. |
| `POST /mock/devices/{id}/rotate` | Rotates a till's credential on its next poll. |
| `GET /mock/sample-bill` | The sample bill HTML. |
| `GET /` | The demo page. |

The mock keeps everything in memory, so restarting it means pairing the
tills again. Its `/mock/…` endpoints have no login and listen on your own
machine only; they are for trying things out, not part of the contract.
