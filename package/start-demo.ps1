<#
.SYNOPSIS
  Live demo of the Palate print agent on this PC: the mock Palate server,
  three on-screen printers and two tills (two real agents), driven from a
  page in the browser.

.DESCRIPTION
  Front till: Tandoor (backup: Pass, 58 mm), Pass
  Back till:  Wok
  If the front till goes silent, the server moves its tickets to Wok.

  Nothing is installed: the agents run in the background from this folder
  and everything stops when you press Enter. No administrator rights needed.

.PARAMETER RealPrinter
  Print Tandoor's tickets on a real network printer at this IP address.
.PARAMETER RealUsbPrinter
  ...or on a real USB printer, by its Windows name (see: .\palate-agent.exe printers).
.PARAMETER RealWidth
  The real printer's paper width in mm: 58 or 80 (default 80).
.PARAMETER Port
  Where the page and the mock server listen (default 8080).

.EXAMPLE
  .\start-demo.ps1
.EXAMPLE
  .\start-demo.ps1 -RealPrinter 192.168.1.50
#>
param(
  [string]$RealPrinter = "",
  [string]$RealUsbPrinter = "",
  [ValidateSet(58, 80)][int]$RealWidth = 80,
  [int]$Port = 8080,
  [switch]$NoBrowser,
  # For unattended checks: stop after this many seconds instead of waiting for Enter.
  [int]$StopAfter = 0
)

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$agentExe = Join-Path $here "palate-agent.exe"
$mockExe = Join-Path $here "mockpalate.exe"
$printerExe = Join-Path $here "fakeprinter.exe"
foreach ($f in $agentExe, $mockExe, $printerExe) {
  if (-not (Test-Path $f)) { throw "missing ${f}: run this from the unpacked package folder" }
}
if ($RealPrinter -and $RealUsbPrinter) { throw "give -RealPrinter or -RealUsbPrinter, not both" }

$url = "http://127.0.0.1:$Port"
$work = Join-Path $env:TEMP ("palate-demo-" + (Get-Date -Format "yyyyMMdd-HHmmss"))
New-Item -ItemType Directory -Force -Path $work, "$work\front", "$work\back" | Out-Null
$procs = New-Object System.Collections.ArrayList

function Start-Bg($name, $exe, $argList) {
  $p = Start-Process -FilePath $exe -ArgumentList $argList -WindowStyle Hidden -PassThru `
    -RedirectStandardOutput "$work\$name.out.log" -RedirectStandardError "$work\$name.log"
  [void]$procs.Add($p)
  return $p
}

function Write-Json($path, $obj) {
  # UTF-8 without a byte-order mark: the agent reads plain JSON.
  [IO.File]::WriteAllText($path, ($obj | ConvertTo-Json -Depth 6), (New-Object Text.UTF8Encoding $false))
}

function Stop-All {
  Write-Host "`nstopping..."
  foreach ($p in $procs) { if (-not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } }
  Write-Host "logs are in $work"
}

try {
  # On-screen printers, each with a control API the page uses.
  $controls = "p-pass=http://127.0.0.1:19191,p-wok=http://127.0.0.1:19192"
  Start-Bg "pass" $printerExe @("-name", "Pass", "-addr", "127.0.0.1:19101", "-control", "127.0.0.1:19191", "-out", '""') | Out-Null
  Start-Bg "wok" $printerExe @("-name", "Wok", "-addr", "127.0.0.1:19102", "-control", "127.0.0.1:19192", "-out", '""') | Out-Null
  if ($RealPrinter) {
    $tandoor = [ordered]@{ id = "p-tandoor"; name = "Tandoor"; kind = "network"; address = $RealPrinter; width = $RealWidth; backup = "p-pass" }
    Write-Host "Tandoor tickets go to the real network printer at $RealPrinter ($RealWidth mm)"
  } elseif ($RealUsbPrinter) {
    $tandoor = [ordered]@{ id = "p-tandoor"; name = "Tandoor"; kind = "usb"; printer_name = $RealUsbPrinter; width = $RealWidth; backup = "p-pass" }
    Write-Host "Tandoor tickets go to the real USB printer `"$RealUsbPrinter`" ($RealWidth mm)"
  } else {
    $tandoor = [ordered]@{ id = "p-tandoor"; name = "Tandoor"; kind = "network"; address = "127.0.0.1:19100"; width = 80; backup = "p-pass" }
    Start-Bg "tandoor" $printerExe @("-name", "Tandoor", "-addr", "127.0.0.1:19100", "-control", "127.0.0.1:19190", "-out", '""') | Out-Null
    $controls = "p-tandoor=http://127.0.0.1:19190,$controls"
  }

  # The mock server, with short timings so failures show quickly on stage:
  # a silent till's tickets move after 15 s.
  Start-Bg "mockpalate" $mockExe @("-addr", "127.0.0.1:$Port", "-hold", "10s", "-lease", "20s", "-not-answering", "15s", "-printer-control", $controls) | Out-Null
  $up = $false
  for ($i = 0; $i -lt 100; $i++) {
    try { Invoke-RestMethod "$url/mock/state" -TimeoutSec 2 | Out-Null; $up = $true; break } catch { Start-Sleep -Milliseconds 200 }
  }
  if (-not $up) { throw "the mock server didn't start; see $work\mockpalate.log" }

  # Two tills.
  Write-Json "$work\front\agent.json" ([ordered]@{
      server = $url; agent_name = "front-till"
      printers = @($tandoor, [ordered]@{ id = "p-pass"; name = "Pass"; kind = "network"; address = "127.0.0.1:19101"; width = 58 })
    })
  Write-Json "$work\back\agent.json" ([ordered]@{
      server = $url; agent_name = "back-till"
      printers = @([ordered]@{ id = "p-wok"; name = "Wok"; kind = "network"; address = "127.0.0.1:19102"; width = 80 })
    })

  Write-Host "pairing the tills..."
  foreach ($till in "front", "back") {
    $code = (Invoke-RestMethod -Method Post "$url/mock/pairing-codes" -Body '{"restaurant_id":"demo"}' -ContentType "application/json").code
    & $agentExe pair -config "$work\$till\agent.json" -code $code
    if ($LASTEXITCODE -ne 0) { throw "pairing the $till till failed" }
  }

  # Backups across tills, for when a whole till goes silent.
  Invoke-RestMethod -Method Put "$url/mock/backups" -Body '{"backups":{"p-tandoor":"p-wok","p-pass":"p-wok"}}' -ContentType "application/json" | Out-Null

  Start-Bg "front-till" $agentExe @("run", "-config", "`"$work\front\agent.json`"") | Out-Null
  Start-Bg "back-till" $agentExe @("run", "-config", "`"$work\back\agent.json`"") | Out-Null

  Write-Host ""
  Write-Host "  Live demo running: $url/"
  Write-Host "  Logs: $work"
  Write-Host ""
  if (-not $NoBrowser) { Start-Process "$url/" }
  if ($StopAfter -gt 0) { Start-Sleep -Seconds $StopAfter } else { Read-Host "Press Enter to stop the demo" | Out-Null }
} finally {
  Stop-All
}
