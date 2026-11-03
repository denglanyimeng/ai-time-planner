// Package ai 封装大模型调用。
//
// 核心设计约束（对原需求文档的关键校准）：
//
//	原文档要求「AI 一键对齐当前北京时间、刷新全部时间戳」。
//	但大模型做时间算术并不可靠，而且这件事根本不需要模型来做 ——
//	它是纯确定性计算，交给 planner 包即可。
//
//	因此本包把 AI 的职责严格限制为「产出结构化草稿」：
//	任务名、标签、预估时长、优先级、建议。
//	AI 的输出中不包含、也不允许包含任何时间点。
//	所有时间戳一律由服务端 planner.Schedule 计算。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client 是一个 OpenAI 兼容接口的极简客户端。
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// New 创建客户端。apiKey 为空表示未配置 AI，调用方应走本地降级路径。
func New(baseURL, apiKey, model string, timeoutSeconds int) *Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		http:    &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// Configured 返回是否配置了可用的 AI 接口。
func (c *Client) Configured() bool { return c != nil && c.apiKey != "" }

// DraftTask 是 AI 被允许输出的任务草稿。注意：没有任何时间字段。
type DraftTask struct {
	Name        string `json:"name"`
	Tag         string `json:"tag"`
	EstimateMin int    `json:"estimate_min"`
	Priority    int    `json:"priority"`
	Note        string `json:"note"`
}

// PlanDraft 是 AI 生成计划的完整输出。
type PlanDraft struct {
	Tasks  []DraftTask `json:"tasks"`
	Advice string      `json:"advice"`
}

// PlanRequest 是生成计划的输入。
type PlanRequest struct {
	PlanDate     string   // 业务日期
	UserInput    string   // 用户的自然语言需求
	Constraint   string   // 本次生成的临时约束（仅本次生效）
	AvailableMin int      // 当日可用总时长（由服务端算出，供 AI 参考负载）
	Tags         []string // 可用标签列表，AI 必须从中选择
	BusyNote     string   // 已有任务摘要，避免重复排
}

const planSystemPrompt = `你是一个严谨的时间规划助手。用户会给出今天要做的事情和约束条件。

你必须只输出一个 JSON 对象，不要输出任何解释文字，不要使用 Markdown 代码块。

JSON 结构：
{
  "tasks": [
    {"name": "任务名称", "tag": "标签名", "estimate_min": 30, "priority": 1, "note": "可选备注"}
  ],
  "advice": "针对今天规划的建议，1-3 句中文"
}

硬性要求：
1. 绝对不要输出任何时间点、开始时间、结束时间或时刻。时间由系统统一计算，你输出的时间一定是错的。
2. estimate_min 必须是整数分钟，取值 5 到 180 之间。
3. tag 必须从用户给出的标签列表中选择；确实无法归类时使用空字符串。
4. priority 为 1 到 3 的整数，1 表示最高优先级。
5. 任务数量控制在 3 到 12 条之间，粒度适中，不要把一件事拆得过碎。
6. 如果用户给出的任务总时长明显超出当天可用时长，请不要删减任务，但要在 advice 中明确指出负载过高并给出取舍建议。`

// GeneratePlan 调用大模型生成任务草稿。
// 返回的草稿保证已经过清洗：名称非空、时长已收敛到合法范围。
func (c *Client) GeneratePlan(ctx context.Context, req PlanRequest) (*PlanDraft, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("未配置 AI 接口")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "日期：%s\n", req.PlanDate)
	fmt.Fprintf(&b, "当日可用总时长：%d 分钟\n", req.AvailableMin)
	if len(req.Tags) > 0 {
		fmt.Fprintf(&b, "可用标签列表：%s\n", strings.Join(req.Tags, "、"))
	} else {
		b.WriteString("可用标签列表：（无，tag 请留空）\n")
	}
	if strings.TrimSpace(req.Constraint) != "" {
		fmt.Fprintf(&b, "本次规划约束（必须遵守）：%s\n", req.Constraint)
	}
	if strings.TrimSpace(req.BusyNote) != "" {
		fmt.Fprintf(&b, "当天已有任务（避免重复安排）：%s\n", req.BusyNote)
	}
	fmt.Fprintf(&b, "\n用户需求：\n%s\n", req.UserInput)

	content, err := c.chat(ctx, planSystemPrompt, b.String())
	if err != nil {
		return nil, err
	}

	raw, err := extractJSON(content)
	if err != nil {
		return nil, fmt.Errorf("模型响应不是合法 JSON: %w", err)
	}

	var draft PlanDraft
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		return nil, fmt.Errorf("解析计划 JSON 失败: %w", err)
	}
	draft.sanitize()
	if len(draft.Tasks) == 0 {
		return nil, fmt.Errorf("模型未生成任何有效任务")
	}
	return &draft, nil
}

