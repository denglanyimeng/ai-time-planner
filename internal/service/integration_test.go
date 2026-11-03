package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ai-time-planner/internal/config"
	"ai-time-planner/internal/model"
	"ai-time-planner/internal/planner"
	"ai-time-planner/internal/store"
)

// 本文件是对业务逻辑的真实集成测试：跑在内存 SQLite 上，
// 覆盖那些「纯函数测不到、又最容易藏 bug」的部分 ——
// 计时结算、幂等物化、归集与清理顺序、重排的状态迁移。

func newTestService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("PLANNER_DATA_DIR", t.TempDir())

	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试数据库失败：%v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("迁移表结构失败：%v", err)
	}
	return New(db, nil, config.Config{})
}

// reload 重新读取任务最新状态。
func reload(t *testing.T, s *Service, id int64) model.Task {
	t.Helper()
	var task model.Task
	if err := s.DB().First(&task, id).Error; err != nil {
		t.Fatalf("读取任务 %d 失败：%v", id, err)
	}
	return task
}

// backdate 把「本轮开始时刻」往前回拨，用来在测试中模拟已经执行了一段时间。
func backdate(t *testing.T, s *Service, id int64, d time.Duration) {
	t.Helper()
	at := Now().Add(-d)
	if err := s.DB().Model(&model.Task{}).Where("id = ?", id).
		Update("run_started_at", at).Error; err != nil {
		t.Fatalf("回拨计时开始时刻失败：%v", err)
	}
}

// mustCreate 创建任务并在失败时终止测试。
func mustCreate(t *testing.T, s *Service, date, name string, estimate int) model.Task {
	t.Helper()
	task, err := s.CreateTask(date, name, nil, estimate)
	if err != nil {
		t.Fatalf("创建任务 %q 失败：%v", name, err)
	}
	return *task
}

// nowMinutesBJ 返回当前北京时间距零点的分钟数。
func nowMinutesBJ() int {
	bj := Now().In(planner.Beijing())
	return bj.Hour()*60 + bj.Minute()
}

// ---------- 计时 ----------

func TestTimingLifecycle(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "写实验报告", 60)

	// 开始计时
	if _, err := s.StartTask(task.ID); err != nil {
		t.Fatalf("开始计时失败：%v", err)
	}
	running := reload(t, s, task.ID)
	if running.Status != model.StatusRunning {
		t.Fatalf("状态应为 running，实际 %s", running.Status)
	}
	if running.RunStartedAt == nil {
		t.Fatal("开始计时后 run_started_at 不应为空")
	}

	// 回拨 10 分钟，模拟已经做了 10 分钟
	backdate(t, s, task.ID, 10*time.Minute)
	running = reload(t, s, task.ID)
	if el := ElapsedSeconds(&running, Now()); el < 595 || el > 605 {
		t.Errorf("实际耗时应约为 600 秒，实际 %d", el)
	}

	// 暂停：计时必须冻结，已耗时必须保存
	if _, err := s.PauseTask(task.ID); err != nil {
		t.Fatalf("暂停失败：%v", err)
	}
	paused := reload(t, s, task.ID)
	if paused.Status != model.StatusPaused {
		t.Errorf("状态应为 paused，实际 %s", paused.Status)
	}
	if paused.RunStartedAt != nil {
		t.Error("暂停后 run_started_at 必须清空，否则计时会继续增长")
	}
	if paused.ActualSeconds < 595 || paused.ActualSeconds > 605 {
		t.Errorf("暂停时应保存约 600 秒，实际 %d", paused.ActualSeconds)
	}

	// 暂停状态下耗时不再增长
	before := ElapsedSeconds(&paused, Now())
	after := ElapsedSeconds(&paused, Now().Add(30*time.Minute))
	if before != after {
		t.Errorf("暂停后耗时不应随时间增长：%d → %d", before, after)
	}

	// 恢复：从此刻继续累计
	if _, err := s.ResumeTask(task.ID); err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	resumed := reload(t, s, task.ID)
	if resumed.Status != model.StatusRunning || resumed.RunStartedAt == nil {
		t.Fatal("恢复后应重新进入计时状态")
	}
	if resumed.ActualSeconds != paused.ActualSeconds {
		t.Errorf("恢复不应改变已累计耗时：%d → %d", paused.ActualSeconds, resumed.ActualSeconds)
	}

	// 完成：结算并清除计时
	if _, err := s.CompleteTask(task.ID); err != nil {
		t.Fatalf("完成失败：%v", err)
	}
	done := reload(t, s, task.ID)
	if done.Status != model.StatusDone {
		t.Errorf("状态应为 done，实际 %s", done.Status)
	}
	if done.RunStartedAt != nil {
		t.Error("完成后不应仍在计时")
	}
	if done.CompletedAt == nil {
		t.Error("完成后应记录完成时刻")
	}
}

