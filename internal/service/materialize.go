package service

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"ai-time-planner/internal/model"
)

// 周期任务冲突的处理方式。
const (
	ConflictOverwrite = "overwrite" // 覆盖：删除当日同名已有任务后生成实例
	ConflictSkip      = "skip"      // 跳过：本次不为该周期任务生成实例
)

// Conflict 描述一条周期任务与当日已有任务的冲突。
//
// 原需求只写了「若与当日已有任务冲突 → 弹窗询问用户抉择（覆盖 / 跳过）」，
// 但没有定义什么叫「冲突」。这里把它固定为可实现的明确规则：
// 当日已存在同名任务（忽略首尾空白与大小写）即视为冲突。
//
// 之所以不用「时间重叠」作为判据：物化发生在排程之前，此刻新任务还没有时间戳，
// 时间重叠根本无从判断。按名字判冲突既符合直觉，也真正可落地。
type Conflict struct {
	RecurringID    int64  `json:"recurringId"`
	Name           string `json:"name"`
	ExistingTaskID int64  `json:"existingTaskId"`
	DurationMin    int    `json:"durationMin"`
}

// MaterializeResult 描述一次周期任务物化的结果。
type MaterializeResult struct {
	PlanDate   string     `json:"planDate"`
	Created    int        `json:"created"`
	CreatedIDs []int64    `json:"createdIds"`
	Conflicts  []Conflict `json:"conflicts"`
}

// MaterializeRecurring 把当日应执行的周期任务物化为真实任务。
//
// 幂等性是这里的核心要求：原需求说「每日打开网页自动加载当日周期任务」，
// 而用户一天可能刷新很多次页面。如果不做幂等，每刷新一次就会多出一份重复任务。
// 双保险：
//  1. 先查当日是否已存在该周期任务的实例，存在则跳过；
//  2. 数据库层面用唯一索引 (recurring_id, plan_date) 兜底，
//     即使并发请求同时到达，也只会成功插入一条。
func (s *Service) MaterializeRecurring(planDate string) (*MaterializeResult, error) {
	out := &MaterializeResult{PlanDate: planDate, Conflicts: []Conflict{}, CreatedIDs: []int64{}}

	if _, err := parseDate(planDate); err != nil {
		return nil, err
	}

	var templates []model.RecurringTask
	if err := s.db.Where("enabled = ?", true).Order("id").Find(&templates).Error; err != nil {
		return nil, err
	}
	if len(templates) == 0 {
		return out, nil
	}

	existing, err := s.tasksForDate(planDate, true)
	if err != nil {
		return nil, err
	}

	byName := make(map[string]model.Task, len(existing))
	alreadyDone := make(map[int64]bool)
	for _, t := range existing {
		byName[normalizeName(t.Name)] = t
		if t.RecurringID != nil {
			alreadyDone[*t.RecurringID] = true
		}
	}

	for _, tpl := range templates {
		ok, err := RecurringMatches(tpl, planDate)
		if err != nil {
			// 单条周期配置有问题不应阻塞其它周期任务
			continue
		}
		if !ok || alreadyDone[tpl.ID] {
			continue
		}
		if dup, hit := byName[normalizeName(tpl.Name)]; hit {
			out.Conflicts = append(out.Conflicts, Conflict{
				RecurringID:    tpl.ID,
				Name:           tpl.Name,
				ExistingTaskID: dup.ID,
				DurationMin:    effectiveRecurringDuration(tpl),
			})
			continue
		}

		id, err := s.createRecurringInstance(tpl, planDate)
		if err != nil {
			// 唯一索引冲突说明并发下已被创建，视为成功而非错误
			if isDuplicateKey(err) {
				continue
			}
			return out, err
		}
		out.Created++
		out.CreatedIDs = append(out.CreatedIDs, id)
	}
	return out, nil
}

// ResolveRecurringConflict 处理冲突：覆盖或跳过。
func (s *Service) ResolveRecurringConflict(planDate string, recurringID int64, action string) (*MaterializeResult, error) {
	var tpl model.RecurringTask
	if err := s.db.First(&tpl, recurringID).Error; err != nil {
		return nil, wrapNotFound(err)
	}

	switch action {
	case ConflictSkip:
		// 跳过不需要落库：下次物化时若同名任务已不存在仍会重新询问，
		// 这是刻意的 —— 用户跳过的是一次抉择，不是永久忽略该周期任务。
		return &MaterializeResult{PlanDate: planDate, Conflicts: []Conflict{}, CreatedIDs: []int64{}}, nil

	case ConflictOverwrite:
		err := s.db.Transaction(func(tx *gorm.DB) error {
			// 仅删除当日同名且尚未开始的任务，避免误删正在计时或已完成的任务
			if err := tx.Where("plan_date = ? AND name = ? AND status IN ?",
				planDate, tpl.Name,
				[]string{model.StatusPending, model.StatusUndone}).
				Delete(&model.Task{}).Error; err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		id, err := s.createRecurringInstance(tpl, planDate)
		if err != nil {
			if isDuplicateKey(err) {
				return &MaterializeResult{PlanDate: planDate, Conflicts: []Conflict{}, CreatedIDs: []int64{}}, nil
			}
			return nil, err
		}
		return &MaterializeResult{
			PlanDate:   planDate,
			Created:    1,
			CreatedIDs: []int64{id},
			Conflicts:  []Conflict{},
		}, nil

	default:
		return nil, fmt.Errorf("%w: 冲突处理方式只能是 overwrite 或 skip", ErrInvalid)
	}
}

// createRecurringInstance 生成一条周期任务实例。
func (s *Service) createRecurringInstance(tpl model.RecurringTask, planDate string) (int64, error) {
	var id int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var maxOrder int
		if err := tx.Model(&model.Task{}).Where("plan_date = ?", planDate).
			Select("COALESCE(MAX(order_index), -1)").Scan(&maxOrder).Error; err != nil {
			return err
		}
		rid := tpl.ID
		task := model.Task{
			PlanDate:      planDate,
			OrderIndex:    maxOrder + 1,
			Name:          tpl.Name,
			TagID:         tpl.TagID,
			AIEstimateMin: effectiveRecurringDuration(tpl),
			Status:        model.StatusPending,
			Source:        model.SourceRecurring,
			RecurringID:   &rid,
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		id = task.ID
		return nil
	})
	return id, err
}

func effectiveRecurringDuration(tpl model.RecurringTask) int {
	if tpl.DurationMin <= 0 {
		return 30
	}
	return tpl.DurationMin
}

func normalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// isDuplicateKey 判断是否为唯一索引冲突。
//
// 同时兼容两种驱动的报错文案：
//   - MySQL ：错误码 1062 / "Duplicate entry"
//   - SQLite：	"UNIQUE constraint failed"（集成测试使用内存 SQLite）
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "1062") ||
		strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}

