// Package model 定义数据库实体。
//
// 与原需求文档「数据库设计核心字段」相比，这里做了若干必要修正：
//
//  1. 补上 tags 表 —— 原文档 3.9 要求标签名称与配色自定义，但第 5 章没有任何标签表，
//     且把「标签颜色」冗余存在任务表里。
//  2. 补上 daily_tag_stats 表 —— 原文档要求「仅保留最近 21 天数据」，
//     同时又要求「长期目标累计学习时长统计」，二者直接矛盾：
//     明细删掉后就再也无法累计。解决办法是明细滚动清理、按标签的每日汇总永久保留。
//  3. 补上 order_index —— 原文档说「拖拽仅改顺序」，但没有存顺序的字段。
//  4. 补上计时权威字段 —— 原文档让前端计时，而「网页关闭即停止计时」在浏览器里
//     无法可靠实现（beforeunload 不保证触发、后台标签页会被节流）。
//     改为服务端记账：actual_seconds（已累计）+ run_started_at（本轮开始时刻）。
//  5. 补上 time_blocks 表 —— 原文档「自定义时间块」缺少落地载体。
//
// 关于下方的显式 column 标签：
// 业务代码里有若干处使用 Updates(map[string]any{...}) 做部分更新，需要写死列名。
// GORM 的默认命名策略对连续大写字母的断词并不直观 ——
// 例如 AIPlanAdvice 会被映射成 a_iplan_advice，而 AISummary 却是 ai_summary。
// 因此这里对每个字段都显式声明列名，让「Go 字段名 → 数据库列名」的映射
// 完全确定、可审查，不再依赖命名策略的推断。
package model

import "time"

// 任务状态。与 planner.Status 取值保持一致（此处为字符串常量，避免包循环依赖）。
const (
	StatusPending = "pending" // 待开始
	StatusRunning = "running" // 进行中
	StatusPaused  = "paused"  // 已暂停
	StatusDone    = "done"    // 已完成
	StatusUndone  = "undone"  // 未完成
)

// 任务来源。
const (
	SourceAI        = "ai"        // AI 生成
	SourceManual    = "manual"    // 手动新增
	SourceRecurring = "recurring" // 周期任务物化
	SourceCarryover = "carryover" // 承接历史未完成
)

// Tag 任务标签。颜色用于分类统计与长期目标绑定。
type Tag struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id"`
	Name      string    `gorm:"column:name;size:64;uniqueIndex;not null" json:"name"`
	Color     string    `gorm:"column:color;size:16;not null;default:#3b82f6" json:"color"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// Task 是单条计划任务。
//
// PlanDate 使用 "2006-01-02" 字符串表示「业务日期」，而不是 DATETIME：
// 业务上的「哪一天」和物理时间戳是两回事，用纯日期字符串可以彻底避开时区换算陷阱。
type Task struct {
	ID         int64  `gorm:"column:id;primaryKey" json:"id"`
	PlanDate   string `gorm:"column:plan_date;type:char(10);not null;index:idx_plan_order,priority:1;uniqueIndex:uk_recurring_date,priority:2" json:"planDate"`
	OrderIndex int    `gorm:"column:order_index;not null;default:0;index:idx_plan_order,priority:2" json:"orderIndex"`
	Name       string `gorm:"column:name;size:255;not null" json:"name"`
	TagID      *int64 `gorm:"column:tag_id;index" json:"tagId"`

	// 双维度预估：AI 预估 + 用户自填预估（用户未填时为 NULL）
	AIEstimateMin   int  `gorm:"column:ai_estimate_min;not null;default:0" json:"aiEstimateMin"`
	UserEstimateMin *int `gorm:"column:user_estimate_min" json:"userEstimateMin"`

	// 实际耗时（秒）。权威计时数据，由服务端结算。
	ActualSeconds int `gorm:"column:actual_seconds;not null;default:0" json:"actualSeconds"`

	Status string `gorm:"column:status;size:16;not null;default:pending;index" json:"status"`

	// 计划时间戳（派生结果，由排程引擎写入）
	PlannedStart *time.Time `gorm:"column:planned_start" json:"plannedStart"`
	PlannedEnd   *time.Time `gorm:"column:planned_end" json:"plannedEnd"`

	// 计时权威字段：本轮开始计时的时刻。为 NULL 表示当前未在计时。
	RunStartedAt *time.Time `gorm:"column:run_started_at" json:"runStartedAt"`
	// 最后一次前端心跳时刻。用于实现「网页关闭即停止计时」：
	// 心跳超时后由清理协程把计时结算到 LastHeartbeatAt，而不是无限累加。
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at" json:"lastHeartbeatAt"`
	CompletedAt     *time.Time `gorm:"column:completed_at" json:"completedAt"`

	Source      string `gorm:"column:source;size:16;not null;default:manual" json:"source"`
	RecurringID *int64 `gorm:"column:recurring_id;uniqueIndex:uk_recurring_date,priority:1" json:"recurringId"`
	CarryFromID *int64 `gorm:"column:carry_from_id" json:"carryFromId"`

	// 超时未确认：执行中越过当日截止时间且用户未表态，由后台任务归为未完成
	IsOvertime bool `gorm:"column:is_overtime;not null;default:false" json:"isOvertime"`

	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// EffectiveEstimateMin 返回排程实际采用的预估时长。
