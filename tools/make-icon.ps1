# Regenerates the app icon (PNG set + multi-size .ico) with System.Drawing.
# ASCII-only on purpose: PowerShell 5.1 parses BOM-less scripts as ANSI.
#
# Art: dark rounded tile, a faint full ring (the track), a lime 3/4 arc,
# and a thin inner ring. Same visual language as the glass theme hero dial.

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$proj = Split-Path -Parent $PSScriptRoot
$outDir = Join-Path $proj 'web\assets'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$S = 512                                  # master canvas size
$bmp = New-Object System.Drawing.Bitmap($S, $S)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
$g.Clear([System.Drawing.Color]::Transparent)

function New-RoundRect([int]$x, [int]$y, [int]$w, [int]$h, [int]$r) {
  $p = New-Object System.Drawing.Drawing2D.GraphicsPath
  $d = $r * 2
  $p.AddArc($x, $y, $d, $d, 180, 90)
  $p.AddArc($x + $w - $d, $y, $d, $d, 270, 90)
  $p.AddArc($x + $w - $d, $y + $h - $d, $d, $d, 0, 90)
  $p.AddArc($x, $y + $h - $d, $d, $d, 90, 90)
  $p.CloseFigure()
  return $p
}

# ---------- tile ----------
$tile = New-RoundRect 16 16 480 480 116
$rect = New-Object System.Drawing.Rectangle(16, 16, 480, 480)
$brush = New-Object System.Drawing.Drawing2D.LinearGradientBrush(
  $rect,
  [System.Drawing.Color]::FromArgb(255, 26, 30, 37),
  [System.Drawing.Color]::FromArgb(255, 10, 12, 15),
  90.0)
$g.FillPath($brush, $tile)

# hairline rim
$rimPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(46, 255, 255, 255), 3.0)
$g.DrawPath($rimPen, $tile)

# ---------- dial ----------
$cx = 256; $cy = 256
$r = 148
$box = New-Object System.Drawing.Rectangle(($cx - $r), ($cy - $r), ($r * 2), ($r * 2))

# track
$trackPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(40, 255, 255, 255), 30.0)
$trackPen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
$trackPen.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
$g.DrawArc($trackPen, $box, 0, 360)

# lime progress arc (from 12 o'clock, 270 degrees)
$arcPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(255, 201, 255, 74), 30.0)
$arcPen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
$arcPen.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
$g.DrawArc($arcPen, $box, -90, 270)

# inner thin ring
$ir = 74
$ibox = New-Object System.Drawing.Rectangle(($cx - $ir), ($cy - $ir), ($ir * 2), ($ir * 2))
$innerPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(30, 255, 255, 255), 8.0)
$g.DrawEllipse($innerPen, $ibox)

# center dot
$dotBrush = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 201, 255, 74))
$g.FillEllipse($dotBrush, ($cx - 15), ($cy - 15), 30, 30)

$g.Dispose()

# ---------- export ----------
$sizes = @(256, 128, 64, 48, 32, 16)
$pngBytes = @{}

foreach ($sz in $sizes) {
  $small = New-Object System.Drawing.Bitmap($sz, $sz)
  $sg = [System.Drawing.Graphics]::FromImage($small)
  $sg.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $sg.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
  $sg.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
  $sg.Clear([System.Drawing.Color]::Transparent)
  $sg.DrawImage($bmp, 0, 0, $sz, $sz)
  $sg.Dispose()

  $ms = New-Object System.IO.MemoryStream
  $small.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
  $pngBytes[$sz] = $ms.ToArray()
  $ms.Dispose()

  if ($sz -in @(192, 512)) { }
  $small.Dispose()
}

# standalone PNGs (favicon / touch icon)
foreach ($sz in @(64, 192, 512)) {
  $small = New-Object System.Drawing.Bitmap($sz, $sz)
  $sg = [System.Drawing.Graphics]::FromImage($small)
  $sg.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $sg.Clear([System.Drawing.Color]::Transparent)
  $sg.DrawImage($bmp, 0, 0, $sz, $sz)
  $sg.Dispose()
  $small.Save((Join-Path $outDir ("icon-" + $sz + ".png")), [System.Drawing.Imaging.ImageFormat]::Png)
  $small.Dispose()
}

# ---------- assemble .ico (PNG-compressed entries) ----------
$fs = New-Object System.IO.MemoryStream
$bw = New-Object System.IO.BinaryWriter($fs)

$bw.Write([UInt16]0)                    # reserved
$bw.Write([UInt16]1)                    # type: icon
$bw.Write([UInt16]$sizes.Count)

$offset = 6 + (16 * $sizes.Count)
foreach ($sz in $sizes) {
  $data = $pngBytes[$sz]
  $dim = if ($sz -ge 256) { 0 } else { $sz }
  $bw.Write([Byte]$dim)                 # width (0 = 256)
  $bw.Write([Byte]$dim)                 # height
  $bw.Write([Byte]0)                    # palette
  $bw.Write([Byte]0)                    # reserved
  $bw.Write([UInt16]1)                  # planes
  $bw.Write([UInt16]32)                 # bpp
  $bw.Write([UInt32]$data.Length)       # size
  $bw.Write([UInt32]$offset)            # offset
  $offset += $data.Length
}
foreach ($sz in $sizes) { $bw.Write($pngBytes[$sz]) }
$bw.Flush()

[System.IO.File]::WriteAllBytes((Join-Path $outDir 'favicon.ico'), $fs.ToArray())
$bw.Dispose(); $fs.Dispose(); $bmp.Dispose()

Get-ChildItem $outDir -Filter 'icon-*.png' | Select-Object Length, Name
Get-Item (Join-Path $outDir 'favicon.ico') | Select-Object Length, Name
