package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"stream_json_partial_parser/pipeline"
)

const (
	defaultWebAddr = ":8080"
)

type sseMessage struct {
	event string
	data  string
}

func main() {
	addr := strings.TrimSpace(os.Getenv("WEB_ADDR"))
	if addr == "" {
		addr = defaultWebAddr
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/api/run", runPipelineSSE)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("frontend server started: http://127.0.0.1%s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func runPipelineSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	lang := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lang")))
	if lang != "en" {
		lang = "zh"
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))

	_ = writeSSEEvent(w, "started", "running")
	flusher.Flush()

	msgCh := make(chan sseMessage, 256)

	go func() {
		defer close(msgCh)

		send := func(msg sseMessage) {
			select {
			case msgCh <- msg:
			case <-ctx.Done():
			}
		}

		opts := pipeline.RunOptions{
			SummaryLang:   lang,
			ModelOverride: model,
		}
		cbs := pipeline.Callbacks{
			OnRawChunk: func(chunk string) {
				if chunk == "" {
					return
				}
				send(sseMessage{event: "raw", data: chunk})
			},
			OnParsedEvent: func(evt pipeline.StreamEvent) {
				line, err := json.Marshal(evt)
				if err != nil {
					return
				}
				send(sseMessage{event: "lexer", data: string(line)})
			},
			OnLog: func(line string) {
				line = strings.TrimSpace(line)
				if line == "" {
					return
				}
				send(sseMessage{event: "stderr", data: line})
			},
		}

		if err := pipeline.Run(ctx, opts, cbs); err != nil {
			send(sseMessage{event: "pipeline_error", data: err.Error()})
			return
		}
		send(sseMessage{event: "done", data: "ok"})
	}()

	for msg := range msgCh {
		if err := writeSSEEvent(w, msg.event, msg.data); err != nil {
			return
		}
		flusher.Flush()
	}
}

