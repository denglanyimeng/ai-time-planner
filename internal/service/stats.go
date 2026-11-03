package service

import (
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"

	"ai-time-planner/internal/model"
)

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// deadlineTime 把「距零点分钟数」换算成指定业务日期的绝对时刻（北京时间）。
func (s *Service) deadlineTime(planDate string, deadlineMin int) (time.Time, error) {
	day, err := parseDate(planDate)
	if err != nil {
		return time.Time{}, err
	}
	if deadlineMin <= 0 {
		deadlineMin = 23 * 60
	}
	if deadlineMin >= 24*60 {
		deadlineMin = 24*60 - 1
	}
	return day.Add(time.Duration(deadlineMin) * time.Minute), nil
}

// RollupDate 把某一天的任务明细归集到 daily_tag_stats。
//
// 这是「21 天滚动清理」与「长期目标累计时长」能够共存的关键一步：
// 明细会被删除，但聚合结果永久保留，所以长期统计口径不会随时间丢失。
func (s *Service) RollupDate(planDate string) error {
	now := Now()

	var tasks []model.Task
	if err := s.db.Where("plan_date = ?", planDate).Find(&tasks).Error; err != nil {
		return err
	}

	type agg struct {
		totalSec int
		totalEst int
		count    int
		done     int
	}
	byTag := map[int64]*agg{}

	for i := range tasks {
		t := &tasks[i]
		var tagID int64 // 0 表示未打标签（真实标签 ID 从 1 开始，不会冲突）
		if t.TagID != nil {
			tagID = *t.TagID
		}
		a := byTag[tagID]
		if a == nil {
			a = &agg{}
			byTag[tagID] = a
		}
		a.totalSec += ElapsedSeconds(t, now)
		a.totalEst += t.EffectiveEstimateMin(0)
		a.count++
		if t.Status == model.StatusDone {
			a.done++
		}
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_date = ?", planDate).Delete(&model.DailyTagStat{}).Error; err != nil {
			return err
		}
		if len(byTag) == 0 {
			return nil
		}
		rows := make([]model.DailyTagStat, 0, len(byTag))
		for tagID, a := range byTag {
			rows = append(rows, model.DailyTagStat{
				PlanDate:       planDate,
				TagID:          tagID,
				TotalSeconds:   a.totalSec,
				TotalEstMin:    a.totalEst,
				TaskCount:      a.count,
				CompletedCount: a.done,
				UpdatedAt:      now,
			})
		}
		return tx.Create(&rows).Error
	})
}

// CleanupResult 描述一次清理的结果。
type CleanupResult struct {
	CutoffDate     string `json:"cutoffDate"`
	DeletedTasks   int64  `json:"deletedTasks"`
	DeletedConfigs int64  `json:"deletedConfigs"`
	RolledUpDays   int    `json:"rolledUpDays"`
}

// CleanupOldData 清理超出保留期的原始明细。
//
// 严格执行「先归集、后删除」：如果先删明细，聚合统计就永远算不出来了。
func (s *Service) CleanupOldData(retentionDays int) (CleanupResult, error) {
	var res CleanupResult
	if retentionDays <= 0 {
		retentionDays = 21
	}

	base, err := parseDate(Today())
	if err != nil {
		return res, err
	}
	// 保留最近 retentionDays 天（含今天），更早的日期进入清理范围
	cutoff := FormatDate(base.AddDate(0, 0, -(retentionDays - 1)))
	res.CutoffDate = cutoff

	var dates []string
	if err := s.db.Model(&model.Task{}).
		Where("plan_date < ?", cutoff).
		Distinct().Pluck("plan_date", &dates).Error; err != nil {
		return res, err
	}

	for _, d := range dates {
		if err := s.RollupDate(d); err != nil {
			return res, err
		}
		res.RolledUpDays++
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		out := tx.Where("plan_date < ?", cutoff).Delete(&model.Task{})
		if out.Error != nil {
			return out.Error
		}
		res.DeletedTasks = out.RowsAffected

		out = tx.Where("plan_date < ?", cutoff).Delete(&model.DailyConfig{})
		if out.Error != nil {
			return out.Error
		}
		res.DeletedConfigs = out.RowsAffected
		return nil
	})
	return res, err
}

// RollupRange 归集 [from, to] 区间（闭区间）的每日统计。
func (s *Service) RollupRange(from, to string) error {
	start, err := parseDate(from)
	if err != nil {
		return err
	}
	end, err := parseDate(to)
	if err != nil {
		return err
	}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if err := s.RollupDate(FormatDate(d)); err != nil {
			return err
		}
	}
	return nil
}

// TrendPoint 是 21 天趋势中的一个数据点。
type TrendPoint struct {
	PlanDate       string `json:"planDate"`
	TotalSeconds   int    `json:"totalSeconds"`
	TotalEstMin    int    `json:"totalEstMin"`
	TaskCount      int    `json:"taskCount"`
	CompletedCount int    `json:"completedCount"`
}

