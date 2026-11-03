package service

import (
	"testing"
	"time"

	"ai-time-planner/internal/model"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"  背单词  ": "背单词",
		"ABC":     "abc",
		"AbC ":    "abc",
		"":        "",
	}
	for in, want := range cases {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 用于冲突判定的关键性质：大小写与空白差异不应被当成不同任务
	if normalizeName("背单词") != normalizeName(" 背单词 ") {
		t.Error("空白差异应被视为同一个任务")
	}
}

func TestPickTagColorIsStable(t *testing.T) {
	a := pickTagColor("学习")
	b := pickTagColor("学习")
	if a != b {
		t.Errorf("同一名称必须得到稳定配色，实际 %s vs %s", a, b)
	}
	if !isHexColor(a) {
		t.Errorf("生成的配色不是合法十六进制颜色：%s", a)
	}
	// 不同名称整体上应当分散到调色板的不同槽位
	seen := map[string]bool{}
	for _, name := range []string{"学习", "工作", "运动", "阅读", "生活", "写作", "编程"} {
		seen[pickTagColor(name)] = true
	}
	if len(seen) < 3 {
		t.Errorf("配色过于集中，7 个名称只用到 %d 种颜色", len(seen))
	}
}

func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			return false
		}
	}
	return true
}

// ---------- 剩余时长计算 ----------

