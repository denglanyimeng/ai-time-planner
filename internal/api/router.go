// Package api 提供 HTTP 接口层。
package api

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ai-time-planner/internal/config"
	"ai-time-planner/internal/model"
	"ai-time-planner/internal/service"
)

// Server 持有依赖并注册路由。
type Server struct {
	svc    *service.Service
	cfg    config.Config
	webDir string
}

// NewRouter 构建 Gin 引擎。
func NewRouter(svc *service.Service, cfg config.Config, webDir string) *gin.Engine {
	s := &Server{svc: svc, cfg: cfg, webDir: webDir}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	// 备份包可能较大
	r.MaxMultipartMemory = 64 << 20

	api := r.Group("/api")
	{
		api.GET("/health", s.health)

		// 任务
		api.GET("/tasks", s.listTasks)
		api.POST("/tasks", s.createTask)
		api.PUT("/tasks/:id", s.updateTask)
		api.DELETE("/tasks/:id", s.deleteTask)
		api.PUT("/tasks/:id/actual", s.adjustActual)

		// 计时
		api.POST("/tasks/:id/start", s.startTask)
		api.POST("/tasks/:id/pause", s.pauseTask)
		api.POST("/tasks/:id/resume", s.resumeTask)
		api.POST("/tasks/:id/complete", s.completeTask)
		api.POST("/tasks/:id/undone", s.undoneTask)
		api.POST("/tasks/:id/heartbeat", s.heartbeatTask)

		// 计划
		api.GET("/dashboard", s.dashboard)
		api.POST("/plan/generate", s.generatePlan)
		api.POST("/plan/align", s.alignPlan)
		api.POST("/plan/reorder", s.reorderTasks)
		api.GET("/plan/config", s.getPlanConfig)
		api.PUT("/plan/config", s.updatePlanConfig)

		// 标签
		api.GET("/tags", s.listTags)
		api.POST("/tags", s.createTag)
		api.PUT("/tags/:id", s.updateTag)
		api.DELETE("/tags/:id", s.deleteTag)

		// 时间块
		api.GET("/timeblocks", s.listTimeBlocks)
		api.POST("/timeblocks", s.createTimeBlock)
		api.PUT("/timeblocks/:id", s.updateTimeBlock)
		api.DELETE("/timeblocks/:id", s.deleteTimeBlock)

		// 周期任务
		api.GET("/recurring", s.listRecurring)
		api.POST("/recurring", s.createRecurring)
		api.PUT("/recurring/:id", s.updateRecurring)
		api.DELETE("/recurring/:id", s.deleteRecurring)
		api.POST("/recurring/materialize", s.materializeRecurring)
		api.POST("/recurring/conflict", s.resolveConflict)

		// 未完成池
		api.GET("/pool", s.pool)
		api.POST("/pool/adopt", s.adoptTask)

		// 长期目标
		api.GET("/goals", s.listGoals)
		api.POST("/goals", s.createGoal)
		api.PUT("/goals/:id", s.updateGoal)
		api.DELETE("/goals/:id", s.deleteGoal)
		api.GET("/goals/progress", s.goalProgress)

		// 统计
		api.GET("/stats/trend", s.trend)
		api.GET("/stats/tags", s.tagStats)
		api.POST("/stats/analyze", s.analyze)

		// 数据管理
		api.GET("/export/tasks.csv", s.exportTasksCSV)
		api.GET("/export/backup", s.exportBackup)
		api.POST("/import/backup", s.importBackup)
		api.POST("/admin/cleanup", s.cleanup)
		api.POST("/admin/clear", s.clearData)

		// 系统配置
		api.GET("/config", s.getConfig)
		api.PUT("/config", s.updateConfig)
	}

	// 静态前端
	s.mountWeb(r)

	// NoRoute 必须无条件注册。
	// 如果只在挂载了前端目录时才注册，那么未配置前端目录时，
	// 未知 API 路径会退化成 Gin 默认的 text/plain 404，
	// 而前端是统一按 JSON 信封解析错误响应的 —— 那样就会解析失败。
	r.NoRoute(s.notFound)

	return r
}