// Trend 返回最近 n 天的效率趋势（基于永久聚合数据，不受明细清理影响）。
func (s *Service) Trend(days int) ([]TrendPoint, error) {
	if days <= 0 {
		days = 21
	}
	base, err := parseDate(Today())
	if err != nil {
		return nil, err
	}
	from := FormatDate(base.AddDate(0, 0, -(days - 1)))
	to := FormatDate(base)

	// 保证区间内数据已归集，这样「今天」也是最新的
	if err := s.RollupRange(from, to); err != nil {
		return nil, err
	}

	var rows []model.DailyTagStat
	if err := s.db.Where("plan_date >= ? AND plan_date <= ?", from, to).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	byDate := map[string]*TrendPoint{}
	for _, r := range rows {
		p := byDate[r.PlanDate]
		if p == nil {
			p = &TrendPoint{PlanDate: r.PlanDate}
			byDate[r.PlanDate] = p
		}
		p.TotalSeconds += r.TotalSeconds
		p.TotalEstMin += r.TotalEstMin
		p.TaskCount += r.TaskCount
		p.CompletedCount += r.CompletedCount
	}

	out := make([]TrendPoint, 0, days)
	for d := base.AddDate(0, 0, -(days - 1)); !d.After(base); d = d.AddDate(0, 0, 1) {
		key := FormatDate(d)
		if p, ok := byDate[key]; ok {
			out = append(out, *p)
		} else {
			out = append(out, TrendPoint{PlanDate: key})
		}
	}
	return out, nil
}

// GoalProgress 是长期目标的进度参考。
type GoalProgress struct {
	GoalID       int64  `json:"goalId"`
	Content      string `json:"content"`
	TagID        *int64 `json:"tagId"`
	TagName      string `json:"tagName"`
	TagColor     string `json:"tagColor"`
	TotalSeconds int    `json:"totalSeconds"`
	TaskCount    int    `json:"taskCount"`
	ActiveDays   int    `json:"activeDays"`
}

// LongTermProgress 统计每个长期目标所绑定标签的累计投入时长。
//
// 数据来源是永久保留的 daily_tag_stats，因此可以无限期累计，
// 不受 21 天明细清理的影响。
func (s *Service) LongTermProgress() ([]GoalProgress, error) {
	var goals []model.LongTermGoal
	if err := s.db.Order("id").Find(&goals).Error; err != nil {
		return nil, err
	}
	if len(goals) == 0 {
		return []GoalProgress{}, nil
	}

	// 先确保最近 21 天已归集（今天的实时数据也要算进去）
	base, err := parseDate(Today())
	if err != nil {
		return nil, err
	}
	if err := s.RollupRange(FormatDate(base.AddDate(0, 0, -20)), FormatDate(base)); err != nil {
		return nil, err
	}

	out := make([]GoalProgress, 0, len(goals))
	for _, g := range goals {
		item := GoalProgress{GoalID: g.ID, Content: g.Content, TagID: g.TagID}
		if g.TagID != nil {
			var tag model.Tag
			if err := s.db.First(&tag, *g.TagID).Error; err == nil {
				item.TagName = tag.Name
				item.TagColor = tag.Color
			}
			var rows []model.DailyTagStat
			if err := s.db.Where("tag_id = ?", *g.TagID).Find(&rows).Error; err != nil {
				return nil, err
			}
			for _, r := range rows {
				item.TotalSeconds += r.TotalSeconds
				item.TaskCount += r.TaskCount
				if r.TotalSeconds > 0 || r.TaskCount > 0 {
					item.ActiveDays++
				}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// TagStat 是标签维度的统计。
type TagStat struct {
	TagID        int64  `json:"tagId"`
	TagName      string `json:"tagName"`
	Color        string `json:"color"`
	TotalSeconds int    `json:"totalSeconds"`
	TaskCount    int    `json:"taskCount"`
}

// TagStats 返回最近 n 天各标签的投入时长排行。
func (s *Service) TagStats(days int) ([]TagStat, error) {
	if days <= 0 {
		days = 21
	}
	base, err := parseDate(Today())
	if err != nil {
		return nil, err
	}
	from := FormatDate(base.AddDate(0, 0, -(days - 1)))
	to := FormatDate(base)
	if err := s.RollupRange(from, to); err != nil {
		return nil, err
	}

	var rows []model.DailyTagStat
	if err := s.db.Where("plan_date >= ? AND plan_date <= ?", from, to).Find(&rows).Error; err != nil {
		return nil, err
	}

	agg := map[int64]*TagStat{}
	for _, r := range rows {
		s := agg[r.TagID]
		if s == nil {
			s = &TagStat{TagID: r.TagID}
			agg[r.TagID] = s
		}
		s.TotalSeconds += r.TotalSeconds
		s.TaskCount += r.TaskCount
	}

	var tags []model.Tag
	if err := s.db.Find(&tags).Error; err != nil {
		return nil, err
	}
	nameOf := map[int64]model.Tag{}
	for _, t := range tags {
		nameOf[t.ID] = t
	}

	out := make([]TagStat, 0, len(agg))
	for id, s := range agg {
		if t, ok := nameOf[id]; ok {
			s.TagName = t.Name
			s.Color = t.Color
		} else {
			s.TagName = "未分类"
			s.Color = "#94a3b8"
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalSeconds != out[j].TotalSeconds {
			return out[i].TotalSeconds > out[j].TotalSeconds
		}
		return out[i].TagID < out[j].TagID
	})
	return out, nil
}
