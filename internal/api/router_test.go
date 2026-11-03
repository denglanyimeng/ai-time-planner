package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ai-time-planner/internal/config"
	"ai-time-planner/internal/service"
)

// buildTestRouter 构造一个仅用于路由校验的引擎。
// 路由注册本身不访问数据库，因此可以传 nil 依赖 ——
// 这样即使本机还没建库，也能验证路由表是否正确。
func buildTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := service.New(nil, nil, config.Config{})
	return NewRouter(svc, config.Config{}, "")
}

// TestRoutesRegisterWithoutConflict 验证路由表能成功注册。
// Gin 在静态路径与参数路径冲突时会 panic，这个测试用来在启动前就挡住这类问题。
func TestRoutesRegisterWithoutConflict(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("路由注册发生 panic：%v", r)
		}
	}()
	r := buildTestRouter(t)
	if len(r.Routes()) == 0 {
		t.Fatal("没有注册任何路由")
	}
}

func TestExpectedRoutesExist(t *testing.T) {
	r := buildTestRouter(t)

	registered := map[string]bool{}
	for _, route := range r.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/health",
		"GET /api/dashboard",
		"GET /api/tasks",
		"POST /api/tasks",
		"PUT /api/tasks/:id",
		"DELETE /api/tasks/:id",
		"PUT /api/tasks/:id/actual",
		"POST /api/tasks/:id/start",
		"POST /api/tasks/:id/pause",
		"POST /api/tasks/:id/resume",
		"POST /api/tasks/:id/complete",
		"POST /api/tasks/:id/undone",
		"POST /api/tasks/:id/heartbeat",
		"POST /api/plan/generate",
		"POST /api/plan/align",
		"POST /api/plan/reorder",
		"GET /api/plan/config",
		"PUT /api/plan/config",
		"GET /api/tags",
		"POST /api/tags",
		"GET /api/timeblocks",
		"GET /api/recurring",
		"POST /api/recurring/materialize",
		"POST /api/recurring/conflict",
		"GET /api/pool",
		"POST /api/pool/adopt",
		"GET /api/goals",
		"GET /api/goals/progress",
		"GET /api/stats/trend",
		"GET /api/stats/tags",
		"POST /api/stats/analyze",
		"GET /api/export/tasks.csv",
		"GET /api/export/backup",
		"POST /api/import/backup",
		"POST /api/admin/cleanup",
		"POST /api/admin/clear",
		"GET /api/config",
		"PUT /api/config",
	}

	for _, w := range want {
		if !registered[w] {
			t.Errorf("缺少路由：%s", w)
		}
	}
}

// ---------- 纯校验函数 ----------

func TestValidColor(t *testing.T) {
	valid := []string{"#fff", "#FFF", "#3b82f6", "#AABBCC"}
	for _, s := range valid {
		if !validColor(s) {
			t.Errorf("%q 应被判定为合法颜色", s)
		}
	}
	invalid := []string{"", "red", "#12", "#12345", "#gggggg", "rgb(1,2,3)", "#3b82f6;color:red"}
	for _, s := range invalid {
		if validColor(s) {
			t.Errorf("%q 应被判定为非法颜色", s)
		}
	}
}

func TestValidateTimeBlock(t *testing.T) {
	// 合法
	if msg := validateBlock(-1, 9*60, 12*60); msg != "" {
		t.Errorf("每天都通用的时间块应当合法，却报错：%s", msg)
	}
	if msg := validateBlock(0, 0, 1440); msg != "" {
		t.Errorf("边界值应当合法，却报错：%s", msg)
	}

	// 非法
	cases := []struct {
		name          string
		weekday, s, e int
	}{
		{"星期越界(7)", 7, 540, 720},
		{"星期越界(-2)", -2, 540, 720},
		{"开始时间为负", 1, -1, 720},
		{"开始时间超出一天", 1, 1440, 1500},
		{"结束时间为 0", 1, 540, 0},
		{"结束时间超过一天", 1, 540, 1441},
		{"结束早于开始", 1, 720, 540},
		{"结束等于开始", 1, 600, 600},
	}
	for _, tc := range cases {
		if msg := validateBlock(tc.weekday, tc.s, tc.e); msg == "" {
			t.Errorf("%s：应当被拒绝，却通过了", tc.name)
		}
	}
}

func TestValidateRecurring(t *testing.T) {
	base := recurringReq{
		Name:          "背单词",
		DurationMin:   30,
		Frequency:     service.FreqDaily,
		EffectiveFrom: "2026-09-14",
	}
	if msg := validateRecurring(base); msg != "" {
		t.Fatalf("基础配置应当合法，却报错：%s", msg)
	}

	weekly := base
	weekly.Frequency = service.FreqWeekly
	weekly.Weekdays = "1,3,5"
	if msg := validateRecurring(weekly); msg != "" {
		t.Errorf("weekly + weekdays 应当合法，却报错：%s", msg)
	}

	weeklyNoDays := weekly
	weeklyNoDays.Weekdays = ""
	if msg := validateRecurring(weeklyNoDays); msg == "" {
		t.Error("weekly 未指定星期应当被拒绝")
	}

	emptyName := base
	emptyName.Name = "   "
	if msg := validateRecurring(emptyName); msg == "" {
		t.Error("空任务名应当被拒绝")
	}

	badFreq := base
	badFreq.Frequency = "monthly"
	if msg := validateRecurring(badFreq); msg == "" {
		t.Error("未知周期类型应当被拒绝")
	}

	badDate := base
	badDate.EffectiveFrom = "2026/09/14"
	if msg := validateRecurring(badDate); msg == "" {
		t.Error("非法生效日期应当被拒绝")
	}

	reversed := base
	to := "2026-09-01"
	reversed.EffectiveTo = &to
	if msg := validateRecurring(reversed); msg == "" {
		t.Error("结束日期早于生效日期应当被拒绝")
	}

	badDuration := base
	badDuration.DurationMin = 2000
	if msg := validateRecurring(badDuration); msg == "" {
		t.Error("超范围时长应当被拒绝")
	}

	openEnded := base
	empty := ""
	openEnded.EffectiveTo = &empty
	if msg := validateRecurring(openEnded); msg != "" {
		t.Errorf("结束日期留空（无限期）应当合法，却报错：%s", msg)
	}
}

// ---------- 响应与错误处理 ----------

func TestUnknownApiPathReturnsJSON404(t *testing.T) {
	r := buildTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("未知 API 路径应返回 404，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("未知 API 路径应返回 JSON，实际 Content-Type=%q", ct)
	}
}

func TestInvalidIDReturns400(t *testing.T) {
	r := buildTestRouter(t)

	for _, path := range []string{"/api/tasks/abc", "/api/tasks/0", "/api/tasks/-5"} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d", path, w.Code)
		}
	}
}
