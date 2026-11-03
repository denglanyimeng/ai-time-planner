package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ai-time-planner/internal/service"
)

// exportTasksCSV 导出任务明细为 CSV（Excel 可直接打开）。
func (s *Server) exportTasksCSV(c *gin.Context) {
	to := dateQuery(c)
	from := strings.TrimSpace(c.Query("from"))
	if from == "" {
		// 默认导出保留窗口内的数据
		base, err := service.ParseDate(to)
		if err != nil {
			fail(c, service.ErrInvalid)
			return
		}
		from = service.FormatDate(base.AddDate(0, 0, -(s.svc.RetentionDays() - 1)))
	}
	if _, err := service.ParseDate(from); err != nil {
		fail(c, service.ErrInvalid)
		return
	}

	filename := fmt.Sprintf("tasks-%s_%s.csv", from, to)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Cache-Control", "no-store")

	if err := s.svc.ExportTasksCSV(c.Writer, from, to); err != nil {
		// 响应头已经发出，此时无法再返回 JSON 错误，只能记录
		_ = c.Error(err)
	}
}

// exportBackup 导出完整备份压缩包。
func (s *Server) exportBackup(c *gin.Context) {
	filename := fmt.Sprintf("planner-backup-%s.zip", time.Now().Format("20060102-150405"))
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Cache-Control", "no-store")

	if err := s.svc.ExportBackup(c.Writer); err != nil {
		_ = c.Error(err)
	}
}

// importBackup 从上传的备份包恢复数据。
func (s *Server) importBackup(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "请选择要导入的备份文件"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		fail(c, err)
		return
	}
	defer f.Close()

	res, err := s.svc.ImportBackup(f, fh.Size)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, res)
}

// cleanup 手动触发 21 天数据清理。
func (s *Server) cleanup(c *gin.Context) {
	res, err := s.svc.CleanupNow()
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, res)
}

// clearData 批量清空业务数据（管理中心）。
func (s *Server) clearData(c *gin.Context) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&req)
	// 危险操作要求显式确认，避免误点
	if strings.TrimSpace(req.Confirm) != "DELETE" {
		c.JSON(http.StatusBadRequest, gin.H{
			"ok":    false,
			"error": "该操作会清空全部任务、标签与统计数据，请在 confirm 字段填写 DELETE 以确认",
		})
		return
	}
	deleted, err := s.svc.ClearAllData()
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"deleted": deleted})
}
