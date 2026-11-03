# ============================================================
# 端到端验证脚本（兼容 Windows PowerShell 5.1 与 PowerShell 7）
#
# 用法：
#   1. 先启动服务（任选一种模式）：
#        MySQL 模式 ： go run ./cmd/server
#        SQLite 模式： $env:DB_DRIVER='sqlite'; go run ./cmd/server
#   2. 另一个终端运行：
#        powershell -ExecutionPolicy Bypass -File .\scripts\check-e2e.ps1
#        powershell -ExecutionPolicy Bypass -File .\scripts\check-e2e.ps1 -Base http://127.0.0.1:8099
#
# 会按真实业务流程依次调用接口并逐项打印结果。
# 注意：会往目标数据库写入测试数据（结束时自动清理），请勿对生产库运行。
#
# 重要：本文件必须以 UTF-8 with BOM 保存。
# Windows PowerShell 5.1 对无 BOM 的脚本按系统 ANSI 代码页解析，中文会乱码。
# ============================================================
param([string]$Base = 'http://127.0.0.1:8080', [switch]$Force)

$ErrorActionPreference = 'Stop'
$script:pass = 0
$script:fail = 0
$script:failures = @()

function Section([string]$title) {
    Write-Host ""
    Write-Host ("== " + $title) -ForegroundColor Cyan
}

function Check([string]$name, [scriptblock]$body) {
    try {
        $result = & $body
        if ($result) {
            Write-Host ("  [PASS] " + $name) -ForegroundColor Green
            $script:pass++
        } else {
            Write-Host ("  [FAIL] " + $name) -ForegroundColor Red
            $script:fail++
            $script:failures += $name
        }
    } catch {
        Write-Host ("  [FAIL] " + $name + "  -> " + $_.Exception.Message) -ForegroundColor Red
        $script:fail++
        $script:failures += $name
    }
}

# Raw 发起请求，返回状态码 + UTF-8 解码后的响应文本（或下载文件）。
#
# 这里刻意不使用 Invoke-WebRequest：
# Windows PowerShell 5.1 的 Invoke-WebRequest 处理文本响应时会忽略
# Content-Type 里的 charset，改用系统 ANSI 代码页（中文系统即 GBK）解码。
# 后果是服务端返回的 UTF-8 中文全部乱码、ConvertFrom-Json 随之解析失败，
# 而且请求体里的中文会被替换成 "?"。
# 直接用 HttpWebRequest 并显式指定 UTF-8，可彻底避开这两个问题。
function Raw([string]$method, [string]$path, $body, [string]$outFile) {
    $req = [System.Net.WebRequest]::Create($Base + $path)
    $req.Method = $method
    $req.Timeout = 120000

    if ($null -ne $body) {
        $req.ContentType = 'application/json; charset=utf-8'
        $json = $body | ConvertTo-Json -Depth 10 -Compress
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
        $req.ContentLength = $bytes.Length
        $stream = $req.GetRequestStream()
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.Close()
    }

    $resp = $null
    try {
        $resp = $req.GetResponse()
    } catch [System.Net.WebException] {
        $resp = $_.Exception.Response
        if ($null -eq $resp) { throw }
    }

    $status = [int]$resp.StatusCode
    $text = ''
    if ($outFile) {
        $fs = [System.IO.File]::Create($outFile)
        $rs = $resp.GetResponseStream()
        $rs.CopyTo($fs)
        $fs.Close()
        $rs.Close()
    } else {
        $sr = New-Object System.IO.StreamReader($resp.GetResponseStream(), [System.Text.Encoding]::UTF8)
        $text = $sr.ReadToEnd()
        $sr.Close()
    }
    $resp.Close()

    return [pscustomobject]@{ Status = $status; Content = $text }
}

