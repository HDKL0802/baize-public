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

Write-Host "=== build desktop (windows/amd64, CGO off) ===" -ForegroundColor Cyan
& $Go -C $Src build -trimpath -ldflags "-s -w" -o $Out .
$env:GOOS = $g0; $env:GOARCH = $g1
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

$sz = [math]::Round((Get-Item $Out).Length / 1MB, 2)
Write-Host ""
Write-Host "OK -> $Out ($sz MB)" -ForegroundColor Green
Write-Host "Run:  $Out --server http://192.168.1.100:8787 --token <token>" -ForegroundColor DarkGray
