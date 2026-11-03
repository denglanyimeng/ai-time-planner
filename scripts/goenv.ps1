# 在本沙箱下运行 Go 必须重定向缓存目录（默认路径在工作区外，会被拒绝）
$proj = Split-Path -Parent $PSScriptRoot
$env:GOPATH     = Join-Path $proj '.gopath'
$env:GOCACHE    = Join-Path $proj '.gocache'
$env:GOMODCACHE = Join-Path $env:GOPATH 'pkg\mod'
$env:GOTELEMETRY = 'off'
$env:GOFLAGS    = '-mod=mod'
$env:GOPROXY    = 'https://goproxy.cn,direct'
foreach ($d in @($env:GOPATH, $env:GOCACHE)) { if (-not (Test-Path $d)) { New-Item -ItemType Directory -Force -Path $d | Out-Null } }
