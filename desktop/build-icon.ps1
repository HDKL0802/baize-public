<#
  build-icon.ps1 - turn desktop\assets\icon-src.png into the Windows app icon.

  Source: assets\icon-src.png  (the AI-generated artwork; it has a light outer margin
          around the dark tile, which this script removes).

  Steps:
    1. auto-detect the dark tile's bounding box (coarse scan for dark pixels)
    2. crop it to a square, draw it through a rounded-corner alpha mask
       (radius = 22.5% of the side, like a normal app icon)
    3. downscale to 16/24/32/48/64/128/256 and pack a PNG-compressed .ico
    4. also emit ui\icon.png (256) for the in-app brand mark

  Output: assets\icon.ico, ui\icon.png   (+ rsrc.syso when -Syso is passed)

  No ImageMagick / Node / Python needed - plain System.Drawing.
  Keep this file ASCII-only (PowerShell 5.1 reads .ps1 as ANSI).
#>
param([switch]$Syso)

$ErrorActionPreference = "Stop"
$Src      = $PSScriptRoot
$Raster   = Join-Path $Src "assets\icon-src.png"
$Ico      = Join-Path $Src "assets\icon.ico"
$UiPng    = Join-Path $Src "ui\icon.png"
$SysoPath = Join-Path $Src "rsrc.syso"

if (-not (Test-Path $Raster)) { throw "missing $Raster (the app icon source)" }

$stage = Join-Path $env:TEMP "baize-icon"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null
$big = Join-Path $stage "icon-1024.png"

Add-Type -AssemblyName System.Drawing

# ---------- crop the dark tile out of the source + rounded mask ----------
Write-Host "--- source: assets\icon-src.png (crop + rounded mask) ---" -ForegroundColor Cyan
$bmp = [System.Drawing.Bitmap]::FromFile((Resolve-Path $Raster).Path)
$w = $bmp.Width; $h = $bmp.Height

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

# ---------- downscale + pack the ICO ----------
$sizes = @(16, 24, 32, 48, 64, 128, 256)
$srcImg = [System.Drawing.Image]::FromFile($big)
$pngs = @{}
foreach ($s in $sizes) {
  $b2 = New-Object System.Drawing.Bitmap($s, $s, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  $g2 = [System.Drawing.Graphics]::FromImage($b2)
  $g2.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $g2.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
  $g2.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
  $g2.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
  $g2.Clear([System.Drawing.Color]::Transparent)
  $g2.DrawImage($srcImg, (New-Object System.Drawing.Rectangle(0, 0, $s, $s)))
  $g2.Dispose()
  $msPng = New-Object IO.MemoryStream
  $b2.Save($msPng, [System.Drawing.Imaging.ImageFormat]::Png)
  $b2.Dispose()
  $pngs[$s] = $msPng.ToArray()
  if ($s -eq 256) {
    # in-app brand mark
    [IO.File]::WriteAllBytes($UiPng, $pngs[$s])
  }
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
Write-Host ("OK -> {0} (256, for the in-app brand mark)" -f $UiPng) -ForegroundColor Green

if ($Syso) {
  $Go = if (Test-Path "D:\xm\tools\go\bin\go.exe") { "D:\xm\tools\go\bin\go.exe" } else { "go" }
  $env:GOPROXY = "https://goproxy.cn,direct"
  Write-Host "--- rsrc -> rsrc.syso ---" -ForegroundColor Cyan
  & $Go run github.com/akavel/rsrc@latest -ico $Ico -o $SysoPath -arch amd64
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path $SysoPath)) { throw "rsrc failed" }
  Write-Host ("OK -> {0} ({1} KB)" -f $SysoPath, [math]::Round((Get-Item $SysoPath).Length / 1KB, 1)) -ForegroundColor Green
}

Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
