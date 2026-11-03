-- ============================================================
-- AI 智能时间规划效率助手 —— 数据库初始化脚本
-- 用法（在 PowerShell 或 CMD 中执行，会提示输入 MySQL root 密码）：
--   & "C:\Program Files\MySQL\MySQL Server 8.0\bin\mysql.exe" -u root -p < scripts\init_db.sql
-- 或用仓库内的 scripts\init_db.bat 双击执行。
-- ============================================================

CREATE DATABASE IF NOT EXISTS ai_time_planner
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

-- 专用应用账号：不使用 root，权限仅限本库
-- ⚠️ 请把下面的密码改成你自己的，并同步写入项目根目录的 .env 文件
CREATE USER IF NOT EXISTS 'planner_app'@'localhost' IDENTIFIED BY 'planner_local_pw_2026';
CREATE USER IF NOT EXISTS 'planner_app'@'127.0.0.1' IDENTIFIED BY 'planner_local_pw_2026';

GRANT ALL PRIVILEGES ON ai_time_planner.* TO 'planner_app'@'localhost';
GRANT ALL PRIVILEGES ON ai_time_planner.* TO 'planner_app'@'127.0.0.1';

FLUSH PRIVILEGES;

-- 验证
SELECT SCHEMA_NAME AS created_database
FROM information_schema.SCHEMATA
WHERE SCHEMA_NAME = 'ai_time_planner';

SELECT User, Host FROM mysql.user WHERE User = 'planner_app';
