# Build a Baize plugin source:  plugins/<id>/  ->  packages/<id>-<version>.zip + index.json
#
# NOTE: keep this file ASCII-only. PowerShell 5.1 reads .ps1 as ANSI, and non-ASCII bytes can be
#       mis-parsed (they can even swallow the following line). All human-readable Chinese lives in
#       source.json / README.md / the plugin files, which are read as UTF-8 explicitly below.
#
# Usage:  powershell -ExecutionPolicy Bypass -File tools\build.ps1
# Output: packages\*.zip  +  index.json   (commit both, then the repo can be served statically)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression.FileSystem

$root        = Split-Path -Parent $PSScriptRoot
$pluginsDir  = Join-Path $root 'plugins'
$packagesDir = Join-Path $root 'packages'
$indexPath   = Join-Path $root 'index.json'
$sourceCfg   = Join-Path $root 'source.json'
$idPattern   = '^[a-z0-9][a-z0-9._-]*$'

function Read-Utf8([string]$path) {
  return [System.IO.File]::ReadAllText($path, (New-Object System.Text.UTF8Encoding($false)))
}
function Write-Utf8([string]$path, [string]$text) {
  [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

$sourceName = 'plugin source'
if (Test-Path $sourceCfg) {
  $sc = Read-Utf8 $sourceCfg | ConvertFrom-Json
  if ($sc.name) { $sourceName = [string]$sc.name }
}

if (-not (Test-Path $pluginsDir)) { throw "no plugins/ directory under $root" }
if (Test-Path $packagesDir) { Remove-Item -LiteralPath $packagesDir -Recurse -Force }
New-Item -ItemType Directory -Path $packagesDir | Out-Null

$entries = New-Object System.Collections.ArrayList

foreach ($dir in (Get-ChildItem -LiteralPath $pluginsDir -Directory | Sort-Object Name)) {
  $id = $dir.Name
  if ($id -notmatch $idPattern) {
    throw "$id : plugin directory name not allowed (lowercase letters / digits / - / . / _ , starting with a letter or digit)"
  }

  $manifest = Join-Path $dir.FullName 'plugin.json'
  if (-not (Test-Path $manifest)) { throw "$id : missing plugin.json" }
  $m = Read-Utf8 $manifest | ConvertFrom-Json

  if (-not $m.id)      { throw "$id : plugin.json has no id" }
  if ($m.id -ne $id)   { throw "$id : plugin.json id is '$($m.id)' but the directory is '$id' (they must match)" }
  if (-not $m.name)    { throw "$id : plugin.json has no name" }
  if (-not $m.version) { throw "$id : plugin.json has no version" }

  # every skill must be a directory with a SKILL.md that carries a front-matter (name + description)
  $skillRoot = Join-Path $dir.FullName 'skills'
  if (-not (Test-Path $skillRoot)) { throw "$id : no skills/ directory (a plugin must bring at least one skill)" }
  $skillCount = 0
  foreach ($sk in (Get-ChildItem -LiteralPath $skillRoot -Directory)) {
    $skillCount++
    $skillMd = Join-Path $sk.FullName 'SKILL.md'
    if (-not (Test-Path $skillMd)) { throw "$id / $($sk.Name) : missing SKILL.md" }
    $text = Read-Utf8 $skillMd
    if (-not $text.TrimStart().StartsWith('---')) {
      throw "$id / $($sk.Name) : SKILL.md must start with a YAML front-matter (---)"
    }
    $fm = $text.Substring(0, [Math]::Min(800, $text.Length))
    if ($fm -notmatch '(?m)^\s*name:')        { throw "$id / $($sk.Name) : SKILL.md front-matter has no name" }
    if ($fm -notmatch '(?m)^\s*description:') { throw "$id / $($sk.Name) : SKILL.md front-matter has no description" }
  }
  if ($skillCount -eq 0) { throw "$id : skills/ is empty" }

  $zipName = "$id-$($m.version).zip"
  $zipPath = Join-Path $packagesDir $zipName
  Compress-Archive -Path (Join-Path $dir.FullName '*') -DestinationPath $zipPath -Force

  # sanity: plugin.json must sit at the zip root (Baize rejects a package without it)
  $z = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
  $hasManifest = $false
  foreach ($e in $z.Entries) { if ($e.FullName -eq 'plugin.json') { $hasManifest = $true } }
  $z.Dispose()
  if (-not $hasManifest) { throw "$id : the zip root has no plugin.json" }

  $sha  = (Get-FileHash -Algorithm SHA256 -LiteralPath $zipPath).Hash.ToLower()
  $size = (Get-Item -LiteralPath $zipPath).Length

  $entry = [ordered]@{ id = $id; name = [string]$m.name; version = [string]$m.version }
  foreach ($k in @('description', 'author', 'homepage', 'license', 'minBaize')) {
    if ($m.$k) { $entry[$k] = [string]$m.$k }
  }
  if ($m.tags -and @($m.tags).Count -gt 0) { $entry['tags'] = @($m.tags) }
  $entry['url']    = "packages/$zipName"
  $entry['sha256'] = $sha
  $entry['size']   = $size
  [void]$entries.Add($entry)

  "  $id  v$($m.version)  ->  packages/$zipName  ($size bytes, sha256 $($sha.Substring(0,12))...)"
}

if ($entries.Count -eq 0) { throw "no plugins found under plugins/ -- nothing to publish" }

$index = [ordered]@{
  schema  = 1
  name    = $sourceName
  updated = (Get-Date -Format 'yyyy-MM-dd')
  plugins = @($entries)
}
Write-Utf8 $indexPath ($index | ConvertTo-Json -Depth 6)

""
"OK: wrote $indexPath with $($entries.Count) plugin(s)"
"next: commit packages/ + index.json, push the repo, then add its raw index.json URL as a plugin source in Baize."
