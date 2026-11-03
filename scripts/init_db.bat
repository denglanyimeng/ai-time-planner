@echo off
chcp 65001 >nul
setlocal

set MYSQL="C:\Program Files\MySQL\MySQL Server 8.0\bin\mysql.exe"
set SQLFILE=%~dp0init_db.sql

echo ============================================
echo  初始化 ai_time_planner 数据库
echo ============================================
echo.
echo 接下来会提示输入 MySQL 的 root 密码。
echo.

if not exist %MYSQL% (
  echo [错误] 未找到 mysql.exe：%MYSQL%
  echo 请修改本文件中的 MYSQL 变量为你机器上的实际路径。
  pause
  exit /b 1
)

%MYSQL% -u root -p < "%SQLFILE%"
if errorlevel 1 (
  echo.
  echo [失败] 初始化未成功，请检查 root 密码是否正确。
  pause
  exit /b 1
)

echo.
echo [成功] 数据库 ai_time_planner 与账号 planner_app 已创建。
echo 请记得把 scripts\init_db.sql 里的密码同步写入项目根目录的 .env 文件。
pause