# Api 发起 JSON 请求并解包统一响应信封，4xx/5xx 不抛异常。
function Api([string]$method, [string]$path, $body) {
    $r = Raw $method $path $body $null
    $json = $null
    try { $json = $r.Content | ConvertFrom-Json } catch { }
    return [pscustomobject]@{
        Status = $r.Status
        Ok     = if ($json) { $json.ok } else { $false }
        Data   = if ($json) { $json.data } else { $null }
        Error  = if ($json) { $json.error } else { $null }
        Raw    = $r.Content
    }
}

# 上传备份包（multipart/form-data），响应同样按 UTF-8 解码。
function UploadBackup([string]$file) {
    $boundary = [System.Guid]::NewGuid().ToString()
    $enc = [System.Text.Encoding]::GetEncoding('iso-8859-1')
    $fileBytes = [System.IO.File]::ReadAllBytes($file)

    $ms = New-Object System.IO.MemoryStream
    $head = "--$boundary`r`n" +
            "Content-Disposition: form-data; name=`"file`"; filename=`"backup.zip`"`r`n" +
            "Content-Type: application/zip`r`n`r`n"
    $tail = "`r`n--$boundary--`r`n"
    $hb = $enc.GetBytes($head)
    $tb = $enc.GetBytes($tail)
    $ms.Write($hb, 0, $hb.Length)
    $ms.Write($fileBytes, 0, $fileBytes.Length)
    $ms.Write($tb, 0, $tb.Length)

    $req = [System.Net.WebRequest]::Create($Base + '/api/import/backup')
    $req.Method = 'POST'
    $req.ContentType = "multipart/form-data; boundary=$boundary"
    $req.Timeout = 120000

    $bytes = $ms.ToArray()
    $req.ContentLength = $bytes.Length
    $stream = $req.GetRequestStream()
    $stream.Write($bytes, 0, $bytes.Length)
    $stream.Close()

    $resp = $null
    try {
        $resp = $req.GetResponse()
    } catch [System.Net.WebException] {
        $resp = $_.Exception.Response
        if ($null -eq $resp) { throw }
    }

    $status = [int]$resp.StatusCode
    $sr = New-Object System.IO.StreamReader($resp.GetResponseStream(), [System.Text.Encoding]::UTF8)
    $text = $sr.ReadToEnd()
    $sr.Close()
    $resp.Close()

    return [pscustomobject]@{ Status = $status; Content = $text }
}

# ============================================================
Write-Host ""
Write-Host "AI 智能时间规划效率助手 —— 端到端验证" -ForegroundColor White
Write-Host ("目标：" + $Base)
Write-Host ("PowerShell：" + $PSVersionTable.PSVersion)

# ---------- 等待服务就绪 ----------
$ready = $false
for ($i = 0; $i -lt 40; $i++) {
    try {
        $h = Raw 'GET' '/api/health' $null $null
        if ($h.Status -eq 200) { $ready = $true; break }
    } catch { }
    Start-Sleep -Milliseconds 500
}
if (-not $ready) {
    Write-Host "服务未就绪，请先启动服务再运行本脚本。" -ForegroundColor Red
    exit 1
}

# ---------- 安全检查：绝不在含真实数据的库上运行 ----------
# 本脚本会写入测试数据，其中「开始计时」按设计会暂停其它正在计时的任务
# （单用户同时只允许一个任务计时）。因此它在真实数据上运行会干扰用户正在做的事。
if (-not $Force) {
    $probe = Api 'GET' '/api/tasks' $null
    $existing = @($probe.Data).Count
    if ($existing -gt 0) {
        Write-Host ""
        Write-Host ("[拒绝执行] 目标库当天已有 " + $existing + " 条任务。") -ForegroundColor Red
        Write-Host "本脚本会写入测试数据，且「开始计时」会暂停其它正在计时的任务，" -ForegroundColor Red
        Write-Host "在真实数据上运行会把你正在做的事暂停掉。" -ForegroundColor Red
        Write-Host ""
        Write-Host "请改用独立数据库，例如：" -ForegroundColor Yellow
        Write-Host '  $env:DB_DRIVER="sqlite"; $env:DB_PATH="data/e2e-tmp.db"; $env:PORT="8099"' -ForegroundColor Gray
        Write-Host '  go run ./cmd/server' -ForegroundColor Gray
        Write-Host "确实要在当前库上运行，请显式加 -Force 参数。" -ForegroundColor Yellow
        exit 1
    }
}
Section "1. 基础与前端资源"
$health = Api 'GET' '/api/health' $null
Check "健康检查返回 200" { $health.Status -eq 200 }
Check "健康检查包含今天日期" { $health.Data.today -match '^\d{4}-\d{2}-\d{2}$' }
Check "健康检查报告 AI 就绪状态" { $null -ne $health.Data.aiReady }

