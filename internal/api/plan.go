package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ai-time-planner/internal/ai"
	"ai-time-planner/internal/model"
	"ai-time-planner/internal/service"
)

// ---------- 仪表盘 / 计划 ----------

func (s *Server) dashboard(c *gin.Context) {
	dash, err := s.svc.Dashboard(dateQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, dash)
}

type generateReq struct {
	PlanDate   string `json:"planDate"`
	Input      string `json:"input"`
	Constraint string `json:"constraint"`
	Replace    bool   `json:"replace"`
}

// generatePlan 由 AI 生成当日计划。
//
// 约束条件只对本次生成生效，不做全局保存（符合需求 2.2.10），
// 但会记录在当日配置里，便于事后复盘当时是怎么规划的。
func (s *Server) generatePlan(c *gin.Context) {
	var req generateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}

	// AI 生成可能较慢，给足超时时间
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	out, err := s.svc.GeneratePlan(ctx, service.GenerateOptions{
		PlanDate:   date,
		UserInput:  req.Input,
		Constraint: req.Constraint,
		Replace:    req.Replace,
	})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, out)
}

type alignReq struct {
	PlanDate string `json:"planDate"`
}

// alignPlan 一键对齐当前北京时间，刷新全部时间戳。
func (s *Server) alignPlan(c *gin.Context) {
	var req alignReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}
	out, err := s.svc.Reschedule(date)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, out)
}

func (s *Server) getPlanConfig(c *gin.Context) {
	date := dateQuery(c)
	var dc model.DailyConfig
	if err := s.svc.DB().Where("plan_date = ?", date).First(&dc).Error; err != nil {
		// 尚未设置过就返回系统默认值，而不是报错
		var sc model.SystemConfig
		_ = s.svc.DB().First(&sc, 1).Error
		if sc.DefaultDeadlineMin <= 0 {
			sc.DefaultDeadlineMin = 23 * 60
		}
		ok(c, gin.H{
			"planDate":    date,
			"deadlineMin": sc.DefaultDeadlineMin,
			"breakMin":    sc.DefaultBreakMin,
			"isDefault":   true,
		})
		return
	}
	ok(c, gin.H{
		"planDate":       dc.PlanDate,
		"deadlineMin":    dc.DeadlineMin,
		"breakMin":       dc.BreakMin,
		"constraintText": dc.ConstraintText,
		"aiPlanAdvice":   dc.AIPlanAdvice,
		"aiSummary":      dc.AISummary,
		"isDefault":      false,
	})
}

type planConfigReq struct {
	PlanDate    string `json:"planDate"`
	DeadlineMin *int   `json:"deadlineMin"`
	BreakMin    *int   `json:"breakMin"`
}

// updatePlanConfig 设置当日截止时间与休息间隔。
func (s *Server) updatePlanConfig(c *gin.Context) {
	var req planConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}
	if _, err := service.ParseDate(date); err != nil {
		fail(c, service.ErrInvalid)
		return
	}

	updates := map[string]any{"updated_at": service.Now()}
	if req.DeadlineMin != nil {
		v := *req.DeadlineMin
		if v <= 0 || v >= 24*60 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "截止时间必须在 1 到 1439 分钟之间"})
			return
		}
		updates["deadline_min"] = v
	}
	if req.BreakMin != nil {
		v := *req.BreakMin
		if v < 0 || v > 120 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "休息间隔必须在 0 到 120 分钟之间"})
			return
		}
		updates["break_min"] = v
	}
	if len(updates) == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "没有需要更新的字段"})
		return
	}

	// upsert：没有当日记录时先建一行
	var existing model.DailyConfig
	err := s.svc.DB().Where("plan_date = ?", date).First(&existing).Error
	if err != nil {
		var sc model.SystemConfig
		_ = s.svc.DB().First(&sc, 1).Error
		def := model.DailyConfig{
			PlanDate:    date,
			DeadlineMin: sc.DefaultDeadlineMin,
			BreakMin:    sc.DefaultBreakMin,
		}
		if def.DeadlineMin <= 0 {
			def.DeadlineMin = 23 * 60
		}
		if err := s.svc.DB().Create(&def).Error; err != nil {
			fail(c, err)
			return
		}
	}
	if err := s.svc.DB().Model(&model.DailyConfig{}).Where("plan_date = ?", date).
		Updates(updates).Error; err != nil {
		fail(c, err)
		return
	}
	s.getPlanConfig(c)
}

