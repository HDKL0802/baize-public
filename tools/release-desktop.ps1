# Publish the desktop app to the NAS (build -> sha256 -> latest.json -> upload -> smoke test).
# NOTE: keep this file ASCII-only (PowerShell 5.1 reads .ps1 as ANSI; non-ASCII bytes can
#       be mis-parsed and even swallow the following line). Release notes live in
#       desktop/RELEASE_NOTES.txt, which is read as UTF-8.
# Usage: powershell -ExecutionPolicy Bypass -File tools\release-desktop.ps1
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$desk = Join-Path $root "desktop"

# ---- 1. credentials from deploy.local.env (gitignored) ----
# Parsing deliberately mirrors tools/deploy-nas.ps1 (IndexOf/Substring) instead of
# String.Split, so both scripts fail/succeed exactly the same way.
# Read as UTF8 explicitly: PS 5.1 defaults to ANSI/GBK, where a non-ASCII comment
# in the env file can swallow the next newline and hide the following key.
$envFile = Join-Path $root "deploy.local.env"
if (-not (Test-Path $envFile)) { throw "missing deploy.local.env (see deploy.local.env.example)" }
$nasHost = ""; $nasUser = ""; $nasPass = ""; $token = ""
$seen = @()
Get-Content $envFile -Encoding UTF8 | ForEach-Object {
  $line = $_.Trim()
  if ($line -eq "" -or $line.StartsWith("#")) { return }
  $i = $line.IndexOf("=")
  if ($i -lt 1) { return }
  $k = $line.Substring(0, $i).Trim()
  $v = $line.Substring($i + 1).Trim()
  $seen += $k
  switch ($k) {
    "BAIZE_NAS_HOST" { $nasHost = $v }
    "BAIZE_NAS_USER" { $nasUser = $v }
    "BAIZE_NAS_PASS" { $nasPass = $v }
    "BAIZE_TOKEN"    { $token = $v }
  }
}
Write-Host ("env keys: " + ($seen -join ", ") + "  (pass set: " + [bool]$nasPass + ", token set: " + [bool]$token + ")")
if (-not $nasHost -or -not $nasUser -or -not $nasPass -or -not $token) {
  throw "deploy.local.env is incomplete (need BAIZE_NAS_HOST/USER/PASS + BAIZE_TOKEN)"
}

# ---- 2. version: single source of truth is device_windows.go ----
$verFile = Join-Path $desk "device_windows.go"
$m = [regex]::Match((Get-Content $verFile -Raw -Encoding UTF8), 'desktopAppVersion\s*=\s*"([^"]+)"')
if (-not $m.Success) { throw "cannot find desktopAppVersion in device_windows.go" }
$ver = $m.Groups[1].Value
Write-Host "version = $ver"

# ---- 3. build ----
& powershell -ExecutionPolicy Bypass -File (Join-Path $desk "build.ps1")
$exe = Join-Path $desk "bin\baize-desktop.exe"
if (-not (Test-Path $exe)) { throw "build output missing: $exe" }
$ui = Join-Path $desk "ui"
if (-not (Test-Path $ui)) { throw "missing ui dir: $ui (multi-file layout needs it)" }

# ---- 3b. pack the multi-file update (zip: baize-desktop.exe + ui/) ----
$zip = Join-Path $desk ("bin\baize-desktop-" + $ver + ".zip")
if (Test-Path $zip) { Remove-Item $zip -Force }
Compress-Archive -Path $exe, $ui -DestinationPath $zip -Force
if (-not (Test-Path $zip)) { throw "zip not created: $zip" }

# ---- 4. hash + size (of the zip the client downloads) ----
$sha = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
$size = (Get-Item $zip).Length
$notes = ""
$notesFile = Join-Path $desk "RELEASE_NOTES.txt"
if (Test-Path $notesFile) {
  $notes = [System.IO.File]::ReadAllText($notesFile, (New-Object System.Text.UTF8Encoding($false))).Trim()
}
Write-Host ("sha256 = " + $sha + "  size = " + [math]::Round($size / 1MB, 2) + " MB")

# ---- 5. latest.json (url is relative so it follows the client's own backend address) ----
$man = [ordered]@{
  version    = $ver
  url        = "/dl/baize-desktop-$ver.zip"
  sha256     = $sha
  sizeBytes  = $size
  notes      = $notes
  releasedAt = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
}
$outDir = Join-Path $PSScriptRoot ".release"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$jsonPath = Join-Path $outDir "latest.json"
[System.IO.File]::WriteAllText($jsonPath, ($man | ConvertTo-Json), (New-Object System.Text.UTF8Encoding($false)))

# ---- 6. upload to the NAS (remote path MUST use forward slashes) ----
$sshrun = Join-Path $PSScriptRoot "sshrun\bin\sshrun.exe"
if (-not (Test-Path $sshrun)) { throw "missing sshrun: $sshrun" }
$remote = "/vol1/@appdata/baize/data/dl"

Write-Host "--- upload ---"
& $sshrun -host $nasHost -user $nasUser -pass $nasPass -cmd "mkdir -p $remote" | Out-Host
& $sshrun -host $nasHost -user $nasUser -pass $nasPass -put "$zip`:$remote/baize-desktop-$ver.zip" | Out-Host
& $sshrun -host $nasHost -user $nasUser -pass $nasPass -put "$jsonPath`:$remote/latest.json" | Out-Host

# ---- 7. smoke test: can the backend serve it? ----
# NOTE: do NOT use Invoke-RestMethod here. PowerShell 5.1 decodes a response as Latin-1
# when the Content-Type carries no charset, which turns the UTF-8 notes into mojibake and
# makes it look like the upload was broken. Fetch raw bytes and decode as UTF-8 explicitly.
Write-Host "--- smoke ---"
$wc = New-Object System.Net.WebClient
$wc.Headers.Add("X-Baize-Token", $token)
$bytes = $wc.DownloadData("http://" + $nasHost + ":8787/dl/latest.json")
Write-Host ("backend serves: " + [System.Text.Encoding]::UTF8.GetString($bytes))
Write-Host "DONE"
