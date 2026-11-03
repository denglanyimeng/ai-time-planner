# Creates (or refreshes) the desktop shortcut for the time planner.
#
#   powershell -ExecutionPolicy Bypass -File .\tools\install-shortcut.ps1
#   powershell -ExecutionPolicy Bypass -File .\tools\install-shortcut.ps1 -Startup
#
# -Startup additionally drops a copy into the current user's Startup folder,
# so the server is already warm the next time Windows boots.

param(
  [switch]$Startup,
  [string]$Name = '时间规划'
)

$ErrorActionPreference = 'Stop'

$proj = Split-Path -Parent $PSScriptRoot
$launcher = Join-Path $proj '打开时间规划.vbs'
$icon = Join-Path $proj 'web\assets\favicon.ico'

if (-not (Test-Path $launcher)) { throw "launcher not found: $launcher" }
if (-not (Test-Path $icon)) { throw "icon not found: $icon - run tools\make-icon.ps1 first" }

function New-Shortcut([string]$folder, [string]$label) {
  $path = Join-Path $folder ($label + '.lnk')
  $ws = New-Object -ComObject WScript.Shell
  $sc = $ws.CreateShortcut($path)
  # 直接指向 .vbs：Windows 会用 wscript 打开它，全程不弹控制台窗口
  $sc.TargetPath = $launcher
  $sc.WorkingDirectory = $proj
  $sc.IconLocation = "$icon,0"
  $sc.Description = '时间规划 · 本地运行的 AI 时间规划助手'
  $sc.WindowStyle = 1
  $sc.Save()
  return $path
}

$desktop = [Environment]::GetFolderPath('Desktop')
$made = @(New-Shortcut $desktop $Name)

if ($Startup) {
  $startupDir = [Environment]::GetFolderPath('Startup')
  $made += New-Shortcut $startupDir $Name
}

$made | ForEach-Object { Write-Host "已创建: $_" }
Write-Host "图标: $icon"
