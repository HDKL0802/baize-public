<#
  deploy-nas.ps1  -  build baize-backend, ship it to the NAS, run it as a Docker container.

  Why this exists: the backend must live on the NAS only (no local backend anymore).
  This is the single command for "backend changed -> deploy".

  Credentials (NEVER hard-coded here; they must not enter the git repo):
     read from environment variables, or from <repo root>\deploy.local.env (git-ignored):
        BAIZE_NAS_HOST   required (no default; e.g. 192.168.1.100)
        BAIZE_NAS_USER   default HD
        BAIZE_NAS_PASS   required
        BAIZE_TOKEN      optional, used for the smoke test

  Usage:
     powershell -ExecutionPolicy Bypass -File tools\deploy-nas.ps1
     powershell -ExecutionPolicy Bypass -File tools\deploy-nas.ps1 -SkipBuild -NoRestart
     powershell -ExecutionPolicy Bypass -File tools\deploy-nas.ps1 -Tag v0.8.1
     powershell -ExecutionPolicy Bypass -File tools\deploy-nas.ps1 -Push      # after docker login ghcr.io
     powershell -ExecutionPolicy Bypass -File tools\deploy-nas.ps1 -Push -Registry ghcr.io/<user> -ImageName baize-backend

  Safety: the old systemd unit is only "stop"ped first; it is "disable"d only after the
  container passes the health check. If the container is unhealthy we roll back to systemd,
  so the NAS backend is never left dead.

  Notes:
     - The PC has no Docker, so the image is built ON the NAS from the uploaded linux/amd64 binary.
#>
param(
    [switch]$SkipBuild,
    [switch]$NoRestart,
    [switch]$Push,
    [string]$Tag = "local",
    [string]$Registry = "ghcr.io/hdkl0802",
    [string]$ImageName = "baize-backend"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Root = Split-Path -Parent $PSScriptRoot          # script lives in tools\, so root is its parent

# ---------- load deploy.local.env (does not override real env vars) ----------
# NOTE: read as UTF8 explicitly. PowerShell 5.1 defaults to the ANSI/GBK codepage,
# and a non-ASCII comment there can swallow the next newline (the following key
# then looks like part of the comment and gets silently skipped).
$envFile = Join-Path $Root "deploy.local.env"
if (Test-Path $envFile) {
    Get-Content $envFile -Encoding UTF8 | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) { return }
        $i = $line.IndexOf("=")
        if ($i -lt 1) { return }
        $k = $line.Substring(0, $i).Trim()
        $v = $line.Substring($i + 1).Trim()
        if (-not [Environment]::GetEnvironmentVariable($k)) {
            [Environment]::SetEnvironmentVariable($k, $v)
        }
    }
}

$NasHost = $env:BAIZE_NAS_HOST
$NasUser = if ($env:BAIZE_NAS_USER) { $env:BAIZE_NAS_USER } else { "HD" }
$NasPass = $env:BAIZE_NAS_PASS
$Token   = $env:BAIZE_TOKEN
if (-not $NasHost) {
    throw "missing BAIZE_NAS_HOST. Put it in deploy.local.env (see .gitignore) or set the env var."
}
if (-not $NasPass) {
    throw "missing BAIZE_NAS_PASS. Put it in deploy.local.env (see .gitignore) or set the env var."
}

$Ssh        = Join-Path $Root "tools\sshrun\bin\sshrun.exe"
if (-not (Test-Path $Ssh)) { throw "sshrun not found: $Ssh" }
$Go         = if (Test-Path "D:\xm\tools\go\bin\go.exe") { "D:\xm\tools\go\bin\go.exe" } else { "go" }
$OutBin     = Join-Path $Root "agent\bin\baize-backend-linux-amd64"
$RemoteDir  = "/vol1/@appdata/baize/build"
$ComposeDir = "/vol1/@appdata/baize/compose"
$Image      = "baize-backend:$Tag"
# The NAS's built-in fnOS mirror returns 401 for library/*, so default to the daocloud
# mirror (verified reachable). Override with BAIZE_BASE_IMAGE.
$BaseImage  = if ($env:BAIZE_BASE_IMAGE) { $env:BAIZE_BASE_IMAGE } else { "docker.m.daocloud.io/library/alpine:3.20" }