// 单用户场景下同时只能有一个任务在计时，否则多标签页会导致时长翻倍。
func TestStartAutoPausesOtherRunningTask(t *testing.T) {
	s := newTestService(t)
	date := Today()
	first := mustCreate(t, s, date, "任务A", 30)
	second := mustCreate(t, s, date, "任务B", 30)

	if _, err := s.StartTask(first.ID); err != nil {
		t.Fatalf("启动任务A 失败：%v", err)
	}
	backdate(t, s, first.ID, 5*time.Minute)

	if _, err := s.StartTask(second.ID); err != nil {
		t.Fatalf("启动任务B 失败：%v", err)
	}

	a := reload(t, s, first.ID)
	if a.Status != model.StatusPaused {
		t.Errorf("启动新任务后，旧任务应被自动暂停，实际 %s", a.Status)
	}
	if a.ActualSeconds < 295 || a.ActualSeconds > 305 {
		t.Errorf("旧任务应结算约 300 秒，实际 %d", a.ActualSeconds)
	}

	b := reload(t, s, second.ID)
	if b.Status != model.StatusRunning {
		t.Errorf("新任务应处于进行中，实际 %s", b.Status)
	}
}

// 这是「网页关闭即停止计时」的核心兜底逻辑。
// 关键点：结算到「最后一次心跳」而不是「当前时刻」，
// 否则用户关掉页面到后台发现之间的空档会被错误地算成学习时长。
func TestSettleStaleUsesLastHeartbeatNotNow(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "看网课", 60)

	if _, err := s.StartTask(task.ID); err != nil {
		t.Fatalf("开始计时失败：%v", err)
	}
	// 本轮已开始 60 分钟，但最后一次心跳停在 20 分钟前
	backdate(t, s, task.ID, 60*time.Minute)
	hb := Now().Add(-20 * time.Minute)
	if err := s.DB().Model(&model.Task{}).Where("id = ?", task.ID).
		Update("last_heartbeat_at", hb).Error; err != nil {
		t.Fatalf("设置心跳失败：%v", err)
	}

	n, err := s.SettleStale(HeartbeatGrace)
	if err != nil {
		t.Fatalf("结算超时计时失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("应结算 1 个任务，实际 %d", n)
	}

	got := reload(t, s, task.ID)
	if got.Status != model.StatusPaused {
		t.Errorf("心跳超时后应自动暂停，实际 %s", got.Status)
	}
	// 应结算到心跳时刻（40 分钟），而不是当前时刻（60 分钟）
	if got.ActualSeconds < 2395 || got.ActualSeconds > 2405 {
		t.Errorf("应结算到心跳时刻约 2400 秒，实际 %d（若为 3600 则说明错误地按当前时刻结算）",
			got.ActualSeconds)
	}
}

// 心跳仍然新鲜的任务不应被结算。
func TestSettleStaleSkipsFreshHeartbeat(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "正在做的事", 60)

	if _, err := s.StartTask(task.ID); err != nil {
		t.Fatalf("开始计时失败：%v", err)
	}
	// StartTask 会把心跳设为当前时刻，属于新鲜心跳
	n, err := s.SettleStale(HeartbeatGrace)
	if err != nil {
		t.Fatalf("结算失败：%v", err)
	}
	if n != 0 {
		t.Errorf("心跳新鲜时不应结算任何任务，实际结算 %d 个", n)
	}
	if got := reload(t, s, task.ID); got.Status != model.StatusRunning {
		t.Errorf("任务应保持进行中，实际 %s", got.Status)
	}
}

func TestHeartbeatUpdatesTimestamp(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "心跳任务", 30)
	if _, err := s.StartTask(task.ID); err != nil {
		t.Fatalf("开始计时失败：%v", err)
	}

	// 把心跳改旧，再调用心跳接口，应被刷新
	old := Now().Add(-10 * time.Minute)
	if err := s.DB().Model(&model.Task{}).Where("id = ?", task.ID).
		Update("last_heartbeat_at", old).Error; err != nil {
		t.Fatalf("设置旧心跳失败：%v", err)
	}
	if err := s.Heartbeat(task.ID); err != nil {
		t.Fatalf("心跳失败：%v", err)
	}
	got := reload(t, s, task.ID)
	if got.LastHeartbeatAt == nil || got.LastHeartbeatAt.Before(Now().Add(-time.Minute)) {
		t.Error("心跳后 last_heartbeat_at 应被刷新为接近当前时刻")
	}

	// 对未在计时的任务发心跳不应报错，也不应产生副作用
	other := mustCreate(t, s, Today(), "未开始的任务", 30)
	if err := s.Heartbeat(other.ID); err != nil {
		t.Errorf("对未计时的任务发心跳不应报错：%v", err)
	}
}