$page = Raw 'GET' '/' $null $null
Check "根路径返回前端页面" { $page.Status -eq 200 -and $page.Content -match '<html' }
$css = Raw 'GET' '/assets/app.css' $null $null
Check "静态样式表可访问" { $css.Status -eq 200 -and $css.Content.Length -gt 1000 }
$js = Raw 'GET' '/assets/app.js' $null $null
Check "静态脚本可访问" { $js.Status -eq 200 -and $js.Content.Length -gt 10000 }
Check "前端无外部 CDN 依赖" { $page.Content -notmatch 'cdn\.' -and $page.Content -notmatch 'unpkg' }

$nope = Api 'GET' '/api/does-not-exist' $null
Check "未知接口返回 JSON 404" { $nope.Status -eq 404 -and $nope.Ok -eq $false -and $nope.Error }

$badid = Api 'DELETE' '/api/tasks/abc' $null
Check "非法 ID 返回 400" { $badid.Status -eq 400 }

Section "2. 标签"
$tags = Api 'GET' '/api/tags' $null
Check "默认标签已初始化" { @($tags.Data).Count -ge 5 }

$newTag = Api 'POST' '/api/tags' @{ name = 'E2E验证标签'; color = '#123456' }
Check "新建标签成功" { $newTag.Status -eq 201 -and $newTag.Data.id -gt 0 }
$tagId = $newTag.Data.id
Check "中文标签名往返无损" { $newTag.Data.name -eq 'E2E验证标签' }

$badColor = Api 'POST' '/api/tags' @{ name = 'E2E非法颜色'; color = 'javascript:alert(1)' }
Check "非法颜色被回退为安全默认色" { $badColor.Status -eq 201 -and $badColor.Data.color -match '^#[0-9a-fA-F]{6}$' }
$badColorId = $badColor.Data.id

Section "3. 时间块与当日配置"
# 时间块必须覆盖「当前时刻」，否则此刻已无可用时间，排程会把所有任务归为未完成。
# 这里用 00:00–24:00 全天可用，保证测试在一天中任何时刻运行都成立。
$tb = Api 'POST' '/api/timeblocks' @{ weekday = -1; startMin = 0; endMin = 1440 }
Check "新建时间块成功" { $tb.Status -eq 201 }
$tbId = $tb.Data.id

$badTb = Api 'POST' '/api/timeblocks' @{ weekday = 1; startMin = 720; endMin = 540 }
Check "结束早于开始的时间块被拒绝" { $badTb.Status -eq 400 }

$badTb2 = Api 'POST' '/api/timeblocks' @{ weekday = 9; startMin = 540; endMin = 720 }
Check "非法星期取值被拒绝" { $badTb2.Status -eq 400 }

$pc = Api 'PUT' '/api/plan/config' @{ deadlineMin = 1380; breakMin = 10 }
Check "设置当日截止时间与休息间隔" { $pc.Status -eq 200 -and $pc.Data.deadlineMin -eq 1380 }

$badPc = Api 'PUT' '/api/plan/config' @{ deadlineMin = 9999 }
Check "非法截止时间被拒绝" { $badPc.Status -eq 400 }

