# Setting up the print agent on a till

For the person installing the agent on a restaurant's Windows till and its
printers. It takes about 15 minutes per till.

## What you need

- **The till:** Windows 10 or 11, 64-bit, with Microsoft Edge (every Windows
  10 and 11 has it; it draws customer bills). An administrator account.
- **Printers:** ESC/POS thermal printers (Epson TM, and most 58 mm and 80 mm
  kitchen printers).
  - **Network printers:** connected to the same network as the till,
    port 9100, with a **fixed IP address** (set on the printer, or reserved
    on the router), so it doesn't change after a restart. Setup finds them
    by itself; if it can't, print the printer's self-test page to see its
    address.
  - **USB printers:** installed in Windows **for all users** (Settings →
    Bluetooth & devices → Printers & scanners), so the service can see
    them.
- **Palate's server address** (`https://…`) and a **pairing code** from the
  Palate portal ("Add a till"). Codes expire after 10 minutes, so get it
  when you reach step 5.

## The quick way: double-click

1. Unblock and extract the zip (step 1 below).
2. **Double-click `palate-agent.exe`.** Windows asks to allow it to make
   changes: choose **Yes**. The **Palate print agent setup** window opens.
3. Fill it in, top to bottom:
   - **1 · Palate's server address** (`https://…`), exactly as Palate gave
     it, including any path such as `/api/print-agent`, but **without
     `/v1`** on the end: the agent adds that itself.
   - **2 · Printers:** when the window opens it looks for receipt
     printers on the till's network by itself (a few seconds) and says what
     it found in **Progress**. Press **Find printers…** to see them. If you
     can't tell which is which, press **Print address slips**: each printer
     prints its own address. Choose one and press **Add selected…**; its
     address is filled in. Give it the name the kitchen uses (Tandoor,
     Pass, Bill…); the id fills itself in. A printer the search didn't find
     can be added with **Add printer…**: choose **Network printer** and type
     its IP address, or **USB printer** and pick it from the list (Windows'
     PDF, XPS, OneNote and fax "printers" are left out). Pick the paper
     width and, optionally, a backup printer. A **test slip prints** as soon
     as you add a printer; the list shows ✓ or ✗. **Edit…**, **Remove** and
     **Test print** work on the selected printer.
   - **3 · Pairing code** from the Palate portal ("Add a till"). You can
     leave it empty and pair later.
4. Press **Install and start**. The **Progress** box shows each step:
   saving, installing the service, pairing, starting. When it says
   **Running**, close the window.

Double-clicking again later opens the **status window** (Palate printers),
which needs no administrator rights, so restaurant staff can open it too:

- each printer, **working** or **NOT WORKING** and why (out of paper, can't
  connect to it, not answering), and since when;
- whether the till is connected to Palate, and tickets waiting;
- what to do ("Put a new paper roll in Pass").

It refreshes every 2 seconds and flashes in the taskbar when a printer goes
down, so it can stay open, minimised, all day. The agent checks idle printers
once a minute, so a printer switched off between orders shows up before the
next ticket. USB printers show their state once they print.

**Change setup...** in that window opens the setup window (Windows asks for
administrator rights): its printers can be tested or changed there (each
change is test-printed, saved and applied by restarting the service), its
list shows each printer's live state, and an agent that isn't paired yet can
be paired. The steps below do the same thing one command at
a time, for scripted installs or when something needs fixing; `palate-agent
setup` asks the same questions in the console.

## 1. Unpack and unblock

The program isn't code-signed yet, so Windows may warn about it.

1. Right-click the zip → **Properties** → tick **Unblock** → OK. Then extract
   it, for example to `C:\PalatePrintAgent-setup`.
2. Open **PowerShell as administrator** (Start → type PowerShell →
   right-click → Run as administrator) and go to the folder:

```powershell
cd C:\PalatePrintAgent-setup
```

If Windows Defender SmartScreen says "Windows protected your PC" when you
run it, choose **More info → Run anyway**. (The signed release won't need
this.)

`palate-agent.exe` is also a command-line program: run in PowerShell with a
command, it does that one thing; double-clicked, it opens the setup window
above.

## 2. Find the printers

```powershell
.\palate-agent.exe printers
```

lists the USB printers Windows can see, by the exact names to use below.

```powershell
.\palate-agent.exe printer find
```

looks for network receipt printers on the till's network and lists their
addresses. Add `-identify` to make each one print its own address, so you
can tell which printer is which.

## 3. Create the config with the first printer

Give each printer a short **id** that stays the same for good (Palate's jobs
name it), a **name** for the kitchen, and its paper **width**.

```powershell
.\palate-agent.exe init p-tandoor -server https://SERVER -name Tandoor -address 192.168.1.50 -width 80
```

