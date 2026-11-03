package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"ai-time-planner/internal/model"
)

// HeartbeatGrace 是心跳宽限期：超过这个时长没有收到前端心跳，
// 就认为页面已被关闭，把正在计时的任务结算并暂停。
//
// 为什么是 180 秒而不是更短：
// 需求要求「网页后台运行 → 计时继续」，但浏览器会对隐藏标签页的定时器限流 ——
// Chrome 在页面隐藏约 5 分钟后会把定时器压到最多每分钟一次，
// 于是 30 秒的心跳实际变成约 60 秒一次。原先 90 秒的宽限期只留了 1.5 倍余量，
// 用户只是把窗口切到后台去干点别的，任务就会被误判成「页面已关闭」而暂停。
//
// 放宽宽限期几乎不损失计时精度：结算取的是「最后一次心跳时刻」而非「当前时刻」，
// 所以它只会让状态晚一点变成「已暂停」，不会把停机时间算成工作时长。
const HeartbeatGrace = 180 * time.Second

// StartTask 开始任务计时。
//
// 单用户场景下同时只允许一个任务计时，因此开始新任务前会先自动暂停
// 其它正在计时的任务。这同时解决了原需求未覆盖的一个真实问题：
// 用户开两个浏览器标签页操作时，时长会被重复累加。
func (s *Service) StartTask(id int64) (*model.Task, error) {
	now := Now()
	var out model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := pauseRunning(tx, nil, now); err != nil {
			return err
		}

		var t model.Task
		if err := tx.First(&t, id).Error; err != nil {
			return wrapNotFound(err)
		}
		if t.Status == model.StatusDone {
			return fmt.Errorf("%w: 已完成的任务不能重新开始计时", ErrInvalid)
		}

		t.Status = model.StatusRunning
		t.RunStartedAt = &now
		t.LastHeartbeatAt = &now
		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		out = t
		return nil
	})

	return &out, err
}

// PauseTask 暂停任务：冻结计时并保存已耗时。
func (s *Service) PauseTask(id int64) (*model.Task, error) {
	now := Now()
	var out model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var t model.Task
		if err := tx.First(&t, id).Error; err != nil {
			return wrapNotFound(err)
		}
		if t.Status == model.StatusDone {
			return fmt.Errorf("%w: 已完成的任务不能暂停", ErrInvalid)
		}
		settle(&t, now)
		// 未开始过的任务被暂停，语义上等价于「已暂停」
		t.Status = model.StatusPaused
		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		out = t
		return nil
	})

	return &out, err
}

// ResumeTask 恢复任务：从当前时刻继续累计计时。
func (s *Service) ResumeTask(id int64) (*model.Task, error) {
	return s.StartTask(id)
}

// CompleteTask 完成任务：结算计时并标记完成。
func (s *Service) CompleteTask(id int64) (*model.Task, error) {
	now := Now()
	var out model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var t model.Task
		if err := tx.First(&t, id).Error; err != nil {
			return wrapNotFound(err)
		}
		settle(&t, now)
		t.Status = model.StatusDone
		t.CompletedAt = &now
		t.IsOvertime = false // 用户已明确表态，超时未确认标记清除
		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		out = t
		return nil
	})

	return &out, err
}

// MarkUndone 把任务标记为未完成（用户手动确认）。
func (s *Service) MarkUndone(id int64) (*model.Task, error) {
	now := Now()
	var out model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var t model.Task
		if err := tx.First(&t, id).Error; err != nil {
			return wrapNotFound(err)
		}
		settle(&t, now)
		t.Status = model.StatusUndone
		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		out = t
		return nil
	})

	return &out, err
}

// Heartbeat 记录前端心跳。
//
// 「网页彻底关闭即停止计时」这件事在浏览器里没有可靠的事件可以依赖：
// beforeunload/unload 不保证触发（页面崩溃、进程被强杀、移动端切后台都不会触发）。
// 因此改为正向心跳 + 超时结算，由 SettleStale 兜底。
func (s *Service) Heartbeat(id int64) error {
	now := Now()
	res := s.db.Model(&model.Task{}).
		Where("id = ? AND status = ?", id, model.StatusRunning).
		Update("last_heartbeat_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 任务可能已被结算，不算错误
		return nil
	}
	return nil
}

