# Palate print agent: start here

The print agent runs on each restaurant's Windows till. It collects kitchen
tickets and customer bills from Palate's server and prints each one on the
right printer, with no double prints, backups when a printer fails, and
recovery when the internet drops.

## What's in this folder

| File | What it is | For |
| --- | --- | --- |
| `palate-agent.exe` | **The print agent.** Double-click it on a till to open the setup window; it installs as a Windows service. | Restaurants |
| `docs/TILL-SETUP.md` | How to install it on a till and its printers, step by step. | Whoever installs it |
| `docs/SERVER-API.md` | **What Palate's server must provide:** the four endpoints, every field of a kitchen ticket and a bill, the delivery rules, errors, security, and the HTML bill format. | Palate's backend team |
| `mockpalate.exe` | A mock Palate server that follows `SERVER-API.md` exactly: the reference implementation, and the server for the demo. | Reference and demo |
| `fakeprinter.exe` | An on-screen printer for the demo. | Demo |
| `start-demo.ps1` | Runs the whole system on one PC and opens it in the browser. | Demo |
| `examples/bill.html` | A sample customer bill in HTML that fits 80 mm and 58 mm paper, and pictures of how it printed. | Palate's bill design |
| `SHA256SUMS.txt` | Checksums of the programs. | Checking the download |

## Install it on a till

**Double-click `palate-agent.exe`.** After Windows asks for permission, a
setup window opens: type Palate's server address, add each printer (a test
slip prints on each), enter the pairing code, and press **Install and
start**. It installs the Windows service, pairs and starts it. `docs/TILL-SETUP.md` has the details and the same steps as
individual commands.

## See it working (5 minutes, no installation)

In this folder, open PowerShell and run:

```powershell
powershell -ExecutionPolicy Bypass -File .\start-demo.ps1
```

The browser opens the demo page: two tills, three printers on screen.

- **Confirm order:** tickets split by station and print on each printer.
- **Send a bill:** the sample HTML bill prints, fitted to the paper.
- **Printers:** switch one off and its tickets go to its backup.
- **Tills:** cut a till's network and its tickets move to the other till.
- **Server mode:** make the server misbehave on purpose; nothing wrong
  reaches the paper.

To print on a real printer too: `.\start-demo.ps1 -RealPrinter 192.168.1.50`
(or `-RealUsbPrinter "EPSON TM-T82 Receipt"`, with `-RealWidth 58` for 58 mm
paper). Press Enter in the PowerShell window to stop.

## Before you start

- **Unblock the zip** before extracting it (right-click → Properties →
  Unblock). The programs aren't code-signed yet, so Windows SmartScreen may
  say "Windows protected your PC": choose More info → Run anyway.
- **Windows 10 or 11, 64-bit,** with Microsoft Edge (built in). Edge draws
  the HTML bills.

## Where it stands

Built and tested on a simulated setup: real agents, the mock server, and
simulated printers that switch off, jam, hang, run out of paper and drop
connections; a server that misbehaves on purpose; crashes mid-print;
internet outages. Next: real printers in a real restaurant.

Not in this version:

- **Signed program and automatic updates.** To update, stop the service,
  `palate-agent uninstall`, then `install` and `start` from the new
  version's folder; the config, credential and printed-job history are kept.
- **Printing during an internet outage for orders taken in the restaurant.**
  Orders made in Palate's cloud wait and print when the line returns.

## Questions about the data

`docs/SERVER-API.md` is the contract. The mock implements it, so anything the
document doesn't make clear, the mock shows: run it, call its endpoints, and
watch `GET /mock/state`.