// ---------- 周期任务 ----------

func TestRecurringMaterializeIsIdempotent(t *testing.T) {
	s := newTestService(t)
	date := Today()

	from := date
	row := model.RecurringTask{
		Name: "背单词", DurationMin: 30,
		Frequency: FreqDaily, EffectiveFrom: from, Enabled: true,
	}
	if err := s.DB().Create(&row).Error; err != nil {
		t.Fatalf("创建周期任务失败：%v", err)
	}

	// 重复物化三次（模拟用户一天刷新多次页面）
	for i := 1; i <= 3; i++ {
		res, err := s.MaterializeRecurring(date)
		if err != nil {
			t.Fatalf("第 %d 次物化失败：%v", i, err)
		}
		if i == 1 && res.Created != 1 {
			t.Errorf("首次物化应生成 1 条，实际 %d", res.Created)
		}
		if i > 1 && res.Created != 0 {
			t.Errorf("第 %d 次物化不应重复生成，实际生成 %d 条", i, res.Created)
		}
	}

	tasks, err := s.tasksForDate(date, true)
	if err != nil {
		t.Fatalf("读取任务失败：%v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("无论刷新多少次都应只有 1 条任务，实际 %d 条", len(tasks))
	}
	if tasks[0].Source != model.SourceRecurring {
		t.Errorf("来源应标记为 recurring，实际 %s", tasks[0].Source)
	}
	if tasks[0].RecurringID == nil || *tasks[0].RecurringID != row.ID {
		t.Error("任务应关联回周期任务模板")
	}
}

func TestRecurringMaterializeDetectsConflict(t *testing.T) {
	s := newTestService(t)
	date := Today()

	// 当天已有一条同名的手动任务
	existing := mustCreate(t, s, date, "背单词", 20)

	row := model.RecurringTask{
		Name: "背单词", DurationMin: 30,
		Frequency: FreqDaily, EffectiveFrom: date, Enabled: true,
	}
	if err := s.DB().Create(&row).Error; err != nil {
		t.Fatalf("创建周期任务失败：%v", err)
	}

	res, err := s.MaterializeRecurring(date)
	if err != nil {
		t.Fatalf("物化失败：%v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("应检出 1 条冲突，实际 %d 条", len(res.Conflicts))
	}
	if res.Conflicts[0].ExistingTaskID != existing.ID {
		t.Errorf("冲突应指向已存在的任务 %d，实际 %d", existing.ID, res.Conflicts[0].ExistingTaskID)
	}
	if res.Created != 0 {
		t.Errorf("存在冲突时不应擅自生成，实际生成 %d 条", res.Created)
	}

	// 名称大小写与空白差异也应算作同一任务
	tasks, _ := s.tasksForDate(date, true)
	if len(tasks) != 1 {
		t.Errorf("冲突未决时仍应只有原来的 1 条任务，实际 %d 条", len(tasks))
	}
}

func TestResolveConflictOverwriteAndSkip(t *testing.T) {
	s := newTestService(t)
	date := Today()
	mustCreate(t, s, date, "晨跑", 20)

	row := model.RecurringTask{
		Name: "晨跑", DurationMin: 45,
		Frequency: FreqDaily, EffectiveFrom: date, Enabled: true,
	}
	if err := s.DB().Create(&row).Error; err != nil {
		t.Fatalf("创建周期任务失败：%v", err)
	}

	// 跳过：不应产生任何变化
	if _, err := s.ResolveRecurringConflict(date, row.ID, ConflictSkip); err != nil {
		t.Fatalf("跳过失败：%v", err)
	}
	tasks, _ := s.tasksForDate(date, true)
	if len(tasks) != 1 {
		t.Errorf("跳过后任务数不应变化，实际 %d", len(tasks))
	}

	// 覆盖：删除同名待开始任务，生成周期任务实例
	if _, err := s.ResolveRecurringConflict(date, row.ID, ConflictOverwrite); err != nil {
		t.Fatalf("覆盖失败：%v", err)
	}
	tasks, _ = s.tasksForDate(date, true)
	if len(tasks) != 1 {
		t.Fatalf("覆盖后应仍有 1 条任务，实际 %d", len(tasks))
	}
	if tasks[0].Source != model.SourceRecurring {
		t.Errorf("覆盖后应是周期任务实例，实际来源 %s", tasks[0].Source)
	}
	if tasks[0].AIEstimateMin != 45 {
		t.Errorf("应使用周期任务配置的时长 45，实际 %d", tasks[0].AIEstimateMin)
	}

	// 非法 action 应被拒绝
	if _, err := s.ResolveRecurringConflict(date, row.ID, "whatever"); err == nil {
		t.Error("非法冲突处理方式应被拒绝")
	}
}

// ---------- 重排 ----------

func TestRescheduleMarksOverflowAsUndone(t *testing.T) {
	s := newTestService(t)
	date := Today()

	// 构造一个很紧的可用窗口：从此刻到当前分钟数 +30
	deadline := nowMinutesBJ() + 30
	if deadline >= 24*60 {
		t.Skip("当前时间过晚，无法构造稳定的测试窗口")
	}
	if err := s.DB().Create(&model.DailyConfig{
		PlanDate: date, DeadlineMin: deadline,
	}).Error; err != nil {
		t.Fatalf("创建当日配置失败：%v", err)
	}

	mustCreate(t, s, date, "短任务", 10)
	long := mustCreate(t, s, date, "超长任务", 300)

	if _, err := s.Reschedule(date); err != nil {
		t.Fatalf("重排失败：%v", err)
	}

	got := reload(t, s, long.ID)
	if got.Status != model.StatusUndone {
		t.Errorf("排不下的任务应归为未完成，实际 %s", got.Status)
	}
	if got.PlannedStart != nil || got.PlannedEnd != nil {
		t.Error("未排入的任务不应残留计划时间戳")
	}
}

// 用户延长截止时间后再次对齐，之前排不下的任务应自动回到计划中。
func TestRescheduleBringsBackUndoneWhenCapacityGrows(t *testing.T) {
	s := newTestService(t)
	tomorrow := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, 1))

	// 明天 00:00–01:00 可用
	if err := s.DB().Create(&model.TimeBlock{
		Weekday: -1, StartMin: 0, EndMin: 60, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("创建时间块失败：%v", err)
	}
	if err := s.DB().Create(&model.DailyConfig{
		PlanDate: tomorrow, DeadlineMin: 60,
	}).Error; err != nil {
		t.Fatalf("创建当日配置失败：%v", err)
	}

	task := mustCreate(t, s, tomorrow, "三小时的大任务", 180)

	if _, err := s.Reschedule(tomorrow); err != nil {
		t.Fatalf("首次重排失败：%v", err)
	}
	if got := reload(t, s, task.ID); got.Status != model.StatusUndone {
		t.Fatalf("容量不足时应归为未完成，实际 %s", got.Status)
	}

	// 扩大可用容量。注意：可用时间是「时间块」与「当日截止时间」的交集，
	// 两者都必须放宽才会真正变大 —— 只改截止时间而时间块仍卡在 01:00 是无效的。
	if err := s.DB().Model(&model.TimeBlock{}).Where("weekday = ?", -1).
		Update("end_min", 600).Error; err != nil {
		t.Fatalf("扩大时间块失败：%v", err)
	}
	if err := s.DB().Model(&model.DailyConfig{}).Where("plan_date = ?", tomorrow).
		Update("deadline_min", 600).Error; err != nil {
		t.Fatalf("延长截止时间失败：%v", err)
	}

	if _, err := s.Reschedule(tomorrow); err != nil {
		t.Fatalf("再次重排失败：%v", err)
	}
	got := reload(t, s, task.ID)
	if got.Status != model.StatusPending {
		t.Errorf("容量恢复后应回到待开始，实际 %s", got.Status)
	}
	if got.PlannedStart == nil || got.PlannedEnd == nil {
		t.Error("恢复后应重新获得计划时间戳")
	}
}

// 补充验证上一条测试里点明的语义：当日截止时间只能「收紧」可用时间，
// 不能突破时间块设定的范围。这是刻意的设计 —— 时间块代表用户真实可用的时段。
func TestDeadlineCannotExceedTimeBlock(t *testing.T) {
	s := newTestService(t)
	tomorrow := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, 1))

	// 明天只有 00:00–01:00 可用
	if err := s.DB().Create(&model.TimeBlock{
		Weekday: -1, StartMin: 0, EndMin: 60, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("创建时间块失败：%v", err)
	}
	// 截止时间放到 10:00
	if err := s.DB().Create(&model.DailyConfig{
		PlanDate: tomorrow, DeadlineMin: 600,
	}).Error; err != nil {
		t.Fatalf("创建当日配置失败：%v", err)
	}

	preview, err := s.SchedulePreview(tomorrow)
	if err != nil {
		t.Fatalf("计算负载失败：%v", err)
	}
	if preview.AvailableMin > 60 {
		t.Errorf("可用时长不应超过时间块范围（60 分钟），实际 %.0f 分钟", preview.AvailableMin)
	}
}

