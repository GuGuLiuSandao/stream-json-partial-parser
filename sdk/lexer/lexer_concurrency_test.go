package lexer

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcessChunkConcurrentCallsUseInternalBuffer(t *testing.T) {
	parser := NewJSONPathStreamParser("concurrency")

	chunk1 := []byte(`{"01_title":"A`)
	chunk2 := []byte(`B"}`)

	type result struct {
		idx    int
		events []StreamEvent
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup

	// 先把 parser 置为 processing=true，让并发调用只入队不立刻消费，
	// 从而稳定地验证队列行为，不依赖短暂调度窗口。
	parser.mu.Lock()
	parser.processing = true
	parser.mu.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- result{idx: 1, events: parser.ProcessChunk(chunk1, 1)}
	}()

	if !waitParserState(parser, 2*time.Second, func(p *JSONPathStreamParser) bool {
		return len(p.pendingOps) == 1 && p.pendingOps[0].chunkNum == 1
	}) {
		t.Fatalf("first op was not queued as expected")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- result{idx: 2, events: parser.ProcessChunk(chunk2, 2)}
	}()

	if !waitParserState(parser, 2*time.Second, func(p *JSONPathStreamParser) bool {
		return len(p.pendingOps) == 2 &&
			p.pendingOps[0].chunkNum == 1 &&
			p.pendingOps[1].chunkNum == 2
	}) {
		t.Fatalf("queued ops order mismatch, want [1,2]")
	}

	// 手动触发一次 drain，消费上面排队的两个 op。
	parser.drainPendingOps()

	wg.Wait()
	close(results)

	byChunk := make(map[int][]StreamEvent, 2)
	for item := range results {
		byChunk[item.idx] = item.events
	}

	flushEvents := parser.Flush(2)

	var gotTitle strings.Builder
	for _, idx := range []int{1, 2} {
		for _, evt := range byChunk[idx] {
			if evt.Type == "title" {
				gotTitle.WriteString(evt.Content)
			}
		}
	}
	for _, evt := range flushEvents {
		if evt.Type == "title" {
			gotTitle.WriteString(evt.Content)
		}
	}

	if gotTitle.String() != "AB" {
		t.Fatalf("unexpected title reconstruction, got=%q want=%q", gotTitle.String(), "AB")
	}
}

func waitParserState(p *JSONPathStreamParser, timeout time.Duration, predicate func(*JSONPathStreamParser) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ok := predicate(p)
		p.mu.Unlock()
		if ok {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}
