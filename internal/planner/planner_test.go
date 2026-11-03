package planner

import (
	"math"
	"testing"
	"time"
)

// bt 构造一个北京时间的时刻，固定用同一天以便断言。
func bt(h, m int) time.Time {
	return time.Date(2026, 9, 14, h, m, 0, 0, Beijing())
}

func win(h1, m1, h2, m2 int) Window {
	return Window{Start: bt(h1, m1), End: bt(h2, m2)}
}

func tasks(durations ...int) []TaskInput {
	out := make([]TaskInput, len(durations))
	for i, d := range durations {
		out[i] = TaskInput{ID: int64(i + 1), OrderIndex: i, DurationMin: d}
	}
	return out
}

// hm 把时刻格式化成 "15:04"，方便断言。
func hm(t time.Time) string { return t.Format("15:04") }

func TestScheduleBasicPacking(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
		Tasks:    tasks(60, 90, 30),
	})

	if len(res.Overflow) != 0 {
		t.Fatalf("不应有溢出，实际 %v", res.Overflow)
	}
	want := []struct{ start, end string }{
		{"09:00", "10:00"},
		{"10:00", "11:30"},
		{"11:30", "12:00"},
	}
	if len(res.Placed) != len(want) {
		t.Fatalf("排入 %d 个任务，期望 %d", len(res.Placed), len(want))
	}
	for i, w := range want {
		if got := hm(res.Placed[i].Start); got != w.start {
			t.Errorf("任务%d 开始 %s，期望 %s", i+1, got, w.start)
		}
		if got := hm(res.Placed[i].End); got != w.end {
			t.Errorf("任务%d 结束 %s，期望 %s", i+1, got, w.end)
		}
	}
}

func TestScheduleStartsFromNowWhenInsideWindow(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(9, 30),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
		Tasks:    tasks(60),
	})
	if got := hm(res.Placed[0].Start); got != "09:30" {
		t.Errorf("应从当前时间 09:30 开始，实际 %s", got)
	}
	if got := hm(res.Placed[0].End); got != "10:30" {
		t.Errorf("结束应为 10:30，实际 %s", got)
	}
}

func TestScheduleBreakInsertedWhenItFits(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
		BreakMin: 15,
		Tasks:    tasks(60, 60),
	})
	if got := hm(res.Placed[1].Start); got != "10:15" {
		t.Errorf("第二个任务应因休息在 10:15 开始，实际 %s", got)
	}
	if res.BreakUsedMin != 15 {
		t.Errorf("休息时长应为 15 分钟，实际 %v", res.BreakUsedMin)
	}
}

// 关键边界：休息会导致任务放不下时，必须放弃休息以保证任务不溢出。
func TestScheduleBreakDroppedWhenTooTight(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 10, 30)},
		BreakMin: 30,
		Tasks:    tasks(60, 30),
	})
	if len(res.Overflow) != 0 {
		t.Fatalf("休息不应导致溢出，实际溢出 %v", res.Overflow)
	}
	if got := hm(res.Placed[1].Start); got != "10:00" {
		t.Errorf("第二个任务应贴着排在 10:00，实际 %s", got)
	}
	if res.BreakUsedMin != 0 {
		t.Errorf("此处不应产生休息，实际 %v", res.BreakUsedMin)
	}
}

func TestScheduleMovesToNextWindowWhenNotFit(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 10, 0), win(14, 0, 15, 0)},
		Tasks:    tasks(45, 45),
	})
	if len(res.Overflow) != 0 {
		t.Fatalf("不应溢出，实际 %v", res.Overflow)
	}
	if got := hm(res.Placed[1].Start); got != "14:00" {
		t.Errorf("放不下的任务应顺延到下一个窗口 14:00，实际 %s", got)
	}
}

func TestScheduleOverflowBeyondDeadline(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(12, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
		Tasks:    tasks(60, 200),
	})
	if len(res.Placed) != 1 {
		t.Fatalf("应只排入 1 个任务，实际 %d", len(res.Placed))
	}
	if len(res.Overflow) != 1 || res.Overflow[0] != 2 {
		t.Fatalf("任务2 应进入未完成池，实际 %v", res.Overflow)
	}
}

func TestScheduleTaskLongerThanAnyWindow(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 10, 0)},
		Tasks:    tasks(120),
	})
	if len(res.Placed) != 0 {
		t.Fatalf("超长任务不应被排入，实际 %v", res.Placed)
	}
	if len(res.Overflow) != 1 {
		t.Fatalf("超长任务应全部进入未完成池，实际 %v", res.Overflow)
	}
}

func TestScheduleNoWindowsFallsBackToNowUntilDeadline(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(10, 0),
		Deadline: bt(12, 0),
		Tasks:    tasks(30),
	})
	if res.AvailableMin != 120 {
		t.Errorf("可用时长应为 120 分钟，实际 %v", res.AvailableMin)
	}
	if got := hm(res.Placed[0].Start); got != "10:00" {
		t.Errorf("应从 10:00 开始，实际 %s", got)
	}
}

