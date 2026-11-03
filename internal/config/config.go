// Package config 负责加载运行配置。
//
// 优先级：真实环境变量 > 项目根目录 .env 文件 > 代码内默认值。
// 不引入第三方依赖，保持依赖面最小。
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Port  string
	DB    DB
	Redis Redis
	AI    AI
}

type DB struct {
	// Driver: mysql（默认）或 sqlite
	Driver string
	// Path 仅在 sqlite 模式下使用
	Path string

	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

// IsSQLite 返回是否使用单文件模式。
func (d DB) IsSQLite() bool { return strings.EqualFold(d.Driver, "sqlite") }

// DSN 返回 MySQL 连接串。
//
// 时间统一以 UTC 存储（loc=UTC），只在「业务日期」与展示层换算为北京时间。
// 这样可以避免 MySQL 驱动在 Windows 上解析时区名失败，也不会因为机器时区
// 设置错误而把时间写歪 —— 时区问题在时间管理类项目里是最常见的隐性事故源。
func (d DB) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC",
		d.User, d.Password, d.Host, d.Port, d.Name,
	)
}

type Redis struct {
	Enabled  bool
	Addr     string
	Password string
	DB       int
}

type AI struct {
	BaseURL        string
	APIKey         string
	Model          string
	TimeoutSeconds int
}

// Enabled 表示是否配置了真实可用的 AI 接口。未配置时系统降级为本地规则生成。
func (a AI) Enabled() bool { return strings.TrimSpace(a.APIKey) != "" }

// Load 读取配置。
func Load() Config {
	env := readDotEnv()

	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		if v, ok := env[key]; ok && v != "" {
			return v
		}
		return def
	}
	getInt := func(key string, def int) int {
		if n, err := strconv.Atoi(strings.TrimSpace(get(key, ""))); err == nil {
			return n
		}
		return def
	}
	getBool := func(key string, def bool) bool {
		switch strings.ToLower(strings.TrimSpace(get(key, ""))) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
		return def
	}

	return Config{
		Port: get("PORT", "8080"),
		DB: DB{
			// 默认 mysql；设为 sqlite 可零依赖单文件运行
			Driver:   strings.ToLower(get("DB_DRIVER", "mysql")),
			Path:     get("DB_PATH", "data/planner.db"),
			Host:     get("DB_HOST", "127.0.0.1"),
			Port:     get("DB_PORT", "3306"),
			User:     get("DB_USER", "planner_app"),
			Password: get("DB_PASSWORD", ""),
			Name:     get("DB_NAME", "ai_time_planner"),
		},
		Redis: Redis{
			// 默认关闭：单用户本地工具，Redis 只是可选加速器，不应成为运行门槛。
			Enabled:  getBool("REDIS_ENABLED", false),
			Addr:     get("REDIS_ADDR", "127.0.0.1:6379"),
			Password: get("REDIS_PASSWORD", ""),
			DB:       getInt("REDIS_DB", 0),
		},
		AI: AI{
			BaseURL:        strings.TrimRight(get("AI_BASE_URL", "https://api.deepseek.com"), "/"),
			APIKey:         get("AI_API_KEY", ""),
			Model:          get("AI_MODEL", "deepseek-chat"),
			TimeoutSeconds: getInt("AI_TIMEOUT_SECONDS", 60),
		},
	}
}

// readDotEnv 从项目根目录读取 .env（若存在）。解析失败的行会被忽略，
// 不会因为一个手写文件的格式问题导致服务起不来。
func readDotEnv() map[string]string {
	out := map[string]string{}

	path := findDotEnv()
	if path == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// 去掉可能存在的成对引号
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out
}

// findDotEnv 从当前工作目录向上逐级查找 .env，
// 这样无论从仓库根目录还是从 cmd/server 启动都能找到配置。
func findDotEnv() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
