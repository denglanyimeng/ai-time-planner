package service

import (
	"ai-time-planner/internal/model"
	"ai-time-planner/internal/planner"
)

// AvailabilityInfo 描述当日负载情况。
type AvailabilityInfo struct {
	AvailableMin  float64 `json:"availableMin"`
	DemandMin     float64 `json:"demandMin"`
	LoadRatio     float64 `json:"loadRatio"`
	Overloaded    bool    `json:"overloaded"`
	OverflowCount int     `json:"overflowCount"`
}

// Dashboard 是首页所需的全部数据。
//
// 按原需求 2.2.11「AI 提示仅仪表盘统一展示，不做单条任务细碎提示」，
// 这里把所有提示信息集中汇聚，保证界面干净。
type Dashboard struct {
	PlanDate string `json:"planDate"`
	Now      string `json:"now"`
	NowMin   int    `json:"nowMin"` // 当前北京时间距零点分钟数
	Deadline string `json:"deadline"`
	BreakMin int    `json:"breakMin"`

	Tasks   []TaskDTO `json:"tasks"`
	Running *TaskDTO  `json:"running"`

	Total   int `json:"total"`
	Done    int `json:"done"`
	Undone  int `json:"undone"`
	Pending int `json:"pending"`

	ActualMinutes   float64 `json:"actualMinutes"`
	EstimateMinutes int     `json:"estimateMinutes"`

	Advice     string `json:"advice"`
	Constraint string `json:"constraint"`
	Summary    string `json:"summary"`

	Availability AvailabilityInfo `json:"availability"`

	Trend    []TrendPoint `json:"trend"`
	TagStats []TagStat    `json:"tagStats"`

	PoolCount int  `json:"poolCount"`
	AIEnabled bool `json:"aiEnabled"`

	// OvertimeRunning 表示当前有任务已越过当日截止时间仍在进行
	OvertimeRunning bool `json:"overtimeRunning"`
	// OvertimeNotice 是给用户的人性化提示文案
	OvertimeNotice string `json:"overtimeNotice"`
}

// SchedulePreview 只计算排程结果而不写库，用于仪表盘展示负载情况。
func (s *Service) SchedulePreview(planDate string) (*ScheduleOutcome, error) {
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

	ratio := res.LoadRatio()
	return &ScheduleOutcome{
		Placed:       len(res.Placed),
		Overflow:     res.Overflow,
		AvailableMin: res.AvailableMin,
		DemandMin:    res.DemandMin,
		LoadRatio:    ratio,
		Overloaded:   res.Overloaded,
	}, nil
}

// Dashboard 汇总首页所需数据。
func (s *Service) Dashboard(planDate string) (*Dashboard, error) {
	if _, err := parseDate(planDate); err != nil {
		return nil, err
	}

	dc, err := s.ensureDailyConfig(s.db, planDate)
	if err != nil {
		return nil, err
	}
	tasks, err := s.TasksWithView(planDate)
	if err != nil {
		return nil, err
	}

	now := Now()
	bjNow := now.In(planner.Beijing())
	deadline, err := s.deadlineTime(planDate, dc.DeadlineMin)
	if err != nil {
		return nil, err
	}

	dash := &Dashboard{
		PlanDate:   planDate,
		Now:        bjNow.Format("2006-01-02 15:04:05"),
		NowMin:     bjNow.Hour()*60 + bjNow.Minute(),
		Deadline:   deadline.In(planner.Beijing()).Format("15:04"),
		BreakMin:   dc.BreakMin,
		Tasks:      tasks,
		Advice:     dc.AIPlanAdvice,
		Constraint: dc.ConstraintText,
		Summary:    dc.AISummary,
		AIEnabled:  s.AIClient().Configured(),
	}

	for i := range tasks {
		t := &tasks[i]
		dash.EstimateMinutes += t.EstimateMin
		dash.ActualMinutes += float64(t.ElapsedSeconds) / 60

		switch t.Status {
		case model.StatusDone:
			dash.Done++
		case model.StatusUndone:
			dash.Undone++
		default:
			dash.Pending++
		}
		if t.Status == model.StatusRunning {
			running := *t
			dash.Running = &running
			if t.OvertimeNow {
				dash.OvertimeRunning = true
			}
		}
	}
	dash.Total = len(tasks)
	dash.ActualMinutes = round1(dash.ActualMinutes)

	// 超时任务：不强制终止，只做人性化提示
	if dash.OvertimeRunning {
		dash.OvertimeNotice = "当前任务已超过当日结束时间，注意休息。可以随时结束它，或直接标记完成 / 未完成。"
	}

	// 负载情况（只算不写）
	preview, err := s.SchedulePreview(planDate)
	if err != nil {
		return nil, err
	}
	dash.Availability = AvailabilityInfo{
		AvailableMin:  round1(preview.AvailableMin),
		DemandMin:     round1(preview.DemandMin),
		LoadRatio:     round2(preview.LoadRatio),
		Overloaded:    preview.Overloaded,
		OverflowCount: len(preview.Overflow),
	}

	trend, err := s.Trend(21)
	if err != nil {
		return nil, err
	}
	dash.Trend = trend

	tagStats, err := s.TagStats(21)
	if err != nil {
		return nil, err
	}
	dash.TagStats = tagStats

	if planDate > "" {
		pool, err := s.Pool(planDate)
		if err != nil {
			return nil, err
		}
		dash.PoolCount = len(pool)
	}

	return dash, nil
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