// remainingEstimateMin 决定重排时任务占用多长时间。
// 如果它算错，暂停恢复后的整体顺延就会把已完成的部分重复计算，
// 导致时间轴越排越靠后，因此这里是重点测试对象。
func TestRemainingEstimateMin(t *testing.T) {
	userEst := 45
	base := model.Task{
		AIEstimateMin: 60,
		Status:        model.StatusPending,
	}

	// 1) 未开始：完整预估
	if got := remainingEstimateMin(base, Now()); got != 60 {
		t.Errorf("未开始任务应返回完整预估 60，实际 %d", got)
	}

	// 2) 用户自填预估优先于 AI 预估
	withUser := base
	withUser.UserEstimateMin = &userEst
	if got := remainingEstimateMin(withUser, Now()); got != 45 {
		t.Errorf("用户预估应优先，期望 45，实际 %d", got)
	}

	// 3) 进行中已过 20 分钟：剩余应为 40
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	running := model.Task{
		AIEstimateMin: 60,
		Status:        model.StatusRunning,
		RunStartedAt:  ptrTime(now.Add(-20 * time.Minute)),
	}
	if got := remainingEstimateMin(running, now); got != 40 {
		t.Errorf("已进行 20 分钟时剩余应为 40，实际 %d", got)
	}

	// 4) 已累计 30 分钟 + 本轮 15 分钟 = 45，剩余 15
	accrued := model.Task{
		AIEstimateMin: 60,
		ActualSeconds: 30 * 60,
		Status:        model.StatusRunning,
		RunStartedAt:  ptrTime(now.Add(-15 * time.Minute)),
	}
	if got := remainingEstimateMin(accrued, now); got != 15 {
		t.Errorf("累计计算错误，期望 15，实际 %d", got)
	}

	// 5) 实际耗时已超过预估：保留最小值 5，避免任务在时间轴上塌缩为零长度
	over := model.Task{
		AIEstimateMin: 60,
		ActualSeconds: 100 * 60,
		Status:        model.StatusPaused,
	}
	if got := remainingEstimateMin(over, now); got != 5 {
		t.Errorf("超额任务应返回最小 5 分钟，实际 %d", got)
	}

	// 6) 预估为 0 时使用兜底值 15
	noEst := model.Task{Status: model.StatusPending}
	if got := remainingEstimateMin(noEst, now); got != 15 {
		t.Errorf("无预估时应兜底为 15，实际 %d", got)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---------- 本地降级生成 ----------

func TestLocalDraftSplitsInput(t *testing.T) {
	draft := localDraft("复习高数，写实验报告\n跑步；背单词、整理笔记", 30)
	if len(draft.Tasks) != 5 {
		t.Fatalf("应拆出 5 条任务，实际 %d：%+v", len(draft.Tasks), draft.Tasks)
	}
	want := []string{"复习高数", "写实验报告", "跑步", "背单词", "整理笔记"}
	for i, w := range want {
		if draft.Tasks[i].Name != w {
			t.Errorf("第 %d 条应为 %q，实际 %q", i+1, w, draft.Tasks[i].Name)
		}
	}
	if draft.Tasks[0].EstimateMin != 30 {
		t.Errorf("默认时长应为 30，实际 %d", draft.Tasks[0].EstimateMin)
	}
	if draft.Advice == "" {
		t.Error("本地降级也应当给出说明性建议")
	}
}

func TestLocalDraftDeduplicatesAndCaps(t *testing.T) {
	// 重复项应被去掉
	draft := localDraft("跑步，跑步，跑步", 20)
	if len(draft.Tasks) != 1 {
		t.Errorf("重复任务应被去重，实际 %d 条", len(draft.Tasks))
	}

	// 数量上限 12
	var input string
	for i := 0; i < 40; i++ {
		input += "任务" + string(rune('A'+i%26)) + string(rune('0'+i/26)) + "，"
	}
	draft = localDraft(input, 20)
	if len(draft.Tasks) > 12 {
		t.Errorf("本地生成应有条数上限，实际 %d", len(draft.Tasks))
	}
}

func TestLocalDraftHandlesEmptyAndBlank(t *testing.T) {
	for _, in := range []string{"", "   ", "，，，", "\n\n"} {
		draft := localDraft(in, 30)
		if len(draft.Tasks) != 0 {
			t.Errorf("空白输入 %q 不应产生任务，实际 %d 条", in, len(draft.Tasks))
		}
	}
}

func TestLocalDraftZeroDefaultEstimateFallsBack(t *testing.T) {
	draft := localDraft("跑步", 0)
	if len(draft.Tasks) != 1 {
		t.Fatalf("应产生 1 条任务，实际 %d", len(draft.Tasks))
	}
	if draft.Tasks[0].EstimateMin != 30 {
		t.Errorf("默认时长非法时应兜底为 30，实际 %d", draft.Tasks[0].EstimateMin)
	}
}

// ---------- 密钥加解密 ----------

func TestSecretRoundTrip(t *testing.T) {
	// 使用项目内的临时目录，避免污染其它路径
	t.Setenv("PLANNER_DATA_DIR", t.TempDir())

	const plain = "sk-abcdef1234567890"
	enc := EncryptSecret(plain)
	if enc == "" {
		t.Fatal("加密结果不应为空")
	}
	if enc == plain {
		t.Fatal("加密结果不能等于明文")
	}
	if got := DecryptSecret(enc); got != plain {
		t.Errorf("解密结果应为 %q，实际 %q", plain, got)
	}
}

func TestSecretEmptyAndPlaintextPassthrough(t *testing.T) {
	t.Setenv("PLANNER_DATA_DIR", t.TempDir())

	if got := EncryptSecret(""); got != "" {
		t.Errorf("空字符串应加密为空串，实际 %q", got)
	}
	if got := DecryptSecret(""); got != "" {
		t.Errorf("空字符串应解密为空串，实际 %q", got)
	}
	// 无加密前缀的值按明文原样返回（兼容直接写在 .env 里的密钥）
	if got := DecryptSecret("sk-plain-text"); got != "sk-plain-text" {
		t.Errorf("明文应原样返回，实际 %q", got)
	}
}

func TestDecryptRejectsGarbage(t *testing.T) {
	t.Setenv("PLANNER_DATA_DIR", t.TempDir())

	for _, bad := range []string{"enc:not-base64!!!", "enc:", "enc:YWJj"} {
		if got := DecryptSecret(bad); got != "" {
			t.Errorf("损坏的密文 %q 应返回空串，实际 %q", bad, got)
		}
	}
}
