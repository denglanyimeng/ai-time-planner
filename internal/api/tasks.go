package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"ai-time-planner/internal/service"
)

// ---------- 任务查询与编辑 ----------

func (s *Server) listTasks(c *gin.Context) {
	tasks, err := s.svc.TasksWithView(dateQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, tasks)
}

type createTaskReq struct {
	PlanDate    string `json:"planDate"`
	Name        string `json:"name"`
	TagID       *int64 `json:"tagId"`
	EstimateMin int    `json:"estimateMin"`
}

func (s *Server) createTask(c *gin.Context) {
	var req createTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}
	task, err := s.svc.CreateTask(date, req.Name, req.TagID, req.EstimateMin)
	if err != nil {
		fail(c, err)
		return
	}
	created(c, task)
}

type updateTaskReq struct {
	Name              *string `json:"name"`
	TagID             *int64  `json:"tagId"`
	ClearTag          bool    `json:"clearTag"`
	AIEstimateMin     *int    `json:"aiEstimateMin"`
	UserEstimateMin   *int    `json:"userEstimateMin"`
	ClearUserEstimate bool    `json:"clearUserEstimate"`
	Status            *string `json:"status"`
	OrderIndex        *int    `json:"orderIndex"`
}

func (s *Server) updateTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	var req updateTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	task, err := s.svc.UpdateTask(id, service.TaskPatch{
		Name:              req.Name,
		TagID:             req.TagID,
		ClearTag:          req.ClearTag,
		AIEstimateMin:     req.AIEstimateMin,
		UserEstimateMin:   req.UserEstimateMin,
		ClearUserEstimate: req.ClearUserEstimate,
		Status:            req.Status,
		OrderIndex:        req.OrderIndex,
	})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

func (s *Server) deleteTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	if err := s.svc.DeleteTask(id); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"deleted": id})
}

type adjustActualReq struct {
	Seconds int `json:"seconds"`
}

// adjustActual 手动修正实际耗时（对应需求「支持用户手动修改实际耗时」）。
func (s *Server) adjustActual(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	var req adjustActualReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	task, err := s.svc.AdjustActualSeconds(id, req.Seconds)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

type reorderReq struct {
	PlanDate   string  `json:"planDate"`
	OrderedIDs []int64 `json:"orderedIds"`
}

// reorderTasks 调整顺序。按需求规则，只改顺序，不触碰时间戳。
func (s *Server) reorderTasks(c *gin.Context) {
	var req reorderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, service.ErrInvalid)
		return
	}
	date := req.PlanDate
	if date == "" {
		date = service.Today()
	}
	if err := s.svc.Reorder(date, req.OrderedIDs); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"reordered": len(req.OrderedIDs)})
}

// ---------- 计时 ----------

func (s *Server) startTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	task, err := s.svc.StartTask(id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

func (s *Server) pauseTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	task, err := s.svc.PauseTask(id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

func (s *Server) resumeTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	task, err := s.svc.ResumeTask(id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

func (s *Server) completeTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	task, err := s.svc.CompleteTask(id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

func (s *Server) undoneTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	task, err := s.svc.MarkUndone(id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, task)
}

// heartbeatTask 接收前端心跳。
// 前端每隔一段时间上报一次，服务端据此判断页面是否还开着。
func (s *Server) heartbeatTask(c *gin.Context) {
	id, okID := idParam(c)
	if !okID {
		return
	}
	if err := s.svc.Heartbeat(id); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
