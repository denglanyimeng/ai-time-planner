package service

import (
	"testing"

	"ai-time-planner/internal/model"
)

func ptr(s string) *string { return &s }

func TestRecurringMatchesDaily(t *testing.T) {
	tpl := model.RecurringTask{Frequency: FreqDaily, EffectiveFrom: "2026-09-01"}
	for _, d := range []string{"2026-09-01", "2026-09-02", "2026-12-31"} {
		ok, err := RecurringMatches(tpl, d)
		if err != nil {
			t.Fatalf("%s 出错: %v", d, err)
		}
		if !ok {
			t.Errorf("daily 周期在 %s 应生效", d)
		}
	}
}

func TestRecurringMatchesRespectsEffectiveWindow(t *testing.T) {
	tpl := model.RecurringTask{
		Frequency:     FreqDaily,
		EffectiveFrom: "2026-09-10",
		EffectiveTo:   ptr("2026-09-20"),
	}
	cases := map[string]bool{
		"2026-09-09": false, // 生效日期之前
		"2026-09-10": true,  // 生效当天（闭区间）
		"2026-09-15": true,
		"2026-09-20": true, // 结束当天（闭区间）
		"2026-09-21": false,
	}
	for d, want := range cases {
		got, err := RecurringMatches(tpl, d)
		if err != nil {
			t.Fatalf("%s 出错: %v", d, err)
		}
		if got != want {
			t.Errorf("%s 期望 %v，实际 %v", d, want, got)
		}
	}
}

func TestRecurringMatchesEveryNDays(t *testing.T) {
	tpl := model.RecurringTask{Frequency: FreqEvery2, EffectiveFrom: "2026-09-14"}
	cases := map[string]bool{
		"2026-09-14": true, // 第 1 天
		"2026-09-15": false,
		"2026-09-16": true,
		"2026-09-17": false,
		"2026-09-18": true,
	}
	for d, want := range cases {
		got, err := RecurringMatches(tpl, d)
		if err != nil {
			t.Fatalf("%s 出错: %v", d, err)
		}
		if got != want {
			t.Errorf("every2days %s 期望 %v，实际 %v", d, want, got)
		}
	}

	tpl3 := model.RecurringTask{Frequency: FreqEvery3, EffectiveFrom: "2026-09-14"}
	cases3 := map[string]bool{
		"2026-09-14": true,
		"2026-09-15": false,
		"2026-09-16": false,
		"2026-09-17": true,
	}
	for d, want := range cases3 {
		got, err := RecurringMatches(tpl3, d)
		if err != nil {
			t.Fatalf("%s 出错: %v", d, err)
		}
		if got != want {
			t.Errorf("every3days %s 期望 %v，实际 %v", d, want, got)
		}
	}
}

// 2026-09-14 是周一。
func TestRecurringMatchesWeekly(t *testing.T) {
	tpl := model.RecurringTask{
		Frequency:     FreqWeekly,
		Weekdays:      "1,3", // 周一、周三
		EffectiveFrom: "2026-09-01",
	}
	cases := map[string]bool{
		"2026-09-14": true,  // 周一
		"2026-09-15": false, // 周二
		"2026-09-16": true,  // 周三
		"2026-09-20": false, // 周日
	}
	for d, want := range cases {
		got, err := RecurringMatches(tpl, d)
		if err != nil {
			t.Fatalf("%s 出错: %v", d, err)
		}
		if got != want {
			t.Errorf("weekly %s 期望 %v，实际 %v", d, want, got)
		}
	}
}

func TestRecurringMatchesRejectsBadInput(t *testing.T) {
	if _, err := RecurringMatches(
		model.RecurringTask{Frequency: FreqWeekly, Weekdays: "", EffectiveFrom: "2026-09-01"},
		"2026-09-14",
	); err == nil {
		t.Error("weekly 未指定星期应报错，而不是静默不生效")
	}

	if _, err := RecurringMatches(
		model.RecurringTask{Frequency: FreqWeekly, Weekdays: "9", EffectiveFrom: "2026-09-01"},
		"2026-09-14",
	); err == nil {
		t.Error("星期取值 9 应报错")
	}

	if _, err := RecurringMatches(
		model.RecurringTask{Frequency: "monthly", EffectiveFrom: "2026-09-01"},
		"2026-09-14",
	); err == nil {
		t.Error("未知周期类型应报错")
	}

	if _, err := RecurringMatches(
		model.RecurringTask{Frequency: FreqDaily, EffectiveFrom: "not-a-date"},
		"2026-09-14",
	); err == nil {
		t.Error("非法生效日期应报错")
	}

	if _, err := RecurringMatches(
		model.RecurringTask{Frequency: FreqDaily, EffectiveFrom: "2026-09-01"},
		"2026-13-45",
	); err == nil {
		t.Error("非法业务日期应报错")
	}
}

func TestDaysBetweenIsExactAcrossMonthBoundary(t *testing.T) {
	a, _ := parseDate("2026-08-31")
	b, _ := parseDate("2026-09-01")
	if got := daysBetween(a, b); got != 1 {
		t.Errorf("跨月天数应为 1，实际 %d", got)
	}
	c, _ := parseDate("2026-12-31")
	d, _ := parseDate("2027-01-01")
	if got := daysBetween(c, d); got != 1 {
		t.Errorf("跨年天数应为 1，实际 %d", got)
	}
}
