package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"ai-time-planner/internal/ai"
	"ai-time-planner/internal/model"
	"ai-time-planner/internal/planner"
)

// TaskDTO 是返回给前端的任务视图，附带服务端结算出的实际耗时。
type TaskDTO struct {
	model.Task
	ElapsedSeconds int    `json:"elapsedSeconds"`
	TagName        string `json:"tagName"`
	TagColor       string `json:"tagColor"`
	// EstimateMin 是排程实际采用的预估（用户自填优先，其次 AI 预估）
	EstimateMin int `json:"estimateMin"`
	// OvertimeNow 表示此刻已越过当日截止时间且任务仍在进行中
	OvertimeNow bool `json:"overtimeNow"`
}

// ScheduleOutcome 描述一次排程的结果。
type ScheduleOutcome struct {
	Placed       int     `json:"placed"`
	Overflow     []int64 `json:"overflow"`
	AvailableMin float64 `json:"availableMin"`
	DemandMin    float64 `json:"demandMin"`
	LoadRatio    float64 `json:"loadRatio"`
	Overloaded   bool    `json:"overloaded"`
}

// windowsFor 根据时间块模板构造某一天的可用时间窗口。
//
// 规则：若该星期存在专属时间块，则只用专属的；否则使用「每天通用」模板。
// 这样用户既可以配一套通用作息，也可以为特定星期单独调整。
func (s *Service) windowsFor(planDate string) ([]planner.Window, error) {
	day, err := parseDate(planDate)
	if err != nil {
		return nil, err
	}
	weekday := int(day.Weekday())

	var blocks []model.TimeBlock
	if err := s.db.Where("enabled = ? AND weekday = ?", true, weekday).
		Order("start_min").Find(&blocks).Error; err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		if err := s.db.Where("enabled = ? AND weekday = ?", true, -1).
			Order("start_min").Find(&blocks).Error; err != nil {
			return nil, err
		}
	}

	windows := make([]planner.Window, 0, len(blocks))
	for _, b := range blocks {
		if b.EndMin <= b.StartMin {
			continue // 跨零点的块暂不支持，跳过而不是产生负长度窗口
		}
		windows = append(windows, planner.Window{
			Start: day.Add(time.Duration(b.StartMin) * time.Minute),
			End:   day.Add(time.Duration(b.EndMin) * time.Minute),
		})
	}
	return windows, nil
}

