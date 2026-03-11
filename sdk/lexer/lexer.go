package lexer

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"unicode/utf8"
)

// stringMode 表示“当前字符串 token 在语法中的角色”：
// - key：对象 key（例如 {"title": ...} 里的 title）
// - value：对象/数组中的字符串值
type stringMode int

const (
	stringModeNone stringMode = iota
	stringModeKey
	stringModeValue
)

type containerKind int

const (
	containerObject containerKind = iota
	containerArray
)

type objectState int

const (
	objExpectKeyOrEnd objectState = iota
	objExpectColon
	objExpectValue
	objExpectCommaOrEnd
)

type arrayState int

const (
	arrExpectValueOrEnd arrayState = iota
	arrExpectCommaOrEnd
)

// parseCtx 表示当前栈帧（一个对象或数组容器）的解析上下文。
type parseCtx struct {
	kind       containerKind
	prefix     string
	objState   objectState
	arrState   arrayState
	pendingKey string
	nextIndex  int
}

// StreamEvent 是解析器的输出事件。
type StreamEvent struct {
	ID       string `json:"id"`
	ChunkNum int    `json:"chunk_num"`
	Type     string `json:"type"`
	Content  string `json:"content"`
}

// JSONPathStreamParser 以“字符流”方式解析 JSON，并按路径输出增量事件。
// 使用方式：
// - 每个请求/每条流创建一个新实例；
// - 同一实例按 chunk 顺序调用 ProcessChunk，结束后调用 Flush。
type JSONPathStreamParser struct {
	id string

	mu         sync.Mutex
	processing bool
	pendingOps []parserOp

	stack []parseCtx

	inString      bool
	mode          stringMode
	stringEscaped bool

	keyToken []byte

	currentValueType string
	chunkValueBytes  []byte

	utf8Tails map[string][]byte
}

type parserOpKind int

const (
	opProcessChunk parserOpKind = iota
	opFlush
)

type parserOp struct {
	kind     parserOpKind
	chunk    []byte
	chunkNum int
	done     chan []StreamEvent
}

var (
	// 支持两套 key：
	// - 无前缀：title/topics/name
	// - 带前缀：01_title/04_topics/01_name
	reTopicName      = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:name|01_name)$`)
	reTopicTimeRange = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:time_range|02_time_range)$`)
	reTopicPlayerObj = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:players|03_players)\[(\d+)\]\.(?:name|01_name)$`)
	reTopicPlayerRaw = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:players|03_players)\[(\d+)\]$`)
	reTopicDesc      = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:description|04_description)$`)
	reTopicImageID   = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:images|05_images)\[(\d+)\]\.(?:id|01_id)$`)
	reTopicImageRaw  = regexp.MustCompile(`^(?:topics|04_topics)\[(\d+)\]\.(?:images|05_images)\[(\d+)\]$`)
	reTypeTopicTR    = regexp.MustCompile(`^topic\[\d+\]\.time_range$`)
	reTypeTopicImgID = regexp.MustCompile(`^topic\[\d+\]\.images\[\d+\]\.id$`)
)

// NewJSONPathStreamParser 创建解析器实例。
func NewJSONPathStreamParser(id string) *JSONPathStreamParser {
	return &JSONPathStreamParser{
		id:         id,
		utf8Tails:  make(map[string][]byte),
		pendingOps: make([]parserOp, 0, 16),
	}
}

// ProcessChunk 消费一个 chunk，返回这个 chunk 产生的增量事件。
// 该方法是并发安全的：并发调用会先进入内部缓冲队列，再由单线程按入队顺序串行解析。
func (p *JSONPathStreamParser) ProcessChunk(chunk []byte, chunkNum int) []StreamEvent {
	op := parserOp{
		kind:     opProcessChunk,
		chunk:    append([]byte(nil), chunk...),
		chunkNum: chunkNum,
		done:     make(chan []StreamEvent, 1),
	}
	p.enqueueOp(op)
	return <-op.done
}

// Flush 在整条流结束时调用，补发 UTF-8 尾字节残留。
// Flush 也会进入同一缓冲队列，因此与 ProcessChunk 保持严格时序。
func (p *JSONPathStreamParser) Flush(chunkNum int) []StreamEvent {
	op := parserOp{
		kind:     opFlush,
		chunkNum: chunkNum,
		done:     make(chan []StreamEvent, 1),
	}
	p.enqueueOp(op)
	return <-op.done
}