For a USB printer, use `-usb "EPSON TM-T82 Receipt"` instead of `-address`.
A **test slip prints**; check it's readable (bold station name, accented
letters, a Kannada line) and that the paper is cut. If the test print fails,
nothing is saved: check the printer is on, has paper and is at that address.

## 4. Add the other printers

```powershell
.\palate-agent.exe printer add p-wok -name Wok -address 192.168.1.51 -width 80
```

```powershell
.\palate-agent.exe printer add p-pass -name Pass -usb "EPSON TM-T82 Receipt" -width 80
```

Optionally give a printer a **backup on this till**, used automatically when
it's off, jammed or out of paper:

```powershell
.\palate-agent.exe printer set p-tandoor -backup p-pass
```

Check the list:

```powershell
.\palate-agent.exe printer list
```

## 5. Install, pair and start

```powershell
.\palate-agent.exe install -start=false
```

installs the Windows service: the program goes to
`C:\Program Files\PalatePrintAgent`, it runs under its own account, starts
with Windows and restarts itself after a crash. Then, with a fresh code from
the portal:

```powershell
.\palate-agent.exe pair -code K7Q2-M9XD
```

```powershell
.\palate-agent.exe start
```

```powershell
.\palate-agent.exe status
```

`status` should say **running**, with log lines ending in "printers
registered".

## 6. Check with a real order

1. In the portal, link each station (and the bill) to one of this till's
   printers.
2. Place a test order with dishes for two stations. Each station's ticket
   should print on its printer within a second or two.
3. Print a bill. It should fill the paper's width, with nothing cut off on
   the right.
4. Switch one printer off and place another order: its ticket should print
   on its backup with a "BACKUP FOR …" banner.
5. Unplug the till's network cable for a minute, place an order, plug it
   back in: the ticket prints within about 12 seconds of the network
   returning.

## Day to day

| Task | Command (PowerShell as administrator) |
| --- | --- |
| Is it running? Are the printers working? | Double-click `palate-agent.exe` (status window), or `palate-agent status` |
| A printer got a new IP address | `palate-agent printer set p-tandoor -address 192.168.1.60` |
| A printer was replaced | `printer set` with its new address or `-usb` name; same id |
| Change paper width | `palate-agent printer set p-pass -width 58` |
| Test a printer | `palate-agent test-print -printer p-tandoor` |
| Restart | `palate-agent stop`, then `palate-agent start` |
| Remove the agent | `palate-agent uninstall` (keeps `C:\ProgramData\PalatePrintAgent`) |

After install, `palate-agent` is in `C:\Program Files\PalatePrintAgent`; run
it from there (or from the setup folder).

Every printer change is test-printed first, saved with the old config kept
as `agent.json.previous`, written to `logs\config-changes.log`, and applied
by restarting the service.

Which station prints on which printer, and what each station cooks, are set
in the Palate portal, not on the till.

## Where things are

| What | Where |
| --- | --- |
| Program | `C:\Program Files\PalatePrintAgent\palate-agent.exe` |
| Config | `C:\ProgramData\PalatePrintAgent\agent.json` |
| Credential (encrypted) | `C:\ProgramData\PalatePrintAgent\credential.bin` |
| Printed-job ledger (encrypted, job ids only) | `C:\ProgramData\PalatePrintAgent\ledger.bin` |
| Log | `C:\ProgramData\PalatePrintAgent\logs\agent.log` |
| Printer change history | `C:\ProgramData\PalatePrintAgent\logs\config-changes.log` |
| Printer status (what the status window shows) | `C:\ProgramData\PalatePrintAgent\status.json` |

The data folder is closed to everyone but Windows itself, administrators and
the agent. The one exception is `status.json`, which every local user can
read (not change): printer names, addresses and states, and the connection
to Palate. No credential or ticket contents.

## If something's wrong

| Symptom | What to check |
| --- | --- |
| The log says "re-pair this till" (the service keeps stopping and restarting) | The till was revoked or its credential replaced: get a new code, run `pair`, then `palate-agent stop` and `palate-agent start`. |
| A USB printer "can't be seen" in the log | Install it for all users, or reinstall the service with `palate-agent install -account localsystem`. |
| Tickets print as BACKUP FOR … | The main printer is off, out of paper or its address changed; the status window says which. `printer set … -address`. |
| The status window says "This Windows account can't read the printer status" | The agent was installed by an older version. Install this one over it. |
| Bills fail with "Edge drew nothing" | Edge is missing or blocked by policy; set `"edge_path"` in `agent.json` to `msedge.exe`'s location. |
| Bills cut off on the right | The bill's HTML has a fixed width; Palate's design must use the full width (their spec, section 10). |
| Nothing prints, `status` running | Check the portal links stations to this till's printers, and the till can reach `https://SERVER`. |

The log (`logs\agent.log`) says what happened to every ticket, in plain
words.