// 严格遵循需求规则：拖拽只改顺序，不动时间戳。
func TestReorderChangesOrderButNotTimestamps(t *testing.T) {
	s := newTestService(t)
	date := Today()

	a := mustCreate(t, s, date, "任务A", 30)
	b := mustCreate(t, s, date, "任务B", 30)
	c := mustCreate(t, s, date, "任务C", 30)

	if _, err := s.Reschedule(date); err != nil {
		t.Fatalf("重排失败：%v", err)
	}
	beforeA := reload(t, s, a.ID)
	beforeB := reload(t, s, b.ID)

	// 把顺序改成 C, A, B
	if err := s.Reorder(date, []int64{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("调整顺序失败：%v", err)
	}

	afterA := reload(t, s, a.ID)
	afterB := reload(t, s, b.ID)
	afterC := reload(t, s, c.ID)

	if afterC.OrderIndex != 0 || afterA.OrderIndex != 1 || afterB.OrderIndex != 2 {
		t.Errorf("顺序应为 C=0,A=1,B=2，实际 C=%d,A=%d,B=%d",
			afterC.OrderIndex, afterA.OrderIndex, afterB.OrderIndex)
	}

	// 时间戳必须原封不动
	if !sameTime(beforeA.PlannedStart, afterA.PlannedStart) {
		t.Error("拖拽不应修改任务A 的计划开始时间")
	}
	if !sameTime(beforeB.PlannedEnd, afterB.PlannedEnd) {
		t.Error("拖拽不应修改任务B 的计划结束时间")
	}
}

func TestReorderRejectsMismatchedOrForeignIDs(t *testing.T) {
	s := newTestService(t)
	date := Today()
	a := mustCreate(t, s, date, "任务A", 30)
	mustCreate(t, s, date, "任务B", 30)

	// 数量不一致
	if err := s.Reorder(date, []int64{a.ID}); err == nil {
		t.Error("数量不一致应被拒绝")
	}
	// 重复提交
	if err := s.Reorder(date, []int64{a.ID, a.ID}); err == nil {
		t.Error("重复 ID 应被拒绝")
	}
	// 不属于当天
	if err := s.Reorder(date, []int64{a.ID, 99999}); err == nil {
		t.Error("不属于当天的任务 ID 应被拒绝")
	}
}

func sameTime(x, y *time.Time) bool {
	if x == nil || y == nil {
		return x == nil && y == nil
	}
	return x.Equal(*y)
}

// ---------- 归集与清理 ----------

// 这是原文档硬矛盾的修复验证：
// 明细被清理之后，按标签的累计统计必须依然存在。
func TestCleanupKeepsPermanentTagStats(t *testing.T) {
	s := newTestService(t)

	// 建一个标签
	tag := model.Tag{Name: "学习", Color: "#3b82f6"}
	if err := s.DB().Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败：%v", err)
	}

	// 造一条 30 天前的任务（超出默认 21 天保留期）
	oldDate := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, -30))
	tid := tag.ID
	old := model.Task{
		PlanDate: oldDate, OrderIndex: 0, Name: "很久以前的学习",
		TagID: &tid, AIEstimateMin: 60,
		ActualSeconds: 3600, Status: model.StatusDone, Source: model.SourceManual,
	}
	if err := s.DB().Create(&old).Error; err != nil {
		t.Fatalf("创建历史任务失败：%v", err)
	}

	res, err := s.CleanupOldData(21)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if res.DeletedTasks != 1 {
		t.Errorf("应删除 1 条过期明细，实际 %d", res.DeletedTasks)
	}
	if res.RolledUpDays != 1 {
		t.Errorf("删除前应先归集 1 天，实际 %d", res.RolledUpDays)
	}

	// 明细已删
	var remaining int64
	s.DB().Model(&model.Task{}).Where("plan_date = ?", oldDate).Count(&remaining)
	if remaining != 0 {
		t.Errorf("过期明细应被删除，实际还剩 %d 条", remaining)
	}

	// 但汇总必须保留 —— 这是长期目标统计能够累计的前提
	var stat model.DailyTagStat
	if err := s.DB().Where("plan_date = ? AND tag_id = ?", oldDate, tid).
		First(&stat).Error; err != nil {
		t.Fatalf("汇总数据必须保留，却查不到：%v", err)
	}
	if stat.TotalSeconds != 3600 {
		t.Errorf("汇总时长应为 3600 秒，实际 %d", stat.TotalSeconds)
	}
	if stat.CompletedCount != 1 {
		t.Errorf("汇总完成数应为 1，实际 %d", stat.CompletedCount)
	}
}

