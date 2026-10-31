<#
  build.ps1  -  build the Baize Windows desktop app (Go + WebView2, pure Go, NO cgo).

  Steps:
    1. build the two Vue frontends (webui -> ui/, float -> float/dist/)
    2. compile desktop\bin\baize-desktop.exe
    3. copy the built UIs next to the exe (bin\ui, bin\float\dist, bin\browser-ext)
       so the bin\ folder is a complete portable install (same layout as the installer).

  Output: desktop\bin\baize-desktop.exe

  Note: keep this file ASCII-only. PowerShell 5.1 reads .ps1 as ANSI and non-ASCII
  bytes inside comments can be mis-parsed (e.g. swallow the following line).
#>
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Src = $PSScriptRoot
$Go  = if (Test-Path "D:\xm\tools\go\bin\go.exe") { "D:\xm\tools\go\bin\go.exe" } else { "go" }
$Out = Join-Path $Src "bin\baize-desktop.exe"

# --- 1. frontends -----------------------------------------------------------
# npm is optional: if it is missing we fall back to whatever dist/ already exists.
$npm = Get-Command npm -ErrorAction SilentlyContinue
foreach ($fe in @("webui", "float")) {
  $feDir = Join-Path $Src $fe
  $dist  = Join-Path $feDir "dist"
  if ($npm -and (Test-Path (Join-Path $feDir "node_modules"))) {
    Write-Host "=== build $fe (vite) ===" -ForegroundColor Cyan
    Push-Location $feDir
    & $npm.Source "run" "build"
    $code = $LASTEXITCODE
    Pop-Location
    if ($code -ne 0) { throw "npm run build failed in $feDir" }
  } elseif (Test-Path (Join-Path $dist "index.html")) {
    Write-Host "WARN: npm/node_modules missing for $fe - reusing existing dist/" -ForegroundColor Yellow
  } else {
    throw "missing $fe frontend build: run 'npm install && npm run build' in $feDir"
  }
}

# --- 2. compile -------------------------------------------------------------
# Pure-Go WebView2 bindings: CGO must stay off (no C compiler on this box anyway).
$env:CGO_ENABLED = "0"
$g0 = $env:GOOS; $g1 = $env:GOARCH
$env:GOOS = "windows"; $env:GOARCH = "amd64"

Write-Host "=== build desktop (windows/amd64, CGO off, GUI subsystem) ===" -ForegroundColor Cyan
# -H windowsgui: link as a GUI app so launching it does NOT pop a console window.
# Logs then go to %APPDATA%\Baize\desktop.log (see setupLogging in main_windows.go).
& $Go -C $Src build -trimpath -ldflags "-s -w -H windowsgui" -o $Out .
$env:GOOS = $g0; $env:GOARCH = $g1
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

# --- 3. assemble the portable layout ---------------------------------------
# Installer layout is exe + ui/ + float/dist/ + browser-ext/, so bin/ must match.
# Keeping the same shape means "run bin\baize-desktop.exe" behaves exactly like an
# installed copy (server.go finds ui/ and float/dist/ next to the exe).
$copies = @(
  @{ Src = (Join-Path $Src "webui\dist"); Dst = (Join-Path $Src "bin\ui") },
  @{ Src = (Join-Path $Src "float\dist"); Dst = (Join-Path $Src "bin\float\dist") },
  @{ Src = (Join-Path $Src "browser-ext"); Dst = (Join-Path $Src "bin\browser-ext") }
)
foreach ($c in $copies) {
  if (-not (Test-Path $c.Src)) {
    if ($c.Src -like "*browser-ext*") { Write-Host "WARN: missing $($c.Src) - skip Chrome extension" -ForegroundColor Yellow; continue }
    throw "missing source dir: $($c.Src)"
  }
  if (Test-Path $c.Dst) { Remove-Item $c.Dst -Recurse -Force }
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $c.Dst) | Out-Null
  Copy-Item $c.Src $c.Dst -Recurse -Force
}

$sz = [math]::Round((Get-Item $Out).Length / 1MB, 2)
Write-Host ""
Write-Host "OK -> $Out ($sz MB)" -ForegroundColor Green
Write-Host ("ui      -> " + (Join-Path $Src "bin\ui")) -ForegroundColor Green
Write-Host ("float   -> " + (Join-Path $Src "bin\float\dist")) -ForegroundColor Green
Write-Host ("ext     -> " + (Join-Path $Src "bin\browser-ext")) -ForegroundColor Green
Write-Host "Run:  $Out --server http://192.168.1.100:8787 --token <token>" -ForegroundColor DarkGray
