package service

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"gorm.io/gorm"

	"ai-time-planner/internal/model"
	"ai-time-planner/internal/planner"
)

// backupVersion 是备份文件格式版本。还原时会校验，避免把不兼容的旧备份灌进来。
const backupVersion = 1

// BackupFile 是完整的备份内容。
//
// 为什么真正的还原不用 CSV：CSV 是无类型文本格式，
// 恢复时无法可靠区分空字符串与 NULL、无法表达布尔与外键关系，
// 而且 Excel 打开再保存会擅自改写日期格式和长数字，导致数据悄悄损坏。
// 因此备份包内同时提供两种文件：
//   - *.csv      ：给人看、给 Excel 做分析用
//   - backup.json：给程序做无损还原用
type BackupFile struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exportedAt"`
	App        string    `json:"app"`

	Tags          []model.Tag           `json:"tags"`
	Tasks         []model.Task          `json:"tasks"`
	Recurring     []model.RecurringTask `json:"recurringTasks"`
	Goals         []model.LongTermGoal  `json:"longTermGoals"`
	DailyConfigs  []model.DailyConfig   `json:"dailyConfigs"`
	DailyTagStats []model.DailyTagStat  `json:"dailyTagStats"`
	TimeBlocks    []model.TimeBlock     `json:"timeBlocks"`
	SystemConfig  []model.SystemConfig  `json:"systemConfig"`
}

// TaskCSVRow 是导出给用户看的扁平任务行，列名直接用中文。
type TaskCSVRow struct {
	PlanDate     string  `json:"日期"`
	OrderIndex   int     `json:"顺序"`
	Name         string  `json:"任务名称"`
	TagName      string  `json:"标签"`
	AIEstimate   int     `json:"AI预估分钟"`
	UserEstimate string  `json:"用户预估分钟"`
	UsedEstimate int     `json:"实际采用预估分钟"`
	ActualMin    float64 `json:"实际耗时分钟"`
	DeviationMin float64 `json:"偏差分钟"`
	Status       string  `json:"状态"`
	Source       string  `json:"来源"`
	PlannedStart string  `json:"计划开始"`
	PlannedEnd   string  `json:"计划结束"`
	IsOvertime   bool    `json:"超时未确认"`
}

// ExportTasksCSV 导出指定日期区间（闭区间）的任务明细为 CSV。
//
// 会写入 UTF-8 BOM：中文 Windows 上的 Excel 默认按 GBK 解析 CSV，
// 不加 BOM 打开就是乱码 —— 这是中文用户最容易踩的坑。
func (s *Service) ExportTasksCSV(w io.Writer, from, to string) error {
	if _, err := parseDate(from); err != nil {
		return err
	}
	if _, err := parseDate(to); err != nil {
		return err
	}

	var tasks []model.Task
	if err := s.db.Where("plan_date >= ? AND plan_date <= ?", from, to).
		Order("plan_date, order_index, id").Find(&tasks).Error; err != nil {
		return err
	}

	var tags []model.Tag
	if err := s.db.Find(&tags).Error; err != nil {
		return err
	}
	tagOf := make(map[int64]string, len(tags))
	for _, t := range tags {
		tagOf[t.ID] = t.Name
	}

	now := Now()
	rows := make([]TaskCSVRow, 0, len(tasks))
	for _, t := range tasks {
		used := t.EffectiveEstimateMin(15)
		actual := float64(ElapsedSeconds(&t, now)) / 60
		row := TaskCSVRow{
			PlanDate:     t.PlanDate,
			OrderIndex:   t.OrderIndex,
			Name:         t.Name,
			AIEstimate:   t.AIEstimateMin,
			UsedEstimate: used,
			ActualMin:    round1(actual),
			DeviationMin: round1(actual - float64(used)),
			Status:       t.Status,
			Source:       t.Source,
			IsOvertime:   t.IsOvertime,
		}
		if t.TagID != nil {
			row.TagName = tagOf[*t.TagID]
		}
		if t.UserEstimateMin != nil {
			row.UserEstimate = strconv.Itoa(*t.UserEstimateMin)
		}
		if t.PlannedStart != nil {
			row.PlannedStart = bjString(*t.PlannedStart)
		}
		if t.PlannedEnd != nil {
			row.PlannedEnd = bjString(*t.PlannedEnd)
		}
		rows = append(rows, row)
	}

	if _, err := io.WriteString(w, "\uFEFF"); err != nil { // UTF-8 BOM
		return err
	}
	return writeCSV(w, rows)
}