func TestCleanupKeepsRecentData(t *testing.T) {
	s := newTestService(t)
	date := Today()
	mustCreate(t, s, date, "今天的任务", 30)

	res, err := s.CleanupOldData(21)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if res.DeletedTasks != 0 {
		t.Errorf("保留期内的数据不应被删除，实际删除 %d 条", res.DeletedTasks)
	}

	tasks, _ := s.tasksForDate(date, true)
	if len(tasks) != 1 {
		t.Errorf("今天的任务应保留，实际 %d 条", len(tasks))
	}
}

func TestLongTermProgressAccumulatesAcrossCleanup(t *testing.T) {
	s := newTestService(t)

	tag := model.Tag{Name: "编程", Color: "#22c55e"}
	if err := s.DB().Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败：%v", err)
	}
	tid := tag.ID

	// 一条 30 天前（会被清理）与一条今天（保留）
	oldDate := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, -30))
	if err := s.DB().Create(&model.Task{
		PlanDate: oldDate, Name: "历史编程练习", TagID: &tid,
		AIEstimateMin: 60, ActualSeconds: 7200,
		Status: model.StatusDone, Source: model.SourceManual,
	}).Error; err != nil {
		t.Fatalf("创建历史任务失败：%v", err)
	}
	if err := s.DB().Create(&model.Task{
		PlanDate: Today(), Name: "今天的编程练习", TagID: &tid,
		AIEstimateMin: 60, ActualSeconds: 1800,
		Status: model.StatusDone, Source: model.SourceManual,
	}).Error; err != nil {
		t.Fatalf("创建今日任务失败：%v", err)
	}

	if _, err := s.CleanupOldData(21); err != nil {
		t.Fatalf("清理失败：%v", err)
	}

	goal := model.LongTermGoal{Content: "成为更好的工程师", TagID: &tid}
	if err := s.DB().Create(&goal).Error; err != nil {
		t.Fatalf("创建长期目标失败：%v", err)
	}

	progress, err := s.LongTermProgress()
	if err != nil {
		t.Fatalf("统计长期目标进度失败：%v", err)
	}
	if len(progress) != 1 {
		t.Fatalf("应有 1 个目标，实际 %d", len(progress))
	}
	// 7200（已清理的历史数据，靠汇总保留）+ 1800（今天）
	if progress[0].TotalSeconds != 9000 {
		t.Errorf("累计时长应为 9000 秒（历史汇总 + 今日），实际 %d", progress[0].TotalSeconds)
	}
}