func (p *JSONPathStreamParser) enqueueOp(op parserOp) {
	leader := false
	p.mu.Lock()
	p.pendingOps = append(p.pendingOps, op)
	if !p.processing {
		p.processing = true
		leader = true
	}
	p.mu.Unlock()

	if leader {
		p.drainPendingOps()
	}
}

func (p *JSONPathStreamParser) drainPendingOps() {
	for {
		p.mu.Lock()
		if len(p.pendingOps) == 0 {
			p.processing = false
			p.mu.Unlock()
			return
		}
		op := p.pendingOps[0]
		p.pendingOps = p.pendingOps[1:]
		p.mu.Unlock()

		var events []StreamEvent
		switch op.kind {
		case opProcessChunk:
			events = p.processChunkInternal(op.chunk, op.chunkNum)
		case opFlush:
			events = p.flushInternal(op.chunkNum)
		default:
			events = nil
		}
		op.done <- events
		close(op.done)
	}
}

func (p *JSONPathStreamParser) processChunkInternal(chunk []byte, chunkNum int) []StreamEvent {
	events := make([]StreamEvent, 0, 8)

	for _, ch := range chunk {
		if p.inString {
			p.processStringByte(ch, chunkNum, &events)
			continue
		}

		if isSpace(ch) {
			continue
		}

		switch ch {
		case '{':
			p.startObject()
		case '[':
			p.startArray()
		case '}':
			p.endObject()
		case ']':
			p.endArray()
		case ':':
			p.onColon()
		case ',':
			p.onComma()
		case '"':
			p.startString()
		default:
			p.consumePrimitive()
		}
	}

	if p.inString && p.mode == stringModeValue {
		p.flushCurrentValueDelta(chunkNum, &events, false)
	}

	return events
}

