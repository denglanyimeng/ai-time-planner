package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ai-time-planner/internal/model"
	"ai-time-planner/internal/planner"
)

// DateLayout 是系统统一的「业务日期」格式。
const DateLayout = "2006-01-02"

// 周期类型。
const (
	FreqDaily  = "daily"
	FreqEvery2 = "every2days"
	FreqEvery3 = "every3days"
	FreqWeekly = "weekly"
)

// RecurringMatches 判断某条周期任务模板是否应在 planDate 当天生成实例。
//
// 原需求文档只写了「可设置：周期频率 + 任务结束日期」，没有定义频率语义。
// 这里把它固定为可测的明确规则：
//   - daily      ：每天
//   - every2days ：自生效日期起每 2 天一次（生效日当天算第 1 天）
//   - every3days ：自生效日期起每 3 天一次
//   - weekly     ：指定星期几（0=周日 … 6=周六），可多选
//
// 生效区间为闭区间：生效日期 <= planDate <= 结束日期。结束日期为空表示无限期。
func RecurringMatches(t model.RecurringTask, planDate string) (bool, error) {
	day, err := parseDate(planDate)
	if err != nil {
		return false, err
	}
	from, err := parseDate(t.EffectiveFrom)
	if err != nil {
		return false, fmt.Errorf("生效日期无效: %w", err)
	}
	if day.Before(from) {
		return false, nil
	}
	if t.EffectiveTo != nil && strings.TrimSpace(*t.EffectiveTo) != "" {
		to, err := parseDate(*t.EffectiveTo)
		if err != nil {
			return false, fmt.Errorf("结束日期无效: %w", err)
		}
		if day.After(to) {
			return false, nil
		}
	}

	switch strings.ToLower(strings.TrimSpace(t.Frequency)) {
	case FreqDaily, "":
		return true, nil
	case FreqEvery2:
		return daysBetween(from, day)%2 == 0, nil
	case FreqEvery3:
		return daysBetween(from, day)%3 == 0, nil
	case FreqWeekly:
		wk, err := parseWeekdays(t.Weekdays)
		if err != nil {
			return false, err
		}
		return wk[int(day.Weekday())], nil
	default:
		return false, fmt.Errorf("未知周期类型: %q", t.Frequency)
	}
}

// ParseDate 解析业务日期字符串。
func ParseDate(s string) (time.Time, error) { return parseDate(s) }

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("日期为空")
	}
	return time.ParseInLocation(DateLayout, s, planner.Beijing())
}

// FormatDate 把时间格式化为业务日期。
func FormatDate(t time.Time) string { return t.In(planner.Beijing()).Format(DateLayout) }

// daysBetween 返回 b-a 的天数。
// 两个入参都是固定时区（+08:00）的零点，因此整除 24h 是精确的，不存在夏令时误差。
func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}

// parseWeekdays 解析 "1,3,5" 这样的星期列表（0=周日 … 6=周六）。
func parseWeekdays(s string) ([7]bool, error) {
	var out [7]bool
	s = strings.TrimSpace(s)
	if s == "" {
		return out, fmt.Errorf("weekly 周期必须指定星期（0=周日 … 6=周六）")
	}
	got := false
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 6 {
			return out, fmt.Errorf("星期取值必须是 0-6，收到 %q", part)
		}
		out[n] = true
		got = true
	}
	if !got {
		return out, fmt.Errorf("weekly 周期必须指定至少一个星期")
	}
	return out, nil
}
