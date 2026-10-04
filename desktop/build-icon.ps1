<#
  build-icon.ps1 - produce desktop\assets\icon.ico (+ rsrc.syso) for the Windows app icon.

  Two sources, in priority order:
    1. assets\icon-src.png  - a raster artwork (e.g. AI-generated). It gets auto-cropped to
                              the dark square and given a rounded-corner alpha mask, so it
                              becomes a clean app-icon tile regardless of the source margins.
    2. assets\icon.svg      - the vector fallback, rasterized with headless Edge.

  Then the tile is downscaled to 16/24/32/48/64/128/256 and packed into a PNG-compressed .ico.
  No ImageMagick / Node / Python needed.

  Output: desktop\assets\icon.ico   (+ desktop\rsrc.syso when -Syso is passed)

  Keep this file ASCII-only (PowerShell 5.1 reads .ps1 as ANSI).
#>
param([switch]$Syso)

$ErrorActionPreference = "Stop"
$Src      = $PSScriptRoot
$Svg      = Join-Path $Src "assets\icon.svg"
$Raster   = Join-Path $Src "assets\icon-src.png"
$Ico      = Join-Path $Src "assets\icon.ico"
$SysoPath = Join-Path $Src "rsrc.syso"

$stage = Join-Path $env:TEMP "baize-icon"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null
$big = Join-Path $stage "icon-1024.png"

Add-Type -AssemblyName System.Drawing