func (p *JSONPathStreamParser) flushInternal(chunkNum int) []StreamEvent {
	if len(p.utf8Tails) == 0 {
		return nil
	}

	keys := make([]string, 0, len(p.utf8Tails))
	for k := range p.utf8Tails {
		if len(p.utf8Tails[k]) > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	events := make([]StreamEvent, 0, len(keys))
	for _, k := range keys {
		tail := p.utf8Tails[k]
		if len(tail) == 0 {
			continue
		}
		events = append(events, StreamEvent{
			ID:       p.id,
			ChunkNum: chunkNum,
			Type:     k,
			Content:  string(tail),
		})
		p.utf8Tails[k] = nil
	}
	return events
}

// processStringByte 处理字符串内单字节（转义、闭合、累积）。
func (p *JSONPathStreamParser) processStringByte(ch byte, chunkNum int, events *[]StreamEvent) {
	if p.stringEscaped {
		switch p.mode {
		case stringModeKey:
			p.keyToken = append(p.keyToken, ch)
		case stringModeValue:
			if p.currentValueType != "" {
				p.chunkValueBytes = append(p.chunkValueBytes, '\\', ch)
			}
		}
		p.stringEscaped = false
		return
	}

	if ch == '\\' {
		p.stringEscaped = true
		if p.mode == stringModeKey {
			p.keyToken = append(p.keyToken, '\\')
		}
		return
	}

	if ch == '"' {
		p.endStringToken(chunkNum, events)
		return
	}

	switch p.mode {
	case stringModeKey:
		p.keyToken = append(p.keyToken, ch)
	case stringModeValue:
		if p.currentValueType != "" {
			p.chunkValueBytes = append(p.chunkValueBytes, ch)
		}
	}
}

// startString 初始化新字符串 token 的解析状态。
func (p *JSONPathStreamParser) startString() {
	ctx := p.top()

	if ctx == nil {
		p.inString = true
		p.mode = stringModeValue
		p.currentValueType = ""
		p.chunkValueBytes = p.chunkValueBytes[:0]
		p.stringEscaped = false
		return
	}

	switch ctx.kind {
	case containerObject:
		if ctx.objState == objExpectKeyOrEnd {
			p.inString = true
			p.mode = stringModeKey
			p.keyToken = p.keyToken[:0]
			p.stringEscaped = false
			return
		}
		if ctx.objState == objExpectValue {
			path, ok := p.beginValue()
			if !ok {
				return
			}
			p.inString = true
			p.mode = stringModeValue
			p.currentValueType = pathToEventType(path)
			p.chunkValueBytes = p.chunkValueBytes[:0]
			p.stringEscaped = false
			return
		}
	case containerArray:
		if ctx.arrState == arrExpectValueOrEnd {
			path, ok := p.beginValue()
			if !ok {
				return
			}
			p.inString = true
			p.mode = stringModeValue
			p.currentValueType = pathToEventType(path)
			p.chunkValueBytes = p.chunkValueBytes[:0]
			p.stringEscaped = false
			return
		}
	}
}

// endStringToken 处理字符串 token 收尾。
func (p *JSONPathStreamParser) endStringToken(chunkNum int, events *[]StreamEvent) {
	switch p.mode {
	case stringModeKey:
		key := decodeJSONStringToken(p.keyToken)
		if ctx := p.top(); ctx != nil && ctx.kind == containerObject {
			ctx.pendingKey = key
			ctx.objState = objExpectColon
		}
	case stringModeValue:
		p.flushCurrentValueDelta(chunkNum, events, true)
		p.currentValueType = ""
		p.chunkValueBytes = p.chunkValueBytes[:0]
	}

	p.inString = false
	p.mode = stringModeNone
	p.stringEscaped = false
	p.keyToken = p.keyToken[:0]
}

// flushCurrentValueDelta 将当前 value 增量转成事件输出。
func (p *JSONPathStreamParser) flushCurrentValueDelta(chunkNum int, events *[]StreamEvent, forceOnClose bool) {
	if p.currentValueType == "" || len(p.chunkValueBytes) == 0 {
		return
	}

	if !forceOnClose && mustEmitAfterFieldClosed(p.currentValueType) {
		return
	}

	delta := p.takeValidUTF8(p.currentValueType, p.chunkValueBytes)
	p.chunkValueBytes = p.chunkValueBytes[:0]
	if delta == "" {
		return
	}

	*events = append(*events, StreamEvent{
		ID:       p.id,
		ChunkNum: chunkNum,
		Type:     p.currentValueType,
		Content:  delta,
	})
}

// takeValidUTF8 只返回完整 rune 前缀，不完整尾巴缓存到下次。
func (p *JSONPathStreamParser) takeValidUTF8(eventType string, newBytes []byte) string {
	if len(newBytes) == 0 && len(p.utf8Tails[eventType]) == 0 {
		return ""
	}

	buf := make([]byte, 0, len(p.utf8Tails[eventType])+len(newBytes))
	buf = append(buf, p.utf8Tails[eventType]...)
	buf = append(buf, newBytes...)

	validEnd := 0
	for i := 0; i < len(buf); {
		if !utf8.FullRune(buf[i:]) {
			break
		}
		_, size := utf8.DecodeRune(buf[i:])
		if size <= 0 {
			break
		}
		i += size
		validEnd = i
	}

	p.utf8Tails[eventType] = append(p.utf8Tails[eventType][:0], buf[validEnd:]...)
	if validEnd == 0 {
		return ""
	}
	return string(buf[:validEnd])
}

// startObject 处理 '{'：创建对象上下文并入栈。
func (p *JSONPathStreamParser) startObject() {
	path, ok := p.beginValue()
	if !ok && p.top() != nil {
		return
	}
	ctx := parseCtx{kind: containerObject, prefix: path, objState: objExpectKeyOrEnd}
	p.stack = append(p.stack, ctx)
}

// endObject 处理 '}'：对象上下文出栈。
func (p *JSONPathStreamParser) endObject() {
	if len(p.stack) == 0 {
		return
	}
	if p.stack[len(p.stack)-1].kind != containerObject {
		return
	}
	p.stack = p.stack[:len(p.stack)-1]
}

// startArray 处理 '['：创建数组上下文并入栈。
func (p *JSONPathStreamParser) startArray() {
	path, ok := p.beginValue()
	if !ok && p.top() != nil {
		return
	}
	ctx := parseCtx{kind: containerArray, prefix: path, arrState: arrExpectValueOrEnd}
	p.stack = append(p.stack, ctx)
}

// endArray 处理 ']'：数组上下文出栈。
func (p *JSONPathStreamParser) endArray() {
	if len(p.stack) == 0 {
		return
	}
	if p.stack[len(p.stack)-1].kind != containerArray {
		return
	}
	p.stack = p.stack[:len(p.stack)-1]
}

// onColon 处理 ':'：对象从 expectColon 切到 expectValue。
func (p *JSONPathStreamParser) onColon() {
	ctx := p.top()
	if ctx == nil || ctx.kind != containerObject {
		return
	}
	if ctx.objState == objExpectColon {
		ctx.objState = objExpectValue
	}
}

// onComma 处理 ','：对象/数组切回“期待下一个元素”。
func (p *JSONPathStreamParser) onComma() {
	ctx := p.top()
	if ctx == nil {
		return
	}
	if ctx.kind == containerObject && ctx.objState == objExpectCommaOrEnd {
		ctx.objState = objExpectKeyOrEnd
		return
	}
	if ctx.kind == containerArray && ctx.arrState == arrExpectCommaOrEnd {
		ctx.arrState = arrExpectValueOrEnd
	}
}

// consumePrimitive 消费非字符串 primitive，仅推进状态，不产出事件。
func (p *JSONPathStreamParser) consumePrimitive() {
	ctx := p.top()
	if ctx == nil {
		return
	}

	switch ctx.kind {
	case containerObject:
		if ctx.objState == objExpectValue {
			ctx.pendingKey = ""
			ctx.objState = objExpectCommaOrEnd
		}
	case containerArray:
		if ctx.arrState == arrExpectValueOrEnd {
			ctx.nextIndex++
			ctx.arrState = arrExpectCommaOrEnd
		}
	}
}

// beginValue 校验当前位置可否开始读 value，并返回该 value 的路径。
func (p *JSONPathStreamParser) beginValue() (string, bool) {
	ctx := p.top()
	if ctx == nil {
		return "", true
	}

	if ctx.kind == containerObject {
		if ctx.objState != objExpectValue {
			return "", false
		}
		path := joinPath(ctx.prefix, ctx.pendingKey)
		ctx.pendingKey = ""
		ctx.objState = objExpectCommaOrEnd
		return path, true
	}

	if ctx.kind == containerArray {
		if ctx.arrState != arrExpectValueOrEnd {
			return "", false
		}
		idx := ctx.nextIndex
		ctx.nextIndex++
		ctx.arrState = arrExpectCommaOrEnd
		if ctx.prefix == "" {
			return fmt.Sprintf("[%d]", idx), true
		}
		return fmt.Sprintf("%s[%d]", ctx.prefix, idx), true
	}

	return "", false
}

// top 返回当前解析栈顶上下文。
func (p *JSONPathStreamParser) top() *parseCtx {
	if len(p.stack) == 0 {
		return nil
	}
	return &p.stack[len(p.stack)-1]
}

// mustEmitAfterFieldClosed 定义必须在字段闭合后输出的类型。
func mustEmitAfterFieldClosed(eventType string) bool {
	if eventType == "time_range" {
		return true
	}
	if reTypeTopicTR.MatchString(eventType) {
		return true
	}
	if reTypeTopicImgID.MatchString(eventType) {
		return true
	}
	return false
}

// pathToEventType 把原始 JSON 路径映射为稳定业务事件类型。
func pathToEventType(path string) string {
	switch path {
	case "title", "01_title":
		return "title"
	case "time_range", "02_time_range":
		return "time_range"
	case "summary", "03_summary":
		return "summary"
	}

	if m := reTopicName.FindStringSubmatch(path); len(m) == 2 {
		return "topic[" + m[1] + "].name"
	}
	if m := reTopicTimeRange.FindStringSubmatch(path); len(m) == 2 {
		return "topic[" + m[1] + "].time_range"
	}
	if m := reTopicPlayerObj.FindStringSubmatch(path); len(m) == 3 {
		return "topic[" + m[1] + "].players[" + m[2] + "].name"
	}
	if m := reTopicPlayerRaw.FindStringSubmatch(path); len(m) == 3 {
		return "topic[" + m[1] + "].players[" + m[2] + "].name"
	}
	if m := reTopicDesc.FindStringSubmatch(path); len(m) == 2 {
		return "topic[" + m[1] + "].description"
	}
	if m := reTopicImageID.FindStringSubmatch(path); len(m) == 3 {
		return "topic[" + m[1] + "].images[" + m[2] + "].id"
	}
	if m := reTopicImageRaw.FindStringSubmatch(path); len(m) == 3 {
		return "topic[" + m[1] + "].images[" + m[2] + "].id"
	}

	return ""
}

// joinPath 拼接路径前缀和 key。
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + "." + key
}

// decodeJSONStringToken 对 key token 做 JSON 反转义。
func decodeJSONStringToken(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	s := string(raw)
	decoded, err := strconv.Unquote(`"` + s + `"`)
	if err != nil {
		return s
	}
	return decoded
}

// isSpace 判断是否可忽略空白字符。
func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}