func writeSSEEvent(w io.Writer, event, data string) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}

	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	lines := strings.Split(data, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}

	for _, line := range lines {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}

	_, err := fmt.Fprint(w, "\n")
	return err
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Chat Summary Demo</title>
  <style>
    :root {
      --bg-top: #f5f1e7;
      --bg-bottom: #dae5d8;
      --panel: rgba(255, 255, 255, 0.82);
      --border: rgba(38, 67, 58, 0.22);
      --text-main: #1f2b2a;
      --text-subtle: #4f605d;
      --accent: #26433a;
      --accent-press: #1d342d;
      --accent-soft: rgba(38, 67, 58, 0.1);
      --report-bg: #fffef8;
      --shadow: 0 14px 38px rgba(38, 67, 58, 0.12);
    }

    * {
      box-sizing: border-box;
    }

    body {
      margin: 0;
      min-height: 100vh;
      color: var(--text-main);
      font-family: "Avenir Next", "PingFang SC", "Hiragino Sans GB", "Noto Sans CJK SC", sans-serif;
      background: linear-gradient(130deg, var(--bg-top) 0%, var(--bg-bottom) 100%);
      display: flex;
      justify-content: center;
      padding: 24px;
    }

    .app {
      width: min(1280px, 100%);
      display: flex;
      flex-direction: column;
      gap: 18px;
      animation: rise 480ms ease-out;
    }

    .toolbar {
      display: flex;
      align-items: center;
      gap: 12px;
      flex-wrap: wrap;
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 16px;
      padding: 12px 14px;
      box-shadow: var(--shadow);
      backdrop-filter: blur(6px);
    }

    .toolbar button {
      border: 0;
      border-radius: 10px;
      padding: 10px 16px;
      background: var(--accent);
      color: #f5f7f6;
      font-size: 15px;
      letter-spacing: 0.2px;
      cursor: pointer;
      transition: transform 160ms ease, background 160ms ease;
    }

    .toolbar button:hover {
      transform: translateY(-1px);
    }

    .toolbar button:active {
      transform: translateY(0);
      background: var(--accent-press);
    }

    .toolbar button:disabled {
      cursor: wait;
      opacity: 0.7;
      transform: none;
    }

    .model-field {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--text-subtle);
      font-size: 14px;
      margin-left: 4px;
    }

    .model-field input {
      width: min(420px, 62vw);
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 9px 11px;
      color: var(--text-main);
      background: rgba(255, 255, 255, 0.9);
      font-size: 14px;
    }

    .model-field input:focus {
      outline: 2px solid rgba(38, 67, 58, 0.3);
      outline-offset: 1px;
    }

    .model-field input:disabled {
      opacity: 0.65;
      cursor: wait;
    }

    .status {
      color: var(--text-subtle);
      font-size: 14px;
    }

    .tabs {
      display: flex;
      gap: 10px;
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 14px;
      padding: 10px;
      box-shadow: var(--shadow);
      backdrop-filter: blur(6px);
    }

    .tab-btn {
      border: 1px solid transparent;
      border-radius: 999px;
      padding: 8px 14px;
      color: var(--text-main);
      background: transparent;
      font-size: 14px;
      cursor: pointer;
      transition: background 160ms ease, border-color 160ms ease;
    }

    .tab-btn:hover {
      border-color: var(--border);
      background: rgba(255, 255, 255, 0.55);
    }

    .tab-btn.is-active {
      color: #f5f7f6;
      border-color: transparent;
      background: var(--accent);
    }

    .tab-view {
      display: none;
      animation: rise 280ms ease-out;
    }

    .tab-view.is-active {
      display: block;
    }

    .columns {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 14px;
      min-height: 72vh;
    }

    .columns.single-column {
      grid-template-columns: 1fr;
    }

    .panel {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 16px;
      box-shadow: var(--shadow);
      overflow: hidden;
      display: flex;
      flex-direction: column;
      min-height: 380px;
      backdrop-filter: blur(6px);
    }

    .panel h2 {
      margin: 0;
      font-size: 15px;
      font-weight: 600;
      padding: 12px 14px;
      border-bottom: 1px solid var(--border);
      color: var(--accent);
      letter-spacing: 0.3px;
    }

    .panel pre {
      margin: 0;
      padding: 14px;
      flex: 1;
      overflow: auto;
      white-space: pre-wrap;
      word-break: break-word;
      line-height: 1.5;
      font-size: 13px;
      font-family: "JetBrains Mono", "SFMono-Regular", "Menlo", monospace;
      background: linear-gradient(180deg, rgba(255, 255, 255, 0.88) 0%, rgba(250, 253, 251, 0.8) 100%);
    }

    .report-wrap {
      min-height: 72vh;
    }

    .report-columns {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 14px;
      min-height: 72vh;
    }

    .report-host {
      flex: 1;
      overflow: auto;
      padding: 20px;
      background: linear-gradient(180deg, rgba(255, 255, 255, 0.9) 0%, rgba(247, 252, 247, 0.85) 100%);
    }

    .report-placeholder {
      margin: 0;
      padding: 18px;
      color: var(--text-subtle);
      border-radius: 12px;
      border: 1px dashed var(--border);
      background: rgba(255, 255, 255, 0.62);
    }

    .report-doc {
      width: min(880px, 100%);
      margin: 0 auto;
      padding: 28px;
      border-radius: 18px;
      border: 1px solid rgba(38, 67, 58, 0.18);
      background: var(--report-bg);
      box-shadow: 0 10px 28px rgba(24, 44, 38, 0.1);
    }

    .report-header h1 {
      margin: 0;
      font-size: 28px;
      line-height: 1.25;
      color: #1d3029;
      letter-spacing: 0.4px;
    }

    .report-meta {
      margin-top: 8px;
      font-size: 14px;
      color: var(--text-subtle);
    }

    .report-section {
      margin-top: 24px;
    }

    .report-section h2 {
      margin: 0 0 10px;
      font-size: 18px;
      color: #234036;
      letter-spacing: 0.2px;
    }

    .report-summary p {
      margin: 0;
      line-height: 1.68;
      font-size: 15px;
      color: #243530;
      white-space: pre-wrap;
    }

    .report-topic {
      margin-top: 12px;
      padding: 14px 14px 12px;
      border-radius: 12px;
      border: 1px solid rgba(38, 67, 58, 0.16);
      background: #fff;
    }

    .topic-head {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: 10px;
      flex-wrap: wrap;
    }

    .topic-head h3 {
      margin: 0;
      font-size: 16px;
      color: #1f322c;
    }

    .topic-time {
      font-size: 13px;
      color: #567068;
      padding: 2px 8px;
      border-radius: 999px;
      background: var(--accent-soft);
    }

    .topic-desc {
      margin: 10px 0 0;
      line-height: 1.62;
      color: #2a3c37;
      white-space: pre-wrap;
    }

    .topic-meta {
      margin: 9px 0 0;
      font-size: 14px;
      color: #2b3f39;
      white-space: pre-wrap;
    }

    .topic-meta strong {
      font-weight: 600;
      color: #233d33;
    }

    @media (max-width: 920px) {
      .columns {
        grid-template-columns: 1fr;
        min-height: 0;
      }

      .panel {
        min-height: 280px;
      }

      .report-wrap {
        min-height: 0;
      }

      .report-columns {
        grid-template-columns: 1fr;
        min-height: 0;
      }

      .report-host {
        padding: 14px;
      }

      .report-doc {
        padding: 18px;
      }

      .report-header h1 {
        font-size: 24px;
      }
    }

    @keyframes rise {
      from {
        opacity: 0;
        transform: translateY(10px);
      }
      to {
        opacity: 1;
        transform: translateY(0);
      }
    }
  </style>
