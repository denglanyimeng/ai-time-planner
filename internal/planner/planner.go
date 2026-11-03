// Package planner 实现确定性的任务排程引擎。
//
// 设计原则（对原需求文档的关键校准）：
//
//  1. 排程是纯函数：不依赖数据库、不依赖 AI、不读系统时钟（now 由调用方注入），
//     因此可以被完整单测覆盖，行为完全可预测。
//
//  2. 所有时间运算都在服务端完成。AI 只负责产出「任务名 / 标签 / 预估时长 /
//     优先级」这类结构化草稿，绝不参与时间戳计算 —— 大模型的算术能力不可靠，
//     且让它算时间是纯粹的风险来源。
//
//  3. 顺序（OrderIndex）是权威，时间戳是派生结果。用户拖拽只改顺序，
//     「对齐北京时间」= 以当前时间重新调用 Schedule 刷新时间戳。
//
//  4. 放不进任何可用时间窗口的任务不会被丢弃，而是进入 Overflow（今日未完成池）。
package planner

import (
	"math"
	"sort"
	"time"
)

// Beijing 返回北京时区。中国不实行夏令时，固定 +08:00 偏移可避免依赖系统 tzdata
// （Windows 上 LoadLocation("Asia/Shanghai") 在缺少 tzdata 时可能失败）。
func Beijing() *time.Location {
	return time.FixedZone("CST", 8*60*60)
}

// MinDuration 是任务时长下限（分钟）。预估为 0 或负数时按下限排布，
// 避免出现零长度任务导致的时间戳塌陷。
const MinDuration = 1

// Status 是任务状态。
type Status string

const (
	StatusPending Status = "pending" // 待开始
	StatusRunning Status = "running" // 进行中
	StatusPaused  Status = "paused"  // 已暂停
	StatusDone    Status = "done"    // 已完成
	StatusUndone  Status = "undone"  // 未完成
)

// TaskInput 是排程引擎的输入任务。
// 调用方需负责过滤掉已完成任务，并（对进行中的任务）传入「剩余时长」。
type TaskInput struct {
	ID          int64
	OrderIndex  int
	DurationMin int
	Status      Status
}

// Window 是一个可用时间窗口（绝对时间，闭开区间 [Start, End)）。
type Window struct {
	Start time.Time
	End   time.Time
}

// Minutes 返回窗口长度（分钟）。
func (w Window) Minutes() float64 { return w.End.Sub(w.Start).Minutes() }

// Placement 表示某个任务被安排在哪个时间段。
type Placement struct {
	TaskID int64
	Start  time.Time
	End    time.Time
}

// Result 是排程输出。
type Result struct {
	Placed       []Placement // 成功排入的任务，按时间先后
	Overflow     []int64     // 放不下的任务 ID（保持原顺序）→ 今日未完成事项
	AvailableMin float64     // 当日可用总时长
	DemandMin    float64     // 待排任务总需求时长
	BreakUsedMin float64     // 实际插入的休息时长合计
	Overloaded   bool        // 需求 > 可用 → 触发超载预警
}

// LoadRatio 返回负载率（需求 / 可用）。可用为 0 且有需求时返回 +Inf。
func (r Result) LoadRatio() float64 {
	if r.AvailableMin <= 0 {
		if r.DemandMin > 0 {
			return math.Inf(1)
		}
		return 0
	}
	return r.DemandMin / r.AvailableMin
}

// Input 是排程输入。
//
// Now 与 Deadline 都必须使用同一时区（通常为 Beijing()）。
// Windows 为空时，视为「从现在到当日截止」一整段可用时间。
type Input struct {
	Now      time.Time // 当前时间，由调用方注入以保证可测
	Deadline time.Time // 当日截止时间；超过它的任务进入 Overflow
	Windows  []Window  // 用户自定义时间块；空则用 [Now, Deadline]
	BreakMin int       // 任务之间的休息间隔（分钟），尽力插入
	Tasks    []TaskInput
}

