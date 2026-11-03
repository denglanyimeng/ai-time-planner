// Package service 承载业务逻辑。
package service

import (
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"ai-time-planner/internal/ai"
	"ai-time-planner/internal/config"
	"ai-time-planner/internal/model"
)

var (
	// ErrNotFound 记录不存在
	ErrNotFound = errors.New("记录不存在")
	// ErrInvalid 参数无效
	ErrInvalid = errors.New("参数无效")
)

// Service 是业务逻辑入口。
type Service struct {
	db  *gorm.DB
	ai  *ai.Client
	cfg config.Config

	// mu 保护后台维护协程与手动清理之间的共享状态
	mu             sync.Mutex
	lastCleanupDay string
}

// New 创建 Service。
func New(db *gorm.DB, aiClient *ai.Client, cfg config.Config) *Service {
	return &Service{db: db, ai: aiClient, cfg: cfg}
}

// DB 暴露底层连接，供少数需要自定义查询的场景使用。
func (s *Service) DB() *gorm.DB { return s.db }

// AI 暴露当前生效的 AI 客户端（数据库配置优先，环境变量兜底）。
func (s *Service) AI() *ai.Client { return s.AIClient() }

// Now 返回当前 UTC 时间。系统内部统一用 UTC 存储，展示层再换算为北京时间。
func Now() time.Time { return time.Now().UTC() }

// Today 返回当前北京时间的业务日期。
func Today() string { return FormatDate(Now()) }

// ElapsedSeconds 结算任务的实际耗时（秒）。
//
// 权威公式：已累计秒数 + （若正在计时）当前时刻 − 本轮开始时刻。
//
// 之所以不在前端做计时器：前端计时在后台标签页会被浏览器节流，
// 页面被关闭或崩溃时也无法可靠上报，最终记录下来的时长必然失真。
// 服务端只存两个时刻，任何时刻都能精确还原真实耗时。
func ElapsedSeconds(t *model.Task, now time.Time) int {
	total := t.ActualSeconds
	if t.Status == model.StatusRunning && t.RunStartedAt != nil {
		if d := now.Sub(*t.RunStartedAt); d > 0 {
			total += int(d.Seconds())
		}
	}
	if total < 0 {
		return 0
	}
	return total
}

// settle 把任务的计时结算到 at 时刻，并停止本轮计时。
func settle(t *model.Task, at time.Time) {
	if t.RunStartedAt != nil {
		if d := at.Sub(*t.RunStartedAt); d > 0 {
			t.ActualSeconds += int(d.Seconds())
		}
		t.RunStartedAt = nil
	}
	t.LastHeartbeatAt = nil
}

// wrapNotFound 把 GORM 的未找到错误转换为业务错误。
func wrapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ensureDailyConfig 取得当日配置，不存在则按系统默认值创建。
func (s *Service) ensureDailyConfig(tx *gorm.DB, planDate string) (*model.DailyConfig, error) {
	var dc model.DailyConfig
	err := tx.Where("plan_date = ?", planDate).First(&dc).Error
	if err == nil {
		return &dc, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var sc model.SystemConfig
	if err := tx.First(&sc, 1).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		sc = model.SystemConfig{DefaultDeadlineMin: 23 * 60, RetentionDays: 21}
	}

	dc = model.DailyConfig{
		PlanDate:    planDate,
		DeadlineMin: sc.DefaultDeadlineMin,
		BreakMin:    sc.DefaultBreakMin,
	}
	if dc.DeadlineMin <= 0 {
		dc.DeadlineMin = 23 * 60
	}
	if err := tx.Create(&dc).Error; err != nil {
		return nil, err
	}
	return &dc, nil
}