Section "4. 任务增删改查"
$t1 = Api 'POST' '/api/tasks' @{ name = 'E2E任务一'; estimateMin = 45; tagId = $tagId }
Check "新建任务成功" { $t1.Status -eq 201 -and $t1.Data.id -gt 0 }
$id1 = $t1.Data.id
Check "中文任务名往返无损" { $t1.Data.name -eq 'E2E任务一' }

$t2 = Api 'POST' '/api/tasks' @{ name = 'E2E任务二'; estimateMin = 30 }
Check "新建第二个任务成功" { $t2.Status -eq 201 }
$id2 = $t2.Data.id

$empty = Api 'POST' '/api/tasks' @{ name = '   ' }
Check "空名称任务被拒绝" { $empty.Status -eq 400 }

$upd = Api 'PUT' ("/api/tasks/" + $id1) @{ name = 'E2E任务一已改名'; userEstimateMin = 60 }
Check "修改任务名称与用户预估" { $upd.Status -eq 200 -and $upd.Data.name -eq 'E2E任务一已改名' -and $upd.Data.userEstimateMin -eq 60 }

$clr = Api 'PUT' ("/api/tasks/" + $id1) @{ clearUserEstimate = $true }
Check "清空用户预估并回退到 AI 预估" { $clr.Status -eq 200 -and $null -eq $clr.Data.userEstimateMin }

$badStatus = Api 'PUT' ("/api/tasks/" + $id1) @{ status = 'finished' }
Check "未知任务状态被拒绝" { $badStatus.Status -eq 400 }

$list = Api 'GET' '/api/tasks' $null
Check "按日期列出任务" { @($list.Data).Count -ge 2 }

Section "5. 排程与时间戳"
$align = Api 'POST' '/api/plan/align' @{ }
Check "一键对齐北京时间成功" { $align.Status -eq 200 -and $null -ne $align.Data.placed }
Check "排程返回可用时长" { $align.Data.availableMin -gt 0 }
Check "负载率已计算" { $null -ne $align.Data.loadRatio }

$afterAlign = Api 'GET' '/api/tasks' $null
$withTime = @($afterAlign.Data | Where-Object { $null -ne $_.plannedStart })
Check "任务已获得计划时间戳" { $withTime.Count -ge 1 }

# 拖拽排序：必须提交当日全部任务 ID（含已完成）
$allIds = @($afterAlign.Data | Sort-Object orderIndex | ForEach-Object { $_.id })
$reversed = @($allIds)
[array]::Reverse($reversed)
$ro = Api 'POST' '/api/plan/reorder' @{ orderedIds = $reversed }
Check "拖拽排序成功" { $ro.Status -eq 200 }

$afterReorder = Api 'GET' '/api/tasks' $null
$firstNow = ($afterReorder.Data | Sort-Object orderIndex | Select-Object -First 1).id
Check "顺序已按提交结果生效" { $firstNow -eq $reversed[0] }

$roBad = Api 'POST' '/api/plan/reorder' @{ orderedIds = @($allIds[0]) }
Check "数量不一致的排序被拒绝" { $roBad.Status -eq 400 }

Section "6. 计时全流程"
$start = Api 'POST' ("/api/tasks/" + $id2 + '/start') $null
Check "开始计时" { $start.Status -eq 200 -and $start.Data.status -eq 'running' -and $null -ne $start.Data.runStartedAt }

$hb = Raw 'POST' ("/api/tasks/" + $id2 + '/heartbeat') $null $null
Check "心跳上报成功" { $hb.Status -eq 200 }

$pause = Api 'POST' ("/api/tasks/" + $id2 + '/pause') $null
Check "暂停计时并冻结已耗时" { $pause.Status -eq 200 -and $pause.Data.status -eq 'paused' -and $null -eq $pause.Data.runStartedAt }

$resume = Api 'POST' ("/api/tasks/" + $id2 + '/resume') $null
Check "恢复计时" { $resume.Status -eq 200 -and $resume.Data.status -eq 'running' }

