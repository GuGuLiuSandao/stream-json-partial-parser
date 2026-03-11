package lexer

import (
	"strings"
	"testing"
)

var benchmarkEventSink int

// benchmarkReadableJSON 给出一个“可直接阅读”的完整 JSON 示例：
// - 包含顶层全部字段：01_title / 02_time_range / 03_summary / 04_topics
// - topics 数量 > 1
// - summary 与每个 topic 的 description 都不少于 20 个字
const benchmarkReadableJSON = `{
  "01_title": "公会速览基准样例",
  "02_time_range": "2026-03-09 12:00 - 2026-03-09 13:00",
  "03_summary": "这是一个用于基准测试的摘要输出文本，覆盖所有字段并且不少于二十个字。",
  "04_topics": [
    {
      "01_name": "话题-0",
      "02_time_range": "2026-03-09 12:00 - 2026-03-09 12:15",
      "03_players": ["玩家-0-0", "玩家-0-1", "玩家-0-2"],
      "04_description": "这是第一个话题的详细描述文本，用于验证description字段长度不少于二十字。",
      "05_images": [
        {"01_id": "img-0-0"},
        {"01_id": "img-0-1"}
      ]
    },
    {
      "01_name": "话题-1",
      "02_time_range": "2026-03-09 12:15 - 2026-03-09 12:30",
      "03_players": ["玩家-1-0", "玩家-1-1", "玩家-1-2"],
      "04_description": "这是第二个话题的详细描述文本，确保示例包含多个topic并满足长度要求。",
      "05_images": [
        {"01_id": "img-1-0"},
        {"01_id": "img-1-1"}
      ]
    }
  ]
}`

func BenchmarkJSONPathStreamParser(b *testing.B) {
	// payloadStandard: 可直接阅读的完整 JSON 示例（含全部字段，topics>1）。
	payloadStandard := []byte(benchmarkReadableJSON)
	// payloadCloseOnly: 压 close-only 路径（image id）的大字段缓冲行为。
	payloadCloseOnly := buildCloseOnlyBenchmarkJSON(1800)

	cases := []struct {
		name    string
		payload []byte
		chunks  [][]byte
	}{
		{
			name:    "ReplayLike_MixedRunes",
			payload: payloadStandard,
			chunks:  splitRunesByPattern(string(payloadStandard), []int{1, 1, 2, 1, 3, 2, 1, 4, 2, 5}),
		},
		{
			name:    "ReplayLike_TinyRunes",
			payload: payloadStandard,
			chunks:  splitRunesByPattern(string(payloadStandard), []int{1, 1, 1, 2, 1, 2, 1}),
		},
		{
			name:    "Baseline_Fixed64Bytes",
			payload: payloadStandard,
			chunks:  splitIntoChunks(payloadStandard, 64),
		},
		{
			name:    "CloseOnly_ReplayLike",
			payload: payloadCloseOnly,
			chunks:  splitRunesByPattern(string(payloadCloseOnly), []int{1, 1, 2, 1, 3, 1, 2, 1}),
		},
		{
			name:    "CloseOnly_Baseline128Bytes",
			payload: payloadCloseOnly,
			chunks:  splitIntoChunks(payloadCloseOnly, 128),
		},
	}

	for _, tc := range cases {
		tc := tc
		if len(tc.chunks) == 0 {
			b.Fatalf("empty chunks for case %s", tc.name)
		}
		if warmup := runParserOnce(tc.chunks); warmup == 0 {
			b.Fatalf("unexpected zero events in warmup for case %s", tc.name)
		}

		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkEventSink = runParserOnce(tc.chunks)
			}
		})
	}
}

func runParserOnce(chunks [][]byte) int {
	parser := NewJSONPathStreamParser("bench")
	totalEvents := 0
	for i, ch := range chunks {
		totalEvents += len(parser.ProcessChunk(ch, i+1))
	}
	totalEvents += len(parser.Flush(len(chunks) + 1))
	return totalEvents
}

func splitIntoChunks(data []byte, chunkSize int) [][]byte {
	if chunkSize <= 0 {
		return nil
	}
	out := make([][]byte, 0, (len(data)+chunkSize-1)/chunkSize)
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[i:end])
	}
	return out
}

func splitRunesByPattern(s string, pattern []int) [][]byte {
	if len(pattern) == 0 {
		return nil
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}

	out := make([][]byte, 0, len(runes)/2+1)
	idx := 0
	patIdx := 0

	for idx < len(runes) {
		step := pattern[patIdx%len(pattern)]
		patIdx++
		if step < 1 {
			step = 1
		}

		end := idx + step
		if end > len(runes) {
			end = len(runes)
		}

		// Prefer cutting at punctuation near target to mimic token-ish chunk boundaries.
		maxLookAhead := idx + step + 2
		if maxLookAhead > len(runes) {
			maxLookAhead = len(runes)
		}
		for p := end; p < maxLookAhead; p++ {
			if isLikelyBoundaryRune(runes[p-1]) {
				end = p
				break
			}
		}

		out = append(out, []byte(string(runes[idx:end])))
		idx = end
	}
	return out
}

func isLikelyBoundaryRune(r rune) bool {
	switch r {
	case ',', ':', '{', '}', '[', ']', '"', '\n', '。', '，', '！', '？', '；':
		return true
	default:
		return false
	}
}

func buildCloseOnlyBenchmarkJSON(repeat int) []byte {
	if repeat < 1 {
		repeat = 1
	}
	longID := strings.Repeat("图像标识", repeat)

	var sb strings.Builder
	sb.Grow(len(longID) + 512)
	sb.WriteString(`{"04_topics":[{"01_name":"close-only","05_images":[{"01_id":"`)
	sb.WriteString(longID)
	sb.WriteString(`"}]}]}`)
	return []byte(sb.String())
}
