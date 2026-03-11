package pipeline

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdklexer "stream_json_partial_parser/sdk/lexer"
)

const (
	demoXLSXPath         = "assets/chat_history_1000.xlsx"
	demoSchemaPath       = "assets/json_schema"
	demoSystemPromptPath = "assets/prompt_system.txt"
	demoUserPromptPath   = "assets/prompt_user_reminder.txt"
	demoEnvPath          = ".env"
	demoRequestID        = "1"
	demoTemp             = 0.2
	demoMaxTokens        = 2000
	demoTopP             = 1.0
)

// RunOptions 定义一次总结流水线运行时可选参数。
type RunOptions struct {
	SummaryLang   string
	ModelOverride string
	RequestID     string
}

// Callbacks 定义流水线事件回调。
type Callbacks struct {
	OnRawChunk    func(string)
	OnParsedEvent func(StreamEvent)
	OnLog         func(string)
}

// StreamEvent 复用 lexer SDK 的事件定义，避免在 pipeline 层重复声明结构。
type StreamEvent = sdklexer.StreamEvent

// Run 执行完整的“读取输入 -> 调模型流式 -> 解析事件”流水线。
func Run(ctx context.Context, opts RunOptions, cb Callbacks) error {
	if err := loadDotEnv(demoEnvPath); err != nil {
		return fmt.Errorf("读取 .env 失败: %w", err)
	}

	summaryLang := normalizeSummaryLang(firstNonEmpty(opts.SummaryLang, getenvAny("summary_lang", "SUMMARY_LANG")))
	fileSuffix := summaryFileSuffix(summaryLang)
	xlsxPath := appendSuffixBeforeExt(demoXLSXPath, fileSuffix)
	systemPromptPath := appendSuffixBeforeExt(demoSystemPromptPath, fileSuffix)
	userPromptPath := appendSuffixBeforeExt(demoUserPromptPath, fileSuffix)

	userContent, rowCount, err := buildMessageFromXLSX(xlsxPath)
	if err != nil {
		return fmt.Errorf("解析 xlsx 失败: %w", err)
	}
	emitLog(cb, fmt.Sprintf("已从 %s 读取 %d 条聊天记录", xlsxPath, rowCount))

	apiBase := getenvAny("api_base", "API_BASE")
	if strings.TrimSpace(apiBase) == "" {
		return fmt.Errorf("请在 .env 中设置 api_base")
	}

	apiKey := getenvAny("api_key", "API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("请在 .env 中设置 api_key")
	}

	model := firstNonEmpty(strings.TrimSpace(opts.ModelOverride), getenvAny("model", "MODEL"))
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("请在 .env 中设置 model")
	}

	baseURL := normalizeURL(apiBase)
	endpoint := baseURL + "/chat/completions"

	systemPrompt, err := loadPromptFile(systemPromptPath, "system prompt")
	if err != nil {
		return err
	}
	userPrompt, err := loadPromptFile(userPromptPath, "user prompt")
	if err != nil {
		return err
	}
	userMessage := composeUserMessage(userContent, userPrompt)

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
		"temperature": demoTemp,
		"max_tokens":  demoMaxTokens,
		"stream":      true,
	}
	if demoTopP < 1.0 {
		payload["top_p"] = demoTopP
	}

	responseFormat, err := loadResponseFormat(demoSchemaPath)
	if err != nil {
		return err
	}
	payload["response_format"] = responseFormat

	requestID := firstNonEmpty(strings.TrimSpace(opts.RequestID), demoRequestID)
	return streamCall(ctx, endpoint, apiKey, payload, requestID, cb)
}

func streamCall(ctx context.Context, endpoint, apiKey string, payload map[string]any, requestID string, cb Callbacks) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	reader := bufio.NewReader(resp.Body)
	parser := sdklexer.NewJSONPathStreamParser(requestID)
	parsedChunkNum := 0

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("读取流失败: %w", err)
		}

		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		content, _ := delta["content"].(string)
		if content == "" {
			continue
		}

		emitRaw(cb, content)
		parsedChunkNum++
		events := parser.ProcessChunk([]byte(content), parsedChunkNum)
		for _, evt := range events {
			emitParsed(cb, evt)
		}
	}

	for _, evt := range parser.Flush(parsedChunkNum) {
		emitParsed(cb, evt)
	}
	return nil
}