// ---------- 未完成任务池 ----------

func TestAdoptTaskCopiesWithCarryLink(t *testing.T) {
	s := newTestService(t)

	yesterday := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, -1))
	original := mustCreate(t, s, yesterday, "没写完的报告", 90)
	if _, err := s.MarkUndone(original.ID); err != nil {
		t.Fatalf("标记未完成失败：%v", err)
	}

	// 承接前：池中可见且未被承接
	pool, err := s.Pool(Today())
	if err != nil {
		t.Fatalf("读取任务池失败：%v", err)
	}
	if len(pool) != 1 {
		t.Fatalf("池中应有 1 条，实际 %d", len(pool))
	}
	if pool[0].Adopted {
		t.Error("承接前不应标记为已承接")
	}

	adopted, err := s.AdoptTask(original.ID, Today())
	if err != nil {
		t.Fatalf("承接失败：%v", err)
	}
	if adopted.PlanDate != Today() {
		t.Errorf("承接后应属于今天，实际 %s", adopted.PlanDate)
	}
	if adopted.Source != model.SourceCarryover {
		t.Errorf("来源应为 carryover，实际 %s", adopted.Source)
	}
	if adopted.CarryFromID == nil || *adopted.CarryFromID != original.ID {
		t.Error("应记录承接来源 ID")
	}

	// 原任务必须保留在原日期（复制而非移动），否则历史统计会被改写
	still := reload(t, s, original.ID)
	if still.PlanDate != yesterday || still.Status != model.StatusUndone {
		t.Error("承接后原任务应保持不变")
	}

	// 承接后池中该项应标记为已承接
	pool, _ = s.Pool(Today())
	if len(pool) != 1 || !pool[0].Adopted {
		t.Error("承接后池中该项应标记为已承接")
	}

	// 已完成的任务不能被承接
	done := mustCreate(t, s, Today(), "已完成的任务", 30)
	if _, err := s.CompleteTask(done.ID); err != nil {
		t.Fatalf("完成任务失败：%v", err)
	}
	if _, err := s.AdoptTask(done.ID, Today()); err == nil {
		t.Error("只有未完成的任务才能被承接")
	}
}

// ---------- 任务编辑 ----------