// 优先级：用户自填 > AI 预估 > 兜底默认值。
func (t Task) EffectiveEstimateMin(fallback int) int {
	if t.UserEstimateMin != nil && *t.UserEstimateMin > 0 {
		return *t.UserEstimateMin
	}
	if t.AIEstimateMin > 0 {
		return t.AIEstimateMin
	}
	if fallback > 0 {
		return fallback
	}
	return 15
}

// RecurringTask 周期任务模板。
type RecurringTask struct {
	ID          int64  `gorm:"column:id;primaryKey" json:"id"`
	Name        string `gorm:"column:name;size:255;not null" json:"name"`
	TagID       *int64 `gorm:"column:tag_id" json:"tagId"`
	DurationMin int    `gorm:"column:duration_min;not null;default:30" json:"durationMin"`

	// Frequency: daily / every2days / every3days / weekly
	Frequency string `gorm:"column:frequency;size:16;not null;default:daily" json:"frequency"`
	// Weekdays 仅在 weekly 时使用，逗号分隔，0=周日 … 6=周六
	Weekdays string `gorm:"column:weekdays;size:32;default:''" json:"weekdays"`

	EffectiveFrom string  `gorm:"column:effective_from;type:char(10);not null" json:"effectiveFrom"`
	EffectiveTo   *string `gorm:"column:effective_to;type:char(10)" json:"effectiveTo"` // NULL = 无限期

	Enabled   bool      `gorm:"column:enabled;not null;default:true" json:"enabled"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// LongTermGoal 长期目标。仅作参考展示、标签绑定与时长统计，不强制拆解到每日。
type LongTermGoal struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id"`
	Content   string    `gorm:"column:content;type:text;not null" json:"content"`
	TagID     *int64    `gorm:"column:tag_id;index" json:"tagId"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// DailyConfig 每日配置。PlanDate 作为主键，一天一行。
type DailyConfig struct {
	PlanDate string `gorm:"column:plan_date;type:char(10);primaryKey" json:"planDate"`

	// 当日截止时间，以「距零点分钟数」表示（如 1380 = 23:00），避开时区换算
	DeadlineMin int `gorm:"column:deadline_min;not null;default:1380" json:"deadlineMin"`
	BreakMin    int `gorm:"column:break_min;not null;default:0" json:"breakMin"`

	// 当日规划约束（仅本次生成生效，不做全局保存，但留存供复盘）
	ConstraintText string `gorm:"column:constraint_text;type:text" json:"constraintText"`
	// 生成计划时 AI 附带的科学规划建议（仪表盘统一展示）
	AIPlanAdvice string `gorm:"column:ai_plan_advice;type:text" json:"aiPlanAdvice"`
	// 当日 AI 效率总结
	AISummary string `gorm:"column:ai_summary;type:text" json:"aiSummary"`

	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// DailyTagStat 是按标签聚合的每日统计，永久保留、不参与 21 天清理。
//
// 这是本系统能够同时满足「21 天滚动」与「长期目标累计时长」的关键：
// 明细数据 21 天后删除，聚合数据永久留存，统计口径不丢失。
type DailyTagStat struct {
	PlanDate       string    `gorm:"column:plan_date;type:char(10);primaryKey" json:"planDate"`
	TagID          int64     `gorm:"column:tag_id;primaryKey" json:"tagId"`
	TotalSeconds   int       `gorm:"column:total_seconds;not null;default:0" json:"totalSeconds"`
	TotalEstMin    int       `gorm:"column:total_est_min;not null;default:0" json:"totalEstMin"`
	TaskCount      int       `gorm:"column:task_count;not null;default:0" json:"taskCount"`
	CompletedCount int       `gorm:"column:completed_count;not null;default:0" json:"completedCount"`
	UpdatedAt      time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// TimeBlock 用户自定义时间块。
// Weekday: -1 表示每天通用模板，0=周日 … 6=周六 表示仅该星期生效。
type TimeBlock struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id"`
	Weekday   int       `gorm:"column:weekday;not null;default:-1;index" json:"weekday"`
	StartMin  int       `gorm:"column:start_min;not null" json:"startMin"`
	EndMin    int       `gorm:"column:end_min;not null" json:"endMin"`
	Enabled   bool      `gorm:"column:enabled;not null;default:true" json:"enabled"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// SystemConfig 全局配置，单行（ID 固定为 1）。
type SystemConfig struct {
	ID                int64  `gorm:"column:id;primaryKey" json:"id"`
	AIBaseURL         string `gorm:"column:ai_base_url;size:255" json:"aiBaseUrl"`
	AIAPIKeyEncrypted string `gorm:"column:ai_api_key_encrypted;type:text" json:"-"`
	AIModel           string `gorm:"column:ai_model;size:64" json:"aiModel"`

	DefaultDeadlineMin int `gorm:"column:default_deadline_min;not null;default:1380" json:"defaultDeadlineMin"`
	DefaultBreakMin    int `gorm:"column:default_break_min;not null;default:0" json:"defaultBreakMin"`

	// 原始明细保留天数（默认 21 天，习惯养成周期）；聚合统计永久保留
	RetentionDays int `gorm:"column:retention_days;not null;default:21" json:"retentionDays"`

	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}