if (Test-Path $Raster) {
  # ---------- 1) raster source: crop to the dark tile + rounded mask ----------
  Write-Host "--- source: assets\icon-src.png (crop + rounded mask) ---" -ForegroundColor Cyan
  $bmp = [System.Drawing.Bitmap]::FromFile((Resolve-Path $Raster).Path)
  $w = $bmp.Width; $h = $bmp.Height

  # locate the dark tile: first/last row+col containing dark pixels
  $minX = $w; $minY = $h; $maxX = -1; $maxY = -1
  for ($y = 0; $y -lt $h; $y += 2) {
    for ($x = 0; $x -lt $w; $x += 2) {
      $c = $bmp.GetPixel($x, $y)
      $lum = 0.299 * $c.R + 0.587 * $c.G + 0.114 * $c.B
      if ($lum -lt 110) {
        if ($x -lt $minX) { $minX = $x }
        if ($x -gt $maxX) { $maxX = $x }
        if ($y -lt $minY) { $minY = $y }
        if ($y -gt $maxY) { $maxY = $y }
      }
    }
  }
  if ($maxX -lt 0) { throw "could not find the dark tile in $Raster" }

  $cx = ($minX + $maxX) / 2.0
  $cy = ($minY + $maxY) / 2.0
  $side = [Math]::Max($maxX - $minX, $maxY - $minY) + 10
  if ($side -gt $w) { $side = $w }
  if ($side -gt $h) { $side = $h }
  $sx = [int]($cx - $side / 2); $sy = [int]($cy - $side / 2)
  if ($sx -lt 0) { $sx = 0 }; if ($sy -lt 0) { $sy = 0 }
  if ($sx + $side -gt $w) { $sx = $w - $side }
  if ($sy + $side -gt $h) { $sy = $h - $side }
  Write-Host ("tile bbox = {0},{1} {2}x{3}" -f $sx, $sy, $side, $side)

  $N = 1024
  $out = New-Object System.Drawing.Bitmap($N, $N, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  $g = [System.Drawing.Graphics]::FromImage($out)
  $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
  $g.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
  $g.Clear([System.Drawing.Color]::Transparent)

  # squircle-ish rounded rect clip (radius ~22.5% of the side, like an app icon)
  $r = [int]($N * 0.225)
  $path = New-Object System.Drawing.Drawing2D.GraphicsPath
  $path.AddArc(0, 0, $r * 2, $r * 2, 180, 90)
  $path.AddArc($N - $r * 2, 0, $r * 2, $r * 2, 270, 90)
  $path.AddArc($N - $r * 2, $N - $r * 2, $r * 2, $r * 2, 0, 90)
  $path.AddArc(0, $N - $r * 2, $r * 2, $r * 2, 90, 90)
  $path.CloseFigure()
  $g.SetClip($path)
  $g.DrawImage($bmp, (New-Object System.Drawing.Rectangle(0, 0, $N, $N)), (New-Object System.Drawing.Rectangle($sx, $sy, $side, $side)), [System.Drawing.GraphicsUnit]::Pixel)
  $g.Dispose()
  $out.Save($big, [System.Drawing.Imaging.ImageFormat]::Png)
  $out.Dispose()
  $bmp.Dispose()
  $path.Dispose()
} else {
  # ---------- 2) vector fallback via headless Edge ----------
  if (-not (Test-Path $Svg)) { throw "need either assets\icon-src.png or assets\icon.svg" }
  $Edge = $null
  foreach ($c in @("C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
                   "C:\Program Files\Microsoft\Edge\Application\msedge.exe")) {
    if (Test-Path $c) { $Edge = $c; break }
  }
  if (-not $Edge) { throw "msedge.exe not found (needed to rasterize the SVG)" }

  Write-Host "--- source: assets\icon.svg (headless Edge) ---" -ForegroundColor Cyan
  # Inline the SVG (not <img src>): an <img> keeps the SVG's intrinsic 512x512 and just
  # gets cropped. Inlining lets one CSS rule scale it to any viewport.
  $svgText = [IO.File]::ReadAllText($Svg, [Text.Encoding]::UTF8)
  $html = @"
<!doctype html><meta charset="utf-8">
<style>html,body{margin:0;padding:0;width:100%;height:100%;overflow:hidden;background:transparent}
svg{display:block;width:100%;height:100%}</style>
$svgText
"@
  [IO.File]::WriteAllText((Join-Path $stage "icon.html"), $html, (New-Object Text.UTF8Encoding($false)))
  $page = "file:///" + ((Join-Path $stage "icon.html") -replace '\\', '/')
  # Render ONE large PNG: Edge's headless window has a minimum width (~500px), so rendering
  # each small size directly comes back cropped to the top-left corner.
  $prevEAP = $ErrorActionPreference
  $ErrorActionPreference = "Continue"   # Edge chatters on stderr; Stop would make it fatal
  & $Edge --headless=new --disable-gpu --no-sandbox --hide-scrollbars --log-level=3 `
      "--user-data-dir=$env:TEMP\baize-icon-edge" "--window-size=1024,1024" `
      --default-background-color=00000000 "--screenshot=$big" $page 2>$null | Out-Null
  $ErrorActionPreference = $prevEAP
  if (-not (Test-Path $big)) { throw "Edge failed to render the icon" }
}

# ---------- downscale to every size and pack the ICO ----------
$sizes = @(16, 24, 32, 48, 64, 128, 256)
$srcImg = [System.Drawing.Image]::FromFile($big)
$pngs = @{}
foreach ($s in $sizes) {
  $bmp2 = New-Object System.Drawing.Bitmap($s, $s, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  $g2 = [System.Drawing.Graphics]::FromImage($bmp2)
  $g2.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $g2.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
  $g2.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
  $g2.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
  $g2.Clear([System.Drawing.Color]::Transparent)
  $g2.DrawImage($srcImg, (New-Object System.Drawing.Rectangle(0, 0, $s, $s)))
  $g2.Dispose()
  $msPng = New-Object IO.MemoryStream
  $bmp2.Save($msPng, [System.Drawing.Imaging.ImageFormat]::Png)
  $bmp2.Dispose()
  $pngs[$s] = $msPng.ToArray()
}
$srcImg.Dispose()

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

if ($Syso) {
  $Go = if (Test-Path "D:\xm\tools\go\bin\go.exe") { "D:\xm\tools\go\bin\go.exe" } else { "go" }
  $env:GOPROXY = "https://goproxy.cn,direct"
  Write-Host "--- rsrc -> rsrc.syso ---" -ForegroundColor Cyan
  & $Go run github.com/akavel/rsrc@latest -ico $Ico -o $SysoPath -arch amd64
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path $SysoPath)) { throw "rsrc failed" }
  Write-Host ("OK -> {0} ({1} KB)" -f $SysoPath, [math]::Round((Get-Item $SysoPath).Length / 1KB, 1)) -ForegroundColor Green
}

Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