func TestScheduleWindowClippedByDeadline(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(10, 0),
		Windows:  []Window{win(9, 0, 18, 0)},
		Tasks:    tasks(60, 30),
	})
	if res.AvailableMin != 60 {
		t.Errorf("窗口应被截止时间裁剪为 60 分钟，实际 %v", res.AvailableMin)
	}
	if len(res.Overflow) != 1 {
		t.Errorf("超出截止的任务应进入未完成池，实际 %v", res.Overflow)
	}
}

func TestScheduleMergesOverlappingWindows(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 11, 0), win(10, 0, 12, 0)},
		Tasks:    tasks(150),
	})
	if res.AvailableMin != 180 {
		t.Errorf("重叠窗口应合并为 180 分钟，实际 %v", res.AvailableMin)
	}
	if len(res.Placed) != 1 {
		t.Fatalf("合并后 150 分钟任务应放得下，实际 %v", res.Overflow)
	}
	if got := hm(res.Placed[0].End); got != "11:30" {
		t.Errorf("结束应为 11:30，实际 %s", got)
	}
}

func TestScheduleZeroDurationClampedToMinimum(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
		Tasks:    tasks(0),
	})
	if len(res.Placed) != 1 {
		t.Fatalf("零时长任务应被排入，实际 %v", res.Overflow)
	}
	if d := res.Placed[0].End.Sub(res.Placed[0].Start).Minutes(); d != float64(MinDuration) {
		t.Errorf("零时长应收敛为 %d 分钟，实际 %v", MinDuration, d)
	}
}

func TestScheduleOrderIndexIsAuthoritative(t *testing.T) {
	in := []TaskInput{
		{ID: 10, OrderIndex: 2, DurationMin: 30},
		{ID: 11, OrderIndex: 0, DurationMin: 30},
		{ID: 12, OrderIndex: 1, DurationMin: 30},
	}
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
		Tasks:    in,
	})
	wantOrder := []int64{11, 12, 10}
	for i, want := range wantOrder {
		if res.Placed[i].TaskID != want {
			t.Errorf("第%d 个排入的任务应为 %d，实际 %d", i+1, want, res.Placed[i].TaskID)
		}
	}
}

// 边界语义：需求「恰好等于」可用时长时不算超载 —— 计划刚好排满，完全可行。
// 只有需求「严格大于」可用时长才触发超载预警。
func TestScheduleOverloadBoundaryIsExclusive(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(10, 0),
		Tasks:    tasks(60, 60),
	})
	if res.Overloaded {
		t.Error("需求恰好等于可用时长（负载率 1.0）不应判定为超载")
	}
	if len(res.Overflow) != 0 {
		t.Errorf("刚好排满时不应有溢出，实际 %v", res.Overflow)
	}
	if got := res.LoadRatio(); got != 1 {
		t.Errorf("负载率应为 1，实际 %v", got)
	}
}

func TestScheduleOverloadDetected(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(9, 0),
		Tasks:    tasks(120),
	})
	if !res.Overloaded {
		t.Error("需求严格超过可用时长时应判定为超载")
	}
	if got := res.LoadRatio(); got != 2 {
		t.Errorf("负载率应为 2，实际 %v", got)
	}
	if len(res.Overflow) != 1 {
		t.Errorf("排不下的任务应进入未完成池，实际 %v", res.Overflow)
	}
}

func TestScheduleNowPastDeadlineYieldsAllOverflow(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(23, 30),
		Deadline: bt(23, 0),
		Tasks:    tasks(30),
	})
	if res.AvailableMin != 0 {
		t.Errorf("截止后可用时长应为 0，实际 %v", res.AvailableMin)
	}
	if len(res.Overflow) != 1 {
		t.Errorf("全部任务应进入未完成池，实际 %v", res.Overflow)
	}
	if got := res.LoadRatio(); !math.IsInf(got, 1) {
		t.Errorf("无可用时间且有需求时负载率应为 +Inf，实际 %v", got)
	}
}

func TestScheduleEmptyTaskList(t *testing.T) {
	res := Schedule(Input{
		Now:      bt(8, 0),
		Deadline: bt(23, 0),
		Windows:  []Window{win(9, 0, 12, 0)},
	})
	if len(res.Placed) != 0 || len(res.Overflow) != 0 {
		t.Errorf("空任务列表不应产生任何结果，实际 placed=%v overflow=%v", res.Placed, res.Overflow)
	}
	if res.Overloaded {
		t.Error("无任务时不应判定为超载")
	}
	if res.LoadRatio() != 0 {
		t.Errorf("无任务时负载率应为 0，实际 %v", res.LoadRatio())
	}
}
