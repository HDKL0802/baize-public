<#
  build-icon.ps1 - rasterize desktop/assets/icon.svg into a multi-size Windows .ico,
                   and (with -Syso) regenerate rsrc.syso so the exe carries the icon.

  Uses Edge headless for SVG -> PNG (this box has no ImageMagick / Node / Python).
  Output: desktop\assets\icon.ico        (+ desktop\rsrc.syso with -Syso)

  Keep this file ASCII-only (PowerShell 5.1 reads .ps1 as ANSI).
#>
param([switch]$Syso)

$ErrorActionPreference = "Stop"
$Src  = $PSScriptRoot
$Svg  = Join-Path $Src "assets\icon.svg"
$Ico  = Join-Path $Src "assets\icon.ico"
$SysoPath = Join-Path $Src "rsrc.syso"

if (-not (Test-Path $Svg)) { throw "missing $Svg" }

# --- locate Edge ---
$Edge = $null
foreach ($c in @("C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
                 "C:\Program Files\Microsoft\Edge\Application\msedge.exe")) {
  if (Test-Path $c) { $Edge = $c; break }
}
if (-not $Edge) { throw "msedge.exe not found (needed to rasterize the SVG)" }

# --- stage an HTML wrapper that draws the SVG full-bleed ---
# Inline the SVG (not <img src>): an <img> keeps the SVG's intrinsic 512x512 and
# then just gets cropped. Inlining lets one CSS rule scale it to any viewport.
$stage = Join-Path $env:TEMP "baize-icon"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null
$svgText = [IO.File]::ReadAllText($Svg, [Text.Encoding]::UTF8)
$html = @"
<!doctype html><meta charset="utf-8">
<style>html,body{margin:0;padding:0;width:100%;height:100%;overflow:hidden;background:transparent}
svg{display:block;width:100%;height:100%}</style>
$svgText
"@
[IO.File]::WriteAllText((Join-Path $stage "icon.html"), $html, (New-Object Text.UTF8Encoding($false)))

$page = "file:///" + ((Join-Path $stage "icon.html") -replace '\\', '/')
$sizes = @(16, 24, 32, 48, 64, 128, 256)

# Render ONE large PNG, then downscale locally.
# Do NOT render each size directly: Edge's headless window has a minimum width
# (~500px), so --window-size=32,32 actually gives a wider viewport and the shot
# comes back cropped to the top-left corner (踩过).
$big = Join-Path $stage "icon-1024.png"
$prevEAP = $ErrorActionPreference
$ErrorActionPreference = "Continue"   # Edge chatters on stderr; Stop would make it fatal
& $Edge --headless=new --disable-gpu --no-sandbox --hide-scrollbars --log-level=3 `
    "--user-data-dir=$env:TEMP\baize-icon-edge" "--window-size=1024,1024" `
    --default-background-color=00000000 `
    "--screenshot=$big" $page 2>$null | Out-Null
$ErrorActionPreference = $prevEAP
if (-not (Test-Path $big)) { throw "Edge failed to render the icon" }

Add-Type -AssemblyName System.Drawing
$src = [System.Drawing.Image]::FromFile($big)
$pngs = @{}
foreach ($s in $sizes) {
  $bmp = New-Object System.Drawing.Bitmap($s, $s, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
  $g.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
  $g.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
  $g.Clear([System.Drawing.Color]::Transparent)
  $g.DrawImage($src, (New-Object System.Drawing.Rectangle(0, 0, $s, $s)))
  $g.Dispose()
  $msPng = New-Object IO.MemoryStream
  $bmp.Save($msPng, [System.Drawing.Imaging.ImageFormat]::Png)
  $bmp.Dispose()
  $pngs[$s] = $msPng.ToArray()
}
$src.Dispose()

# --- assemble a PNG-compressed ICO (Vista+ renders these fine) ---
$ms = New-Object IO.MemoryStream
$bw = New-Object IO.BinaryWriter($ms)
$bw.Write([UInt16]0)                      # reserved
$bw.Write([UInt16]1)                      # type = icon
$bw.Write([UInt16]$sizes.Count)           # image count
$offset = 6 + 16 * $sizes.Count
$blobs = @()
foreach ($s in $sizes) {
  $bytes = $pngs[$s]
  $blobs += ,$bytes
  $dim = 0
  if ($s -lt 256) { $dim = $s }            # 0 means 256 in the ICO directory
  $bw.Write([Byte]$dim); $bw.Write([Byte]$dim)
  $bw.Write([Byte]0); $bw.Write([Byte]0)   # palette count, reserved
  $bw.Write([UInt16]1)                     # color planes
  $bw.Write([UInt16]32)                    # bits per pixel
  $bw.Write([UInt32]$bytes.Length)
  $bw.Write([UInt32]$offset)
  $offset += $bytes.Length
}
foreach ($b in $blobs) { $bw.Write($b) }
$bw.Flush()
[IO.File]::WriteAllBytes($Ico, $ms.ToArray())
$bw.Dispose(); $ms.Dispose()
Write-Host ("OK -> {0} ({1} sizes, {2} KB)" -f $Ico, $sizes.Count, [math]::Round((Get-Item $Ico).Length / 1KB, 1)) -ForegroundColor Green

# --- optional: regenerate the Windows resource object so the exe carries the icon ---
if ($Syso) {
  $Go = if (Test-Path "D:\xm\tools\go\bin\go.exe") { "D:\xm\tools\go\bin\go.exe" } else { "go" }
  $env:GOPROXY = "https://goproxy.cn,direct"
  Write-Host "--- rsrc -> rsrc.syso ---" -ForegroundColor Cyan
  & $Go run github.com/akavel/rsrc@latest -ico $Ico -o $SysoPath -arch amd64
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path $SysoPath)) { throw "rsrc failed" }
  Write-Host ("OK -> {0} ({1} KB)" -f $SysoPath, [math]::Round((Get-Item $SysoPath).Length / 1KB, 1)) -ForegroundColor Green
}

Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