func emitRaw(cb Callbacks, chunk string) {
	if cb.OnRawChunk != nil {
		cb.OnRawChunk(chunk)
	}
}

func emitParsed(cb Callbacks, evt StreamEvent) {
	if cb.OnParsedEvent != nil {
		cb.OnParsedEvent(evt)
	}
}

func emitLog(cb Callbacks, line string) {
	if cb.OnLog != nil {
		cb.OnLog(line)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key == "" {
			continue
		}
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return nil
}

func getenvAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func normalizeSummaryLang(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "en") {
		return "en"
	}
	return "zh"
}

func summaryFileSuffix(summaryLang string) string {
	if summaryLang == "en" {
		return "_en"
	}
	return ""
}

func appendSuffixBeforeExt(path, suffix string) string {
	if suffix == "" {
		return path
	}
	idx := strings.LastIndex(path, ".")
	if idx <= 0 {
		return path + suffix
	}
	return path[:idx] + suffix + path[idx:]
}

func loadPromptFile(path, promptName string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 文件失败: %w", promptName, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("%s 文件为空: %s", promptName, path)
	}
	return content, nil
}

func composeUserMessage(chatLog, userPrompt string) string {
	chatLog = strings.TrimSpace(chatLog)
	userPrompt = strings.TrimSpace(userPrompt)
	if chatLog == "" {
		return userPrompt
	}
	return chatLog + "\n\n" + userPrompt
}

func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, _ := url.Parse(raw)
	if parsed.Path == "" {
		parsed.Path = "/v1"
	}
	return strings.TrimRight(parsed.String(), "/")
}

func loadResponseFormat(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 JSON Schema 文件失败: %w", err)
	}

	var responseFormat map[string]any
	if err := json.Unmarshal(data, &responseFormat); err != nil {
		return nil, fmt.Errorf("JSON Schema 文件不是合法 JSON: %w", err)
	}
	respType, ok := responseFormat["type"].(string)
	if !ok || respType != "json_schema" {
		return nil, fmt.Errorf("JSON Schema 顶层字段 type 必须为 json_schema")
	}
	return responseFormat, nil
}

type xlsxSharedStrings struct {
	Items []xlsxSharedStringItem `xml:"si"`
}

type xlsxSharedStringItem struct {
	Text string        `xml:"t"`
	Runs []xlsxTextRun `xml:"r"`
}

type xlsxTextRun struct {
	Text string `xml:"t"`
}

type xlsxWorksheet struct {
	SheetData struct {
		Rows []xlsxRow `xml:"row"`
	} `xml:"sheetData"`
}

type xlsxRow struct {
	Cells []xlsxCell `xml:"c"`
}

type xlsxCell struct {
	Ref     string         `xml:"r,attr"`
	Type    string         `xml:"t,attr"`
	Value   string         `xml:"v"`
	Inline  xlsxInlineText `xml:"is"`
	Formula string         `xml:"f"`
}

type xlsxInlineText struct {
	Text string        `xml:"t"`
	Runs []xlsxTextRun `xml:"r"`
}

