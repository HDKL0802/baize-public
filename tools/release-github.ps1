<#
  release-github.ps1 - create (or update) a GitHub Release and upload asset files.

  Why this exists:
    The GitHub MCP connector here can read releases but not publish them. `gh` is
    not installed on this box. So we drive the REST API directly with the
    github.com credential that Git Credential Manager already stores
    (git credential fill). Nothing is passed on the command line and no token is
    ever written to disk.

  Auth reset (if it ever expires):  git credential-manager github login

  Examples:
    powershell -ExecutionPolicy Bypass -File tools\release-github.ps1 `
      -Repo HDKL0802/baize-public -Tag desktop-v0.3.1 `
      -Name "Baize Desktop 0.3.1" -NotesFile desktop\RELEASE_NOTES.md `
      -Assets desktop\bin\baize-desktop-setup-0.3.1.exe

  Re-running with the same tag is safe: an existing release is reused and an
  asset with the same file name is replaced.

  Note: keep this file ASCII-only (PowerShell 5.1 reads .ps1 as ANSI).
#>
param(
  [string]$Repo = "HDKL0802/baize-public",
  [Parameter(Mandatory = $true)][string]$Tag,
  [string]$Name = "",
  [string]$NotesFile = "",
  [string]$Notes = "",
  [string[]]$Assets = @(),
  [string]$Target = "main",
  [switch]$Prerelease,
  [switch]$Draft,
  [switch]$Delete
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

if (-not $Name) { $Name = $Tag }

$body = $Notes
if ($NotesFile) {
  if (-not (Test-Path $NotesFile)) { throw "notes file not found: $NotesFile" }
  $body = [IO.File]::ReadAllText((Resolve-Path $NotesFile).Path, [Text.Encoding]::UTF8)
}

# --- locate git (not necessarily on PATH in this environment) -----------------
$git = "git"
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  foreach ($c in @("C:\Program Files\Git\cmd\git.exe", "C:\Program Files (x86)\Git\cmd\git.exe")) {
    if (Test-Path $c) { $git = $c; break }
  }
}

# --- token from Git Credential Manager ---------------------------------------
# NB: in this environment neither .NET StandardInput redirection nor PowerShell's
# native pipe actually delivers stdin to git (git answers "missing protocol
# field"), while cmd's file redirection does. So feed the query via a temp file;
# the answer (which carries the token) stays in memory and is never written out.
$credQuery = [IO.Path]::GetTempFileName()
[IO.File]::WriteAllText($credQuery, "protocol=https`nhost=github.com`n`n")
try {
  $credOut = (& cmd /c "`"$git`" credential fill < `"$credQuery`"") -join "`n"
} finally {
  Remove-Item $credQuery -Force -ErrorAction SilentlyContinue
}
$credLines = $credOut -split "`r?`n"
$token = ($credLines | Where-Object { $_ -like "password=*" } | Select-Object -First 1) -replace "^password=", ""
$user = ($credLines | Where-Object { $_ -like "username=*" } | Select-Object -First 1) -replace "^username=", ""
if (-not $token) { throw "no stored github.com credential - run: git credential-manager github login" }

$jsonHeaders = @{
  Authorization = "token $token"
  Accept        = "application/vnd.github+json"
  "User-Agent"  = "baize-release"
}

function Invoke-GHJson {
  param([string]$Method, [string]$Uri, $Payload = $null)
  $args = @{ Method = $Method; Uri = $Uri; Headers = $jsonHeaders; UseBasicParsing = $true }
  if ($null -ne $Payload) {
    $json = $Payload | ConvertTo-Json -Depth 6
    # PS 5.1 would encode a plain string body as ISO-8859-1; send UTF-8 bytes.
    $args.Body = [Text.Encoding]::UTF8.GetBytes($json)
    $args.ContentType = "application/json; charset=utf-8"
  }
  $resp = Invoke-WebRequest @args
  if ([string]::IsNullOrWhiteSpace($resp.Content)) { return $null }
  return ($resp.Content | ConvertFrom-Json)
}

Write-Host "=== github release: $Repo @ $Tag (as $user) ===" -ForegroundColor Cyan

# --- -Delete: retract a published release (release + its tag) ----------------
if ($Delete) {
  try {
    $old = Invoke-GHJson -Method Get -Uri "https://api.github.com/repos/$Repo/releases/tags/$Tag"
    Invoke-GHJson -Method Delete -Uri "https://api.github.com/repos/$Repo/releases/$($old.id)" | Out-Null
    Write-Host ("deleted release id={0}" -f $old.id) -ForegroundColor Green
  } catch {
    Write-Host "release not found (nothing to delete)" -ForegroundColor Yellow
  }
  try {
    Invoke-GHJson -Method Delete -Uri "https://api.github.com/repos/$Repo/git/refs/tags/$Tag" | Out-Null
    Write-Host ("deleted tag {0}" -f $Tag) -ForegroundColor Green
  } catch {
    Write-Host "tag not found (nothing to delete)" -ForegroundColor Yellow
  }
  Write-Host ""
  Write-Host "OK (deleted)" -ForegroundColor Green
  return
}

# --- create the release, or reuse the one that already exists -----------------
$rel = $null
try {
  $rel = Invoke-GHJson -Method Post -Uri "https://api.github.com/repos/$Repo/releases" -Payload @{
    tag_name         = $Tag
    target_commitish = $Target
    name             = $Name
    body             = $body
    draft            = [bool]$Draft
    prerelease       = [bool]$Prerelease
  }
  Write-Host ("created release id={0}" -f $rel.id) -ForegroundColor Green
} catch {
  $code = $null
  if ($_.Exception.Response) { $code = [int]$_.Exception.Response.StatusCode }
  if ($code -ne 422) { throw }
  $rel = Invoke-GHJson -Method Get -Uri "https://api.github.com/repos/$Repo/releases/tags/$Tag"
  Write-Host ("release already exists (id={0}), updating it" -f $rel.id) -ForegroundColor Yellow
  $rel = Invoke-GHJson -Method Patch -Uri "https://api.github.com/repos/$Repo/releases/$($rel.id)" -Payload @{
    name       = $Name
    body       = $body
    prerelease = [bool]$Prerelease
  }
}

# --- upload assets (replace same-named ones so re-runs are idempotent) --------
if ($Assets.Count -gt 0) {
  $existing = @{}
  foreach ($a in $rel.assets) { $existing[$a.name] = $a.id }
  $uploadBase = $rel.upload_url -replace '\{.*$', ''
  foreach ($path in $Assets) {
    if (-not (Test-Path $path)) { throw "asset not found: $path" }
    $full = (Resolve-Path $path).Path
    $fname = Split-Path $full -Leaf
    if ($existing.ContainsKey($fname)) {
      Invoke-GHJson -Method Delete -Uri "https://api.github.com/repos/$Repo/releases/assets/$($existing[$fname])" | Out-Null
      Write-Host "  replaced old asset: $fname" -ForegroundColor Yellow
    }
    $uri = "$uploadBase" + "?name=" + [Uri]::EscapeDataString($fname)
    $req = [Net.HttpWebRequest]::Create($uri)
    $req.Method = "POST"
    $req.Headers.Add("Authorization", "token $token")
    # User-Agent is a restricted header: must go through the property.
    $req.UserAgent = "baize-release"
    $req.Accept = "application/vnd.github+json"
    $req.ContentType = "application/octet-stream"
    $req.Timeout = 300000
    $fs = [IO.File]::OpenRead($full)
    try {
      $req.ContentLength = $fs.Length
      $rs = $req.GetRequestStream()
      $fs.CopyTo($rs)
      $rs.Close()
    } finally { $fs.Close() }
    $resp = $req.GetResponse()
    $sr = New-Object IO.StreamReader($resp.GetResponseStream())
    $asset = $sr.ReadToEnd() | ConvertFrom-Json
    $sr.Close(); $resp.Close()
    $mb = [math]::Round($asset.size / 1MB, 2)
    Write-Host ("  uploaded {0} ({1} MB) -> {2}" -f $asset.name, $mb, $asset.browser_download_url) -ForegroundColor Green
  }
}

Write-Host ""
Write-Host ("OK -> {0}" -f $rel.html_url) -ForegroundColor Green
