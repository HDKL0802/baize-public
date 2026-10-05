<#
  build-installer.ps1 - wrap the compiled desktop app into a Windows setup.exe via NSIS.

  Prereqs:
    - NSIS installed (makensis). On this box: winget install --id NSIS.NSIS -e
    - desktop\bin\baize-desktop.exe already built (run build.ps1 first).

  Output: desktop\bin\baize-desktop-setup-<version>.exe

  Note: keep this file ASCII-only. PowerShell 5.1 reads .ps1 as ANSI and non-ASCII
  bytes inside comments can be mis-parsed (e.g. swallow the following line).
#>
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Src = $PSScriptRoot
$Bin = Join-Path $Src "bin"
$Exe = Join-Path $Bin "baize-desktop.exe"
$Nsi = Join-Path $Src "installer.nsi"

# --- locate makensis ---------------------------------------------------------
$Maker = $null
foreach ($c in @(
    "D:\xm\tools\nsis\Bin\makensis.exe",
    "D:\xm\tools\nsis\makensis.exe",
    "C:\Program Files (x86)\NSIS\makensis.exe",
    "C:\Program Files\NSIS\makensis.exe")) {
  if (Test-Path $c) { $Maker = $c; break }
}
if (-not $Maker) {
  $cmd = Get-Command makensis -ErrorAction SilentlyContinue
  if ($cmd) { $Maker = $cmd.Source }
}
if (-not $Maker) { throw "makensis not found. Install NSIS: winget install --id NSIS.NSIS -e" }

if (-not (Test-Path $Exe)) { throw "missing $Exe - run build.ps1 first" }

# --- version, single source of truth -----------------------------------------
$dv = Join-Path $Src "device_windows.go"
$m = Select-String -Path $dv -Pattern 'desktopAppVersion\s*=\s*"([0-9.]+)"' | Select-Object -First 1
if (-not $m) { throw "cannot parse desktopAppVersion from $dv" }
$Version = $m.Matches[0].Groups[1].Value
$VerNum = "$Version.0"
if (($Version -split '\.').Count -ge 4) { $VerNum = $Version }

# --- stage into an ASCII-only dir --------------------------------------------
# The repo path contains non-ASCII characters. makensis is an ANSI native tool,
# so compiling from a plain temp dir avoids any path-decoding surprises.
$Stage = Join-Path $env:TEMP "baize-installer"
if (Test-Path $Stage) { Remove-Item $Stage -Recurse -Force }
New-Item -ItemType Directory -Path $Stage | Out-Null

Copy-Item -LiteralPath $Nsi  -Destination (Join-Path $Stage "installer.nsi") -Force
Copy-Item -LiteralPath $Exe  -Destination (Join-Path $Stage "baize-desktop.exe") -Force

# Multi-file layout: bundle the ui/ folder into the installer so the install has exe + ui/.
$Ui = Join-Path $Src "ui"
if (-not (Test-Path $Ui)) { throw "missing ui dir: $Ui (the ui folder is part of the installer)" }
Copy-Item -LiteralPath $Ui -Destination (Join-Path $Stage "ui") -Recurse -Force
$UiStage = Join-Path $Stage "ui"

# The .nsi pulls its icon from ${__FILEDIR__}\icon.ico, so stage that too.
$Icon = Join-Path $Src "assets\icon.ico"
if (-not (Test-Path $Icon)) { throw "missing $Icon - run build-icon.ps1 first" }
Copy-Item -LiteralPath $Icon -Destination (Join-Path $Stage "icon.ico") -Force

$OutStage = Join-Path $Stage "baize-desktop-setup.exe"
$OutFinal = Join-Path $Bin ("baize-desktop-setup-" + $Version + ".exe")

Write-Host "=== build installer: Baize $Version ===" -ForegroundColor Cyan
& $Maker "/V2" "/DVERSION=$Version" "/DVERNUM=$VerNum" "/DOUTFILE=$OutStage" "/DUIDIR=$UiStage" (Join-Path $Stage "installer.nsi")
if ($LASTEXITCODE -ne 0) { throw "makensis failed (exit $LASTEXITCODE)" }
if (-not (Test-Path $OutStage)) { throw "makensis produced no output" }

Copy-Item -LiteralPath $OutStage -Destination $OutFinal -Force
Remove-Item $Stage -Recurse -Force -ErrorAction SilentlyContinue

$sz = [math]::Round((Get-Item $OutFinal).Length / 1MB, 2)
Write-Host ""
Write-Host "OK -> $OutFinal ($sz MB)" -ForegroundColor Green