// pauseRunning 暂停除 exceptID 之外所有正在计时的任务。
func pauseRunning(tx *gorm.DB, exceptID *int64, now time.Time) error {
	q := tx.Where("status = ?", model.StatusRunning)
	if exceptID != nil {
		q = q.Where("id <> ?", *exceptID)
	}
	var running []model.Task
	if err := q.Find(&running).Error; err != nil {
		return err
	}
	for i := range running {
		settle(&running[i], now)
		running[i].Status = model.StatusPaused
		if err := tx.Save(&running[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// SettleStale 把心跳超时的「进行中」任务结算并暂停，返回处理条数。
//
// 结算时刻取最后一次心跳时间，而不是「当前时刻」：
// 用户在 15:00 关掉页面，服务在 15:02 才发现，这段空档不该计入学习时长。
func (s *Service) SettleStale(grace time.Duration) (int, error) {
	now := Now()
	cutoff := now.Add(-grace)

	var running []model.Task
	if err := s.db.Where("status = ?", model.StatusRunning).Find(&running).Error; err != nil {
		return 0, err
	}

	settled := 0
	for i := range running {
		t := &running[i]
		last := t.RunStartedAt
		if t.LastHeartbeatAt != nil {
			last = t.LastHeartbeatAt
		}
		if last == nil {
			continue
		}
		if last.After(cutoff) {
			continue // 心跳仍然新鲜，用户可能只是没在操作
		}
		settle(t, *last)
		t.Status = model.StatusPaused
		if err := s.db.Save(t).Error; err != nil {
			return settled, err
		}
		settled++
	}
	return settled, nil
}

// SettleAllRunning 结算所有正在计时的任务，用于服务启动时收尾。
//
// 服务重启意味着这段时间前端必然没有在心跳，因此把计时回退到
// 各自最后一次心跳时刻（没有心跳记录则回退到本轮开始时刻），避免虚增时长。
func (s *Service) SettleAllRunning() (int, error) {
	var running []model.Task
	if err := s.db.Where("status = ?", model.StatusRunning).Find(&running).Error; err != nil {
		return 0, err
	}
	settled := 0
	for i := range running {
		t := &running[i]
		at := t.RunStartedAt
		if t.LastHeartbeatAt != nil {
			at = t.LastHeartbeatAt
		}
		if at == nil {
			fallback := Now()
			at = &fallback
		}
		settle(t, *at)
		t.Status = model.StatusPaused
		if err := s.db.Save(t).Error; err != nil {
			return settled, err
		}
		settled++
	}
	return settled, nil
}

// CloseDay 结算某一天的收尾工作（对应原需求「超时未确认 → 夜间自动归为未完成」）。
//
// 关键点：这件事必须由服务端定时任务完成，不能依赖浏览器。
// 用户在傍晚关掉网页之后再也没打开，前端永远不会有机会去归类未完成任务。
//
// 返回值：被归为未完成的任务数。
func (s *Service) CloseDay(planDate string) (int, error) {
	now := Now()

	var dc model.DailyConfig
	if err := s.db.Where("plan_date = ?", planDate).First(&dc).Error; err != nil {
		if !isNotFound(err) {
			return 0, err
		}
		dc = model.DailyConfig{PlanDate: planDate, DeadlineMin: 23 * 60}
	}

	deadline, err := s.deadlineTime(planDate, dc.DeadlineMin)
	if err != nil {
		return 0, err
	}

	// 只处理仍未结束的任务：待开始 / 进行中 / 已暂停
	var open []model.Task
	if err := s.db.Where("plan_date = ? AND status IN ?", planDate,
		[]string{model.StatusPending, model.StatusRunning, model.StatusPaused}).
		Find(&open).Error; err != nil {
		return 0, err
	}

	closed := 0
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for i := range open {
			t := &open[i]
			// 结算到「最后一次心跳」或「本轮开始」或「当轮截止」，取最早的有效时刻
			at := now
			if t.LastHeartbeatAt != nil && t.LastHeartbeatAt.Before(at) {
				at = *t.LastHeartbeatAt
			}
			settle(t, at)
			t.Status = model.StatusUndone
			// 超过当日截止时间才标记超时未确认
			t.IsOvertime = t.PlannedEnd != nil && t.PlannedEnd.After(deadline)
			if err := tx.Save(t).Error; err != nil {
				return err
			}
			closed++
		}
		return nil
	})
	if err != nil {
		return closed, err
	}

	// 收尾后归集统计，保证聚合数据不丢
	if err := s.RollupDate(planDate); err != nil {
		return closed, err
	}
	return closed, nil
}