// mountWeb 挂载前端静态资源（可选）。
func (s *Server) mountWeb(r *gin.Engine) {
	if s.webDir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(s.webDir, "index.html")); err != nil {
		log.Printf("[web] 未找到前端目录 %s，将只提供 API 服务", s.webDir)
		return
	}

	// 前端资源一律要求浏览器每次重新校验。
	//
	// 不加这段时，浏览器会对 index.html / app.css / app.js 走「启发式缓存」——
	// Go 的静态文件只给 Last-Modified，没有 Cache-Control，
	// 于是改了样式之后用户按普通刷新仍然看到旧页面，只能靠手动强刷。
	// 这是个本地单人工具，多一次 304 校验的开销可以忽略。
	noCache := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache, must-revalidate")
		c.Next()
	}

	assets := r.Group("/assets", noCache)
	assets.Static("/", filepath.Join(s.webDir, "assets"))

	r.GET("/", noCache, func(c *gin.Context) {
		c.File(filepath.Join(s.webDir, "index.html"))
	})
	r.GET("/index.html", noCache, func(c *gin.Context) {
		c.File(filepath.Join(s.webDir, "index.html"))
	})
}

// notFound 统一处理未匹配的路径。
func (s *Server) notFound(c *gin.Context) {
	// API 路径一律返回 JSON，保证前端能统一处理错误
	if strings.HasPrefix(c.Request.URL.Path, "/api") {
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "接口不存在"})
		return
	}

	// 其余路径回落到单页应用，避免刷新页面出现 404
	if s.webDir != "" {
		index := filepath.Join(s.webDir, "index.html")
		if _, err := os.Stat(index); err == nil {
			c.File(index)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "页面不存在"})
}

// ---------- 响应辅助 ----------

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, gin.H{"ok": true, "data": data})
}

func fail(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrInvalid):
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"ok": false, "error": err.Error()})
}

// idParam 解析路径中的 :id。
func idParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "无效的 ID"})
		return 0, false
	}
	return id, true
}

// dateQuery 取 date 查询参数，缺省为今天。
func dateQuery(c *gin.Context) string {
	if d := strings.TrimSpace(c.Query("date")); d != "" {
		return d
	}
	return service.Today()
}

// ---------- 基础接口 ----------

func (s *Server) health(c *gin.Context) {
	ok(c, gin.H{
		"status":  "up",
		"today":   service.Today(),
		"now":     service.Now(),
		"aiReady": s.svc.AI().Configured(),
	})
}

// ---------- 标签 ----------

func (s *Server) listTags(c *gin.Context) {
	var tags []model.Tag
	if err := s.svc.DB().Order("id").Find(&tags).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, tags)
}

type tagReq struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Server) createTag(c *gin.Context) {
	var req tagReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "标签名称不能为空"})
		return
	}
	color := strings.TrimSpace(req.Color)
	if !validColor(color) {
		color = "#3b82f6"
	}
	tag := model.Tag{Name: name, Color: color}
	if err := s.svc.DB().Create(&tag).Error; err != nil {
		fail(c, err)
		return
	}
	created(c, tag)
}

func (s *Server) updateTag(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	var req tagReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	updates := map[string]any{}
	if n := strings.TrimSpace(req.Name); n != "" {
		updates["name"] = n
	}
	if validColor(req.Color) {
		updates["color"] = req.Color
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "没有需要更新的字段"})
		return
	}
	res := s.svc.DB().Model(&model.Tag{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(c, service.ErrNotFound)
		return
	}
	var tag model.Tag
	if err := s.svc.DB().First(&tag, id).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, tag)
}

func (s *Server) deleteTag(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	// 先把引用了该标签的任务解绑，避免留下悬空外键
	if err := s.svc.DB().Model(&model.Task{}).Where("tag_id = ?", id).
		Update("tag_id", nil).Error; err != nil {
		fail(c, err)
		return
	}
	res := s.svc.DB().Delete(&model.Tag{}, id)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	ok(c, gin.H{"deleted": res.RowsAffected})
}

// validColor 只接受 #RGB / #RRGGBB 形式的颜色，防止把任意字符串写进样式。
func validColor(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if !strings.HasPrefix(s, "#") {
		return false
	}
	for _, r := range s[1:] {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// ---------- 时间块 ----------

func (s *Server) listTimeBlocks(c *gin.Context) {
	var blocks []model.TimeBlock
	if err := s.svc.DB().Order("weekday, start_min").Find(&blocks).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, blocks)
}

type timeBlockReq struct {
	Weekday  int   `json:"weekday"`
	StartMin int   `json:"startMin"`
	EndMin   int   `json:"endMin"`
	Enabled  *bool `json:"enabled"`
}

func (s *Server) createTimeBlock(c *gin.Context) {
	var req timeBlockReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	if msg := validateBlock(req.Weekday, req.StartMin, req.EndMin); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": msg})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	block := model.TimeBlock{
		Weekday: req.Weekday, StartMin: req.StartMin,
		EndMin: req.EndMin, Enabled: enabled,
	}
	if err := s.svc.DB().Create(&block).Error; err != nil {
		fail(c, err)
		return
	}
	created(c, block)
}

