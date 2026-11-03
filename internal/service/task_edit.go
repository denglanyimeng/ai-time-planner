package service

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"ai-time-planner/internal/model"
)

// TaskPatch 描述对任务的部分修改。
// 全部字段都是「可选」语义：nil 表示不修改，保证了原需求
// 「所有任务、任意状态均可随时人工修改」可以被精确表达。
type TaskPatch struct {
	Name            *string
	TagID           *int64
	ClearTag        bool
	AIEstimateMin   *int
	UserEstimateMin *int
	// ClearUserEstimate 显式把用户预估置空（用于回退到 AI 预估）
	ClearUserEstimate bool
	Status            *string
	OrderIndex        *int
}

// 合法的任务状态集合。
var validStatuses = map[string]bool{
	model.StatusPending: true,
	model.StatusRunning: true,
	model.StatusPaused:  true,
	model.StatusDone:    true,
	model.StatusUndone:  true,
}

// UpdateTask 修改任务任意字段。
//
// 计时相关字段（RunStartedAt / ActualSeconds）故意不开放直接编辑：
// 实际耗时请走「手动修正耗时」接口，避免把计时状态改成一个自相矛盾的值
// （比如状态是「进行中」但没有开始时刻，计时就会永远算不出来）。
func (s *Service) UpdateTask(id int64, p TaskPatch) (*model.Task, error) {
	now := Now()
	var out model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var t model.Task
		if err := tx.First(&t, id).Error; err != nil {
			return wrapNotFound(err)
		}

		if p.Name != nil {
			name := strings.TrimSpace(*p.Name)
			if name == "" {
				return fmt.Errorf("%w: 任务名称不能为空", ErrInvalid)
			}
			if len([]rune(name)) > 120 {
				name = string([]rune(name)[:120])
			}
			t.Name = name
		}

		if p.ClearTag {
			t.TagID = nil
		} else if p.TagID != nil {
			var tag model.Tag
			if err := tx.First(&tag, *p.TagID).Error; err != nil {
				return fmt.Errorf("%w: 标签 %d 不存在", ErrInvalid, *p.TagID)
			}
			tagID := *p.TagID
			t.TagID = &tagID
		}

		if p.AIEstimateMin != nil {
			v := *p.AIEstimateMin
			if v < 0 {
				return fmt.Errorf("%w: 预估耗时不能为负数", ErrInvalid)
			}
			if v > 1440 {
				v = 1440
			}
			t.AIEstimateMin = v
		}

		if p.ClearUserEstimate {
			t.UserEstimateMin = nil
		} else if p.UserEstimateMin != nil {
			v := *p.UserEstimateMin
			if v < 0 {
				return fmt.Errorf("%w: 预估耗时不能为负数", ErrInvalid)
			}
			if v > 1440 {
				v = 1440
			}
			t.UserEstimateMin = &v
		}

		if p.OrderIndex != nil {
			if *p.OrderIndex < 0 {
				return fmt.Errorf("%w: 顺序不能为负数", ErrInvalid)
			}
			t.OrderIndex = *p.OrderIndex
		}

		if p.Status != nil {
			st := strings.TrimSpace(*p.Status)
			if !validStatuses[st] {
				return fmt.Errorf("%w: 未知任务状态 %q", ErrInvalid, st)
			}
			if err := s.applyStatus(tx, &t, st, now); err != nil {
				return err
			}
		}

		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		out = t
		return nil
	})

	return &out, err
}

// applyStatus 在直接改状态时，同步维护计时字段与完成时间，
// 保证「状态」与「计时」两个维度始终自洽。
func (s *Service) applyStatus(tx *gorm.DB, t *model.Task, status string, now time.Time) error {
	if t.Status == status {
		return nil
	}

	switch status {
	case model.StatusRunning:
		// 切到进行中：先结算掉可能存在的旧计时，再从此刻重新开始
		settle(t, now)
		if err := pauseRunning(tx, &t.ID, now); err != nil {
			return err
		}
		start := now
		t.RunStartedAt = &start
		t.LastHeartbeatAt = &start

	case model.StatusPaused:
		settle(t, now)

	case model.StatusDone:
		settle(t, now)
		if t.CompletedAt == nil {
			done := now
			t.CompletedAt = &done
		}
		t.IsOvertime = false

	case model.StatusUndone:
		settle(t, now)

	case model.StatusPending:
		settle(t, now)
		t.CompletedAt = nil
	}

	t.Status = status
	return nil
}

// AdjustActualSeconds 手动修正实际耗时。
//
// 原需求要求「支持用户手动修改实际耗时」，因此这里允许直接覆写累计秒数。
// 若任务此刻正在计时，会先结算再覆写，避免出现「修正后计时又叠加」的困惑。
func (s *Service) AdjustActualSeconds(id int64, seconds int) (*model.Task, error) {
	if seconds < 0 {
		return nil, fmt.Errorf("%w: 实际耗时不能为负数", ErrInvalid)
	}
	// 上限 24 小时，防止误输入天文数字把统计彻底带偏
	if seconds > 24*3600 {
		seconds = 24 * 3600
	}

	now := Now()
	var out model.Task
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var t model.Task
		if err := tx.First(&t, id).Error; err != nil {
			return wrapNotFound(err)
		}
		wasRunning := t.Status == model.StatusRunning
		settle(&t, now)
		t.ActualSeconds = seconds
		// 原本在计时的任务，修正后继续计时
		if wasRunning {
			start := now
			t.RunStartedAt = &start
			t.LastHeartbeatAt = &start
		}
		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		out = t
		return nil
	})
	return &out, err
}

// CreateTask 手动新增任务。
func (s *Service) CreateTask(planDate, name string, tagID *int64, estimateMin int) (*model.Task, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: 任务名称不能为空", ErrInvalid)
	}
	if _, err := parseDate(planDate); err != nil {
		return nil, err
	}
	if estimateMin <= 0 {
		estimateMin = 30
	}
	if estimateMin > 1440 {
		estimateMin = 1440
	}

	var out model.Task
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if tagID != nil {
			var tag model.Tag
			if err := tx.First(&tag, *tagID).Error; err != nil {
				return fmt.Errorf("%w: 标签 %d 不存在", ErrInvalid, *tagID)
			}
		}

		var maxOrder int
		if err := tx.Model(&model.Task{}).Where("plan_date = ?", planDate).
			Select("COALESCE(MAX(order_index), -1)").Scan(&maxOrder).Error; err != nil {
			return err
		}

		task := model.Task{
			PlanDate:      planDate,
			OrderIndex:    maxOrder + 1,
			Name:          name,
			TagID:         tagID,
			AIEstimateMin: estimateMin,
			Status:        model.StatusPending,
			Source:        model.SourceManual,
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		out = task
		return nil
	})
	return &out, err
}

// DeleteTask 删除单条任务（日常删除，只影响当日本条）。
func (s *Service) DeleteTask(id int64) error {
	res := s.db.Delete(&model.Task{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
