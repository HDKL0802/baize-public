# 白泽后端「全功能自检」——一次把后端所有对外功能跑一遍，逐项 PASS/FAIL。
#
# ⚠ 本文件必须存成【UTF-8 with BOM】。PowerShell 5.1 会把无 BOM 的 .ps1 当 ANSI/GBK 读，
#   里面的中文注释会被解码成乱码并导致语法错误（run-demo.ps1 为此当年写了纯英文注释）。
#   用编辑器改完记得把编码改回 UTF-8 with BOM，否则脚本会直接解析失败。
#
# 为什么这么设计：
#   1. 用本地 mock 模型（cmd/mockllm，OpenAI 兼容）驱动 Agent，**零成本 + 结果确定**，
#      真模型每次调的工具都可能不同，不适合当功能回归。
#   2. 用本地 bzcore（手机内核的桌面版）当「假手机」，把跨端那条路也一起测了。
#   3. 危险动作（fs.delete / shell_run）只允许落在临时目录里；越界路径专门做成「必须失败」的用例。
#
# 用法： powershell -ExecutionPolicy Bypass -File test-all.ps1
# 可选： -Port 8910  -KeepRunning
param([int]$Port = 8910, [switch]$KeepRunning)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$bin = Join-Path $root "bin"
$work = Join-Path $root ".sweep"
$base = "http://127.0.0.1:$Port"
$mockBase = "http://127.0.0.1:$($Port + 1)"

$script:pass = 0
$script:fail = 0
$script:bad = @()

function Step($m) { Write-Host ("[sweep] " + $m) -ForegroundColor Cyan }
function Ok($m) { $script:pass++; Write-Host ("  PASS  " + $m) -ForegroundColor Green }
function Bad($m) { $script:fail++; $script:bad += $m; Write-Host ("  FAIL  " + $m) -ForegroundColor Red }

# 单项检查：跑一个脚本块，抛异常即判失败（不中断整轮）
function Check($name, [scriptblock]$body) {
  try { $r = & $body; if ($r -eq $false) { Bad "$name（断言不成立）" } else { Ok $name } }
  catch { Bad "$name -> $($_.Exception.Message)" }
}

function Send-Json($method, $uri, $obj, $headers) {
  $json = $obj | ConvertTo-Json -Depth 10
  $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
  return Invoke-RestMethod -Method $method -Uri $uri -Headers $headers `
    -ContentType "application/json; charset=utf-8" -Body $bytes -TimeoutSec 120
}

function Get-Json($uri, $headers) {
  return Invoke-RestMethod -Uri $uri -Headers $headers -TimeoutSec 60
}

function Wait-For([scriptblock]$cond, [int]$seconds = 30, [int]$everyMs = 300) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $deadline) {
    try { $v = & $cond; if ($v) { return $v } } catch { }
    Start-Sleep -Milliseconds $everyMs
  }
  return $null
}

# 各段之间必须互不串扰：清掉挂着的审批，并等当前运行结束（后端同一时刻只允许一次运行）
function Clear-Runs([int]$seconds = 90) {
  try {
    $a = Get-Json "$base/api/agent/approvals" $h
    foreach ($x in @($a.approvals | Where-Object { $_.status -eq "pending" })) {
      try { $null = Send-Json "Post" "$base/api/agent/approvals/$($x.id)/reject" @{ by = "sweep"; reason = "自检清理" } $h } catch { }
    }
  } catch { }
  $null = Wait-For { $s = Get-Json "$base/api/agent/state" $h; if (-not $s.running) { $s } } $seconds
}

# 等一个设备任务走到终态
function Wait-Task($id, [int]$seconds = 30) {
  for ($i = 0; $i -lt ($seconds * 3); $i++) {
    Start-Sleep -Milliseconds 300
    $t = (Get-Json "$base/api/state" $h).tasks | Where-Object { $_.id -eq $id } | Select-Object -First 1
    if ($t -and $t.status -in @("done", "failed", "rejected")) { return $t }
  }
  return $null
}

# mock 模型「看到过什么」（工具表 / 工具结果），用来断言模型侧的真实输入
function Mock-Saw() {
  $ms = (Get-Json "$mockBase/_mock/state" $h).requests
  $tools = @()
  if ($ms.Count -gt 0 -and $ms[-1].tools) { $tools = @($ms[-1].tools) }
  $text = (($ms | ForEach-Object { $_.messages | ForEach-Object { $_.content } }) -join "`n")
  return @{ tools = $tools; text = $text }
}

function Find-Go {
  foreach ($c in @("D:\xm\tools\go\bin\go.exe", (Join-Path $env:ProgramFiles "Go\bin\go.exe"), "C:\Go\bin\go.exe")) {
    if (Test-Path $c) { return $c }
  }
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  throw "找不到 go"
}