// AnalysisRequest 是效率分析的输入。
type AnalysisRequest struct {
	PlanDate string
	Rows     []AnalysisRow
}

// AnalysisRow 是单条任务的预估/实际偏差数据。
type AnalysisRow struct {
	Name        string
	Tag         string
	EstimateMin int
	ActualMin   int
	Status      string
}

// Analysis 是 AI 的效率分析结果。
type Analysis struct {
	Summary     string   `json:"summary"`
	Suggestions []string `json:"suggestions"`
}

const analyzeSystemPrompt = `你是一个学习效率分析助手。用户会提供今天各任务的预估耗时与实际耗时。

你必须只输出一个 JSON 对象，不要输出任何解释文字，不要使用 Markdown 代码块。

JSON 结构：
{
  "summary": "今天效率表现的总体评价，2-3 句中文",
  "suggestions": ["具体可执行的改进建议", "另一条建议"]
}

要求：
1. suggestions 给 2 到 4 条，必须具体可执行，不要空泛口号。
2. 基于数据说话：指出哪些任务被明显低估或高估、哪类标签偏差最大。
3. 如果整体完成率低，优先建议减少任务数量而不是加快速度。`

// AnalyzeEfficiency 基于双维度耗时偏差数据生成效率分析。
func (c *Client) AnalyzeEfficiency(ctx context.Context, req AnalysisRequest) (*Analysis, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("未配置 AI 接口")
	}
	if len(req.Rows) == 0 {
		return nil, fmt.Errorf("没有可分析的数据")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "日期：%s\n\n任务数据（预估 vs 实际）：\n", req.PlanDate)
	for _, r := range req.Rows {
		fmt.Fprintf(&b, "- %s｜标签：%s｜预估 %d 分钟｜实际 %d 分钟｜状态：%s\n",
			r.Name, orNone(r.Tag), r.EstimateMin, r.ActualMin, r.Status)
	}

	content, err := c.chat(ctx, analyzeSystemPrompt, b.String())
	if err != nil {
		return nil, err
	}
	raw, err := extractJSON(content)
	if err != nil {
		return nil, fmt.Errorf("模型响应不是合法 JSON: %w", err)
	}
	var out Analysis
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("解析分析 JSON 失败: %w", err)
	}
	return &out, nil
}

// chat 发起一次 chat/completions 调用。
func (c *Client) chat(ctx context.Context, system, user string) (string, error) {
	payload := map[string]any{
		"model":       c.model,
		"temperature": 0.3,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("构造请求体失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("请求大模型失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("读取模型响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("大模型返回 %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("解析模型响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("模型返回了空的 choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// sanitize 清洗模型输出：丢弃无效任务、收敛时长与优先级。
// 模型偶尔会给出负数、超大值或空名称，必须在这里挡掉，
// 否则脏数据会一路流进数据库和排程引擎。
func (d *PlanDraft) sanitize() {
	const (
		minEstimate = 5
		maxEstimate = 480
		maxTasks    = 30
	)

	cleaned := make([]DraftTask, 0, len(d.Tasks))
	for _, task := range d.Tasks {
		task.Name = strings.TrimSpace(task.Name)
		if task.Name == "" {
			continue
		}
		if len([]rune(task.Name)) > 120 {
			task.Name = string([]rune(task.Name)[:120])
		}
		task.Tag = strings.TrimSpace(task.Tag)
		task.Note = strings.TrimSpace(task.Note)

		if task.EstimateMin < minEstimate {
			task.EstimateMin = minEstimate
		}
		if task.EstimateMin > maxEstimate {
			task.EstimateMin = maxEstimate
		}
		if task.Priority < 1 || task.Priority > 3 {
			task.Priority = 2
		}
		cleaned = append(cleaned, task)
		if len(cleaned) >= maxTasks {
			break
		}
	}
	d.Tasks = cleaned
	d.Advice = strings.TrimSpace(d.Advice)
}

// extractJSON 从模型响应里抠出第一个完整的 JSON 对象。
//
// 现实中的模型经常加 Markdown 围栏、前后附带解释文字，甚至返回被截断的 JSON。
// 直接 json.Unmarshal 整个响应会大面积失败，因此这里做花括号配对扫描，
// 并正确地跳过字符串字面量内部的括号（任务名里出现 { } 是常见情况）。
func extractJSON(s string) (string, error) {
	s = strings.TrimSpace(s)

	// 去掉 Markdown 代码块围栏
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}

	start := strings.Index(s, "{")
	if start < 0 {
		return "", fmt.Errorf("响应中找不到 JSON 对象")
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("JSON 对象不完整（可能被模型截断）")
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "无"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