// ---------- 未完成任务池 ----------

func (s *Server) pool(c *gin.Context) {
	items, err := s.svc.Pool(dateQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, items)
}

type adoptReq struct {
	TaskID     int64  `json:"taskId"`
	TargetDate string `json:"targetDate"`
}

func (s *Server) adoptTask(c *gin.Context) {
	var req adoptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	if req.TargetDate == "" {
		req.TargetDate = service.Today()
	}
	task, err := s.svc.AdoptTask(req.TaskID, req.TargetDate)
	if err != nil {
		fail(c, err)
		return
	}
	created(c, task)
}

// ---------- 统计 ----------

func (s *Server) trend(c *gin.Context) {
	days := intQuery(c, "days", 21)
	points, err := s.svc.Trend(days)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, points)
}

func (s *Server) tagStats(c *gin.Context) {
	days := intQuery(c, "days", 21)
	stats, err := s.svc.TagStats(days)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stats)
}

type analyzeReq struct {
	PlanDate string `json:"planDate"`
}

// analyze 触发 AI 效率分析（双维度耗时偏差复盘）。
func (s *Server) analyze(c *gin.Context) {
	var req analyzeReq
	_ = c.ShouldBindJSON(&req)
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}

	tasks, err := s.svc.TasksWithView(date)
	if err != nil {
		fail(c, err)
		return
	}
	rows := make([]ai.AnalysisRow, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, ai.AnalysisRow{
			Name:        t.Name,
			Tag:         t.TagName,
			EstimateMin: t.EstimateMin,
			ActualMin:   t.ElapsedSeconds / 60,
			Status:      t.Status,
		})
	}
	if len(rows) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "当天还没有任务数据可供分析"})
		return
	}

	client := s.svc.AIClient()
	if !client.Configured() {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "尚未配置 AI 接口，请先在设置中填写"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	analysis, err := client.AnalyzeEfficiency(ctx, ai.AnalysisRequest{PlanDate: date, Rows: rows})
	if err != nil {
		fail(c, err)
		return
	}

	// 保存为当日总结
	summary := analysis.Summary
	if len(analysis.Suggestions) > 0 {
		summary += "\n\n" + strings.Join(prefixAll(analysis.Suggestions, "· "), "\n")
	}
	if err := s.svc.DB().Model(&model.DailyConfig{}).Where("plan_date = ?", date).
		Updates(map[string]any{"ai_summary": summary, "updated_at": service.Now()}).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, analysis)
}

func prefixAll(items []string, prefix string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = prefix + s
	}
	return out
}

func intQuery(c *gin.Context, key string, def int) int {
	if v := strings.TrimSpace(c.Query(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ---------- 长期目标 ----------

func (s *Server) listGoals(c *gin.Context) {
	var goals []model.LongTermGoal
	if err := s.svc.DB().Order("id").Find(&goals).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, goals)
}

type goalReq struct {
	Content string `json:"content"`
	TagID   *int64 `json:"tagId"`
}

func (s *Server) createGoal(c *gin.Context) {
	var req goalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "目标内容不能为空"})
		return
	}
	goal := model.LongTermGoal{Content: content, TagID: req.TagID}
	if err := s.svc.DB().Create(&goal).Error; err != nil {
		fail(c, err)
		return
	}
	created(c, goal)
}

type updateGoalReq struct {
	Content  *string `json:"content"`
	TagID    *int64  `json:"tagId"`
	ClearTag bool    `json:"clearTag"`
}

func (s *Server) updateGoal(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	var req updateGoalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	updates := map[string]any{"updated_at": service.Now()}
	if req.Content != nil {
		content := strings.TrimSpace(*req.Content)
		if content == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "目标内容不能为空"})
			return
		}
		updates["content"] = content
	}
	if req.ClearTag {
		updates["tag_id"] = nil
	} else if req.TagID != nil {
		updates["tag_id"] = *req.TagID
	}
	res := s.svc.DB().Model(&model.LongTermGoal{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(c, service.ErrNotFound)
		return
	}
	var goal model.LongTermGoal
	if err := s.svc.DB().First(&goal, id).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, goal)
}

func (s *Server) deleteGoal(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	res := s.svc.DB().Delete(&model.LongTermGoal{}, id)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	ok(c, gin.H{"deleted": res.RowsAffected})
}

func (s *Server) goalProgress(c *gin.Context) {
	items, err := s.svc.LongTermProgress()
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, items)
}