func (s *Server) updateTimeBlock(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	var req timeBlockReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	if msg := validateBlock(req.Weekday, req.StartMin, req.EndMin); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": msg})
		return
	}
	updates := map[string]any{
		"weekday":   req.Weekday,
		"start_min": req.StartMin,
		"end_min":   req.EndMin,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	res := s.svc.DB().Model(&model.TimeBlock{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(c, service.ErrNotFound)
		return
	}
	var block model.TimeBlock
	if err := s.svc.DB().First(&block, id).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, block)
}

func (s *Server) deleteTimeBlock(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	res := s.svc.DB().Delete(&model.TimeBlock{}, id)
	if res.Error != nil {
		fail(c, res.Error)
		return
	}
	ok(c, gin.H{"deleted": res.RowsAffected})
}

// validateBlock 校验时间块。跨零点的时间块目前不支持，
// 明确拒绝比默默产生一个空窗口要好。
func validateBlock(weekday, startMin, endMin int) string {
	if weekday < -1 || weekday > 6 {
		return "星期取值必须在 -1（每天）到 6（周六）之间"
	}
	if startMin < 0 || startMin >= 24*60 {
		return "开始时间必须在 0 到 1439 分钟之间"
	}
	if endMin <= 0 || endMin > 24*60 {
		return "结束时间必须在 1 到 1440 分钟之间"
	}
	if endMin <= startMin {
		return "结束时间必须晚于开始时间（暂不支持跨零点的时间块）"
	}
	return ""
}

// ---------- 系统配置 ----------

func (s *Server) getConfig(c *gin.Context) {
	var sc model.SystemConfig
	if err := s.svc.DB().First(&sc, 1).Error; err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{
		"aiBaseUrl": sc.AIBaseURL,
		"aiModel":   sc.AIModel,
		// 出于安全考虑不回传密钥本体，只告知是否已配置
		"aiKeyConfigured":    sc.AIAPIKeyEncrypted != "",
		"defaultDeadlineMin": sc.DefaultDeadlineMin,
		"defaultBreakMin":    sc.DefaultBreakMin,
		"retentionDays":      sc.RetentionDays,
		"updatedAt":          sc.UpdatedAt,
	})
}

type configReq struct {
	AIBaseURL          *string `json:"aiBaseUrl"`
	AIAPIKey           *string `json:"aiApiKey"`
	AIModel            *string `json:"aiModel"`
	DefaultDeadlineMin *int    `json:"defaultDeadlineMin"`
	DefaultBreakMin    *int    `json:"defaultBreakMin"`
	RetentionDays      *int    `json:"retentionDays"`
}

func (s *Server) updateConfig(c *gin.Context) {
	var req configReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}

	updates := map[string]any{"updated_at": service.Now()}
	if req.AIBaseURL != nil {
		updates["ai_base_url"] = strings.TrimRight(strings.TrimSpace(*req.AIBaseURL), "/")
	}
	if req.AIAPIKey != nil {
		// 密钥以密文列存储；此处沿用简单封装，避免明文落库
		updates["ai_api_key_encrypted"] = service.EncryptSecret(*req.AIAPIKey)
	}
	if req.AIModel != nil {
		updates["ai_model"] = strings.TrimSpace(*req.AIModel)
	}
	if req.DefaultDeadlineMin != nil {
		v := *req.DefaultDeadlineMin
		if v <= 0 || v >= 24*60 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "默认截止时间必须在 1 到 1439 分钟之间"})
			return
		}
		updates["default_deadline_min"] = v
	}
	if req.DefaultBreakMin != nil {
		v := *req.DefaultBreakMin
		if v < 0 || v > 120 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "休息间隔必须在 0 到 120 分钟之间"})
			return
		}
		updates["default_break_min"] = v
	}
	if req.RetentionDays != nil {
		v := *req.RetentionDays
		if v < 1 || v > 3650 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "数据保留天数必须在 1 到 3650 之间"})
			return
		}
		updates["retention_days"] = v
	}

	if err := s.svc.DB().Model(&model.SystemConfig{}).Where("id = 1").
		Updates(updates).Error; err != nil {
		fail(c, err)
		return
	}
	s.getConfig(c)
}