</head>
<body>
  <main class="app">
    <section class="toolbar">
      <button id="runZhBtn" type="button">执行中文总结</button>
      <button id="runEnBtn" type="button">Run EN Summary Pipeline</button>
      <label class="model-field" for="modelInput">
        Model
        <input id="modelInput" type="text" placeholder="Leave empty to use model from .env" />
      </label>
      <span id="status" class="status">Click the button to run the summary pipeline via in-process SSE streaming.</span>
    </section>

    <section class="tabs">
      <button id="tabStream" class="tab-btn is-active" data-target="streamView" type="button">Streaming Output</button>
      <button id="tabReport" class="tab-btn" data-target="reportView" type="button">Report Preview</button>
      <button id="tabJSON" class="tab-btn" data-target="jsonView" type="button">JSON Output</button>
    </section>

    <section id="streamView" class="tab-view is-active">
      <section class="columns">
        <article class="panel">
          <h2>Raw LLM Output (Chunked)</h2>
          <pre id="rawOutput">Waiting to run...</pre>
        </article>
        <article class="panel">
          <h2>Lexer Print Output (Streaming)</h2>
          <pre id="lexerOutput">Waiting to run...</pre>
        </article>
      </section>
    </section>

    <section id="reportView" class="tab-view">
      <section class="report-wrap">
        <section class="report-columns">
          <article class="panel">
            <h2>Structured Report (Live from Lexer Output)</h2>
            <div id="reportOutput" class="report-host">
              <p class="report-placeholder">Waiting to run...</p>
            </div>
          </article>
          <article class="panel">
            <h2>Buffered Report (50±10ms, time_range/msg_id as one unit)</h2>
            <div id="bufferedReportOutput" class="report-host">
              <p class="report-placeholder">Waiting to run...</p>
            </div>
          </article>
        </section>
      </section>
    </section>

    <section id="jsonView" class="tab-view">
      <section class="columns single-column">
        <article class="panel">
          <h2>LLM Output as JSON</h2>
          <pre id="jsonOutput">Waiting to run...</pre>
        </article>
      </section>
    </section>
  </main>

  <script>
    const runZhBtn = document.getElementById('runZhBtn');
    const runEnBtn = document.getElementById('runEnBtn');
    const modelInput = document.getElementById('modelInput');
    const statusEl = document.getElementById('status');
    const rawEl = document.getElementById('rawOutput');
    const lexerEl = document.getElementById('lexerOutput');
    const jsonEl = document.getElementById('jsonOutput');
    const reportEl = document.getElementById('reportOutput');
    const bufferedReportEl = document.getElementById('bufferedReportOutput');
    const tabButtons = Array.from(document.querySelectorAll('.tab-btn'));
    const tabViews = {
      streamView: document.getElementById('streamView'),
      reportView: document.getElementById('reportView'),
      jsonView: document.getElementById('jsonView')
    };

    let currentSource = null;
    let reportModel = createEmptyReportModel();
    let reportViewModel = createEmptyReportModel();
    let reportRenderQueued = false;
    let bufferedReportModel = createEmptyReportModel();
    let bufferedRenderQueued = false;
    let reportStreamDone = false;
    let topStage = 0;
    let nextTopicToRender = 0;
    let pendingTopEvents = createEmptyTopPending();
    let pendingTopicEvents = new Map();
    let topicStageByIndex = new Map();
    let bufferedEventQueue = [];
    let bufferedTimer = null;
    let rawChunkCount = 0;
    let rawChunkJoined = '';

    const bufferedBaseDelayMs = 50;
    const bufferedJitterMs = 10;

    const reTopicName = /^topic\[(\d+)\]\.name$/;
    const reTopicTimeRange = /^topic\[(\d+)\]\.time_range$/;
    const reTopicDescription = /^topic\[(\d+)\]\.description$/;
    const reTopicPlayer = /^topic\[(\d+)\]\.players\[(\d+)\]\.name$/;
    const reTopicImage = /^topic\[(\d+)\]\.images\[(\d+)\]\.id$/;

    function appendRawChunk(text) {
      rawChunkCount++;
      if (rawChunkCount === 1) {
        rawEl.textContent = '';
      }

      const line = '[chunk ' + rawChunkCount + '] ' + JSON.stringify(text);
      if (rawEl.textContent.length > 0) {
        rawEl.textContent += '\n';
      }
      rawEl.textContent += line;
      rawEl.scrollTop = rawEl.scrollHeight;

      rawChunkJoined += text;
      renderJSONOutput();
    }

    function appendLexer(line) {
      if (lexerEl.textContent.length > 0) {
        lexerEl.textContent += '\n';
      }
      lexerEl.textContent += line;
      lexerEl.scrollTop = lexerEl.scrollHeight;
    }

    function randomBufferedDelay() {
      const jitter = Math.floor(Math.random() * (bufferedJitterMs*2 + 1)) - bufferedJitterMs;
      return Math.max(20, bufferedBaseDelayMs + jitter);
    }

    function stopBufferedTimer() {
      if (bufferedTimer !== null) {
        window.clearTimeout(bufferedTimer);
        bufferedTimer = null;
      }
    }

    function scheduleBufferedRender() {
      if (bufferedRenderQueued) {
        return;
      }
      bufferedRenderQueued = true;

      const doRender = () => {
        bufferedRenderQueued = false;
        renderBufferedReport();
      };

      if (window.requestAnimationFrame) {
        window.requestAnimationFrame(doRender);
        return;
      }

      window.setTimeout(doRender, 0);
    }

    function ensureBufferedDrainLoop() {
      if (bufferedTimer !== null || bufferedEventQueue.length === 0) {
        return;
      }

      const tick = () => {
        bufferedTimer = null;
        if (bufferedEventQueue.length === 0) {
          return;
        }

        const current = bufferedEventQueue[0];
        const nextChunk = current.chunks[current.chunkIndex];
        current.chunkIndex++;
        applyReportEvent(bufferedReportModel, current.eventType, nextChunk);
        scheduleBufferedRender();

        if (current.chunkIndex >= current.chunks.length) {
          bufferedEventQueue.shift();
        }

        if (bufferedEventQueue.length > 0) {
          bufferedTimer = window.setTimeout(tick, randomBufferedDelay());
        }
      };

      bufferedTimer = window.setTimeout(tick, randomBufferedDelay());
    }

    function shouldBufferAsSingleUnit(eventType) {
      if (eventType === 'time_range') {
        return true;
      }
      if (reTopicTimeRange.test(eventType)) {
        return true;
      }
      if (reTopicImage.test(eventType)) {
        return true;
      }
      return false;
    }

    function enqueueBufferedEvent(eventType, text) {
      if (!text) {
        return;
      }

      const chunks = shouldBufferAsSingleUnit(eventType) ? [text] : Array.from(text);
      bufferedEventQueue.push({
        eventType,
        chunks,
        chunkIndex: 0
      });
      ensureBufferedDrainLoop();
    }

    function resetBufferedOutput() {
      stopBufferedTimer();
      bufferedEventQueue = [];
      bufferedReportModel = createEmptyReportModel();
      renderBufferedReport();
    }

    function switchTab(targetID) {
      tabButtons.forEach((btn) => {
        btn.classList.toggle('is-active', btn.dataset.target === targetID);
      });
      Object.entries(tabViews).forEach(([viewID, el]) => {
        el.classList.toggle('is-active', viewID === targetID);
      });
    }

    function createEmptyReportModel() {
      return {
        title: '',
        timeRange: '',
        summary: '',
        topics: []
      };
    }

    function cloneReportModel(source) {
      return {
        title: source.title,
        timeRange: source.timeRange,
        summary: source.summary,
        topics: source.topics.map((topic) => ({
          name: topic.name,
          timeRange: topic.timeRange,
          description: topic.description,
          players: topic.players.slice(),
          images: topic.images.slice()
        }))
      };
    }

    function createEmptyTopPending() {
      return [[], [], []];
    }

    function createEmptyTopicPendingBucket() {
      return [[], [], [], [], []];
    }

    function classifyTopStage(eventType) {
      if (eventType === 'title') {
        return 0;
      }
      if (eventType === 'time_range') {
        return 1;
      }
      if (eventType === 'summary') {
        return 2;
      }
      if (reTopicName.test(eventType) || reTopicTimeRange.test(eventType) || reTopicDescription.test(eventType) || reTopicPlayer.test(eventType) || reTopicImage.test(eventType)) {
        return 3;
      }
      return -1;
    }

    function classifyTopicField(eventType) {
      let match = reTopicName.exec(eventType);
      if (match) {
        return { topicIndex: Number(match[1]), fieldStage: 0 };
      }

      match = reTopicTimeRange.exec(eventType);
      if (match) {
        return { topicIndex: Number(match[1]), fieldStage: 1 };
      }

      match = reTopicPlayer.exec(eventType);
      if (match) {
        return { topicIndex: Number(match[1]), fieldStage: 2 };
      }

      match = reTopicDescription.exec(eventType);
      if (match) {
        return { topicIndex: Number(match[1]), fieldStage: 3 };
      }

      match = reTopicImage.exec(eventType);
      if (match) {
        return { topicIndex: Number(match[1]), fieldStage: 4 };
      }

      return null;
    }

    function ensureTopic(model, index) {
      while (model.topics.length <= index) {
        model.topics.push({
          name: '',
          timeRange: '',
          description: '',
          players: [],
          images: []
        });
      }
      return model.topics[index];
    }

    function appendIndexedValue(arr, index, content) {
      while (arr.length <= index) {
        arr.push('');
      }
      arr[index] += content;
    }

    function applyReportEvent(model, eventType, content) {
      if (!content) {
        return false;
      }

      if (eventType === 'title') {
        model.title += content;
        return true;
      }
      if (eventType === 'time_range') {
        model.timeRange += content;
        return true;
      }
      if (eventType === 'summary') {
        model.summary += content;
        return true;
      }

      let m = reTopicName.exec(eventType);
      if (m) {
        ensureTopic(model, Number(m[1])).name += content;
        return true;
      }

      m = reTopicTimeRange.exec(eventType);
      if (m) {
        ensureTopic(model, Number(m[1])).timeRange += content;
        return true;
      }

      m = reTopicDescription.exec(eventType);
      if (m) {
        ensureTopic(model, Number(m[1])).description += content;
        return true;
      }

      m = reTopicPlayer.exec(eventType);
      if (m) {
        const topic = ensureTopic(model, Number(m[1]));
        appendIndexedValue(topic.players, Number(m[2]), content);
        return true;
      }

      m = reTopicImage.exec(eventType);
      if (m) {
        const topic = ensureTopic(model, Number(m[1]));
        appendIndexedValue(topic.images, Number(m[2]), content);
        return true;
      }

      return false;
    }

    function hasPendingTopicAfter(topicIndex) {
      for (const [idx, bucket] of pendingTopicEvents.entries()) {
        if (idx <= topicIndex) {
          continue;
        }
        for (let i = 0; i < bucket.length; i++) {
          if (bucket[i].length > 0) {
            return true;
          }
        }
      }
      return false;
    }

    function hasAnyPendingTopic() {
      for (const bucket of pendingTopicEvents.values()) {
        for (let i = 0; i < bucket.length; i++) {
          if (bucket[i].length > 0) {
            return true;
          }
        }
      }
      return false;
    }

    function flushTopStage(stage) {
      let changed = false;
      const queue = pendingTopEvents[stage];
      while (queue.length > 0) {
        const evt = queue.shift();
        if (applyReportEvent(reportViewModel, evt.eventType, evt.content)) {
          changed = true;
        }
      }
      return changed;
    }

    function flushTopicEvents() {
      let changed = false;
      let progressed = false;

      while (true) {
        const bucket = pendingTopicEvents.get(nextTopicToRender);
        const hasLater = hasPendingTopicAfter(nextTopicToRender);

        if (!bucket) {
          if (reportStreamDone && hasLater) {
            nextTopicToRender++;
            progressed = true;
            continue;
          }
          break;
        }

        let topicStage = topicStageByIndex.get(nextTopicToRender) || 0;
        let loopProgress = false;

        while (topicStage <= 4) {
          const queue = bucket[topicStage];
          while (queue.length > 0) {
            const evt = queue.shift();
            if (applyReportEvent(reportViewModel, evt.eventType, evt.content)) {
              changed = true;
            }
            loopProgress = true;
          }

          const allowAdvance = topicStage < 4 && (bucket[topicStage+1].length > 0 || hasLater || reportStreamDone);
          if (!allowAdvance) {
            break;
          }

          topicStage++;
          topicStageByIndex.set(nextTopicToRender, topicStage);
          loopProgress = true;
        }

        if (topicStage === 4 && (hasLater || reportStreamDone)) {
          topicStage = 5;
          topicStageByIndex.set(nextTopicToRender, topicStage);
          loopProgress = true;
        }

        if (topicStage >= 5) {
          pendingTopicEvents.delete(nextTopicToRender);
          nextTopicToRender++;
          progressed = true;
          continue;
        }

        if (loopProgress) {
          progressed = true;
          continue;
        }

        break;
      }

      return { changed, progressed };
    }

    function flushOrderedQueues() {
      let changed = false;
      let progressed = true;

      while (progressed) {
        progressed = false;

        if (topStage === 0) {
          if (flushTopStage(0)) {
            changed = true;
            progressed = true;
          }
          if ((pendingTopEvents[1].length > 0 || reportStreamDone) && pendingTopEvents[0].length === 0) {
            topStage = 1;
            progressed = true;
          }
        }

        if (topStage === 1) {
          if (flushTopStage(1)) {
            changed = true;
            progressed = true;
          }
          if ((pendingTopEvents[2].length > 0 || reportStreamDone) && pendingTopEvents[1].length === 0) {
            topStage = 2;
            progressed = true;
          }
        }

        if (topStage === 2) {
          if (flushTopStage(2)) {
            changed = true;
            progressed = true;
          }
          if ((hasAnyPendingTopic() || reportStreamDone) && pendingTopEvents[2].length === 0) {
            topStage = 3;
            progressed = true;
          }
        }

        if (topStage === 3) {
          const topicResult = flushTopicEvents();
          if (topicResult.changed) {
            changed = true;
          }
          if (topicResult.progressed) {
            progressed = true;
          }
        }
      }

      return changed;
    }

    function enqueueOrderedEvent(eventType, content) {
      const stage = classifyTopStage(eventType);
      if (stage < 0) {
        return false;
      }

      const eventData = { eventType, content };
      if (stage < 3) {
        pendingTopEvents[stage].push(eventData);
        return true;
      }

      const topicField = classifyTopicField(eventType);
      if (!topicField) {
        return false;
      }

      let bucket = pendingTopicEvents.get(topicField.topicIndex);
      if (!bucket) {
        bucket = createEmptyTopicPendingBucket();
        pendingTopicEvents.set(topicField.topicIndex, bucket);
      }
      bucket[topicField.fieldStage].push(eventData);
      return true;
    }

    function finalizeReportStream() {
      reportStreamDone = true;
      flushOrderedQueues();
      reportViewModel = cloneReportModel(reportModel);
      scheduleReportRender();
    }

    function hasTopicData(topic) {
      if (!topic) {
        return false;
      }

      if (topic.name.trim() || topic.timeRange.trim() || topic.description.trim()) {
        return true;
      }

      const hasPlayer = topic.players.some((player) => player.trim() !== '');
      if (hasPlayer) {
        return true;
      }

      return topic.images.some((imageID) => imageID.trim() !== '');
    }

    function hasReportData(model) {
      if (model.title.trim() || model.timeRange.trim() || model.summary.trim()) {
        return true;
      }
      return model.topics.some((topic) => hasTopicData(topic));
    }

    function scheduleReportRender() {
      if (reportRenderQueued) {
        return;
      }
      reportRenderQueued = true;

      const doRender = () => {
        reportRenderQueued = false;
        renderReport();
      };

      if (window.requestAnimationFrame) {
        window.requestAnimationFrame(doRender);
        return;
      }

      window.setTimeout(doRender, 0);
    }

    function renderReportModel(targetEl, model, emptyText) {
      targetEl.innerHTML = '';

      if (!hasReportData(model)) {
        const placeholder = document.createElement('p');
        placeholder.className = 'report-placeholder';
        placeholder.textContent = emptyText;
        targetEl.appendChild(placeholder);
        return;
      }

      const doc = document.createElement('article');
      doc.className = 'report-doc';

      const header = document.createElement('header');
      header.className = 'report-header';

      const title = document.createElement('h1');
      title.textContent = model.title.trim() || 'Chat Summary Report';
      header.appendChild(title);

      if (model.timeRange.trim()) {
        const meta = document.createElement('div');
        meta.className = 'report-meta';
        meta.textContent = 'Time Range: ' + model.timeRange.trim();
        header.appendChild(meta);
      }

      doc.appendChild(header);

      if (model.summary.trim()) {
        const summarySection = document.createElement('section');
        summarySection.className = 'report-section report-summary';

        const summaryTitle = document.createElement('h2');
        summaryTitle.textContent = 'Overall Summary';
        summarySection.appendChild(summaryTitle);

        const summaryText = document.createElement('p');
        summaryText.textContent = model.summary.trim();
        summarySection.appendChild(summaryText);

        doc.appendChild(summarySection);
      }

      const visibleTopics = model.topics
        .map((topic, index) => ({ topic, index }))
        .filter(({ topic }) => hasTopicData(topic));

      if (visibleTopics.length > 0) {
        const topicsSection = document.createElement('section');
        topicsSection.className = 'report-section';

        const topicsTitle = document.createElement('h2');
        topicsTitle.textContent = 'Topic Details';
        topicsSection.appendChild(topicsTitle);

        visibleTopics.forEach(({ topic, index }) => {
          const topicEl = document.createElement('article');
          topicEl.className = 'report-topic';

          const topicHead = document.createElement('div');
          topicHead.className = 'topic-head';

          const topicTitle = document.createElement('h3');
          topicTitle.textContent = topic.name.trim() || ('Topic ' + (index + 1));
          topicHead.appendChild(topicTitle);

          if (topic.timeRange.trim()) {
            const topicTime = document.createElement('span');
            topicTime.className = 'topic-time';
            topicTime.textContent = topic.timeRange.trim();
            topicHead.appendChild(topicTime);
          }

          topicEl.appendChild(topicHead);

          const players = topic.players.map((player) => player.trim()).filter(Boolean);
          if (players.length > 0) {
            const playerLine = document.createElement('p');
            playerLine.className = 'topic-meta';
            const playerLabel = document.createElement('strong');
            playerLabel.textContent = 'Players: ';
            playerLine.appendChild(playerLabel);
            playerLine.append(players.join(', '));
            topicEl.appendChild(playerLine);
          }

          if (topic.description.trim()) {
            const description = document.createElement('p');
            description.className = 'topic-desc';
            description.textContent = topic.description.trim();
            topicEl.appendChild(description);
          }

          const images = topic.images.map((imageID) => imageID.trim()).filter(Boolean);
          if (images.length > 0) {
            const imageLine = document.createElement('p');
            imageLine.className = 'topic-meta';
            const imageLabel = document.createElement('strong');
            imageLabel.textContent = 'Related Image Message IDs: ';
            imageLine.appendChild(imageLabel);
            imageLine.append(images.join(', '));
            topicEl.appendChild(imageLine);
          }

          topicsSection.appendChild(topicEl);
        });

        doc.appendChild(topicsSection);
      }

      targetEl.appendChild(doc);
    }

    function renderReport() {
      renderReportModel(reportEl, reportViewModel, 'Waiting for lexer output to build the full report...');
    }

    function renderBufferedReport() {
      renderReportModel(bufferedReportEl, bufferedReportModel, 'Waiting for buffered output to build the full report...');
    }

    function ingestLexerLine(line) {
      appendLexer(line);

      let parsed;
      try {
        parsed = JSON.parse(line);
      } catch {
        return;
      }

      if (!parsed || typeof parsed.type !== 'string' || typeof parsed.content !== 'string') {
        return;
      }

      if (!applyReportEvent(reportModel, parsed.type, parsed.content)) {
        return;
      }

      enqueueBufferedEvent(parsed.type, parsed.content);

      if (!enqueueOrderedEvent(parsed.type, parsed.content)) {
        return;
      }

      if (flushOrderedQueues()) {
        scheduleReportRender();
      }
    }

    function resetReport() {
      reportModel = createEmptyReportModel();
      reportViewModel = createEmptyReportModel();
      reportStreamDone = false;
      topStage = 0;
      nextTopicToRender = 0;
      pendingTopEvents = createEmptyTopPending();
      pendingTopicEvents = new Map();
      topicStageByIndex = new Map();
      resetBufferedOutput();
      renderReport();
    }

    function resetRawChunks() {
      rawChunkCount = 0;
      rawEl.textContent = 'Waiting to run...';
      rawChunkJoined = '';
      jsonEl.textContent = 'Waiting to run...';
    }

    function renderJSONOutput() {
      const source = rawChunkJoined.trim();
      if (source === '') {
        jsonEl.textContent = 'Waiting to run...';
        return;
      }

      try {
        const parsed = JSON.parse(rawChunkJoined);
        jsonEl.textContent = JSON.stringify(parsed, null, 2);
      } catch {
        jsonEl.textContent = rawChunkJoined;
      }
      jsonEl.scrollTop = jsonEl.scrollHeight;
    }

    function stopCurrentSource() {
      if (currentSource) {
        currentSource.close();
        currentSource = null;
      }
    }

    function runPipeline(lang) {
      stopCurrentSource();

      runZhBtn.disabled = true;
      runEnBtn.disabled = true;
      modelInput.disabled = true;
      statusEl.textContent = 'Running...';
      lexerEl.textContent = '';
      resetReport();
      resetRawChunks();

      const model = modelInput.value.trim();
      const params = new URLSearchParams();
      params.set('lang', lang);
      params.set('t', String(Date.now()));
      if (model !== '') {
        params.set('model', model);
      }

      const source = new EventSource('/api/run?' + params.toString());
      currentSource = source;
      let completed = false;

      source.addEventListener('started', () => {
        statusEl.textContent = 'Backend started, streaming in progress...';
      });

      source.addEventListener('raw', (event) => {
        appendRawChunk(event.data);
      });

      source.addEventListener('lexer', (event) => {
        ingestLexerLine(event.data);
      });

      source.addEventListener('stderr', (event) => {
        appendLexer('[stderr] ' + event.data);
      });

      source.addEventListener('pipeline_error', (event) => {
        completed = true;
        statusEl.textContent = 'Run failed';
        appendLexer('[error] ' + event.data);
        finalizeReportStream();
        stopCurrentSource();
        runZhBtn.disabled = false;
        runEnBtn.disabled = false;
        modelInput.disabled = false;
      });

      source.addEventListener('done', () => {
        completed = true;
        statusEl.textContent = 'Completed';
        finalizeReportStream();
        stopCurrentSource();
        runZhBtn.disabled = false;
        runEnBtn.disabled = false;
        modelInput.disabled = false;
      });

      source.onerror = () => {
        if (completed) {
          return;
        }
        statusEl.textContent = 'Connection lost';
        appendLexer('[error] SSE connection lost');
        finalizeReportStream();
        stopCurrentSource();
        runZhBtn.disabled = false;
        runEnBtn.disabled = false;
        modelInput.disabled = false;
      };
    }

    tabButtons.forEach((btn) => {
      btn.addEventListener('click', () => {
        switchTab(btn.dataset.target);
      });
    });

    renderReport();
    runZhBtn.addEventListener('click', () => runPipeline('zh'));
    runEnBtn.addEventListener('click', () => runPipeline('en'));
  </script>
</body>
</html>`
