# Baize agent MVP demo (ASCII only - PS 5.1 reads .ps1 as GBK when it contains CJK)
#
# Flow: backend starts -> desktop registers + reports window -> create fs.delete task
#       -> task stays pending_approval (approval gate) -> approve -> desktop executes
#       -> receipt -> task done -> memory persisted. Verifies the file was really deleted.
#       -> MCP hot plug: add a real stdio MCP server, list/call its tools, remove it (no restart)
#       -> backup: create a zip with sha256 manifest, verify, dry-run restore, delete
#
# Usage:  powershell -ExecutionPolicy Bypass -File run-demo.ps1
# Optional: -Port 8799  -KeepRunning

param(
  [int]$Port = 8799,
  [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$bin = Join-Path $root "bin"
$demo = Join-Path $root ".demo"
$base = "http://127.0.0.1:$Port"

function Step($m) { Write-Host ("[demo] " + $m) -ForegroundColor Cyan }
function Ok($m)   { Write-Host ("  OK   " + $m) -ForegroundColor Green }
function Bad($m)  { Write-Host ("  FAIL " + $m) -ForegroundColor Red }

function Find-Go {
  $cands = @(
    "D:\xm\tools\go\bin\go.exe",
    (Join-Path $env:ProgramFiles "Go\bin\go.exe"),
    "C:\Go\bin\go.exe"
  )
  foreach ($c in $cands) { if (Test-Path $c) { return $c } }
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  throw "go toolchain not found (looked for D:\xm\tools\go\bin\go.exe, %ProgramFiles%\Go\bin\go.exe, go in PATH)"
}

# PS 5.1 sends string bodies as non-UTF8 by default, which corrupts CJK paths.
# Post raw UTF-8 bytes and declare the charset explicitly.
function Send-Json($method, $uri, $obj, $headers) {
  $json = $obj | ConvertTo-Json -Depth 8
  $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
  return Invoke-RestMethod -Method $method -Uri $uri -Headers $headers `
    -ContentType "application/json; charset=utf-8" -Body $bytes
}

$backend = $null
$desktop = $null
$exitCode = 0

try {
  # ---------- 1. build ----------
  Step "building backend + desktop"
  $go = Find-Go
  New-Item -ItemType Directory -Force $bin | Out-Null
  Push-Location $root
  try {
    & $go build -o (Join-Path $bin "baize-backend.exe") ./cmd/backend
    if ($LASTEXITCODE -ne 0) { throw "backend build failed" }
    & $go build -o (Join-Path $bin "baize-desktop.exe") ./cmd/desktop
    if ($LASTEXITCODE -ne 0) { throw "desktop build failed" }
    & $go build -o (Join-Path $bin "mcpserver.exe") ./cmd/mcpserver
    if ($LASTEXITCODE -ne 0) { throw "mcpserver build failed" }
  } finally { Pop-Location }
  Ok ("binaries built with " + $go)

  # ---------- 2. prepare demo workspace ----------
  Step "preparing demo workspace (.demo)"
  if (Test-Path $demo) { Remove-Item $demo -Recurse -Force }
  $dataDir = Join-Path $demo "data"
  $tmpDir = Join-Path $demo "tmp"
  New-Item -ItemType Directory -Force $dataDir | Out-Null
  New-Item -ItemType Directory -Force $tmpDir | Out-Null
  $target = Join-Path $tmpDir "target.txt"
  Set-Content -Path $target -Value "this file must be deleted by the agent" -Encoding ASCII
  Ok ("target file created: " + $target)

  # ---------- 3. start backend ----------
  Step "starting backend on $base"
  $beOut = Join-Path $demo "backend.out.log"
  $beErr = Join-Path $demo "backend.err.log"
  $backend = Start-Process -FilePath (Join-Path $bin "baize-backend.exe") `
    -ArgumentList @("--addr", "127.0.0.1:$Port", "--data", $dataDir, "--log-level", "debug") `
    -RedirectStandardOutput $beOut -RedirectStandardError $beErr -PassThru -WindowStyle Hidden

  $tokenFile = Join-Path $dataDir "token"
  $token = $null
  for ($i = 0; $i -lt 40 -and -not $token; $i++) {
    Start-Sleep -Milliseconds 250
    if (Test-Path $tokenFile) { $token = (Get-Content $tokenFile -Raw).Trim() }
  }
  if (-not $token) { throw "backend did not produce a pairing token (see $beErr)" }
  $h = @{ "X-Baize-Token" = $token }

  $health = $null
  for ($i = 0; $i -lt 40 -and -not $health; $i++) {
    Start-Sleep -Milliseconds 250
    try { $health = Invoke-RestMethod -Uri "$base/api/health" -Headers $h -TimeoutSec 5 } catch { }
  }
  if (-not $health -or -not $health.ok) { throw "backend health check failed" }
  Ok ("backend up: version=" + $health.version + " protocol=" + $health.protocol + " token=" + $token.Substring(0,4) + "***")
  Ok ("console page: " + $base + "/")

  # ---------- 4. start desktop ----------
  Step "starting desktop client (allow-root limited to .demo\tmp)"
  $deOut = Join-Path $demo "desktop.out.log"
  $deErr = Join-Path $demo "desktop.err.log"
  $desktop = Start-Process -FilePath (Join-Path $bin "baize-desktop.exe") `
    -ArgumentList @("--server", $base, "--token", $token, "--interval", "2s", "--allow-root", $tmpDir) `
    -RedirectStandardOutput $deOut -RedirectStandardError $deErr -PassThru -WindowStyle Hidden

  $dev = $null
  for ($i = 0; $i -lt 60 -and -not $dev; $i++) {
    Start-Sleep -Milliseconds 300
    $st = Invoke-RestMethod -Uri "$base/api/state" -Headers $h
    $dev = $st.devices | Where-Object { $_.online } | Select-Object -First 1
  }
  if (-not $dev) { throw "device never came online (see $deErr)" }
  Ok ("device online: " + $dev.name + " [" + $dev.id + "] caps=" + ($dev.caps -join ","))

  Start-Sleep -Seconds 3
  $st = Invoke-RestMethod -Uri "$base/api/state?kind=event" -Headers $h
  if ($st.records.Count -gt 0) {
    Ok ("event reported: " + $st.records[0].payload.kind + " title=" + $st.records[0].payload.title)
  } else {
    Write-Host "  note no window event yet (headless session?)" -ForegroundColor Yellow
  }

  # ---------- 5. create dangerous task ----------
  Step "creating fs.delete task (dangerous action)"
  $body = @{
    deviceId = $dev.id
    action   = "fs.delete"
    args     = @{ paths = @($target) }
    origin   = "user"
  }
  $created = Send-Json "Post" "$base/api/tasks" $body $h
  $task = $created.task
  if ($task.status -ne "pending_approval") { throw ("gate failed: expected pending_approval, got " + $task.status) }
  Ok ("task " + $task.id + " status=pending_approval (blocked by approval gate)")

  Start-Sleep -Seconds 1
  if (-not (Test-Path $target)) { throw "approval gate failed: file was deleted before approval" }
  Ok "target file still exists before approval (gate really blocks execution)"

  # ---------- 6. approve ----------
  Step "approving the task"
  $appr = Send-Json "Post" ("$base/api/tasks/" + $task.id + "/approve") @{ by = "demo" } $h
  Ok ("approved by " + $appr.task.approvedBy + ", status=" + $appr.task.status)

  $final = $null
  for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 300
    $st = Invoke-RestMethod -Uri "$base/api/state" -Headers $h
    $final = $st.tasks | Where-Object { $_.id -eq $task.id } | Select-Object -First 1
    if ($final.status -eq "done" -or $final.status -eq "failed" -or $final.status -eq "rejected") { break }
  }
  if ($final.status -ne "done") { throw ("task did not finish: status=" + $final.status + " error=" + $final.error) }
  Ok ("task finished: status=done dispatchedAt=$($final.dispatchedAt) finishedAt=$($final.finishedAt)")
  Ok ("receipt result: " + ($final.result | ConvertTo-Json -Compress))

  # ---------- 7. verify ----------
  Step "verifying the file is really gone"
  if (Test-Path $target) { throw "file still exists - the deletion did NOT happen" }
  Ok "target file deleted for real"

  $st = Invoke-RestMethod -Uri "$base/api/state?records=200" -Headers $h
  $byKind = @{}
  foreach ($r in $st.records) { if ($byKind.ContainsKey($r.kind)) { $byKind[$r.kind]++ } else { $byKind[$r.kind] = 1 } }
  Ok ("records: " + (($byKind.GetEnumerator() | Sort-Object Name | ForEach-Object { $_.Key + "=" + $_.Value }) -join " "))
  Ok ("memories=" + $st.stats.memories + " tasks=" + $st.stats.tasks + " records=" + $st.stats.records)
  $mem = $st.memories | Where-Object { $_.taskId -eq $task.id } | Select-Object -First 1
  if ($mem) { Ok ("memory persisted: " + $mem.title + " | " + $mem.content) }

  # ---------- 8. MCP hot plug: real stdio MCP server ----------
  Step "adding a stdio MCP server (hot plug, no restart)"
  $mcpCfg = @{
    name       = "demo"
    transport  = "stdio"
    command    = (Join-Path $bin "mcpserver.exe")
    enabled    = $true
    safeTools  = @("hello", "now")
    timeoutSec = 20
  }
  $mcpUp = Send-Json "Post" "$base/api/agent/mcp" @{ action = "upsert"; config = $mcpCfg } $h
  $srv = $mcpUp.servers | Where-Object { $_.name -eq "demo" } | Select-Object -First 1
  if (-not $srv) { throw "MCP server was not saved" }
  if ($srv.status -ne "connected") { throw ("MCP not connected: status=" + $srv.status + " error=" + $srv.error) }
  if ($srv.toolCount -ne 2) { throw ("MCP tool count mismatch: " + $srv.toolCount) }
  Ok ("MCP connected: " + $srv.serverName + " " + $srv.serverVersion + " protocol=" + $srv.protocol + " tools=" + ($srv.tools -join ","))

  $called = Send-Json "Post" "$base/api/agent/mcp" @{ action = "call"; server = "demo"; tool = "hello"; args = @{ name = "baize" } } $h
  if ($called.error) { throw ("MCP tool call failed: " + $called.error) }
  if ($called.result.text -notlike "*baize*") { throw ("MCP tool call result unexpected: " + $called.result.text) }
  Ok ("MCP tool call -> " + $called.result.text)

  $removed = Send-Json "Post" "$base/api/agent/mcp" @{ action = "remove"; server = "demo" } $h
  $still = $removed.servers | Where-Object { $_.name -eq "demo" }
  if ($still) { throw "MCP server was not removed" }
  Ok "MCP server removed (hot unplug, backend never restarted)"

  # ---------- 9. backup / verify / dry-run restore ----------
  Step "backup + verify + dry-run restore"
  $bk = Send-Json "Post" "$base/api/agent/backups" @{ note = "demo backup" } $h
  if ($bk.manifest.entries.Count -eq 0) { throw "backup contains no entries" }
  Ok ("backup created: files=" + $bk.manifest.entries.Count + " bytes=" + $bk.manifest.totalSize + " dir=" + $bk.dir)

  $bkList = Invoke-RestMethod -Uri "$base/api/agent/backups" -Headers $h
  $bkName = $bkList.backups[0].name
  $ver = Send-Json "Post" "$base/api/agent/backups/verify" @{ path = $bkName } $h
  if (-not $ver.ok) { throw "backup verify failed" }
  Ok ("backup verified (sha256 manifest ok): " + $bkName + " entries=" + $ver.manifest.entries.Count)

  $dry = Send-Json "Post" "$base/api/agent/backups/restore" @{ path = $bkName; dryRun = $true } $h
  if ($dry.error) { throw ("dry-run restore failed: " + $dry.error) }
  Ok ("dry-run restore ok: " + $dry.result.restored.Count + " files reported, data untouched")

  # restore while the backend holds the SQLite files must fail loudly with advice (never silently)
  $real = Send-Json "Post" "$base/api/agent/backups/restore" @{ path = $bkName } $h
  if (-not $real.error) { throw "restore while the backend is running should have been refused" }
  Ok ("restore refused while DBs are locked (as designed): " + $real.error.Substring(0, [Math]::Min(70, $real.error.Length)))

  $null = Send-Json "Post" "$base/api/agent/backups/delete" @{ path = $bkName } $h
  Ok "backup deleted"

  Write-Host ""
  Write-Host "DEMO PASSED" -ForegroundColor Green
  Write-Host ("artifacts: db=" + (Join-Path $dataDir "agent.db") + "  logs=" + $dataDir)
}
catch {
  Bad $_.Exception.Message
  $exitCode = 1
}
finally {
  if (-not $KeepRunning) {
    if ($desktop -and -not $desktop.HasExited) { Stop-Process -Id $desktop.Id -Force -ErrorAction SilentlyContinue }
    if ($backend -and -not $backend.HasExited) { Stop-Process -Id $backend.Id -Force -ErrorAction SilentlyContinue }
    Step "stopped backend + desktop"
  } else {
    Step "left running (--KeepRunning): console $base/ ; stop with: Stop-Process -Id $($backend.Id),$($desktop.Id)"
  }
}

exit $exitCode
