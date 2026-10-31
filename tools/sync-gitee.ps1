<#
  sync-gitee.ps1 - mirror the GitHub repos to Gitee and publish the same Release.

  What it does:
    1. creates HDKL0037/baize and HDKL0037/baize-public if they do not exist yet
    2. pushes main + tags to both Gitee repos (same commits as GitHub)
    3. creates / updates the Gitee Release for desktop-v<Version> in baize-public
    4. uploads the desktop zip, setup exe, backend binary + sha256, APK
    5. writes a Gitee-flavoured latest.json (download URLs point at Gitee) and
       uploads it last, so the desktop updater / install.sh resolve China URLs.

  Auth: Gitee personal access token (Settings -> Private tokens, scope projects +
        releases). Pass it with -Token or set $env:GITEE_TOKEN. It is never written
        to disk; remote pushes use a temporary Authorization header.

  Examples:
    $env:GITEE_TOKEN = "xxxxx"
    powershell -ExecutionPolicy Bypass -File tools\sync-gitee.ps1 -Version 0.7.0 `
      -Assets desktop\bin\baize-desktop-0.7.0.zip, desktop\bin\baize-desktop-setup-0.7.0.exe, `
              desktop\bin\baize-backend-linux-amd64, desktop\bin\baize-backend-linux-amd64.sha256

  Re-runs are safe: existing repos / release are reused, same-named attachments
  are deleted before re-upload.

  Note: keep this file ASCII-only (PowerShell 5.1 reads .ps1 as ANSI).
#>
param(
  [string]$Token = $env:GITEE_TOKEN,
  [string]$Owner = "HDKL0037",
  [string]$Repo = "baize",
  [string]$PublicRepo = "baize-public",
  [Parameter(Mandatory = $true)][string]$Version,
  [string[]]$Assets = @(),
  [string]$NotesFile = "desktop\RELEASE_NOTES.md",
  [string]$Branch = "main",
  [switch]$SkipPush,
  [switch]$SkipRelease
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

if (-not $Token) { throw "missing Gitee token: pass -Token or set `$env:GITEE_TOKEN" }

$ApiBase = "https://gitee.com/api/v5"
$Tag = "desktop-v$Version"
$Headers = @{
  Authorization = "token $Token"
  "User-Agent"  = "baize-sync"
}

# --- helpers -----------------------------------------------------------------
function GiteeApi {
  param([string]$Method, [string]$Path, $Payload = $null)
  $args = @{ Method = $Method; Uri = "$ApiBase$Path"; Headers = $Headers; UseBasicParsing = $true }
  if ($null -ne $Payload) {
    $json = $Payload | ConvertTo-Json -Depth 6
    $args.Body = [Text.Encoding]::UTF8.GetBytes($json)
    $args.ContentType = "application/json; charset=utf-8"
  }
  $resp = Invoke-WebRequest @args
  if ([string]::IsNullOrWhiteSpace($resp.Content)) { return $null }
  return ($resp.Content | ConvertFrom-Json)
}

function Ensure-Repo {
  param([string]$Name, [string]$Description)
  try {
    $r = GiteeApi -Method Get -Path "/repos/$Owner/$Name"
    if ($r) { Write-Host "repo exists: $Owner/$Name" -ForegroundColor Yellow; return }
  } catch {
    $code = $null
    if ($_.Exception.Response) { $code = [int]$_.Exception.Response.StatusCode }
    if ($code -ne 404) { throw }
  }
  GiteeApi -Method Post -Path "/user/repos" -Payload @{
    name        = $Name
    path        = $Name
    private     = $false
    description = $Description
    has_issues  = $true
    has_wiki    = $true
    auto_init   = $false
  } | Out-Null
  Write-Host "created repo: $Owner/$Name" -ForegroundColor Green
  Start-Sleep -Seconds 2
}

function Push-Main {
  param([string]$Name)
  $pair = "$($Owner):$Token"
  $b64 = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes($pair))
  $url = "https://gitee.com/$Owner/$Name.git"
  $hdr = "http.extraheader=Authorization: Basic $b64"
  Write-Host "push -> $url ($Branch + tags)" -ForegroundColor Cyan
  # git writes progress to stderr; with ErrorActionPreference=Stop a redirected
  # stderr line aborts the script, so relax it around the native calls.
  $eap = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    $out = (& git -c $hdr push $url $Branch 2>&1) | Out-String
    $code = $LASTEXITCODE
    ($out -split "`r?`n") | Where-Object { $_ } | ForEach-Object { Write-Host "  $_" }
    if ($code -ne 0) { throw "push $Name $Branch failed (exit $code)" }
    $out = (& git -c $hdr push $url --tags 2>&1) | Out-String
    $code = $LASTEXITCODE
    ($out -split "`r?`n") | Where-Object { $_ } | ForEach-Object { Write-Host "  $_" }
    if ($code -ne 0) { throw "push $Name tags failed (exit $code)" }
  } finally { $ErrorActionPreference = $eap }
}

