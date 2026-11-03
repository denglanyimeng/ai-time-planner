@echo off
cd /d "%~dp0"
title AI Time Planner

rem ============================================================
rem  AI 智能时间规划效率助手 —— 启动脚本
rem
rem  用法：
rem    双击本文件                  启动服务并打开浏览器
rem    启动.bat /nobrowser         只启动服务，不开浏览器
rem
rem  说明：
rem    - 端口自动从 .env 的 PORT 读取，读不到则用 8080
rem    - 若 data\planner-server.exe 不存在，会尝试用 go 现场编译
rem    - 服务已在运行时不会重复启动，只打开浏览器
rem
rem  两条必须遵守的约束（都会导致脚本被 cmd 解析错乱）：
rem    1) 本文件必须存为 GBK，行尾必须是 CRLF；
rem       不要加 chcp 65001 —— cmd 按字节偏移跳转标签，
rem       中途切换代码页会让标签定位错乱。
rem    2) 所有 goto 一律写在顶层，不要放进 if ( ) 括号块内。
rem ============================================================

set "EXE=data\planner-server.exe"
set "PORT=8080"
set "NOBROWSER=0"
if /i "%~1"=="/nobrowser" set "NOBROWSER=1"

rem 从 .env 读取端口
if exist ".env" (
    for /f "tokens=2 delims==" %%p in ('findstr /b /c:"PORT=" ".env" 2^>nul') do set "PORT=%%p"
)
set "PORT=%PORT: =%"
if "%PORT%"=="" set "PORT=8080"

echo ============================================
echo   AI 智能时间规划效率助手
echo ============================================
echo.

rem ---------- 已在运行则不重复启动 ----------
call :checkport
if errorlevel 1 goto startserver
echo 服务已经在运行中。
goto open

rem ---------- 启动服务 ----------
:startserver
if not exist "%EXE%" goto buildfirst
echo 正在启动服务...
start "AI Time Planner Server" /min "%EXE%"
goto wait

:buildfirst
where go >nul 2>&1
if errorlevel 1 goto nogo
echo 未找到已编译的程序，正在编译（首次较慢，请稍候）...
go build -o "%EXE%" ./cmd/server
if errorlevel 1 goto buildfail
start "AI Time Planner Server" /min "%EXE%"
goto wait

:nogo
echo [错误] 找不到 %EXE%，系统里也没有 go 命令。
echo        请先在项目目录执行：go build -o %EXE% ./cmd/server
echo.
pause
exit /b 1

:buildfail
echo [错误] 编译失败，请查看上面的报错信息。
echo.
pause
exit /b 1

rem ---------- 轮询等待就绪 ----------
:wait
echo 等待服务就绪...
set /a TRIES=0
:waitloop
call :checkport
if not errorlevel 1 goto ready
set /a TRIES+=1
if %TRIES% geq 40 goto failed
ping -n 2 127.0.0.1 >nul
goto waitloop

:ready
echo 服务已就绪。
goto open

:failed
echo.
echo [错误] 等待 40 秒后服务仍未就绪。请依次检查：
echo        1) .env 里的数据库配置（DB_DRIVER 与对应的连接项）
echo        2) 端口 %PORT% 是否被其它程序占用
echo        3) 手动运行 "%EXE%" 查看具体报错
echo.
pause
exit /b 1

rem ---------- 打开浏览器 ----------
:open
if "%NOBROWSER%"=="1" goto justurl
echo 正在打开浏览器：http://localhost:%PORT%
start "" "http://localhost:%PORT%"
ping -n 4 127.0.0.1 >nul
exit /b 0

:justurl
echo 服务地址：http://localhost:%PORT%
echo （已按 /nobrowser 参数跳过打开浏览器）
exit /b 0

rem ---------- 子过程：端口是否在监听（0 = 在监听）----------
:checkport
netstat -an | findstr /r /c:":%PORT% .*LISTENING" >nul 2>&1
exit /b %errorlevel%