// ---------- 周期任务 ----------

func (s *Server) listRecurring(c *gin.Context) {
	var rows []model.RecurringTask
	if err := s.svc.DB().Order("id").Find(&rows).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, rows)
}

type recurringReq struct {
	Name          string  `json:"name"`
	TagID         *int64  `json:"tagId"`
	DurationMin   int     `json:"durationMin"`
	Frequency     string  `json:"frequency"`
	Weekdays      string  `json:"weekdays"`
	EffectiveFrom string  `json:"effectiveFrom"`
	EffectiveTo   *string `json:"effectiveTo"`
	Enabled       *bool   `json:"enabled"`
}

func (s *Server) createRecurring(c *gin.Context) {
	var req recurringReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	if msg := validateRecurring(req); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": msg})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row := model.RecurringTask{
		Name:          strings.TrimSpace(req.Name),
		TagID:         req.TagID,
		DurationMin:   req.DurationMin,
		Frequency:     req.Frequency,
		Weekdays:      strings.TrimSpace(req.Weekdays),
		EffectiveFrom: req.EffectiveFrom,
		EffectiveTo:   req.EffectiveTo,
		Enabled:       enabled,
	}
	if err := s.svc.DB().Create(&row).Error; err != nil {
		fail(c, err)
		return
	}
	created(c, row)
}

func (s *Server) updateRecurring(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	var req recurringReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	if msg := validateRecurring(req); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": msg})
		return
	}
	updates := map[string]any{
		"name":           strings.TrimSpace(req.Name),
		"tag_id":         req.TagID,
		"duration_min":   req.DurationMin,
		"frequency":      req.Frequency,
		"weekdays":       strings.TrimSpace(req.Weekdays),
		"effective_from": req.EffectiveFrom,
		"effective_to":   req.EffectiveTo,
		"updated_at":     service.Now(),
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	res := s.svc.DB().Model(&model.RecurringTask{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(c, service.ErrNotFound)
		return
	}
	var row model.RecurringTask
	if err := s.svc.DB().First(&row, id).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, row)
}

func (s *Server) deleteRecurring(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	res := s.svc.DB().Delete(&model.RecurringTask{}, id)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	ok(c, gin.H{"deleted": res.RowsAffected})
}

type materializeReq struct {
	PlanDate string `json:"planDate"`
}

// materializeRecurring 把当日周期任务物化。前端每次打开页面都会调用，因此必须幂等。
func (s *Server) materializeRecurring(c *gin.Context) {
	var req materializeReq
	_ = c.ShouldBindJSON(&req)
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}
	out, err := s.svc.MaterializeRecurring(date)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, out)
}

type conflictReq struct {
	PlanDate    string `json:"planDate"`
	RecurringID int64  `json:"recurringId"`
	Action      string `json:"action"`
}

func (s *Server) resolveConflict(c *gin.Context) {
	var req conflictReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}
	out, err := s.svc.ResolveRecurringConflict(date, req.RecurringID, req.Action)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, out)
}

// validateRecurring 校验周期任务配置，返回错误文案（空串表示通过）。
func validateRecurring(req recurringReq) string {
	if strings.TrimSpace(req.Name) == "" {
		return "任务内容不能为空"
	}
	switch req.Frequency {
	case service.FreqDaily, service.FreqEvery2, service.FreqEvery3, service.FreqWeekly:
	default:
		return "周期频率必须是 daily / every2days / every3days / weekly 之一"
	}
	if req.Frequency == service.FreqWeekly && strings.TrimSpace(req.Weekdays) == "" {
		return "按星期循环时必须指定星期（0=周日 … 6=周六，可多选，如 1,3,5）"
	}
	if _, err := service.ParseDate(req.EffectiveFrom); err != nil {
		return "生效日期格式必须是 YYYY-MM-DD"
	}
	if req.EffectiveTo != nil && strings.TrimSpace(*req.EffectiveTo) != "" {
		to, err := service.ParseDate(*req.EffectiveTo)
		if err != nil {
			return "结束日期格式必须是 YYYY-MM-DD"
		}
		from, _ := service.ParseDate(req.EffectiveFrom)
		if to.Before(from) {
			return "结束日期不能早于生效日期"
		}
	}
	if req.DurationMin < 0 || req.DurationMin > 1440 {
		return "单次时长必须在 0 到 1440 分钟之间"
	}
	return ""
}