// Schedule 按顺序把任务排入可用时间窗口。
//
// 规则：
//   - 任务按 OrderIndex 排序后顺序填充，不拆分、不重排；
//   - 一个任务必须完整放进某一个窗口，放不下就顺延到下一个窗口；
//   - 所有窗口都放不下 → 进入 Overflow；
//   - 休息间隔是「尽力而为」：若插入休息会导致任务放不下，则贴着上一个任务排，
//     避免因为休息而制造无谓的溢出。
func Schedule(in Input) Result {
	var res Result

	slots := availableSlots(in)
	for _, s := range slots {
		res.AvailableMin += s.Minutes()
	}

	tasks := make([]TaskInput, len(in.Tasks))
	copy(tasks, in.Tasks)
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].OrderIndex < tasks[j].OrderIndex
	})

	for _, t := range tasks {
		res.DemandMin += float64(effectiveDuration(t.DurationMin))
	}

	brk := time.Duration(in.BreakMin) * time.Minute

	idx := 0
	for _, slot := range slots {
		if idx >= len(tasks) {
			break
		}
		var prevEnd time.Time // 本窗口内上一个任务的结束时间
		for idx < len(tasks) {
			dur := time.Duration(effectiveDuration(tasks[idx].DurationMin)) * time.Minute

			start := slot.Start
			if !prevEnd.IsZero() {
				start = prevEnd
				if brk > 0 {
					withBreak := prevEnd.Add(brk)
					// 只有在插入休息后仍然放得下时才真正休息
					if !withBreak.Add(dur).After(slot.End) {
						start = withBreak
					}
				}
			}
			if start.Before(slot.Start) {
				start = slot.Start
			}

			end := start.Add(dur)
			if end.After(slot.End) {
				break // 本窗口剩余空间不足，留给下一个窗口
			}

			res.Placed = append(res.Placed, Placement{
				TaskID: tasks[idx].ID,
				Start:  start,
				End:    end,
			})
			prevEnd = end
			idx++
		}
	}

	for ; idx < len(tasks); idx++ {
		res.Overflow = append(res.Overflow, tasks[idx].ID)
	}

	// 统计实际插入的休息时长（同一窗口内相邻任务之间的空隙）
	for i := 1; i < len(res.Placed); i++ {
		if gap := res.Placed[i].Start.Sub(res.Placed[i-1].End); gap > 0 {
			res.BreakUsedMin += gap.Minutes()
		}
	}

	res.Overloaded = res.DemandMin > res.AvailableMin
	return res
}

// availableSlots 计算真正可用于排程的时间窗口：
// 先把空输入补成 [Now, Deadline]，再按 Now / Deadline 裁剪，最后合并重叠窗口。
func availableSlots(in Input) []Window {
	raw := in.Windows
	if len(raw) == 0 {
		raw = []Window{{Start: in.Now, End: in.Deadline}}
	}

	clipped := make([]Window, 0, len(raw))
	for _, w := range raw {
		start, end := w.Start, w.End
		if start.Before(in.Now) {
			start = in.Now
		}
		if end.After(in.Deadline) {
			end = in.Deadline
		}
		if !end.After(start) {
			continue // 完全在现在之前或截止之后，无可用时间
		}
		clipped = append(clipped, Window{Start: start, End: end})
	}

	sort.Slice(clipped, func(i, j int) bool {
		return clipped[i].Start.Before(clipped[j].Start)
	})

	merged := make([]Window, 0, len(clipped))
	for _, w := range clipped {
		if n := len(merged); n > 0 && !w.Start.After(merged[n-1].End) {
			// 重叠或首尾相接 → 合并为一段连续可用时间
			if w.End.After(merged[n-1].End) {
				merged[n-1].End = w.End
			}
			continue
		}
		merged = append(merged, w)
	}
	return merged
}

// effectiveDuration 把非法/缺失的预估时长收敛到合法范围。
func effectiveDuration(min int) int {
	if min < MinDuration {
		return MinDuration
	}
	return min
}