type chatRow struct {
	MsgID   string `json:"msg_id"`
	Time    string `json:"time"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func buildMessageFromXLSX(path string) (string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("读取文件失败: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, fmt.Errorf("不是合法 xlsx 文件: %w", err)
	}

	sharedStrings, err := readSharedStrings(zr)
	if err != nil {
		return "", 0, err
	}

	sheetPath := firstWorksheetPath(zr)
	if sheetPath == "" {
		return "", 0, fmt.Errorf("xlsx 中未找到工作表")
	}

	sheetData, err := readZipEntry(zr, sheetPath)
	if err != nil {
		return "", 0, err
	}

	var ws xlsxWorksheet
	if err := xml.Unmarshal(sheetData, &ws); err != nil {
		return "", 0, fmt.Errorf("解析工作表失败: %w", err)
	}

	rows := make([]chatRow, 0, len(ws.SheetData.Rows))
	for _, row := range ws.SheetData.Rows {
		cols := [4]string{}
		nextCol := 1
		for _, cell := range row.Cells {
			col := nextCol
			if cell.Ref != "" {
				if idx := columnIndexFromCellRef(cell.Ref); idx > 0 {
					col = idx
				}
			}
			nextCol = col + 1
			if col < 1 || col > 4 {
				continue
			}
			cols[col-1] = decodeCellText(cell, sharedStrings, col)
		}

		if isAllEmpty(cols[:]) {
			continue
		}
		if len(rows) == 0 && isHeaderRow(cols[:]) {
			continue
		}

		rows = append(rows, chatRow{MsgID: cols[0], Time: cols[1], Name: cols[2], Content: cols[3]})
	}

	if len(rows) == 0 {
		return "", 0, fmt.Errorf("未读取到有效聊天数据，请确认前四列为 msg_id/time/name/content")
	}

	var b strings.Builder
	for i, row := range rows {
		line, _ := json.Marshal(row)
		b.Write(line)
		if i < len(rows)-1 {
			b.WriteByte('\n')
		}
	}

	return b.String(), len(rows), nil
}

func readSharedStrings(zr *zip.Reader) ([]string, error) {
	data, err := readZipEntry(zr, "xl/sharedStrings.xml")
	if err != nil {
		if strings.Contains(err.Error(), "不存在 xl/sharedStrings.xml") {
			return nil, nil
		}
		return nil, err
	}

	var sst xlsxSharedStrings
	if err := xml.Unmarshal(data, &sst); err != nil {
		return nil, fmt.Errorf("解析 sharedStrings 失败: %w", err)
	}

	values := make([]string, 0, len(sst.Items))
	for _, item := range sst.Items {
		values = append(values, richText(item.Text, item.Runs))
	}
	return values, nil
}

func firstWorksheetPath(zr *zip.Reader) string {
	paths := make([]string, 0, 4)
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(f.Name, ".xml") {
			paths = append(paths, f.Name)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	return paths[0]
}

func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("打开 %s 失败: %w", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("xlsx 内不存在 %s", name)
}

func columnIndexFromCellRef(ref string) int {
	col := 0
	for i := 0; i < len(ref); i++ {
		ch := ref[i]
		if ch >= 'A' && ch <= 'Z' {
			col = col*26 + int(ch-'A'+1)
			continue
		}
		if ch >= 'a' && ch <= 'z' {
			col = col*26 + int(ch-'a'+1)
			continue
		}
		break
	}
	return col
}

func decodeCellText(cell xlsxCell, sharedStrings []string, col int) string {
	switch cell.Type {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(cell.Value))
		if err == nil && idx >= 0 && idx < len(sharedStrings) {
			return sharedStrings[idx]
		}
		return strings.TrimSpace(cell.Value)
	case "inlineStr":
		return richText(cell.Inline.Text, cell.Inline.Runs)
	case "b":
		if strings.TrimSpace(cell.Value) == "1" {
			return "TRUE"
		}
		return "FALSE"
	default:
		raw := cell.Value
		if raw == "" {
			raw = strings.TrimSpace(cell.Formula)
		}
		if col == 2 {
			if converted, ok := excelSerialToTime(raw); ok {
				return converted
			}
		}
		return raw
	}
}

func richText(text string, runs []xlsxTextRun) string {
	if text != "" {
		return text
	}
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, run := range runs {
		b.WriteString(run.Text)
	}
	return b.String()
}

func excelSerialToTime(raw string) (string, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 {
		return "", false
	}
	day := int(v)
	frac := v - float64(day)
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	t := base.AddDate(0, 0, day).Add(time.Duration(frac * float64(24*time.Hour)))
	return t.Format("01-02 15:04"), true
}

func isAllEmpty(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

func isHeaderRow(cols []string) bool {
	if len(cols) < 4 {
		return false
	}
	c0 := normalizeHeaderCell(cols[0])
	c1 := normalizeHeaderCell(cols[1])
	c2 := normalizeHeaderCell(cols[2])
	c3 := normalizeHeaderCell(cols[3])
	return (strings.Contains(c0, "msgid") || strings.Contains(c0, "msg_id")) &&
		(strings.Contains(c1, "time") || strings.Contains(c1, "时间")) &&
		(strings.Contains(c2, "name") || strings.Contains(c2, "发言者")) &&
		(strings.Contains(c3, "content") || strings.Contains(c3, "内容"))
}

func normalizeHeaderCell(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