function Upload-Asset {
  param([long]$ReleaseId, [string]$File)
  if (-not (Test-Path $File)) { throw "asset not found: $File" }
  $full = (Resolve-Path $File).Path
  $fname = Split-Path $full -Leaf
  $uri = "$ApiBase/repos/$Owner/$PublicRepo/releases/$ReleaseId/attach_files"
  $boundary = [Guid]::NewGuid().ToString("N")
  $enc = [Text.Encoding]::UTF8
  $head = "--$boundary`r`nContent-Disposition: form-data; name=`"file`"; filename=`"$fname`"`r`nContent-Type: application/octet-stream`r`n`r`n"
  $tail = "`r`n--$boundary--`r`n"

  $req = [Net.HttpWebRequest]::Create($uri)
  $req.Method = "POST"
  $req.Headers.Add("Authorization", "token $Token")
  $req.UserAgent = "baize-sync"
  $req.ContentType = "multipart/form-data; boundary=$boundary"
  $req.Timeout = 600000
  $headBytes = $enc.GetBytes($head)
  $tailBytes = $enc.GetBytes($tail)
  $fs = [IO.File]::OpenRead($full)
  try {
    $req.ContentLength = $headBytes.Length + $fs.Length + $tailBytes.Length
    $rs = $req.GetRequestStream()
    $rs.Write($headBytes, 0, $headBytes.Length)
    $fs.CopyTo($rs)
    $rs.Write($tailBytes, 0, $tailBytes.Length)
    $rs.Close()
  } finally { $fs.Close() }

  $resp = $req.GetResponse()
  $sr = New-Object IO.StreamReader($resp.GetResponseStream())
  $asset = $sr.ReadToEnd() | ConvertFrom-Json
  $sr.Close(); $resp.Close()
  $mb = [math]::Round($asset.size / 1MB, 2)
  Write-Host ("  uploaded {0} ({1} MB)" -f $asset.name, $mb) -ForegroundColor Green
}

function Get-Release {
  try { return (GiteeApi -Method Get -Path "/repos/$Owner/$PublicRepo/releases/tags/$Tag") } catch { return $null }
}

# Gitee's release payload does not carry attachment ids, so ask attach_files.
function Get-AttachIds {
  param([long]$ReleaseId)
  $map = @{}
  try {
    $list = @(GiteeApi -Method Get -Path "/repos/$Owner/$PublicRepo/releases/$ReleaseId/attach_files")
    foreach ($a in $list) { if ($a.name) { $map[$a.name] = $a.id } }
  } catch { }
  return $map
}

function Remove-Attach {
  param([long]$ReleaseId, [long]$FileId)
  GiteeApi -Method Delete -Path "/repos/$Owner/$PublicRepo/releases/$ReleaseId/attach_files/$FileId" | Out-Null
}

# --- 1. repos ----------------------------------------------------------------
Write-Host "=== gitee: $Owner/$Repo + $Owner/$PublicRepo ===" -ForegroundColor Cyan
Ensure-Repo -Name $Repo -Description "Baize cross-device personal AI agent (mirror of GitHub)"
Ensure-Repo -Name $PublicRepo -Description "Baize public release mirror (binaries / installers)"

# --- 2. push code ------------------------------------------------------------
if (-not $SkipPush) {
  Write-Host "=== push code ===" -ForegroundColor Cyan
  Push-Main -Name $Repo
  Push-Main -Name $PublicRepo
}