// PoolItem 是未完成任务池中的条目。
type PoolItem struct {
	TaskID        int64  `json:"taskId"`
	PlanDate      string `json:"planDate"`
	Name          string `json:"name"`
	TagName       string `json:"tagName"`
	TagColor      string `json:"tagColor"`
	EstimateMin   int    `json:"estimateMin"`
	ActualSeconds int    `json:"actualSeconds"`
	IsOvertime    bool   `json:"isOvertime"`
	Adopted       bool   `json:"adopted"` // 是否已被承接为后续日期的任务
}

// Pool 返回历史未完成任务（在保留期内）。
//
// 关于「次日承接」的取舍：
// 原需求自相矛盾 —— 2.1 说「未完成任务自动归集、次日承接」，
// 3.6 又说「可一键加入今日计划」。
// 这里选择后者：只入池、不自动插入今日计划。
// 理由很实际：自动插入会在用户毫无察觉的情况下把第二天直接压到超载，
// 与需求本身要求的「超载预警」逻辑正好相悖。由用户一键确认更可控。
func (s *Service) Pool(beforeDate string) ([]PoolItem, error) {
	cutoff, err := s.retentionCutoff()
	if err != nil {
		return nil, err
	}

	var tasks []model.Task
	if err := s.db.Where("status = ? AND plan_date < ? AND plan_date >= ?",
		model.StatusUndone, beforeDate, cutoff).
		Order("plan_date DESC, order_index").Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return []PoolItem{}, nil
	}

	var tags []model.Tag
	if err := s.db.Find(&tags).Error; err != nil {
		return nil, err
	}
	tagOf := make(map[int64]model.Tag, len(tags))
	for _, t := range tags {
		tagOf[t.ID] = t
	}

	// 已被承接的任务（存在 carry_from_id 指向它的任务）
	ids := make([]int64, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	var adoptedFrom []int64
	if err := s.db.Model(&model.Task{}).
		Where("carry_from_id IN ?", ids).
		Distinct().Pluck("carry_from_id", &adoptedFrom).Error; err != nil {
		return nil, err
	}
	adopted := make(map[int64]bool, len(adoptedFrom))
	for _, id := range adoptedFrom {
		adopted[id] = true
	}

	now := Now()
	out := make([]PoolItem, 0, len(tasks))
	for _, t := range tasks {
		item := PoolItem{
			TaskID:        t.ID,
			PlanDate:      t.PlanDate,
			Name:          t.Name,
			EstimateMin:   t.EffectiveEstimateMin(15),
			ActualSeconds: ElapsedSeconds(&t, now),
			IsOvertime:    t.IsOvertime,
			Adopted:       adopted[t.ID],
		}
		if t.TagID != nil {
			if tag, ok := tagOf[*t.TagID]; ok {
				item.TagName = tag.Name
				item.TagColor = tag.Color
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// AdoptTask 把历史未完成任务承接为指定日期的新任务。
//
// 采用「复制」而不是「移动」：历史记录保留在原日期不动，
// 这样 21 天趋势统计不会被改写，用户也能看到这件事被推迟了几次。
func (s *Service) AdoptTask(taskID int64, targetDate string) (*model.Task, error) {
	if _, err := parseDate(targetDate); err != nil {
		return nil, err
	}

	var out model.Task
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var src model.Task
		if err := tx.First(&src, taskID).Error; err != nil {
			return wrapNotFound(err)
		}
		if src.Status != model.StatusUndone {
			return fmt.Errorf("%w: 只有未完成的任务才能被承接", ErrInvalid)
		}

		var maxOrder int
		if err := tx.Model(&model.Task{}).Where("plan_date = ?", targetDate).
			Select("COALESCE(MAX(order_index), -1)").Scan(&maxOrder).Error; err != nil {
			return err
		}

		carried := src.ID
		task := model.Task{
			PlanDate:      targetDate,
			OrderIndex:    maxOrder + 1,
			Name:          src.Name,
			TagID:         src.TagID,
			AIEstimateMin: src.AIEstimateMin,
			Status:        model.StatusPending,
			Source:        model.SourceCarryover,
			CarryFromID:   &carried,
		}
		if src.UserEstimateMin != nil {
			v := *src.UserEstimateMin
			task.UserEstimateMin = &v
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		out = task
		return nil
	})
	return &out, err
}

// retentionCutoff 返回保留窗口的起始日期（含）。
func (s *Service) retentionCutoff() (string, error) {
	days := s.RetentionDays()
	base, err := parseDate(Today())
	if err != nil {
		return "", err
	}
	return FormatDate(base.AddDate(0, 0, -(days - 1))), nil
}