func TestUpdateTaskValidation(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "原始名称", 30)

	// 空名称应被拒绝
	empty := "   "
	if _, err := s.UpdateTask(task.ID, TaskPatch{Name: &empty}); err == nil {
		t.Error("空名称应被拒绝")
	}

	// 非法状态应被拒绝
	bad := "finished"
	if _, err := s.UpdateTask(task.ID, TaskPatch{Status: &bad}); err == nil {
		t.Error("未知状态应被拒绝")
	}

	// 负数预估应被拒绝
	neg := -5
	if _, err := s.UpdateTask(task.ID, TaskPatch{AIEstimateMin: &neg}); err == nil {
		t.Error("负数预估应被拒绝")
	}

	// 不存在的标签应被拒绝
	missing := int64(99999)
	if _, err := s.UpdateTask(task.ID, TaskPatch{TagID: &missing}); err == nil {
		t.Error("不存在的标签应被拒绝")
	}

	// 正常修改应成功
	name := "  改名后的任务  "
	est := 45
	updated, err := s.UpdateTask(task.ID, TaskPatch{Name: &name, AIEstimateMin: &est})
	if err != nil {
		t.Fatalf("正常修改失败：%v", err)
	}
	if updated.Name != "改名后的任务" {
		t.Errorf("名称应被 trim，实际 %q", updated.Name)
	}
	if updated.AIEstimateMin != 45 {
		t.Errorf("预估应为 45，实际 %d", updated.AIEstimateMin)
	}
}

// 通过状态字段直接改状态时，计时字段必须同步维护，不能出现自相矛盾的状态。
func TestUpdateTaskStatusKeepsTimerConsistent(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "状态切换测试", 30)

	running := model.StatusRunning
	updated, err := s.UpdateTask(task.ID, TaskPatch{Status: &running})
	if err != nil {
		t.Fatalf("切到进行中失败：%v", err)
	}
	if updated.RunStartedAt == nil {
		t.Error("切到进行中时必须设置开始时刻，否则计时永远算不出来")
	}

	paused := model.StatusPaused
	updated, err = s.UpdateTask(task.ID, TaskPatch{Status: &paused})
	if err != nil {
		t.Fatalf("切到已暂停失败：%v", err)
	}
	if updated.RunStartedAt != nil {
		t.Error("切到已暂停时必须清除开始时刻")
	}

	done := model.StatusDone
	updated, err = s.UpdateTask(task.ID, TaskPatch{Status: &done})
	if err != nil {
		t.Fatalf("切到已完成失败：%v", err)
	}
	if updated.CompletedAt == nil {
		t.Error("切到已完成时应记录完成时刻")
	}
}

func TestAdjustActualSeconds(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "手动修正耗时", 30)

	// 负数应被拒绝
	if _, err := s.AdjustActualSeconds(task.ID, -1); err == nil {
		t.Error("负数耗时应被拒绝")
	}

	updated, err := s.AdjustActualSeconds(task.ID, 1800)
	if err != nil {
		t.Fatalf("修正耗时失败：%v", err)
	}
	if updated.ActualSeconds != 1800 {
		t.Errorf("耗时应为 1800 秒，实际 %d", updated.ActualSeconds)
	}

	// 超大值应被收敛到 24 小时上限
	updated, err = s.AdjustActualSeconds(task.ID, 999999)
	if err != nil {
		t.Fatalf("修正耗时失败：%v", err)
	}
	if updated.ActualSeconds != 24*3600 {
		t.Errorf("应收敛到 24 小时，实际 %d", updated.ActualSeconds)
	}
}

// 正在计时的任务被手动修正后，应继续计时且不叠加。
func TestAdjustActualSecondsKeepsRunning(t *testing.T) {
	s := newTestService(t)
	task := mustCreate(t, s, Today(), "边计时边修正", 30)
	if _, err := s.StartTask(task.ID); err != nil {
		t.Fatalf("开始计时失败：%v", err)
	}

	updated, err := s.AdjustActualSeconds(task.ID, 600)
	if err != nil {
		t.Fatalf("修正失败：%v", err)
	}
	if updated.Status != model.StatusRunning {
		t.Errorf("修正耗时不应急停计时，实际状态 %s", updated.Status)
	}
	if updated.RunStartedAt == nil {
		t.Error("修正后应重新开始累计")
	}
	if updated.ActualSeconds != 600 {
		t.Errorf("已累计应精确为 600 秒，实际 %d", updated.ActualSeconds)
	}
}

// ---------- 每日收尾 ----------