// ExportBackup 打包完整备份（ZIP：各表 CSV + 无损 backup.json）。
func (s *Service) ExportBackup(w io.Writer) error {
	data, err := s.buildBackup()
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)

	// 1) 无损还原文件
	if err := writeZipEntry(zw, "backup.json", func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}); err != nil {
		return err
	}

	// 2) 人类可读的 CSV（每表一个文件）
	csvTables := []struct {
		name string
		rows any
	}{
		{"tags.csv", data.Tags},
		{"tasks.csv", data.Tasks},
		{"recurring_tasks.csv", data.Recurring},
		{"long_term_goals.csv", data.Goals},
		{"daily_configs.csv", data.DailyConfigs},
		{"daily_tag_stats.csv", data.DailyTagStats},
		{"time_blocks.csv", data.TimeBlocks},
	}
	for _, tbl := range csvTables {
		if err := writeZipEntry(zw, tbl.name, func(w io.Writer) error {
			if _, err := io.WriteString(w, "\uFEFF"); err != nil {
				return err
			}
			return writeCSV(w, tbl.rows)
		}); err != nil {
			return err
		}
	}

	// 3) 说明文件，避免用户拿到压缩包后不知道是什么
	if err := writeZipEntry(zw, "说明.txt", func(w io.Writer) error {
		_, err := fmt.Fprintf(w, `AI 智能时间规划效率助手 —— 数据备份包
导出时间：%s

文件说明：
  backup.json        完整的无损备份，用于「导入恢复」，请勿手动编辑
  *.csv              各数据表的表格版本，可用 Excel 打开查看或做分析
  说明.txt           本文件

恢复方法：
  在系统的「数据管理」页面选择本压缩包上传即可完整恢复。

注意：
  CSV 文件仅用于查看与分析，不要用它做恢复 —— 
  Excel 打开并保存会改写日期格式与长数字，导致数据损坏。
`, bjString(data.ExportedAt))
		return err
	}); err != nil {
		return err
	}

	return zw.Close()
}

// buildBackup 从数据库读出全部数据。
func (s *Service) buildBackup() (*BackupFile, error) {
	out := &BackupFile{
		Version:    backupVersion,
		ExportedAt: Now(),
		App:        "ai-time-planner",
	}

	type target struct {
		dest  any
		model any
	}
	targets := []target{
		{&out.Tags, &model.Tag{}},
		{&out.Tasks, &model.Task{}},
		{&out.Recurring, &model.RecurringTask{}},
		{&out.Goals, &model.LongTermGoal{}},
		{&out.DailyConfigs, &model.DailyConfig{}},
		{&out.DailyTagStats, &model.DailyTagStat{}},
		{&out.TimeBlocks, &model.TimeBlock{}},
		{&out.SystemConfig, &model.SystemConfig{}},
	}
	for _, t := range targets {
		if err := s.db.Find(t.dest, t.model).Error; err != nil {
			return nil, fmt.Errorf("读取备份数据失败: %w", err)
		}
	}
	return out, nil
}

// ImportResult 描述一次恢复的结果。
type ImportResult struct {
	Version      int            `json:"version"`
	ExportedAt   string         `json:"exportedAt"`
	RestoredRows map[string]int `json:"restoredRows"`
}

