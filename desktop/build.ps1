<#
  build.ps1  -  build the Baize Windows desktop app (Go + WebView2, pure Go, NO cgo).
  Output: desktop\bin\baize-desktop.exe

  Note: keep this file ASCII-only. PowerShell 5.1 reads .ps1 as ANSI and non-ASCII
  bytes inside comments can be mis-parsed (e.g. swallow the following line).
#>
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Src = $PSScriptRoot
$Go  = if (Test-Path "D:\xm\tools\go\bin\go.exe") { "D:\xm\tools\go\bin\go.exe" } else { "go" }
$Out = Join-Path $Src "bin\baize-desktop.exe"

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

# Multi-file layout: the UI lives in a ui/ folder next to the exe (no longer embedded).
# Copy it to bin\ui so bin\baize-desktop.exe runs standalone (dev/portable) -- same layout as installed.
$UiSrc = Join-Path $Src "ui"
$UiDst = Join-Path $Src "bin\ui"
if (Test-Path $UiDst) { Remove-Item $UiDst -Recurse -Force }
Copy-Item $UiSrc $UiDst -Recurse -Force

$sz = [math]::Round((Get-Item $Out).Length / 1MB, 2)
Write-Host ""
Write-Host "OK -> $Out ($sz MB)" -ForegroundColor Green
Write-Host ("ui -> " + $UiDst + " (" + (Get-ChildItem $UiDst -Recurse -File | Measure-Object).Count + " files)") -ForegroundColor Green
Write-Host "Run:  $Out --server http://192.168.1.100:8787 --token <token>" -ForegroundColor DarkGray
