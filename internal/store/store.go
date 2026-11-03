// Package store 负责数据库连接与表结构迁移。
//
// 支持两种驱动：
//   - mysql（默认）：生产路径，配置见 .env 的 DB_* 项
//   - sqlite（可选）：零依赖单文件模式，适合「只想马上用起来」的场景
//
// 之所以保留双驱动而不是写死 MySQL：对单人本地工具来说，
// 为了用一个时间规划工具而必须先装好并常驻一个数据库服务，
// 是很高的使用门槛。SQLite 模式让整个应用退化成「一个可执行文件 + 一个数据文件」。
package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ai-time-planner/internal/config"
	"ai-time-planner/internal/model"
)

// Open 按配置建立数据库连接。
func Open(cfg config.DB) (*gorm.DB, error) {
	if strings.EqualFold(cfg.Driver, "sqlite") {
		return openSQLite(cfg)
	}
	return openMySQL(cfg)
}

// openMySQL 建立 MySQL 连接并配置连接池。
func openMySQL(cfg config.DB) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
		// 时间统一按 UTC 处理，业务日期换算在服务层完成
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("MySQL ping 失败: %w", err)
	}
	return db, nil
}

// openSQLite 打开单文件数据库。
//
// 连接数刻意限制为 1：SQLite 只允许单个写者，
// 并发写入会直接报 database is locked。单用户场景下串行执行完全够用，
// 而且彻底消除了这类偶发错误。
func openSQLite(cfg config.DB) (*gorm.DB, error) {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "data/planner.db"
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger:  gormlogger.Default.LogMode(gormlogger.Warn),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	// WAL 提升读写并发表现；busy_timeout 让偶发锁等待自动重试而不是直接报错
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("[store] 设置 WAL 模式失败（不影响使用）：%v", err)
	}
	if _, err := sqlDB.Exec("PRAGMA busy_timeout=5000"); err != nil {
		log.Printf("[store] 设置 busy_timeout 失败（不影响使用）：%v", err)
	}
	return db, nil
}

// Migrate 建表 / 补列，并初始化单行系统配置。
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.Tag{},
		&model.Task{},
		&model.RecurringTask{},
		&model.LongTermGoal{},
		&model.DailyConfig{},
		&model.DailyTagStat{},
		&model.TimeBlock{},
		&model.SystemConfig{},
	); err != nil {
		return fmt.Errorf("表结构迁移失败: %w", err)
	}

	// 初始化单行系统配置
	var n int64
	if err := db.Model(&model.SystemConfig{}).Where("id = ?", 1).Count(&n).Error; err != nil {
		return fmt.Errorf("检查系统配置失败: %w", err)
	}
	if n == 0 {
		cfg := model.SystemConfig{
			ID:                 1,
			AIBaseURL:          "https://api.deepseek.com",
			AIModel:            "deepseek-chat",
			DefaultDeadlineMin: 23 * 60,
			DefaultBreakMin:    0,
			RetentionDays:      21,
		}
		if err := db.Create(&cfg).Error; err != nil {
			return fmt.Errorf("初始化系统配置失败: %w", err)
		}
		log.Println("[store] 已初始化默认系统配置")
	}
	return nil
}

// EnsureDefaultTags 在没有标签时建立一组常用标签，避免首次使用界面空白。
func EnsureDefaultTags(db *gorm.DB) error {
	var n int64
	if err := db.Model(&model.Tag{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	defaults := []model.Tag{
		{Name: "学习", Color: "#3b82f6"},
		{Name: "工作", Color: "#ef4444"},
		{Name: "运动", Color: "#22c55e"},
		{Name: "阅读", Color: "#a855f7"},
		{Name: "生活", Color: "#f59e0b"},
	}
	if err := db.Create(&defaults).Error; err != nil {
		return fmt.Errorf("初始化默认标签失败: %w", err)
	}
	log.Println("[store] 已初始化默认标签")
	return nil
}