$startOther = Api 'POST' ("/api/tasks/" + $id1 + '/start') $null
$prev = Api 'GET' '/api/tasks' $null
$prevTask = $prev.Data | Where-Object { $_.id -eq $id2 }
Check "开始新任务会自动暂停上一个计时中的任务" { $prevTask.status -eq 'paused' }

$adj = Api 'PUT' ("/api/tasks/" + $id2 + '/actual') @{ seconds = 1800 }
Check "手动修正实际耗时" { $adj.Status -eq 200 -and $adj.Data.actualSeconds -ge 1800 }

$adjBad = Api 'PUT' ("/api/tasks/" + $id2 + '/actual') @{ seconds = -5 }
Check "负耗时被拒绝" { $adjBad.Status -eq 400 }

$done = Api 'POST' ("/api/tasks/" + $id2 + '/complete') $null
Check "标记完成并记录完成时刻" { $done.Status -eq 200 -and $done.Data.status -eq 'done' -and $null -ne $done.Data.completedAt }

Section "7. 生成计划（未配置 AI 时应降级而非失败）"
$gen = Api 'POST' '/api/plan/generate' @{
    input      = '复习高等数学，写实验报告，跑步半小时'
    constraint = '上午不要安排任务'
    replace    = $true
}
Check "生成计划成功" { $gen.Status -eq 200 }
Check "返回生成来源（ai 或 local）" { $gen.Data.source -eq 'ai' -or $gen.Data.source -eq 'local' }
Check "未配置 AI 时给出明确降级提示" { $gen.Data.source -eq 'ai' -or $gen.Data.notice }
Check "返回排程结果" { $null -ne $gen.Data.schedule }
Check "生成条数已上报" { $gen.Data.createdCount -ge 3 }

$afterGen = Api 'GET' '/api/tasks' $null
Check "生成的任务已入库" { @($afterGen.Data).Count -ge 3 }
$hasChinese = @($afterGen.Data | Where-Object { $_.name -match '[\u4e00-\u9fa5]' })
Check "入库任务中文未乱码" { $hasChinese.Count -ge 1 }

$genEmpty = Api 'POST' '/api/plan/generate' @{ input = '   ' }
Check "空需求被拒绝" { $genEmpty.Status -eq 400 }

Section "8. 周期任务与幂等性"
$from = (Get-Date).ToString('yyyy-MM-dd')
$rec = Api 'POST' '/api/recurring' @{
    name = 'E2E每日背单词'; durationMin = 30
    frequency = 'daily'; effectiveFrom = $from; enabled = $true
}
Check "新建周期任务" { $rec.Status -eq 201 }
$recId = $rec.Data.id

$badRec = Api 'POST' '/api/recurring' @{
    name = 'E2E非法周期'; frequency = 'weekly'; weekdays = ''; effectiveFrom = $from
}
Check "weekly 未指定星期被拒绝" { $badRec.Status -eq 400 }

$m1 = Api 'POST' '/api/recurring/materialize' @{ }
Check "首次物化生成实例" { $m1.Status -eq 200 -and $m1.Data.created -ge 1 }

$m2 = Api 'POST' '/api/recurring/materialize' @{ }
Check "重复物化不会重复生成（幂等）" { $m2.Status -eq 200 -and $m2.Data.created -eq 0 }

$m3 = Api 'POST' '/api/recurring/materialize' @{ }
Check "第三次物化仍然幂等" { $m3.Status -eq 200 -and $m3.Data.created -eq 0 }

$recList = Api 'GET' '/api/recurring' $null
Check "周期任务列表可读" { @($recList.Data).Count -ge 1 }

Section "9. 未完成任务池"
$undoneTask = (Api 'POST' '/api/tasks' @{ name = 'E2E将被置为未完成'; estimateMin = 20 }).Data
$undone = Api 'POST' ("/api/tasks/" + $undoneTask.id + '/undone') $null
Check "标记任务为未完成" { $undone.Status -eq 200 -and $undone.Data.status -eq 'undone' }

