package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractJSONPlain(t *testing.T) {
	got, err := extractJSON(`{"tasks":[{"name":"看书"}]}`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("结果不是合法 JSON: %s", got)
	}
}

func TestExtractJSONWithMarkdownFence(t *testing.T) {
	in := "```json\n{\"tasks\":[],\"advice\":\"加油\"}\n```"
	got, err := extractJSON(in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("结果不是合法 JSON: %s", got)
	}
}

func TestExtractJSONWithSurroundingProse(t *testing.T) {
	in := "好的，这是你的计划：\n{\"tasks\":[{\"name\":\"写代码\"}]}\n希望对你有帮助！"
	got, err := extractJSON(in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	var out PlanDraft
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if len(out.Tasks) != 1 || out.Tasks[0].Name != "写代码" {
		t.Errorf("解析结果不符：%+v", out)
	}
}

// 任务名里出现花括号是真实场景（例如写代码、做数学题），
// 花括号配对扫描必须跳过字符串字面量内部。
func TestExtractJSONIgnoresBracesInsideStrings(t *testing.T) {
	in := `{"tasks":[{"name":"实现 {a:1} 的解析","note":"注意 } 符号"}]}`
	got, err := extractJSON(in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	var out PlanDraft
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("反序列化失败: %v (原始: %s)", err, got)
	}
	if len(out.Tasks) != 1 || out.Tasks[0].Name != "实现 {a:1} 的解析" {
		t.Errorf("解析结果不符：%+v", out)
	}
}

func TestExtractJSONHandlesEscapedQuotes(t *testing.T) {
	in := `{"tasks":[{"name":"读《Go 语言》的 \"接口\" 章节"}]}`
	got, err := extractJSON(in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("结果不是合法 JSON: %s", got)
	}
}

func TestExtractJSONRejectsIncomplete(t *testing.T) {
	if _, err := extractJSON(`{"tasks":[{"name":"被截断`); err == nil {
		t.Error("被截断的 JSON 应当报错，而不是静默通过")
	}
	if _, err := extractJSON("完全没有 JSON"); err == nil {
		t.Error("不含 JSON 的响应应当报错")
	}
}

func TestSanitizeClampsAndDropsInvalid(t *testing.T) {
	d := PlanDraft{
		Tasks: []DraftTask{
			{Name: "  正常任务  ", EstimateMin: 30, Priority: 1},
			{Name: "   ", EstimateMin: 30},                  // 空名称应被丢弃
			{Name: "负数时长", EstimateMin: -10, Priority: 9},   // 时长与优先级应被收敛
			{Name: "超大时长", EstimateMin: 99999, Priority: 0}, // 应被收敛
		},
		Advice: "  建议内容  ",
	}
	d.sanitize()

	if len(d.Tasks) != 3 {
		t.Fatalf("应保留 3 条有效任务，实际 %d", len(d.Tasks))
	}
	if d.Tasks[0].Name != "正常任务" {
		t.Errorf("名称应被 trim，实际 %q", d.Tasks[0].Name)
	}
	if d.Tasks[1].EstimateMin != 5 {
		t.Errorf("负数时长应收敛到 5，实际 %d", d.Tasks[1].EstimateMin)
	}
	if d.Tasks[1].Priority != 2 {
		t.Errorf("越界优先级应重置为 2，实际 %d", d.Tasks[1].Priority)
	}
	if d.Tasks[2].EstimateMin != 480 {
		t.Errorf("超大时长应收敛到 480，实际 %d", d.Tasks[2].EstimateMin)
	}
	if d.Advice != "建议内容" {
		t.Errorf("建议应被 trim，实际 %q", d.Advice)
	}
}

func TestSanitizeCapsTaskCount(t *testing.T) {
	d := PlanDraft{}
	for i := 0; i < 100; i++ {
		d.Tasks = append(d.Tasks, DraftTask{Name: "任务", EstimateMin: 30})
	}
	d.sanitize()
	if len(d.Tasks) > 30 {
		t.Errorf("任务数量应被限制，实际 %d", len(d.Tasks))
	}
}

func TestSanitizeTruncatesLongName(t *testing.T) {
	long := strings.Repeat("任", 300)
	d := PlanDraft{Tasks: []DraftTask{{Name: long, EstimateMin: 30}}}
	d.sanitize()
	if got := len([]rune(d.Tasks[0].Name)); got != 120 {
		t.Errorf("超长名称应截断到 120 字符，实际 %d", got)
	}
}

func TestClientNotConfigured(t *testing.T) {
	c := New("https://example.com", "", "m", 10)
	if c.Configured() {
		t.Error("空 API Key 应视为未配置")
	}
	if _, err := c.GeneratePlan(nil, PlanRequest{}); err == nil {
		t.Error("未配置时应返回明确错误，而不是静默失败")
	}
}
