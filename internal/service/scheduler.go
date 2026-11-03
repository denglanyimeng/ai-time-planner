package service

import (
	"context"
	"log"
	"time"

	"ai-time-planner/internal/model"
)

// RetentionDays 返回原始明细的保留天数（默认 21 天）。
func (s *Service) RetentionDays() int {
	var sc model.SystemConfig
	if err := s.db.First(&sc, 1).Error; err != nil {
		return 21
	}
	if sc.RetentionDays <= 0 {
		return 21
	}
	return sc.RetentionDays
}

// RunJanitor 启动后台维护协程，直到 ctx 被取消。
//
// 原需求里的两个规则必须由服务端定时任务兜底，不能依赖浏览器：
//
//	「超时未确认 → AI 夜间自动归为未完成事项」—— 用户傍晚关掉网页后就再没打开，
//	                                       前端永远没有机会执行归类。
//	「超 21 天数据自动清理」—— 同理，不能指望用户刚好在那天打开页面。
func (s *Service) RunJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}

	// 启动时先做一次追赶，覆盖「服务整夜没运行」的情况
	s.janitorPass()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.janitorPass()
		}
	}
}

// janitorPass 执行一轮后台维护。任何一步失败都只记录日志，不中断其它步骤。
func (s *Service) janitorPass() {
	// 1. 结算心跳超时的计时任务（实现「网页关闭即停止计时」）
	if n, err := s.SettleStale(HeartbeatGrace); err != nil {
		log.Printf("[janitor] 结算超时计时失败: %v", err)
	} else if n > 0 {
		log.Printf("[janitor] 已结算 %d 个心跳超时的计时任务", n)
	}

	// 2. 历史日期收尾归类
	if n, err := s.CatchUpMissedDays(); err != nil {
		log.Printf("[janitor] 历史日期收尾失败: %v", err)
	} else if n > 0 {
		log.Printf("[janitor] 已把 %d 个逾期未确认的任务归为未完成", n)
	}

	// 3. 数据清理（按天判断，避免每分钟都在扫库）
	s.maybeCleanup()
}

// CatchUpMissedDays 把今天之前仍未结束的任务归为未完成。
//
// 这是「夜间自动归类」的补做机制：无论服务是整夜运行，
// 还是隔了三天才重新打开，漏掉的收尾都会被补上。
func (s *Service) CatchUpMissedDays() (int, error) {
	today := Today()

	var dates []string
	if err := s.db.Model(&model.Task{}).
		Where("plan_date < ? AND status IN ?", today,
			[]string{model.StatusPending, model.StatusRunning, model.StatusPaused}).
		Distinct().Pluck("plan_date", &dates).Error; err != nil {
		return 0, err
	}

	total := 0
	for _, d := range dates {
		n, err := s.CloseDay(d)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// maybeCleanup 每天最多执行一次数据清理。
func (s *Service) maybeCleanup() {
	today := Today()

	s.mu.Lock()
	if s.lastCleanupDay == today {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	res, err := s.CleanupOldData(s.RetentionDays())
	if err != nil {
		log.Printf("[janitor] 数据清理失败: %v", err)
		return
	}

	s.mu.Lock()
	s.lastCleanupDay = today
	s.mu.Unlock()

	if res.DeletedTasks > 0 || res.RolledUpDays > 0 {
		log.Printf("[janitor] 数据清理完成：归集 %d 天，删除 %d 条过期任务（保留起始日 %s）",
			res.RolledUpDays, res.DeletedTasks, res.CutoffDate)
	}
}

// CleanupNow 立即执行一次数据清理（供管理页面手动触发）。
func (s *Service) CleanupNow() (CleanupResult, error) {
	res, err := s.CleanupOldData(s.RetentionDays())
	if err == nil {
		s.mu.Lock()
		s.lastCleanupDay = Today()
		s.mu.Unlock()
	}
	return res, err
}