$pool = Api 'GET' '/api/pool' $null
Check "任务池接口可读" { $pool.Status -eq 200 }

Section "10. 长期目标与统计"
$goal = Api 'POST' '/api/goals' @{ content = 'E2E三个月内掌握 Go 并发编程'; tagId = $tagId }
Check "新建长期目标" { $goal.Status -eq 201 }
$goalId = $goal.Data.id

$progress = Api 'GET' '/api/goals/progress' $null
Check "目标进度可统计" { $progress.Status -eq 200 -and @($progress.Data).Count -ge 1 }
$gp = $progress.Data | Where-Object { $_.goalId -eq $goalId }
Check "目标已绑定标签名" { $gp.tagName -eq 'E2E验证标签' }

$trend = Api 'GET' '/api/stats/trend?days=21' $null
Check "21 天趋势返回 21 个数据点" { @($trend.Data).Count -eq 21 }

$tagStats = Api 'GET' '/api/stats/tags?days=21' $null
Check "标签统计可读" { $tagStats.Status -eq 200 }

$analyze = Api 'POST' '/api/stats/analyze' @{ }
Check "未配置 AI 时效率分析返回明确错误" { $analyze.Status -eq 400 -and $analyze.Error }

Section "11. 仪表盘"
$dash = Api 'GET' '/api/dashboard' $null
Check "仪表盘可读" { $dash.Status -eq 200 }
Check "仪表盘包含任务列表" { $null -ne $dash.Data.tasks }
Check "仪表盘统计已完成数量" { $dash.Data.done -ge 1 }
Check "仪表盘包含负载信息" { $null -ne $dash.Data.availability -and $dash.Data.availability.availableMin -ge 0 }
Check "仪表盘包含 21 天趋势" { @($dash.Data.trend).Count -eq 21 }
Check "仪表盘包含任务池数量" { $null -ne $dash.Data.poolCount }
Check "仪表盘报告 AI 配置状态" { $null -ne $dash.Data.aiEnabled }
Check "仪表盘返回当前北京时间" { $dash.Data.now -match '^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$' }

Section "12. 数据导出与备份"
$tmp = Join-Path $env:TEMP ('planner-e2e-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

$csvPath = Join-Path $tmp 'tasks.csv'
$csvResp = Raw 'GET' '/api/export/tasks.csv' $null $csvPath
Check "CSV 导出返回 200" { $csvResp.Status -eq 200 }
Check "CSV 文件已生成" { (Test-Path $csvPath) -and (Get-Item $csvPath).Length -gt 0 }

if (Test-Path $csvPath) {
    $bytes = [System.IO.File]::ReadAllBytes($csvPath)
    $hasBom = ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF)
    Check "CSV 带 UTF-8 BOM（Excel 中文不乱码）" { $hasBom }
    $csvText = [System.Text.Encoding]::UTF8.GetString($bytes)
    Check "CSV 含中文表头" { $csvText -match '任务名称' -and $csvText -match '实际耗时分钟' }
    Check "CSV 含中文明细数据" { $csvText -match 'E2E' }
}

$zipPath = Join-Path $tmp 'backup.zip'
$zipResp = Raw 'GET' '/api/export/backup' $null $zipPath
Check "备份包已下载" { (Test-Path $zipPath) -and (Get-Item $zipPath).Length -gt 500 }

$exDir = Join-Path $tmp 'x'
Expand-Archive -Path $zipPath -DestinationPath $exDir -Force
$extracted = Get-ChildItem $exDir | Select-Object -ExpandProperty Name
Check "备份包含无损还原文件 backup.json" { $extracted -contains 'backup.json' }
Check "备份包含 CSV 表格" { ($extracted -contains 'tasks.csv') -and ($extracted -contains 'tags.csv') }
Check "备份包含说明文件" { $extracted -contains '说明.txt' }