func TestCloseDayMarksOpenTasksUndone(t *testing.T) {
	s := newTestService(t)

	yesterday := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, -1))
	pending := mustCreate(t, s, yesterday, "忘了做的事", 30)

	running := mustCreate(t, s, yesterday, "做一半的事", 30)
	if _, err := s.StartTask(running.ID); err != nil {
		t.Fatalf("开始计时失败：%v", err)
	}
	backdate(t, s, running.ID, 15*time.Minute)

	done := mustCreate(t, s, yesterday, "已完成的事", 30)
	if _, err := s.CompleteTask(done.ID); err != nil {
		t.Fatalf("完成任务失败：%v", err)
	}

	n, err := s.CloseDay(yesterday)
	if err != nil {
		t.Fatalf("收尾失败：%v", err)
	}
	if n != 2 {
		t.Errorf("应有 2 条未结束任务被归类，实际 %d", n)
	}

	if got := reload(t, s, pending.ID); got.Status != model.StatusUndone {
		t.Errorf("待开始的任务应归为未完成，实际 %s", got.Status)
	}
	got := reload(t, s, running.ID)
	if got.Status != model.StatusUndone {
		t.Errorf("进行中的任务应归为未完成，实际 %s", got.Status)
	}
	if got.RunStartedAt != nil {
		t.Error("收尾时必须停止计时")
	}
	if got.ActualSeconds < 895 || got.ActualSeconds > 905 {
		t.Errorf("应收割约 900 秒，实际 %d", got.ActualSeconds)
	}
	// 已完成的不能被改写
	if got := reload(t, s, done.ID); got.Status != model.StatusDone {
		t.Errorf("已完成的任务不应被收尾改写，实际 %s", got.Status)
	}
}

func TestCatchUpMissedDays(t *testing.T) {
	s := newTestService(t)
	threeDaysAgo := FormatDate(Now().In(planner.Beijing()).AddDate(0, 0, -3))
	task := mustCreate(t, s, threeDaysAgo, "三天前忘记的事", 30)

	// 模拟「服务停了三天，今天才重新开机」
	n, err := s.CatchUpMissedDays()
	if err != nil {
		t.Fatalf("追赶收尾失败：%v", err)
	}
	if n < 1 {
		t.Errorf("应补做至少 1 条任务的收尾，实际 %d", n)
	}
	if got := reload(t, s, task.ID); got.Status != model.StatusUndone {
		t.Errorf("历史遗留任务应被归为未完成，实际 %s", got.Status)
	}
}

// ---------- 本地降级生成 ----------

// 未配置 AI 时，生成计划必须仍然可用（走本地规则），而不是直接失败。
func TestGeneratePlanFallsBackToLocalWithoutAI(t *testing.T) {
	s := newTestService(t)
	date := Today()

	out, err := s.GeneratePlan(nil, GenerateOptions{
		PlanDate:  date,
		UserInput: "复习高数，写实验报告，跑步",
	})
	if err != nil {
		t.Fatalf("未配置 AI 时生成计划不应失败：%v", err)
	}
	if out.Source != "local" {
		t.Errorf("降级时来源应为 local，实际 %s", out.Source)
	}
	if out.Notice == "" {
		t.Error("降级时必须明确告知用户，不能静默降级")
	}

	tasks, _ := s.tasksForDate(date, true)
	if len(tasks) != 3 {
		t.Errorf("应拆出 3 条任务，实际 %d", len(tasks))
	}
	if out.Schedule == nil {
		t.Error("应返回排程结果")
	}
}

func TestGeneratePlanRejectsEmptyInput(t *testing.T) {
	s := newTestService(t)
	if _, err := s.GeneratePlan(nil, GenerateOptions{
		PlanDate: Today(), UserInput: "   ",
	}); err == nil {
		t.Error("空输入应被拒绝")
	}
}

// 重新生成时只清理「AI 生成且未开始」的任务，用户手动维护的内容必须保留。
func TestGeneratePlanReplaceKeepsManualTasks(t *testing.T) {
	s := newTestService(t)
	date := Today()

	manual := mustCreate(t, s, date, "我手动加的任务", 30)

	if _, err := s.GeneratePlan(nil, GenerateOptions{
		PlanDate: date, UserInput: "读书，写代码",
	}); err != nil {
		t.Fatalf("首次生成失败：%v", err)
	}

	if _, err := s.GeneratePlan(nil, GenerateOptions{
		PlanDate: date, UserInput: "只做一件事", Replace: true,
	}); err != nil {
		t.Fatalf("重新生成失败：%v", err)
	}

	tasks, _ := s.tasksForDate(date, true)
	var hasManual, aiCount int
	for _, task := range tasks {
		if task.ID == manual.ID {
			hasManual++
		}
		if task.Source == model.SourceAI {
			aiCount++
		}
	}
	if hasManual != 1 {
		t.Error("重新生成不应删除用户手动添加的任务")
	}
	if aiCount != 1 {
		t.Errorf("重新生成后应只剩本次生成的 1 条 AI 任务，实际 %d 条", aiCount)
	}
}