$procs = @{}
try {
  # ---------- 0. 编译 ----------
  Step "编译 backend / desktop / mcpserver / mockllm / bzcore(假手机)"
  $go = Find-Go
  New-Item -ItemType Directory -Force $bin, $work | Out-Null
  foreach ($pair in @(
      @("baize-backend.exe", "./cmd/backend"), @("baize-desktop.exe", "./cmd/desktop"),
      @("mcpserver.exe", "./cmd/mcpserver"), @("mockllm.exe", "./cmd/mockllm"))) {
    & $go -C $root build -o (Join-Path $bin $pair[0]) $pair[1]
    if ($LASTEXITCODE -ne 0) { throw ("编译失败：" + $pair[0]) }
  }
  $coreRoot = Join-Path (Split-Path $root -Parent) "core"
  & $go -C $coreRoot build -o (Join-Path $bin "bzcore.exe") ./cmd/bzcore
  if ($LASTEXITCODE -ne 0) { throw "编译失败：bzcore.exe" }
  Ok "五个二进制编译完成"

  # ---------- 1. 起 mock 模型 + 后端 ----------
  Step "启动 mock 模型 + 后端（数据目录 .sweep/data）"
  if (Test-Path $work) { Remove-Item $work -Recurse -Force }
  $dataDir = Join-Path $work "data"
  $phoneDir = Join-Path $work "phone"
  $outsideDir = Join-Path $work "outside"       # 允许根之外的目录，用来测越界必须失败
  $tmpRoot = Join-Path $work "tmp"              # desktop 的 allow-root
  New-Item -ItemType Directory -Force $dataDir, $phoneDir, $outsideDir, $tmpRoot | Out-Null

  $scriptFile = Join-Path $work "script.json"
  Set-Content -Path $scriptFile -Value '[]' -Encoding ASCII
  $procs.mock = Start-Process -FilePath (Join-Path $bin "mockllm.exe") -PassThru -WindowStyle Hidden `
    -ArgumentList @("--addr", "127.0.0.1:$($Port + 1)", "--script", $scriptFile) `
    -RedirectStandardOutput (Join-Path $work "mock.out") -RedirectStandardError (Join-Path $work "mock.err")

  $procs.backend = Start-Process -FilePath (Join-Path $bin "baize-backend.exe") -PassThru -WindowStyle Hidden `
    -ArgumentList @("--addr", "127.0.0.1:$Port", "--data", $dataDir, "--log-level", "debug", "--receipt-timeout", "30s") `
    -RedirectStandardOutput (Join-Path $work "be.out") -RedirectStandardError (Join-Path $work "be.err")

  $tokenFile = Join-Path $dataDir "token"
  $token = Wait-For { if (Test-Path $tokenFile) { (Get-Content $tokenFile -Raw).Trim() } } 20
  if (-not $token) { throw "后端没生成配对令牌" }
  $h = @{ "X-Baize-Token" = $token }
  $health = Wait-For { $r = Get-Json "$base/api/health" $h; if ($r.ok) { $r } } 20
  if (-not $health) { throw "后端没起来（看 .sweep\be.err）" }
  Ok ("后端已就绪 version=" + $health.version + " protocol=" + $health.protocol)

  # ================= A. 基础与鉴权 =================
  Step "A. 基础接口与鉴权"
  Check "A1 GET /api/health 返回版本与设备数" {
    $r = Get-Json "$base/api/health" $h
    if ($r.version -eq "" -or $null -eq $r.devicesOnline) { throw "字段不全：$($r|ConvertTo-Json -Compress)" }
  }
  Check "A2 GET / 是控制台页面（HTML）" {
    $html = (Invoke-WebRequest -Uri "$base/" -TimeoutSec 20 -UseBasicParsing).Content
    if ($html -notmatch "<html" -or $html -notmatch "白泽") { throw "不是控制台页面" }
  }
  Check "A3 GET /api/state 结构齐全" {
    $r = Get-Json "$base/api/state" $h
    foreach ($k in @("devices", "tasks", "records", "memories", "stats", "version")) {
      if ($null -eq $r.$k) { throw "少了字段 $k" }
    }
  }
  Check "A4 GET /api/logs 有日志行" {
    $r = Get-Json "$base/api/logs?limit=20" $h
    if ($r.lines.Count -lt 1) { throw "没日志" }
  }
  Check "A5 无令牌必须 401（鉴权真的生效）" {
    $code = 0
    try { Invoke-WebRequest -Uri "$base/api/state" -TimeoutSec 10 -UseBasicParsing | Out-Null; $code = 200 }
    catch { $code = [int]$_.Exception.Response.StatusCode }
    if ($code -ne 401) { throw "期望 401，实际 $code" }
  }
  Check "A6 /api/state 报出「主设备」（后端所在那台）" {
    $r = Get-Json "$base/api/state" $h
    if (-not $r.self -or -not $r.self.hostname) { throw "没有 self.hostname：$($r.self | ConvertTo-Json -Compress)" }
    if (-not $r.self.os) { throw "没有 self.os" }
  }
  Check "A7 设备备注能存能清" {
    $null = Send-Json "Post" "$base/api/agent/device-remark" @{ deviceId = "dev-x"; remark = "客厅那台" } $h
    $r = Get-Json "$base/api/state" $h
    if ($r.deviceRemarks."dev-x" -ne "客厅那台") { throw "备注没存上：$($r.deviceRemarks | ConvertTo-Json -Compress)" }
    # 落盘过（重读配置还在）
    $cfg = Get-Content (Join-Path $dataDir "config.json") -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($cfg.deviceRemarks."dev-x" -ne "客厅那台") { throw "备注没写到 config.json" }
    $null = Send-Json "Post" "$base/api/agent/device-remark" @{ deviceId = "dev-x"; remark = "" } $h
    $r = Get-Json "$base/api/state" $h
    if ($r.deviceRemarks."dev-x") { throw "空备注应当清除，实际还在" }
  }

  # ================= B. 电脑端设备 + 任务审批闸门 =================
  Step "B. 电脑端注册 / 任务闸门 / 执行 / 回执 / 记忆"
  $procs.desktop = Start-Process -FilePath (Join-Path $bin "baize-desktop.exe") -PassThru -WindowStyle Hidden `
    -ArgumentList @("--server", $base, "--token", $token, "--interval", "2s", "--allow-root", $tmpRoot, "--name", "自检电脑") `
    -RedirectStandardOutput (Join-Path $work "de.out") -RedirectStandardError (Join-Path $work "de.err")
  $pcDev = Wait-For { $st = Get-Json "$base/api/state" $h; $st.devices | Where-Object { $_.online -and $_.os -eq "windows" } | Select-Object -First 1 } 40
  if (-not $pcDev) { throw "电脑端没上线（看 .sweep\de.err）" }
  Ok ("电脑端上线：" + $pcDev.id + " caps=" + ($pcDev.caps -join ","))
  Check "B1 设备能力声明包含 window.report/fs" {
    if (($pcDev.caps -join ",") -notmatch "window.report") { throw "caps 不对" }
  }
  Check "B2 活动窗口事件上报" {
    $r = Wait-For { $s = Get-Json "$base/api/state?kind=event" $h; if ($s.records.Count -gt 0) { $s } } 15
    if (-not $r) { throw "没有事件记录（headless 会话可能没有窗口）" }
  }

  $victim = Join-Path $tmpRoot "delete-me.txt"
  Set-Content -Path $victim -Value "must be deleted by the agent" -Encoding ASCII
  $t1 = Send-Json "Post" "$base/api/tasks" @{ deviceId = $pcDev.id; action = "fs.delete"; args = @{ paths = @($victim) }; origin = "sweep" } $h
  Check "B3 危险动作先挂审批（pending_approval）" {
    if ($t1.task.status -ne "pending_approval") { throw "期望 pending_approval，实际 $($t1.task.status)" }
  }
  Check "B4 审批前绝不允许执行（文件还在）" {
    Start-Sleep -Seconds 1
    if (-not (Test-Path $victim)) { throw "没审批就把文件删了" }
  }
  Check "B5 批准后真的执行并回执 done" {
    $null = Send-Json "Post" "$base/api/tasks/$($t1.task.id)/approve" @{ by = "sweep" } $h
    $fin = $null
    for ($i = 0; $i -lt 60; $i++) {
      Start-Sleep -Milliseconds 300
      $fin = (Get-Json "$base/api/state" $h).tasks | Where-Object { $_.id -eq $t1.task.id } | Select-Object -First 1
      if ($fin.status -in @("done", "failed", "rejected")) { break }
    }
    if ($fin.status -ne "done") { throw "任务没完成：status=$($fin.status) err=$($fin.error)" }
    if (Test-Path $victim) { throw "回执说 done，但文件还在" }
  }
  Check "B6 结论落进长期记忆" {
    $mem = Wait-For { $s = Get-Json "$base/api/state?records=200" $h; $s.memories | Where-Object { $_.taskId -eq $t1.task.id } | Select-Object -First 1 } 20
    if (-not $mem) { throw "没有这条任务的记忆" }
  }
  Check "B7 拒绝路径：reject 后不执行" {
    $v2 = Join-Path $tmpRoot "keep-me.txt"; Set-Content -Path $v2 -Value "keep" -Encoding ASCII
    $t2 = Send-Json "Post" "$base/api/tasks" @{ deviceId = $pcDev.id; action = "fs.delete"; args = @{ paths = @($v2) }; origin = "sweep" } $h
    $null = Send-Json "Post" "$base/api/tasks/$($t2.task.id)/reject" @{ reason = "自检拒绝" } $h
    Start-Sleep -Seconds 1
    if (-not (Test-Path $v2)) { throw "拒绝了却还是删了" }
    $after = (Get-Json "$base/api/state" $h).tasks | Where-Object { $_.id -eq $t2.task.id } | Select-Object -First 1
    if ($after.status -ne "rejected") { throw "状态应为 rejected，实际 $($after.status)" }
  }
  Check "B8 越界路径必须失败（allow-root 之外）" {
    $v3 = Join-Path $outsideDir "outside.txt"; Set-Content -Path $v3 -Value "outside" -Encoding ASCII
    $t3 = Send-Json "Post" "$base/api/tasks" @{ deviceId = $pcDev.id; action = "fs.delete"; args = @{ paths = @($v3) }; origin = "sweep" } $h
    $null = Send-Json "Post" "$base/api/tasks/$($t3.task.id)/approve" @{ by = "sweep" } $h
    $fin = $null
    for ($i = 0; $i -lt 60; $i++) {
      Start-Sleep -Milliseconds 300
      $fin = (Get-Json "$base/api/state" $h).tasks | Where-Object { $_.id -eq $t3.task.id } | Select-Object -First 1
      if ($fin.status -in @("done", "failed", "rejected")) { break }
    }
    if ($fin.status -eq "done") { throw "越界删除竟然成功了" }
    if (-not (Test-Path $v3)) { throw "越界文件被删了——护栏失效！" }
  }

  # ================= C. 手机端（本地 bzcore 当假手机） =================
  Step "C. 手机内核注册 / 后端知识库 / 跨端正本"
  $phoneOut = Join-Path $work "phone.out"
  $procs.phone = Start-Process -FilePath (Join-Path $bin "bzcore.exe") -PassThru -WindowStyle Hidden `
    -ArgumentList @("--data", $phoneDir, "--addr", "127.0.0.1:0", "--print-port", "--platform", "android",
      "--device-name", "自检假手机", "--server", $base, "--pair-token", $token) `
    -RedirectStandardOutput $phoneOut -RedirectStandardError (Join-Path $work "phone.err")
  $phonePort = Wait-For { if (Test-Path $phoneOut) { $l = Select-String -Path $phoneOut -Pattern "BZCORE_PORT=" | Select-Object -First 1; if ($l) { [int](($l.Line -split "=")[1]) } } } 25
  if (-not $phonePort) { throw "假手机没拿到端口（看 .sweep\phone.err）" }
  $phoneToken = (Get-Content (Join-Path $phoneDir "token") -Raw).Trim()
  $ph = @{ "X-Baize-Token" = $phoneToken }
  $phoneDev = Wait-For { $st = Get-Json "$base/api/state" $h; $st.devices | Where-Object { $_.os -eq "android" -and $_.online } | Select-Object -First 1 } 40
  if (-not $phoneDev) { throw "假手机没注册上" }
  Ok ("假手机上线：" + $phoneDev.id + " caps=" + ($phoneDev.caps -join ","))

  Check "C1 手机自报的跨端状态（/api/device）" {
    $r = (Get-Json "http://127.0.0.1:$phonePort/api/device" $ph).data
    if (-not $r.connected) { throw "内核说自己没连上：$($r.lastError)" }
    if ($r.deviceId -ne $phoneDev.id) { throw "设备 id 不一致：$($r.deviceId) vs $($phoneDev.id)" }
  }
  Check "C2 手机只提供只读动作（无密码本动作）" {
    $r = (Get-Json "http://127.0.0.1:$phonePort/api/device" $ph).data
    $acts = $r.actions -join ","
    if ($acts -notmatch "todo.list") { throw "缺少 todo.list" }
    if ($acts -match "vault|password") { throw "不该暴露密码本动作" }
  }
  Check "C3 后端知识库：加待办 / 加密码 / 取快照" {
    $st0 = Get-Json "$base/api/kb/state" $h
    if ($null -eq $st0.todos) { throw "知识库快照结构不对" }
    foreach ($op in @(
        @{ op = "todo.upsert"; args = @{ id = "s1"; title = "自检待办一"; owner = "user"; status = "todo"; form = "schedule"; due = "2030-01-02T10:00"; category = "工作开发" } },
        @{ op = "todo.upsert"; args = @{ id = "s2"; title = "自检待办二"; owner = "user"; status = "done"; form = "leisure" } },
        @{ op = "vault.merge"; args = @{ title = "自检站点"; account = "sweep@baize"; password = "pw-1" } }
      )) {
      $r = Send-Json "Post" "$base/api/kb/op" $op $h
      if (-not $r.ok) { throw "知识库操作 $($op.op) 失败：$($r.error)" }
    }
    $st = Get-Json "$base/api/kb/state" $h
    if ($st.todos.Count -ne 2) { throw "知识库待办条数不对：$($st.todos.Count)" }
    if ($st.vault.Count -ne 1) { throw "知识库密码条数不对：$($st.vault.Count)" }
    $kbStats = Get-Json "$base/api/kb/stats" $h
    if ($kbStats.todos -ne 2 -or $kbStats.passwords -ne 1) { throw "知识库统计不对：$($kbStats | ConvertTo-Json -Compress)" }
  }
  Check "C4 手机内核读到的是后端的正本（跨端代理）" {
    $r = (Get-Json "http://127.0.0.1:$phonePort/api/remote" $ph).data
    if (-not $r.remote) { throw "内核没挂上后端知识库" }
    if ($r.stale) { throw "内核说后端是陈旧的：$($r.reason)" }
    if ($r.todos -ne 2) { throw "内核看到的待办条数不对：$($r.todos)" }
    $snap = (Get-Json "http://127.0.0.1:$phonePort/api/state" $ph).data
    if (-not $snap.remote) { throw "手机快照没标记「正本在后端」" }
    if (($snap.todos | ForEach-Object { $_.title }) -notcontains "自检待办一") { throw "手机没读到后端那条待办" }
    # 密码能让手机读到（它就是靠读后端工作的），但**绝不在手机上落文件**：
    # 本机缓存只存待办，密码一律留后端
    if ($snap.vault.Count -ne 1) { throw "手机没读到后端那条密码：$($snap.vault.Count)" }
    $cache = Get-Content (Join-Path $phoneDir "data.json") -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($cache.vault -and @($cache.vault).Count -gt 0) { throw "密码本被缓存到手机上了（安全边界）" }
    if (@($cache.todos).Count -ne 2) { throw "待办缓存条数不对：$(@($cache.todos).Count)" }
  }
  Check "C5 手机写操作也落在后端知识库" {
    $r = Send-Json "Post" "http://127.0.0.1:$phonePort/api/op" @{ op = "todo.upsert"; args = @{ id = "s3"; title = "手机写进来的"; owner = "user"; status = "todo" } } $ph
    if (-not $r.ok) { throw "手机端写失败：$($r.error)" }
    $st = Get-Json "$base/api/kb/state" $h
    if ($st.todos.Count -ne 3) { throw "后端没收到手机写的那条：$($st.todos.Count)" }
    # 跨端模式下绝不允许手机推整表覆盖后端正本（否则会把后端的正本冲掉）
    $rejected = $false
    $rejBody = ''
    try {
      $null = Invoke-WebRequest -Method Post -Uri "http://127.0.0.1:$phonePort/api/op" -Headers $ph `
        -ContentType "application/json; charset=utf-8" -UseBasicParsing `
        -Body ([Text.Encoding]::UTF8.GetBytes((@{ op = "todo.replaceAll"; args = @{ todos = @() } } | ConvertTo-Json -Depth 10)))
    } catch {
      $rejected = $true
      if ($_.ErrorDetails) { $rejBody = $_.ErrorDetails.Message }
    }
    if (-not $rejected) { throw "todo.replaceAll 竟然成功了——后端正本会被冲掉" }
    if ($rejBody -notmatch "跨端") { throw "拒绝原因没说清：$rejBody" }
    $st2 = Get-Json "$base/api/kb/state" $h
    if ($st2.todos.Count -ne 3) { throw "被拒之后后端数据不该变：$($st2.todos.Count)" }
  }
  Check "C6 跨端下发 todo.list / todo.stats 读的是知识库正本" {
    $t = Send-Json "Post" "$base/api/tasks" @{ deviceId = $phoneDev.id; action = "todo.list"; args = @{ limit = 10 }; origin = "sweep" } $h
    $fin = Wait-Task $t.task.id 30
    if (-not $fin) { throw "待办任务一直没到终态" }
    if ($fin.status -ne "done") { throw "todo.list 没成功：$($fin.error)" }
    if ($fin.result.count -ne 3 -or $fin.result.matched -ne 3) { throw "条数不对：$($fin.result | ConvertTo-Json -Compress)" }
    if (($fin.result.todos | ForEach-Object { $_.title }) -notcontains "自检待办一") { throw "标题不对" }

    $t2 = Send-Json "Post" "$base/api/tasks" @{ deviceId = $phoneDev.id; action = "todo.stats"; args = @{}; origin = "sweep" } $h
    $fin2 = Wait-Task $t2.task.id 30
    if (-not $fin2 -or $fin2.status -ne "done") { throw "todo.stats 失败：$($fin2.error)" }
    if ($fin2.result.total -ne 3) { throw "统计不对：$($fin2.result | ConvertTo-Json -Compress)" }
  }
  Check "C7 附件：上传到后端 / 取回 / 删掉" {
    $b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("白泽知识库附件内容"))
    $up = Send-Json "Post" "$base/api/kb/files" @{ name = "自检.md"; kind = "file"; mime = "text/markdown"; dataBase64 = $b64 } $h
    if (-not $up.file.id) { throw "上传没返回 id" }
    $got = Get-Json "$base/api/kb/files/$($up.file.id)" $h
    $back = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($got.dataBase64))
    if ($back -ne "白泽知识库附件内容") { throw "取回的内容和上传的不一致" }
    $del = Send-Json "Delete" "$base/api/kb/files/$($up.file.id)" @{} $h
    if ($del.removed -ne $up.file.id) { throw "删除返回不对" }
    $list = Get-Json "$base/api/kb/files" $h
    if (($list.files | Where-Object { $_.id -eq $up.file.id })) { throw "删完还在列表里" }
  }
  Check "C8 不支持的动作必须回失败并列出支持项" {
    # 用 window.now：它不是危险动作（不会被后端闸门先拦住），手机内核也确实不支持
    $t = Send-Json "Post" "$base/api/tasks" @{ deviceId = $phoneDev.id; action = "window.now"; args = @{}; origin = "sweep" } $h
    $fin = Wait-Task $t.task.id 30
    if (-not $fin) { throw "任务一直没到终态（$($t.task.status)）" }
    if ($fin.status -eq "done") { throw "手机不该执行 window.now" }
    if ($fin.error -notmatch "不支持") { throw "错误信息没说清不支持：'$($fin.error)'" }
  }

  # ================= D. Agent 主循环 / 工具 / 记忆 / 检查点 =================
  Step "D. Agent（mock 模型驱动）"
  Check "D1 保存 mock 模型通道" {
    $r = Send-Json "Post" "$base/api/agent/providers" @{
      action = "upsert"; name = "mock"; allowRemote = $true
      config = @{ name = "mock"; protocol = "openai"; baseUrl = "$mockBase/v1"; apiKey = "sk-mock"; model = "mock-model"; timeoutSec = 30 }
    } $h
    if (($r.providers | Where-Object { $_.name -eq "mock" }).usable -ne $true) { throw "通道不可用" }
  }
  Check "D2 通道探活（真发一次请求）" {
    $r = Send-Json "Post" "$base/api/agent/providers" @{ action = "test"; name = "mock" } $h
    if ($r.error) { throw $r.error }
  }
  Check "D3 GET /api/agent/state 反映配置与运行状态" {
    $r = Get-Json "$base/api/agent/state" $h
    if ($null -eq $r.running) { throw "没有 running 字段" }
    if (-not $r.providers) { throw "providers 为空" }
    if ($r.workdir -notmatch "workspace") { throw "workdir 不对：$($r.workdir)" }
  }
  Check "D4 非本机地址 + allowRemote=false 必须被拒" {
    $null = Send-Json "Post" "$base/api/agent/config" @{ allowRemote = $false } $h
    $code = 0
    try {
      $null = Send-Json "Post" "$base/api/agent/providers" @{
        action = "upsert"; name = "evil"; allowRemote = $false
        config = @{ name = "evil"; protocol = "openai"; baseUrl = "https://example.com/v1"; apiKey = "x"; model = "m" }
      } $h
    } catch { $code = [int]$_.Exception.Response.StatusCode }
    $null = Send-Json "Post" "$base/api/agent/config" @{ allowRemote = $true } $h   # 复原，别影响后面的用例
    if ($code -eq 0) { throw "远端地址竟然被接受了" }
    foreach ($p in (Get-Json "$base/api/agent/providers" $h).providers) {
      if ($p.name -eq "evil") { throw "被拒的通道竟然还是存进去了" }
    }
  }

  # 剧本：先写文件、再总结
  $null = Send-Json "Post" "$mockBase/_mock/script" @(
    @{ tool = "fs_write"; args = @{ path = "note.md"; content = "自检写入的内容" } },
    @{ text = "已经写好了" }
  ) $h
  Check "D5 Agent 跑通：调工具 + 收结论" {
    Clear-Runs
    $r = (Send-Json "Post" "$base/api/agent/run" @{ goal = "写一个 note.md"; wait = $true } $h).result
    if ($r.errors) { throw "运行报错：$($r.errors -join ";")" }
    if ($r.steps -lt 1 -or $r.toolCalls -lt 1) { throw "没走工具：steps=$($r.steps) toolCalls=$($r.toolCalls)" }
    if ($r.text -notmatch "写好") { throw "结论不对：$($r.text)" }
  }
  Check "D6 工具真的落盘了（fs_write 生效）" {
    $f = Join-Path $dataDir "workspace\note.md"
    if (-not (Test-Path $f)) { throw "工作目录里没有 note.md" }
    # PS 5.1 的 Get-Content 默认按 ANSI 读，中文会变乱码 → 必须显式 UTF8
    if ((Get-Content $f -Raw -Encoding UTF8) -notmatch "自检写入") { throw "内容不对" }
  }
  Check "D7 工具表真的下发给模型（含 device_run / memory / 知识库 / 技能管理工具）" {
    $saw = Mock-Saw
    foreach ($t in @("fs_write", "fs_read", "device_run", "memory", "memory_forget", "agent_delegate",
                     "kb_add_todo", "kb_list_todos", "kb_add_password", "kb_delete_todo",
                     "skill_manage", "skill_delete", "skill_load")) {
      if ($saw.tools -notcontains $t) { throw "模型没拿到工具 $t（实际：$($saw.tools -join ",")）" }
    }
  }
  Check "D8 运行记录可查 + 跨端派发（Agent 路径）且工具结果完整送达" {
    $list = Get-Json "$base/api/agent/runs?limit=5" $h
    if ($list.runs.Count -lt 1) { throw "没有运行记录" }
    $one = Get-Json "$base/api/agent/runs/$($list.runs[0].runId)" $h
    if (-not $one.run) { throw "取不到单次运行详情" }
    if ($one.messages.Count -lt 2) { throw "运行消息没落库" }

    # 让 Agent 自己把 todo.list 派给假手机：既测跨端工具，也测「工具结果完整送达模型」。
    # device_run 是危险工具 → 必须走审批，所以这里用「异步起 + 放行」的姿势（wait=true 会死等）
    Clear-Runs
    $null = Send-Json "Post" "$mockBase/_mock/script" @(
      @{ tool = "device_run"; args = @{ deviceId = $phoneDev.id; action = "todo.list"; args = @{ limit = 10 } } },
      @{ text = "手机上的待办拿到了" }
    ) $h
    $started = Send-Json "Post" "$base/api/agent/run" @{ goal = "看看手机上的待办"; wait = $false } $h
    $ap = Wait-For { $a = Get-Json "$base/api/agent/approvals" $h; $a.approvals | Where-Object { $_.runId -eq $started.runId -and $_.status -eq "pending" } | Select-Object -First 1 } 30
    if (-not $ap) { throw "device_run 没进审批队列" }
    $null = Send-Json "Post" "$base/api/agent/approvals/$($ap.id)/approve" @{ by = "sweep" } $h
    $null = Wait-For { $s = Get-Json "$base/api/agent/state" $h; if (-not $s.running) { $s } } 60
    $rec = Get-Json "$base/api/agent/runs/$($started.runId)" $h
    if ($rec.run.status -ne "done") { throw "跨端运行没完成：$($rec.run.status)" }
    $saw = Mock-Saw
    if ($saw.text -notmatch "自检待办一") { throw "模型没看到完整工具结果（截断回归）" }
  }
  Check "D9 记忆写入 + 检索" {
    $null = Send-Json "Post" "$base/api/agent/memory/write" @{
      content = "自检专用关键词：昆塔菠萝；这是一次记忆写入测试。"; title = "自检记忆"; kind = "note"
    } $h
    $hit = Get-Json "$base/api/agent/memory?q=昆塔菠萝&limit=5" $h
    if ($hit.hits.Count -lt 1) { throw "刚写进去的记忆检索不到" }
    $tree = Get-Json "$base/api/agent/memory/tree?limit=20" $h
    if ($tree.nodes.Count -lt 1) { throw "记忆树是空的" }
  }
  Check "D9b 记忆术：分区/文档键/召回闸门/自检 与 向量通道不伪装" {
    # 记忆配置能存能读回
    $null = Send-Json "Post" "$base/api/agent/config" @{
      memoryAutoRecall = $false; memoryRecallProfile = "semantic"
      memoryRecallBudget = 321; memoryRecallMinScore = 0.42; memoryNamespace = "selftest"
    } $h
    $cfg = Get-Json "$base/api/agent/config" $h
    if ($cfg.memory.recallProfile -ne "semantic") { throw "档位没存下来：$($cfg.memory.recallProfile)" }
    if ($cfg.memory.recallBudget -ne 321) { throw "预算没存下来：$($cfg.memory.recallBudget)" }
    if ($cfg.memory.namespace -ne "selftest") { throw "分区没存下来：$($cfg.memory.namespace)" }
    if ($cfg.memory.autoRecall -ne $false) { throw "自动召回开关没存下来" }
    # 非法档位要能收敛（不能把配置写坏）
    $null = Send-Json "Post" "$base/api/agent/config" @{ memoryRecallProfile = "乱写的" } $h
    if ((Get-Json "$base/api/agent/config" $h).memory.recallProfile -ne "balanced") { throw "非法档位应回落 balanced" }
    # 没配向量通道时：状态要明说未启用；探活要明确报错，绝不伪装成功
    $st = Get-Json "$base/api/agent/state" $h
    if ($st.embedding.usable) { throw "没配通道却报可用" }
    if (-not $st.embedding.note) { throw "没配通道时必须说明语义检索未启用" }
    $t = Send-Json "Post" "$base/api/agent/embedding/test" @{} $h
    if ($t.ok) { throw "没配通道时探活不该成功" }
    if (-not $t.error) { throw "探活失败必须给出原因" }
    # 补向量同理：没通道必须明确报错，不能假装补完了
    $ri = Send-Json "Post" "$base/api/agent/embedding/reindex" @{} $h
    if ($ri.ok) { throw "没配通道时补向量不该成功" }
    if (-not $ri.error) { throw "补向量失败必须给出原因" }
    if ($st.memory.chunks -gt 0 -and $st.memory.missingEmbedding -lt 1) { throw "没配向量时应有若干条标记为缺向量" }
    # 恢复默认，别把自检的配置留给后面用
    $null = Send-Json "Post" "$base/api/agent/config" @{
      memoryAutoRecall = $true; memoryRecallProfile = "balanced"
      memoryRecallBudget = 600; memoryRecallMinScore = 0.35; memoryNamespace = "default"
    } $h
  }
  Check "D10 检查点列表 + 回滚" {
    $cps = Get-Json "$base/api/agent/checkpoints" $h
    if ($cps.checkpoints.Count -lt 1) { throw "没有检查点（fs_write 应留下快照）" }
    $id = $cps.checkpoints[0].id
    $r = Send-Json "Post" "$base/api/agent/checkpoints/$id/rollback" @{} $h
    if ($r.rolledBack -ne $id) { throw "回滚返回不对" }
  }
  Check "D11 agent_delegate 子任务" {
    Clear-Runs
    $null = Send-Json "Post" "$mockBase/_mock/script" @(
      @{ tool = "agent_delegate"; args = @{ goal = "数一下 1+1" } },
      @{ text = "子任务已派发" }
    ) $h
    $r = (Send-Json "Post" "$base/api/agent/run" @{ goal = "派个子任务"; wait = $true } $h).result
    if ($r.errors) { throw "运行报错：$($r.errors -join ";")" }
    if ($r.toolCalls -lt 1) { throw "没有工具调用" }
  }
  Check "D12 shell_run 默认关着：审批之后也必须明确拒绝执行" {
    Clear-Runs
    $null = Send-Json "Post" "$base/api/agent/config" @{ allowShell = $false } $h
    $null = Send-Json "Post" "$mockBase/_mock/script" @(
      @{ tool = "shell_run"; args = @{ cmd = "echo hi" } },
      @{ text = "被拦住了" }
    ) $h
    $started = Send-Json "Post" "$base/api/agent/run" @{ goal = "跑个命令"; wait = $false } $h
    $ap = Wait-For { $a = Get-Json "$base/api/agent/approvals" $h; $a.approvals | Where-Object { $_.runId -eq $started.runId -and $_.status -eq "pending" } | Select-Object -First 1 } 30
    if (-not $ap) { throw "shell_run 没进审批队列" }
    $null = Send-Json "Post" "$base/api/agent/approvals/$($ap.id)/approve" @{ by = "sweep" } $h
    $null = Wait-For { $s = Get-Json "$base/api/agent/state" $h; if (-not $s.running) { $s } } 60
    $rec = Get-Json "$base/api/agent/runs/$($started.runId)" $h
    $msg = ($rec.messages | ForEach-Object { $_.content }) -join "`n"
    if ($msg -notmatch "未开启命令执行") {
      throw ("allowShell=false 时没有明确拒绝（模型收到的内容里找不到原因）：" + $msg.Substring(0, [Math]::Min(200, $msg.Length)))
    }
  }

  # ================= E. Agent 审批闸门（approve / reject） =================
  Step "E. Agent 审批闸门"
  Check "E1 危险工具挂审批队列" {
    Clear-Runs
    $null = Send-Json "Post" "$mockBase/_mock/script" @(
      @{ tool = "fs_delete"; args = @{ path = "note.md" } },
      @{ text = "删完了" }
    ) $h
    $started = Send-Json "Post" "$base/api/agent/run" @{ goal = "把 note.md 删掉"; wait = $false } $h
    if (-not $started.runId) { throw "没拿到 runId" }
    $script:runForApproval = $started.runId
    $ap = Wait-For { $a = Get-Json "$base/api/agent/approvals" $h; $a.approvals | Where-Object { $_.runId -eq $started.runId -and $_.status -eq "pending" } | Select-Object -First 1 } 30
    if (-not $ap) { throw "没进审批队列" }
    $script:apId = $ap.id
    if ($ap.tool -ne "fs_delete") { throw "审批工具名不对：$($ap.tool)" }
  }
  Check "E2 批准后继续跑完" {
    $null = Send-Json "Post" "$base/api/agent/approvals/$($script:apId)/approve" @{ by = "sweep" } $h
    $done = Wait-For { $s = Get-Json "$base/api/agent/state" $h; if (-not $s.running) { $s } } 60
    if (-not $done) { throw "批准后还没跑完" }
    $rec = Get-Json "$base/api/agent/runs/$($script:runForApproval)" $h
    if ($rec.run.status -ne "done") { throw "运行状态应为 done，实际 $($rec.run.status)" }
  }
  Check "E3 拒绝路径：reject 后不执行且给出原因" {
    Clear-Runs
    Set-Content -Path (Join-Path $dataDir "workspace\note.md") -Value "again" -Encoding ASCII
    $null = Send-Json "Post" "$mockBase/_mock/script" @(
      @{ tool = "fs_delete"; args = @{ path = "note.md" } },
      @{ text = "删完了" }
    ) $h
    $started = Send-Json "Post" "$base/api/agent/run" @{ goal = "再删一次"; wait = $false } $h
    $ap = Wait-For { $a = Get-Json "$base/api/agent/approvals" $h; $a.approvals | Where-Object { $_.runId -eq $started.runId -and $_.status -eq "pending" } | Select-Object -First 1 } 30
    if (-not $ap) { throw "没进审批队列" }
    $null = Send-Json "Post" "$base/api/agent/approvals/$($ap.id)/reject" @{ by = "sweep"; reason = "自检拒绝" } $h
    $null = Wait-For { $s = Get-Json "$base/api/agent/state" $h; if (-not $s.running) { $s } } 60
    if (-not (Test-Path (Join-Path $dataDir "workspace\note.md"))) { throw "拒绝了却还是删了" }
    # 拒绝要说清楚，让模型知道自己被拦下了（run 本身仍会以「模型给了结论」收尾，这不算错）
    $rec = Get-Json "$base/api/agent/runs/$($started.runId)" $h
    $msg = ($rec.messages | ForEach-Object { $_.content }) -join "`n"
    if ($msg -notmatch "被审批闸门拒绝") {
      throw ("拒绝没有明确告知模型：" + $msg.Substring(0, [Math]::Min(200, $msg.Length)))
    }
  }

  # ================= F. MCP 热插拔 =================
  Step "F. MCP 热插拔"
  Check "F1 挂载 stdio MCP 服务并连接" {
    $r = Send-Json "Post" "$base/api/agent/mcp" @{
      action = "upsert"
      config = @{ name = "demo"; transport = "stdio"; command = (Join-Path $bin "mcpserver.exe"); enabled = $true; safeTools = @("hello", "now"); timeoutSec = 20 }
    } $h
    $srv = $r.servers | Where-Object { $_.name -eq "demo" } | Select-Object -First 1
    if (-not $srv) { throw "没保存" }
    if ($srv.status -ne "connected") { throw "没连上：$($srv.error)" }
    if ($srv.toolCount -ne 2) { throw "工具数不对：$($srv.toolCount)" }
  }
  Check "F2 MCP 工具注册进模型工具表（mcp_ 前缀）" {
    Clear-Runs
    $null = Send-Json "Post" "$mockBase/_mock/script" @(@{ text = "看看工具表" }) $h
    $null = Send-Json "Post" "$base/api/agent/run" @{ goal = "随便说一句"; wait = $true } $h
    $saw = Mock-Saw
    if (-not ($saw.tools | Where-Object { $_ -like "mcp_demo_*" })) {
      throw "模型工具表里没有 mcp_demo_*：$($saw.tools -join ",")"
    }
  }
  Check "F3 直调 MCP 工具" {
    $r = Send-Json "Post" "$base/api/agent/mcp" @{ action = "call"; server = "demo"; tool = "hello"; args = @{ name = "baize" } } $h
    if ($r.error) { throw $r.error }
    if ($r.result.text -notmatch "baize") { throw "结果不对：$($r.result | ConvertTo-Json -Compress)" }
  }
  Check "F4 热重载 + 卸载" {
    $null = Send-Json "Post" "$base/api/agent/mcp" @{ action = "reload"; server = "demo" } $h
    $r = Send-Json "Post" "$base/api/agent/mcp" @{ action = "remove"; server = "demo" } $h
    if ($r.servers | Where-Object { $_.name -eq "demo" }) { throw "没卸载掉" }
  }

  # ================= G. 备份 / 校验 / 恢复 / 删除 =================
  Step "G. 备份与恢复"
  $bkName = $null
  Check "G1 创建备份（zip + sha256 清单）" {
    $r = Send-Json "Post" "$base/api/agent/backups" @{ note = "自检备份" } $h
    if ($r.manifest.entries.Count -lt 1) { throw "备份里没文件" }
    $script:bkName = (Get-Json "$base/api/agent/backups" $h).backups[0].name
  }
  Check "G2 校验备份完整性" {
    $r = Send-Json "Post" "$base/api/agent/backups/verify" @{ path = $script:bkName } $h
    if (-not $r.ok) { throw "校验没过：$($r.error)" }
  }
  Check "G3 dry-run 恢复（不动数据）" {
    $r = Send-Json "Post" "$base/api/agent/backups/restore" @{ path = $script:bkName; dryRun = $true } $h
    if ($r.error) { throw $r.error }
  }
  Check "G4 后端运行时真恢复必须被明确拒绝" {
    # 注意：恢复失败是以响应体里的 error 字段返回的（HTTP 仍是 200），不能只看状态码
    $r = Send-Json "Post" "$base/api/agent/backups/restore" @{ path = $script:bkName } $h
    if (-not $r.error) { throw ("运行中恢复竟然没报错：" + ($r | ConvertTo-Json -Compress -Depth 4)) }
    if ($r.error -notmatch "停止|占用|锁定|使用中") { throw "错误信息没说清怎么办：$($r.error)" }
  }
  Check "G5 删除备份" {
    $r = Send-Json "Post" "$base/api/agent/backups/delete" @{ path = $script:bkName } $h
    if ($r.error) { throw $r.error }
  }

  # ================= H. 配置 / 定时任务 =================
  Step "H. 配置与定时任务"
  Check "H1 改配置立即生效并落盘" {
    $null = Send-Json "Post" "$base/api/agent/config" @{ maxSteps = 5; allowShell = $false } $h
    $st = Get-Json "$base/api/agent/state" $h
    $cfgFile = Get-Content (Join-Path $dataDir "config.json") -Raw | ConvertFrom-Json
    if ($cfgFile.maxSteps -ne 5) { throw "配置没落盘：$($cfgFile.maxSteps)" }
  }
  Check "H2 加一条定时任务（1 分钟后触发）" {
    $r = Send-Json "Post" "$base/api/agent/config" @{ cronExpr = "* * * * *"; cronGoal = "定时自检：回一句 ok" } $h
    $st = Get-Json "$base/api/agent/state" $h
    if (-not $st.cron -or $st.cron.Count -lt 1) { throw "state 里看不到 cron：$($st | ConvertTo-Json -Compress -Depth 3)" }
  }
  Check "H3 定时任务到点真的跑了（最多等 90 秒）" {
    Clear-Runs
    $null = Send-Json "Post" "$mockBase/_mock/script" @(@{ text = "定时任务 ok" }) $h
    $before = (Get-Json "$base/api/agent/runs?limit=50" $h).runs.Count
    $hit = Wait-For { $r = Get-Json "$base/api/agent/runs?limit=50" $h; if ($r.runs.Count -gt $before) { $r } } 90 2000
    if (-not $hit) { throw "等了 90 秒没有新的运行记录（cron 没触发）" }
  }

  Check "H4 定时任务：改 / 停用 / 删除（新接口 /api/agent/cron）" {
    $added = Send-Json "Post" "$base/api/agent/cron" @{ action = "add"; expr = "0 8 * * *"; goal = "自检：定时任务增删改" } $h
    if (-not $added.added) { throw "add 没返回 id：$($added | ConvertTo-Json -Compress -Depth 4)" }
    $id = $added.added
    $off = Send-Json "Post" "$base/api/agent/cron" @{ action = "toggle"; id = $id; enabled = $false } $h
    $one = @($off.jobs | Where-Object { $_.id -eq $id })[0]
    if (-not $one) { throw "toggle 后列表里找不到 $id" }
    if ($one.enabled) { throw "toggle 应停用：$($one | ConvertTo-Json -Compress -Depth 4)" }
    $upd = Send-Json "Post" "$base/api/agent/cron" @{ action = "update"; id = $id; expr = "30 7 * * 1-5"; goal = "改过的目标" } $h
    $one = @($upd.jobs | Where-Object { $_.id -eq $id })[0]
    if ($one.expr -ne "30 7 * * 1-5" -or $one.goal -ne "改过的目标") { throw "update 没生效：$($one | ConvertTo-Json -Compress -Depth 4)" }
    $del = Send-Json "Post" "$base/api/agent/cron" @{ action = "remove"; id = $id } $h
    if (@($del.jobs | Where-Object { $_.id -eq $id }).Count -ne 0) { throw "remove 没删掉 $id" }
  }
  Check "H5 坏 cron 表达式 / 不存在的 id 必须被明确拒绝" {
    $rejected = $false
    try { $null = Send-Json "Post" "$base/api/agent/cron" @{ action = "add"; expr = "99 99 * * *"; goal = "应该被拒" } $h } catch { $rejected = $true }
    if (-not $rejected) { throw "非法表达式居然被接受了" }
    $rejected = $false
    try { $null = Send-Json "Post" "$base/api/agent/cron" @{ action = "remove"; id = "job-not-exist" } $h } catch { $rejected = $true }
    if (-not $rejected) { throw "删除不存在的 id 居然成功了" }
  }

  # ================= I. 技能 =================
  Step "I. 技能"
  Check "I1 新建技能（目录名 ASCII + 展示名中文）" {
    $content = "---`nname: 自检技能`ndescription: 自检用的技能`n---`n`n正文：先做 A`n"
    $r = Send-Json "Post" "$base/api/agent/skills" @{ action = "save"; name = "sweeptest"; content = $content } $h
    $one = @($r.skills | Where-Object { $_.slug -eq "sweeptest" })[0]
    if (-not $one) { throw "新建后清单里没有：$($r | ConvertTo-Json -Compress -Depth 4)" }
    if ($one.name -ne "自检技能") { throw "展示名不对：$($one.name)" }
  }
  Check "I2 缺 front-matter / 非法目录名必须被拒" {
    $rejected = $false
    try { $null = Send-Json "Post" "$base/api/agent/skills" @{ action = "create"; name = "badskill"; content = "没有 front-matter" } $h } catch { $rejected = $true }
    if (-not $rejected) { throw "缺 front-matter 居然被接受了" }
    $rejected = $false
    try { $null = Send-Json "Post" "$base/api/agent/skills" @{ action = "create"; name = "中文目录"; content = "---`nname: x`ndescription: y`n---`n`n正文" } $h } catch { $rejected = $true }
    if (-not $rejected) { throw "非法目录名居然被接受了" }
  }
  Check "I3 按展示名改写 + 局部替换（patch）" {
    $content = "---`nname: 自检技能`ndescription: 改过的描述`n---`n`n正文：只做 A`n"
    $r = Send-Json "Post" "$base/api/agent/skills" @{ action = "save"; name = "自检技能"; content = $content } $h
    $one = @($r.skills | Where-Object { $_.slug -eq "sweeptest" })[0]
    if (-not $one) { throw "按展示名改写后找不到（名字归一有问题）" }
    if ($one.description -ne "改过的描述") { throw "改写没生效：$($one.description)" }
    $p = Send-Json "Post" "$base/api/agent/skills" @{ action = "patch"; name = "sweeptest"; oldString = "只做 A"; newString = "只做 B" } $h
    $one = @($p.skills | Where-Object { $_.slug -eq "sweeptest" })[0]
    if ($one.body -notmatch "只做 B") { throw "patch 没生效：$($one.body)" }
  }
  Check "I4 删除技能（归档，清单里不再出现）" {
    $r = Send-Json "Post" "$base/api/agent/skills" @{ action = "delete"; name = "sweeptest" } $h
    if (@($r.skills | Where-Object { $_.slug -eq "sweeptest" }).Count -ne 0) { throw "删除后还在清单里" }
  }

  Write-Host ""
  Write-Host ("==== 自检结果：" + $script:pass + " 通过 / " + $script:fail + " 失败 ====") -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
  if ($script:fail -gt 0) {
    Write-Host "失败项：" -ForegroundColor Red
    $script:bad | ForEach-Object { Write-Host ("  - " + $_) -ForegroundColor Red }
  }
  Write-Host ("产物与日志：" + $work)
}
catch {
  Bad ("自检中断：" + $_.Exception.Message)
}
finally {
  if (-not $KeepRunning) {
    foreach ($k in @("phone", "desktop", "backend", "mock")) {
      if ($procs[$k] -and -not $procs[$k].HasExited) { Stop-Process -Id $procs[$k].Id -Force -ErrorAction SilentlyContinue }
    }
    Step "已停掉自检起的进程"
  } else {
    Step "保持运行（-KeepRunning）"
  }
}

exit $(if ($script:fail -eq 0) { 0 } else { 1 })