function Ssh-Cmd([string]$Cmd) {
    & $Ssh -host $NasHost -user $NasUser -pass $NasPass -cmd $Cmd
    if ($LASTEXITCODE -ne 0) { throw "remote command failed (exit $LASTEXITCODE): $Cmd" }
}
function Ssh-Try([string]$Cmd) {
    & $Ssh -host $NasHost -user $NasUser -pass $NasPass -cmd $Cmd | Out-Null
}
function Ssh-Put([string]$Local, [string]$Remote) {
    & $Ssh -host $NasHost -user $NasUser -pass $NasPass -put "${Local}:${Remote}"
    if ($LASTEXITCODE -ne 0) { throw "upload failed: $Local -> $Remote" }
}
function Health {
    if (-not $Token) { return $true }
    try {
        # NOTE: ${NasHost} braces are required, otherwise PowerShell parses "$NasHost:8787" as a scoped variable.
        $h = Invoke-RestMethod -Uri "http://${NasHost}:8787/api/health" -Headers @{ "X-Baize-Token" = $Token } -TimeoutSec 10
        Write-Host ("  health: " + ($h | ConvertTo-Json -Compress)) -ForegroundColor Green
        return $true
    } catch {
        Write-Host ("  health failed: " + $_.Exception.Message) -ForegroundColor Red
        return $false
    }
}

Write-Host "=== 1. cross-compile (linux/amd64) ===" -ForegroundColor Cyan
if ($SkipBuild) {
    Write-Host "  skipped (-SkipBuild)"
} else {
    $g0 = $env:GOOS; $g1 = $env:GOARCH; $g2 = $env:CGO_ENABLED
    $env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
    & $Go build -C (Join-Path $Root "agent") -trimpath -ldflags "-s -w" -o $OutBin ./cmd/backend
    $env:GOOS = $g0; $env:GOARCH = $g1; $env:CGO_ENABLED = $g2
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host ("  built " + [math]::Round((Get-Item $OutBin).Length / 1MB, 2) + " MB -> $OutBin")
}

Write-Host "=== 2. upload to NAS ===" -ForegroundColor Cyan
Ssh-Cmd "mkdir -p $RemoteDir $ComposeDir"
# NOTE: remote paths must use forward slashes - Join-Path would emit a Windows backslash.
Ssh-Put $OutBin "$RemoteDir/baize-backend"
Ssh-Put (Join-Path $Root "Dockerfile.nas") "$RemoteDir/Dockerfile.nas"
Ssh-Put (Join-Path $Root "deploy\nas-docker-compose.yml") "$ComposeDir/docker-compose.yml"

Write-Host "=== 3. build image on the NAS ===" -ForegroundColor Cyan
Ssh-Cmd "cd $RemoteDir && echo $NasPass | sudo -S -p '' docker build --build-arg BASE_IMAGE=$BaseImage -f Dockerfile.nas -t $Image ."

if ($Push) {
    Write-Host "=== 3b. push to registry (-Push) ===" -ForegroundColor Cyan
    # one-time on the NAS: docker login ghcr.io -u <github-user> -p <PAT with write:packages>
    # NOTE: ${ImageName} braces are required, otherwise PowerShell parses "$ImageName:$Tag" as a scoped variable.
    $RegistryImage = "$Registry/${ImageName}:$Tag"
    Ssh-Cmd "cd $RemoteDir && echo $NasPass | sudo -S -p '' docker tag $Image $RegistryImage && echo $NasPass | sudo -S -p '' docker push $RegistryImage"
}

if ($NoRestart) {
    Write-Host "=== 4. no restart (-NoRestart), just verifying current backend ===" -ForegroundColor Yellow
    Health | Out-Null
} else {
    Write-Host "=== 4. stop old systemd unit (temporary) ===" -ForegroundColor Cyan
    Ssh-Try "echo $NasPass | sudo -S -p '' systemctl stop baize-backend 2>/dev/null; true"

    Write-Host "=== 5. docker compose up -d ===" -ForegroundColor Cyan
    Ssh-Cmd "cd $ComposeDir && echo $NasPass | sudo -S -p '' env BAIZE_IMAGE=$Image docker compose up -d"
    Start-Sleep -Seconds 4
    Ssh-Try "echo $NasPass | sudo -S -p '' docker ps --filter name=baize-backend --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'"

    Write-Host "=== 6. smoke test ===" -ForegroundColor Cyan
    if (Health) {
        Write-Host "=== 7. healthy -> disable old unit (no boot conflict) ===" -ForegroundColor Cyan
        Ssh-Try "echo $NasPass | sudo -S -p '' systemctl disable baize-backend 2>/dev/null; true"
    } else {
        Write-Host "=== ROLLBACK to systemd ===" -ForegroundColor Red
        Ssh-Try "cd $ComposeDir && echo $NasPass | sudo -S -p '' docker compose down 2>/dev/null; true"
        Ssh-Try "echo $NasPass | sudo -S -p '' systemctl start baize-backend 2>/dev/null; true"
        Ssh-Try "echo $NasPass | sudo -S -p '' systemctl is-active baize-backend"
        throw "container unhealthy; rolled back to the systemd service"
    }
}

Write-Host ""
Write-Host "DONE -> $Image on $NasHost" -ForegroundColor Green