// tasksForDate 读取某天的全部任务，按顺序排列。
func (s *Service) tasksForDate(planDate string, includeDone bool) ([]model.Task, error) {
	q := s.db.Where("plan_date = ?", planDate)
	if !includeDone {
		q = q.Where("status <> ?", model.StatusDone)
	}
	var tasks []model.Task
	if err := q.Order("order_index, id").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// Reschedule 以「当前北京时间」为基准重新排列某天的任务，
// 并刷新全部时间戳。对应原需求里的「一键对齐当前北京时间」。
//
// 这是排程的唯一入口：暂停恢复后的整体顺延、截止时间变更、拖拽后重新对齐，
// 全部走这一条路径，保证行为一致、可预测。
func (s *Service) Reschedule(planDate string) (*ScheduleOutcome, error) {
	var out ScheduleOutcome

	dc, err := s.ensureDailyConfig(s.db, planDate)
	if err != nil {
		return nil, err
	}
	windows, err := s.windowsFor(planDate)
	if err != nil {
		return nil, err
	}
	deadline, err := s.deadlineTime(planDate, dc.DeadlineMin)
	if err != nil {
		return nil, err
	}

	// 已完成的任务不参与重排；其余（待开始/进行中/已暂停/未完成）都重新尝试排入。
	// 这样用户延长截止时间后再次对齐，之前排不下的任务能自动回到计划里。
	tasks, err := s.tasksForDate(planDate, false)
	if err != nil {
		return nil, err
	}

	now := Now()
	inputs := make([]planner.TaskInput, 0, len(tasks))
	for _, t := range tasks {
		inputs = append(inputs, planner.TaskInput{
			ID:          t.ID,
			OrderIndex:  t.OrderIndex,
			DurationMin: remainingEstimateMin(t, now),
			Status:      planner.Status(t.Status),
		})
	}

	res := planner.Schedule(planner.Input{
		Now:      now,
		Deadline: deadline,
		Windows:  windows,
		BreakMin: dc.BreakMin,
		Tasks:    inputs,
	})

	out.Placed = len(res.Placed)
	out.Overflow = res.Overflow
	out.AvailableMin = res.AvailableMin
	out.DemandMin = res.DemandMin
	out.LoadRatio = res.LoadRatio()
	out.Overloaded = res.Overloaded

	placedAt := make(map[int64]planner.Placement, len(res.Placed))
	for _, p := range res.Placed {
		placedAt[p.TaskID] = p
	}
	overflowSet := make(map[int64]bool, len(res.Overflow))
	for _, id := range res.Overflow {
		overflowSet[id] = true
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		for i := range tasks {
			t := &tasks[i]
			if p, ok := placedAt[t.ID]; ok {
				start, end := p.Start, p.End
				t.PlannedStart = &start
				t.PlannedEnd = &end
				// 之前因排不下被归为未完成的，现在排得下就恢复为待开始
				if t.Status == model.StatusUndone {
					t.Status = model.StatusPending
					t.IsOvertime = false
				}
			} else if overflowSet[t.ID] {
				// 超出当日截止时间 → 归入今日未完成事项，并清空无意义的计划时间
				t.PlannedStart = nil
				t.PlannedEnd = nil
				if t.Status != model.StatusRunning {
					t.Status = model.StatusUndone
				}
			}
			if err := tx.Save(t).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// remainingEstimateMin 返回任务剩余需要的时间。
//
// 对正在计时或已暂停的任务，已经花掉的时间要从预估里扣掉，
// 否则重排会把已完成的部分重复计算一遍，导致时间轴整体虚长。
func remainingEstimateMin(t model.Task, now time.Time) int {
	est := t.EffectiveEstimateMin(15)
	used := ElapsedSeconds(&t, now) / 60
	if used <= 0 {
		return est
	}
	if used >= est {
		// 已经超出预估：仍保留一个最小值，避免任务在时间轴上塌缩成零长度
		return 5
	}
	return est - used
}

// Reorder 按给定 ID 顺序调整任务顺序。
//
// 严格遵循原需求规则：拖拽「仅改顺序，不自动改时间戳」。
// 时间戳只有用户显式点击「对齐北京时间」才会刷新。
func (s *Service) Reorder(planDate string, orderedIDs []int64) error {
	tasks, err := s.tasksForDate(planDate, true)
	if err != nil {
		return err
	}
	if len(tasks) != len(orderedIDs) {
		return fmt.Errorf("%w: 提交的任务数量(%d)与当日任务数量(%d)不一致",
			ErrInvalid, len(orderedIDs), len(tasks))
	}

	valid := make(map[int64]bool, len(tasks))
	for _, t := range tasks {
		valid[t.ID] = true
	}
	seen := make(map[int64]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if !valid[id] {
			return fmt.Errorf("%w: 任务 %d 不属于 %s", ErrInvalid, id, planDate)
		}
		if seen[id] {
			return fmt.Errorf("%w: 任务 %d 重复提交", ErrInvalid, id)
		}
		seen[id] = true
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range orderedIDs {
			if err := tx.Model(&model.Task{}).Where("id = ?", id).
				Update("order_index", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GenerateOutcome 描述一次 AI 生成计划的结果。
type GenerateOutcome struct {
	Source       string           `json:"source"` // ai / local
	Advice       string           `json:"advice"`
	CreatedCount int              `json:"createdCount"`
	Schedule     *ScheduleOutcome `json:"schedule"`
	Notice       string           `json:"notice"`
}

// GenerateOptions 是生成计划的入参。
type GenerateOptions struct {
	PlanDate   string
	UserInput  string
	Constraint string
	// Replace 为 true 时，先清掉当日由 AI 生成且尚未开始的任务，再重新生成。
	// 手动新增、周期任务、承接任务不会被清除。
	Replace bool
}

// GeneratePlan 生成当日计划。
//
// 流程刻意分成两段，这是本项目最重要的架构决策：
//
//	第一段（不确定性）：让大模型把自然语言拆成结构化任务草稿
//	                     —— 只有任务名、标签、预估时长、优先级，没有任何时间点。
//	第二段（确定性）：服务端用 planner 引擎把草稿排进时间轴，
//	                     计算所有时间戳、判断溢出和超载。
//
// 原需求文档把两段混在一起，要求「AI 刷新全部时间戳」，
// 这会让时间戳的正确性依赖模型的算术能力，属于设计缺陷。
func (s *Service) GeneratePlan(ctx context.Context, opts GenerateOptions) (*GenerateOutcome, error) {
	if strings.TrimSpace(opts.UserInput) == "" {
		return nil, fmt.Errorf("%w: 请先描述你今天想完成的事情", ErrInvalid)
	}

	dc, err := s.ensureDailyConfig(s.db, opts.PlanDate)
	if err != nil {
		return nil, err
	}
	windows, err := s.windowsFor(opts.PlanDate)
	if err != nil {
		return nil, err
	}
	deadline, err := s.deadlineTime(opts.PlanDate, dc.DeadlineMin)
	if err != nil {
		return nil, err
	}

	// 用排程引擎自身算可用时长，保证「AI 看到的可用时间」和「实际排程用的时间」完全一致
	now := Now()
	avail := planner.Schedule(planner.Input{
		Now:      now,
		Deadline: deadline,
		Windows:  windows,
		BreakMin: dc.BreakMin,
	})

	tags, err := s.listTagNames()
	if err != nil {
		return nil, err
	}
	existing, err := s.tasksForDate(opts.PlanDate, true)
	if err != nil {
		return nil, err
	}

	var busy []string
	for _, t := range existing {
		busy = append(busy, t.Name)
	}

	var draft *ai.PlanDraft
	source := "ai"
	notice := ""

	client := s.AIClient()
	if client.Configured() {
		draft, err = client.GeneratePlan(ctx, ai.PlanRequest{
			PlanDate:     opts.PlanDate,
			UserInput:    opts.UserInput,
			Constraint:   opts.Constraint,
			AvailableMin: int(avail.AvailableMin),
			Tags:         tags,
			BusyNote:     strings.Join(busy, "、"),
		})
		if err != nil {
			// AI 失败不能让整个功能不可用：降级到本地规则，并如实告知用户
			draft = localDraft(opts.UserInput, 30)
			source = "local"
			notice = "AI 调用失败，已改用本地规则生成：" + err.Error()
		}
	} else {
		draft = localDraft(opts.UserInput, 30)
		source = "local"
		notice = "尚未配置 AI 接口，已使用本地规则生成计划（可在设置中配置后获得更智能的拆分）。"
	}

	if draft == nil || len(draft.Tasks) == 0 {
		return nil, fmt.Errorf("%w: 没能从你的描述中拆出任何任务", ErrInvalid)
	}

	created, err := s.persistDraft(opts, draft)
	if err != nil {
		return nil, err
	}

	// 保存建议与本次约束
	if err := s.db.Model(&model.DailyConfig{}).Where("plan_date = ?", opts.PlanDate).
		Updates(map[string]any{
			"ai_plan_advice":  draft.Advice,
			"constraint_text": opts.Constraint,
			"updated_at":      Now(),
		}).Error; err != nil {
		return nil, err
	}

	sched, err := s.Reschedule(opts.PlanDate)
	if err != nil {
		return nil, err
	}

	out := &GenerateOutcome{
		Source:       source,
		Advice:       draft.Advice,
		CreatedCount: created,
		Schedule:     sched,
		Notice:       notice,
	}
	if sched.Overloaded {
		if out.Notice != "" {
			out.Notice += " "
		}
		out.Notice += fmt.Sprintf("当前任务总量约 %.0f 分钟，超出今日可用 %.0f 分钟，负载偏高，建议删减或调整时间块。",
			sched.DemandMin, sched.AvailableMin)
	}
	return out, nil
}

// persistDraft 把 AI 草稿落库为任务。
func (s *Service) persistDraft(opts GenerateOptions, draft *ai.PlanDraft) (int, error) {
	created := 0

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if opts.Replace {
			// 只清理「AI 生成且尚未开始」的任务，避免误删用户手动维护的内容
			if err := tx.Where("plan_date = ? AND source = ? AND status IN ?",
				opts.PlanDate, model.SourceAI,
				[]string{model.StatusPending, model.StatusPaused}).
				Delete(&model.Task{}).Error; err != nil {
				return err
			}
		}

		var maxOrder int
		if err := tx.Model(&model.Task{}).Where("plan_date = ?", opts.PlanDate).
			Select("COALESCE(MAX(order_index), -1)").Scan(&maxOrder).Error; err != nil {
			return err
		}

		for i, d := range draft.Tasks {
			tagID, err := s.findOrCreateTag(tx, d.Tag)
			if err != nil {
				return err
			}
			est := d.EstimateMin
			task := model.Task{
				PlanDate:      opts.PlanDate,
				OrderIndex:    maxOrder + 1 + i,
				Name:          d.Name,
				TagID:         tagID,
				AIEstimateMin: est,
				Status:        model.StatusPending,
				// 无论来自大模型还是本地降级规则，统一记为 ai 来源，
				// 这样用户点「重新生成」时能一致地清掉上一次的产物。
				Source: model.SourceAI,
			}
			if err := tx.Create(&task).Error; err != nil {
				return err
			}
			created++
		}
		return nil
	})

	return created, err
}

// findOrCreateTag 按标签名查找标签，不存在则创建（使用默认色）。
func (s *Service) findOrCreateTag(tx *gorm.DB, name string) (*int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	var tag model.Tag
	err := tx.Where("name = ?", name).First(&tag).Error
	if err == nil {
		return &tag.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	tag = model.Tag{Name: name, Color: pickTagColor(name)}
	if err := tx.Create(&tag).Error; err != nil {
		return nil, err
	}
	return &tag.ID, nil
}

// listTagNames 返回全部标签名，供 AI 选择。
func (s *Service) listTagNames() ([]string, error) {
	var tags []model.Tag
	if err := s.db.Order("id").Find(&tags).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name)
	}
	return out, nil
}

// tagPalette 是新建标签时自动分配的配色，保证界面颜色不会过于单调或撞色。
var tagPalette = []string{
	"#3b82f6", "#ef4444", "#22c55e", "#a855f7",
	"#f59e0b", "#06b6d4", "#ec4899", "#84cc16",
}

func pickTagColor(name string) string {
	var sum int
	for _, r := range name {
		sum += int(r)
	}
	if sum < 0 {
		sum = -sum
	}
	return tagPalette[sum%len(tagPalette)]
}

// localDraft 是未配置 AI（或 AI 调用失败）时的本地降级方案：
// 把用户输入按换行、逗号等分隔符切成任务。
// 它不如大模型聪明，但保证功能永远可用，且行为完全可预测。
func localDraft(userInput string, defaultEstimate int) *ai.PlanDraft {
	if defaultEstimate <= 0 {
		defaultEstimate = 30
	}
	fields := strings.FieldsFunc(userInput, func(r rune) bool {
		switch r {
		case '\n', '\r', ',', '，', ';', '；', '、', '。':
			return true
		}
		return false
	})

	seen := map[string]bool{}
	draft := &ai.PlanDraft{}
	for _, f := range fields {
		name := strings.TrimSpace(f)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		draft.Tasks = append(draft.Tasks, ai.DraftTask{
			Name:        name,
			EstimateMin: defaultEstimate,
			Priority:    2,
		})
		if len(draft.Tasks) >= 12 {
			break
		}
	}
	draft.Advice = "当前为本地规则生成的计划，时长统一按默认值估算，建议按实际情况手动调整每条任务的预估耗时。"
	return draft
}

// TasksWithView 返回某天的任务视图（含结算耗时与标签信息）。
func (s *Service) TasksWithView(planDate string) ([]TaskDTO, error) {
	tasks, err := s.tasksForDate(planDate, true)
	if err != nil {
		return nil, err
	}

	var tags []model.Tag
	if err := s.db.Find(&tags).Error; err != nil {
		return nil, err
	}
	tagOf := make(map[int64]model.Tag, len(tags))
	for _, t := range tags {
		tagOf[t.ID] = t
	}

	dc, err := s.ensureDailyConfig(s.db, planDate)
	if err != nil {
		return nil, err
	}
	deadline, err := s.deadlineTime(planDate, dc.DeadlineMin)
	if err != nil {
		return nil, err
	}

	now := Now()
	out := make([]TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		dto := TaskDTO{
			Task:           t,
			ElapsedSeconds: ElapsedSeconds(&t, now),
			EstimateMin:    t.EffectiveEstimateMin(15),
		}
		if t.TagID != nil {
			if tag, ok := tagOf[*t.TagID]; ok {
				dto.TagName = tag.Name
				dto.TagColor = tag.Color
			}
		}
		// 执行中越过当日截止时间：不强制终止，只提示
		if t.Status == model.StatusRunning && now.After(deadline) {
			dto.OvertimeNow = true
		}
		out = append(out, dto)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OrderIndex != out[j].OrderIndex {
			return out[i].OrderIndex < out[j].OrderIndex
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