if ($SkipRelease) {
  Write-Host "OK (push only)" -ForegroundColor Green
  return
}

# --- 3. release --------------------------------------------------------------
Write-Host "=== gitee release: $Tag ===" -ForegroundColor Cyan
$body = ""
if ($NotesFile -and (Test-Path $NotesFile)) {
  $body = [IO.File]::ReadAllText((Resolve-Path $NotesFile).Path, [Text.Encoding]::UTF8)
}

$rel = Get-Release
if ($rel) {
  Write-Host ("release exists (id={0}), updating" -f $rel.id) -ForegroundColor Yellow
  GiteeApi -Method Patch -Path "/repos/$Owner/$PublicRepo/releases/$($rel.id)" -Payload @{
    tag_name = $Tag
    name     = "Baize $Version"
    body     = $body
  } | Out-Null
  $rel = Get-Release
} else {
  $rel = GiteeApi -Method Post -Path "/repos/$Owner/$PublicRepo/releases" -Payload @{
    tag_name         = $Tag
    target_commitish = $Branch
    name             = "Baize $Version"
    body             = $body
    prerelease       = $false
  }
  Write-Host ("created release id={0}" -f $rel.id) -ForegroundColor Green
}

# --- 4. upload assets (drop same-named ones first so re-runs are clean) ------
$existing = Get-AttachIds -ReleaseId $rel.id
foreach ($path in $Assets) {
  if (-not (Test-Path $path)) { throw "asset not found: $path" }
  $fname = Split-Path (Resolve-Path $path).Path -Leaf
  if ($existing.ContainsKey($fname)) {
    Remove-Attach -ReleaseId $rel.id -FileId $existing[$fname]
    Write-Host "  removed old asset: $fname" -ForegroundColor Yellow
  }
  Upload-Asset -ReleaseId $rel.id -File $path
}

# --- 5. Gitee-flavoured latest.json (URLs stay inside China) -----------------
$rel = Get-Release
$zipName = "baize-desktop-$Version.zip"
$zipUrl = ""
foreach ($a in $rel.assets) {
  if ($a.name -eq $zipName) { $zipUrl = $a.browser_download_url }
}
$zipFile = Join-Path "desktop\bin" $zipName
if ($zipUrl -and (Test-Path $zipFile)) {
  $sha = (Get-FileHash $zipFile -Algorithm SHA256).Hash.ToLower()
  $size = (Get-Item $zipFile).Length
  $notes = ""
  if ($NotesFile -and (Test-Path $NotesFile)) {
    $notes = ([IO.File]::ReadAllText((Resolve-Path $NotesFile).Path, [Text.Encoding]::UTF8)).Trim()
  }
  $man = [ordered]@{
    version    = $Version
    url        = $zipUrl
    sha256     = $sha
    sizeBytes  = $size
    notes      = $notes
    releasedAt = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
  }
  # must be named exactly latest.json: the desktop updater looks that asset up by name
  $out = Join-Path (Get-Location).Path "desktop\bin\latest.json"
  # PS 5.1 has no utf8NoBOM encoding literal; write UTF-8 without BOM explicitly.
  $json = $man | ConvertTo-Json
  [IO.File]::WriteAllText($out, $json, (New-Object Text.UTF8Encoding($false)))

  # drop whatever latest.json was uploaded before (GitHub-flavoured one), then add ours
  $ids = Get-AttachIds -ReleaseId $rel.id
  foreach ($name in @("latest.json", "latest-gitee.json")) {
    if ($ids.ContainsKey($name)) {
      Remove-Attach -ReleaseId $rel.id -FileId $ids[$name]
      Write-Host "  removed old $name" -ForegroundColor Yellow
    }
  }
  Upload-Asset -ReleaseId $rel.id -File $out
  Write-Host "latest.json (gitee) -> $zipUrl" -ForegroundColor Green
} else {
  Write-Host "skip latest.json (zip asset not found in release)" -ForegroundColor Yellow
}

Write-Host ""
Write-Host ("OK -> https://gitee.com/{0}/{1}/releases/tag/{2}" -f $Owner, $PublicRepo, $Tag) -ForegroundColor Green