// ImportBackup 从备份 ZIP 恢复数据。
//
// 采用「清空后整体写入」而不是增量合并：这是备份还原的语义，
// 增量合并会让主键冲突和残留旧数据变成难以排查的问题。
func (s *Service) ImportBackup(r io.ReaderAt, size int64) (*ImportResult, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: 不是有效的 ZIP 备份包", ErrInvalid)
	}

	var raw []byte
	found := false
	for _, f := range zr.File {
		if f.Name != "backup.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("读取 backup.json 失败: %w", err)
		}
		raw, err = io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("读取 backup.json 失败: %w", err)
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("%w: 备份包中缺少 backup.json，无法安全恢复（CSV 不支持完整还原）", ErrInvalid)
	}

	var data BackupFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%w: backup.json 解析失败: %v", ErrInvalid, err)
	}
	if data.Version != backupVersion {
		return nil, fmt.Errorf("%w: 备份版本 %d 与当前支持的版本 %d 不一致",
			ErrInvalid, data.Version, backupVersion)
	}

	res := &ImportResult{
		Version:      data.Version,
		ExportedAt:   bjString(data.ExportedAt),
		RestoredRows: map[string]int{},
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 先清空（顺序考虑外键：先子表后主表）
		clearOrder := []any{
			&model.Task{}, &model.DailyTagStat{}, &model.DailyConfig{},
			&model.RecurringTask{}, &model.LongTermGoal{}, &model.Tag{},
			&model.TimeBlock{}, &model.SystemConfig{},
		}
		for _, m := range clearOrder {
			if err := tx.Where("1 = 1").Delete(m).Error; err != nil {
				return fmt.Errorf("清空旧数据失败: %w", err)
			}
		}

		insert := func(rows any, key string) error {
			if err := insertInBatches(tx, rows); err != nil {
				return err
			}
			res.RestoredRows[key] = sliceLen(rows)
			return nil
		}
		if err := insert(data.Tags, "tags"); err != nil {
			return err
		}
		if err := insert(data.Tasks, "tasks"); err != nil {
			return err
		}
		if err := insert(data.Recurring, "recurringTasks"); err != nil {
			return err
		}
		if err := insert(data.Goals, "longTermGoals"); err != nil {
			return err
		}
		if err := insert(data.DailyConfigs, "dailyConfigs"); err != nil {
			return err
		}
		if err := insert(data.DailyTagStats, "dailyTagStats"); err != nil {
			return err
		}
		if err := insert(data.TimeBlocks, "timeBlocks"); err != nil {
			return err
		}
		if err := insert(data.SystemConfig, "systemConfig"); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// 备份里可能没有系统配置，补一个默认的，避免后续读取失败
	var n int64
	if err := s.db.Model(&model.SystemConfig{}).Count(&n).Error; err == nil && n == 0 {
		_ = s.db.Create(&model.SystemConfig{
			ID: 1, AIBaseURL: "https://api.deepseek.com", AIModel: "deepseek-chat",
			DefaultDeadlineMin: 23 * 60, RetentionDays: 21,
		}).Error
	}
	return res, nil
}

// insertInBatches 分批写入，避免单条 SQL 过大。
func insertInBatches(tx *gorm.DB, rows any) error {
	if sliceLen(rows) == 0 {
		return nil
	}
	return tx.CreateInBatches(rows, 200).Error
}

// sliceLen 返回任意切片值的长度，非切片返回 0。
func sliceLen(v any) int {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return 0
	}
	return rv.Len()
}

// ClearAllData 清空全部业务数据（管理中心批量删除）。
// 保留系统配置，否则用户会连 AI 配置一起丢掉。
func (s *Service) ClearAllData() (map[string]int64, error) {
	out := map[string]int64{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		targets := []struct {
			key   string
			model any
		}{
			{"tasks", &model.Task{}},
			{"dailyTagStats", &model.DailyTagStat{}},
			{"dailyConfigs", &model.DailyConfig{}},
			{"recurringTasks", &model.RecurringTask{}},
			{"longTermGoals", &model.LongTermGoal{}},
			{"timeBlocks", &model.TimeBlock{}},
			{"tags", &model.Tag{}},
		}
		for _, t := range targets {
			res := tx.Where("1 = 1").Delete(t.model)
			if res.Error != nil {
				return res.Error
			}
			out[t.key] = res.RowsAffected
		}
		return nil
	})
	return out, err
}

// ---------- CSV 编码辅助 ----------

// writeCSV 用反射把结构体切片写成 CSV。
// 表头取自 json 标签，因此列名与 API 字段名保持一致，便于对照。
func writeCSV(w io.Writer, rows any) error {
	rv := reflect.ValueOf(rows)
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("writeCSV 需要切片，收到 %T", rows)
	}
	cw := csv.NewWriter(w)

	elemType := rv.Type().Elem()
	headers := jsonFieldNames(elemType)
	if err := cw.Write(headers); err != nil {
		return err
	}

	for i := 0; i < rv.Len(); i++ {
		if err := cw.Write(structCells(rv.Index(i))); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeZipEntry(zw *zip.Writer, name string, fn func(io.Writer) error) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	return fn(f)
}

// jsonFieldNames 返回结构体的 json 字段名列表。
func jsonFieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // 非导出字段
		}
		name := jsonName(f)
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return ""
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}

// structCells 把结构体一行转成字符串切片。
func structCells(v reflect.Value) []string {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if jsonName(f) == "-" {
			continue
		}
		out = append(out, formatCell(v.Field(i)))
	}
	return out
}

func formatCell(v reflect.Value) string {
	if !v.IsValid() {
		return ""
	}
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		return formatCell(v.Elem())
	}
	switch x := v.Interface().(type) {
	case string:
		return x
	case bool:
		if x {
			return "是"
		}
		return "否"
	case int:
		return strconv.Itoa(x)
	case int8:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case time.Time:
		if x.IsZero() {
			return ""
		}
		return bjString(x)
	default:
		return fmt.Sprint(x)
	}
}

// bjString 把 UTC 时间格式化为北京时间字符串。
func bjString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(planner.Beijing()).Format("2006-01-02 15:04:05")
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