# 显式按 UTF-8 读取，避免 PS 5.1 按 ANSI 解码导致 JSON 解析失败
$manifestText = [System.IO.File]::ReadAllText((Join-Path $exDir 'backup.json'), [System.Text.Encoding]::UTF8)
$manifest = $manifestText | ConvertFrom-Json
Check "备份清单版本号正确" { $manifest.version -eq 1 }
Check "备份包含任务数据" { $null -ne $manifest.tasks }
$backupTag = $manifest.tags | Where-Object { $_.id -eq $tagId }
Check "备份中的中文同样无损" { $backupTag.name -eq 'E2E验证标签' }

Section "13. 备份恢复"
$beforeCount = (Api 'GET' '/api/tasks' $null).Data.Count
$importResp = UploadBackup $zipPath
Check "备份恢复接口返回 200" { $importResp.Status -eq 200 }
$afterImportCount = (Api 'GET' '/api/tasks' $null).Data.Count
Check "恢复后任务数量一致" { $afterImportCount -eq $beforeCount }

$afterImportDash = Api 'GET' '/api/dashboard' $null
Check "恢复后仪表盘仍可读" { $afterImportDash.Status -eq 200 }

$badImport = Raw 'POST' '/api/import/backup' $null $null
Check "缺少文件的上传被拒绝" { $badImport.Status -eq 400 }

Section "14. 系统配置"
$cfg = Api 'GET' '/api/config' $null
Check "读取系统配置" { $cfg.Status -eq 200 }
Check "配置中不回传 API Key 明文" { $null -eq $cfg.Data.aiApiKey }
Check "配置报告密钥是否已配置" { $null -ne $cfg.Data.aiKeyConfigured }

$cfgSet = Api 'PUT' '/api/config' @{ defaultBreakMin = 5; retentionDays = 21 }
Check "更新系统默认值" { $cfgSet.Status -eq 200 -and $cfgSet.Data.defaultBreakMin -eq 5 }

$cfgBad = Api 'PUT' '/api/config' @{ retentionDays = 0 }
Check "非法保留天数被拒绝" { $cfgBad.Status -eq 400 }

$clean = Api 'POST' '/api/admin/cleanup' $null
Check "手动触发数据清理" { $clean.Status -eq 200 -and $null -ne $clean.Data.cutoffDate }

$clearNoConfirm = Api 'POST' '/api/admin/clear' @{ confirm = 'yes' }
Check "清空数据缺少确认时被拒绝" { $clearNoConfirm.Status -eq 400 }

Section "15. 清理测试数据"
foreach ($tid in @($id1, $id2, $undoneTask.id)) {
    Raw 'DELETE' ("/api/tasks/" + $tid) $null $null | Out-Null
}
Raw 'DELETE' ("/api/recurring/" + $recId) $null $null | Out-Null
Raw 'DELETE' ("/api/goals/" + $goalId) $null $null | Out-Null
Raw 'DELETE' ("/api/tags/" + $tagId) $null $null | Out-Null
Raw 'DELETE' ("/api/tags/" + $badColorId) $null $null | Out-Null
Raw 'DELETE' ("/api/timeblocks/" + $tbId) $null $null | Out-Null
Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
Check "测试数据已清理" { $true }

# ---------- 汇总 ----------
Write-Host ""
Write-Host "============================================" -ForegroundColor White
$total = $script:pass + $script:fail
if ($script:fail -eq 0) {
    Write-Host ("全部通过：" + $script:pass + "/" + $total) -ForegroundColor Green
} else {
    Write-Host ("通过 " + $script:pass + "/" + $total + "，失败 " + $script:fail) -ForegroundColor Red
    Write-Host "失败项：" -ForegroundColor Red
    $script:failures | ForEach-Object { Write-Host ("  - " + $_) -ForegroundColor Red }
}
Write-Host "============================================" -ForegroundColor White

if ($script:fail -gt 0) { exit 1 }
