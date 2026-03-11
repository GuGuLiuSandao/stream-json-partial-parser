<h1 align="center">Stream JSON Partial Parser</h1>
<p align="center">
  <a href="./README.md">简体中文</a> |
  <a href="./README_EN.md"><b>English</b></a>
</p>
<p align="center">
  A lightweight demo for <b>incremental JSON parsing + event streaming</b> on top of LLM chunked outputs
</p>

<p align="center">
  <a href="#project-preview">Project Preview</a> ·
  <a href="#what-problem-it-solves">What Problem It Solves</a> ·
  <a href="#challenges--approach-short-version">Challenges & Approach</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#performance-at-a-glance">Performance</a>
</p>

<hr />

<h2 id="project-preview">Project Preview</h2>

<p><b>GIF 1</b>: side-by-side view of raw LLM chunks (one line per chunk) vs processed chunks sent to clients.</p>
<figure>
  <img src="assets/demo/streaming_output.gif" alt="Streaming Output: raw llm chunks vs parsed chunks" width="100%" />
  <figcaption><code>Streaming Output</code>: raw stream on the left, parsed event stream on the right.</figcaption>
</figure>

<p><b>GIF 2</b>: what users actually see in the <code>Report Preview</code> tab.</p>
<figure>
  <img src="assets/demo/report_preview.gif" alt="Report Preview demo" width="100%" />
  <figcaption><code>Report Preview</code>: real-time structured report preview for end users.</figcaption>
</figure>

<hr />

<h2 id="what-problem-it-solves">What Problem It Solves</h2>

<p>
  LLM outputs arrive as chunked text streams, not as a complete JSON document.<br />
  This project focuses on <b>producing stable, consumable incremental events before JSON is fully closed</b>,
  while preventing broken UTF-8 rendering and field mix-ups.
</p>

<div>
  <b>In one line:</b> parse and emit continuously, instead of waiting for full JSON completion.
</div>

<hr />

<h2 id="challenges--approach-short-version">Challenges & Approach (Short Version)</h2>

<h3>Main Challenges</h3>
<ul>
  <li>Chunk boundaries are arbitrary (can cut in the middle of JSON tokens or UTF-8 characters).</li>
  <li>Field semantics differ (some fields can stream progressively, others should be emitted only when complete).</li>
  <li>Performance is sensitive to chunk granularity.</li>
</ul>

<h3>Implementation Principles</h3>
<ol>
  <li>Byte-level incremental state machine for JSON syntax progression.</li>
  <li>Path-to-event mapping (convert JSON paths into stable business event types).</li>
  <li>Emission policy layering: normal fields stream incrementally, close-only fields emit on close.</li>
  <li>UTF-8 tail buffering: keep incomplete bytes and complete them with the next chunk.</li>
</ol>

<hr />

<h2 id="quick-start">Quick Start</h2>

<h3>1) Configure <code>.env</code></h3>
<p>Required keys:</p>
<ul>
  <li><code>api_base</code></li>
  <li><code>api_key</code></li>
  <li><code>model</code></li>
  <li><code>summary_lang</code> (optional, default <code>zh</code>, can be <code>en</code>)</li>
</ul>

<pre><code>api_base=https://your-api-base/v1
api_key=your_api_key
model=your_model_name
summary_lang=zh
</code></pre>

<h3>2) Start the web demo</h3>
<pre><code>go run web_server.go
</code></pre>

<p>Default URL: <code>http://127.0.0.1:8080</code></p>

<p>Custom port example:</p>
<pre><code>WEB_ADDR=:8081 go run web_server.go
</code></pre>

<hr />

<h2 id="performance-at-a-glance">Performance at a Glance</h2>

<p>
  Local benchmark command: <code>go test -bench . -benchmem ./sdk/lexer</code><br />
  Environment: <code>darwin/arm64, Apple M4 Pro</code>
</p>

<p><b>Tip (column meanings)</b></p>
<ul>
  <li><code>ns/op</code>: average time per benchmark iteration (lower is better)</li>
  <li><code>MB/s</code>: throughput (higher is better)</li>
  <li><code>B/op</code>: average allocated bytes per iteration (lower is better)</li>
  <li><code>allocs/op</code>: average allocation count per iteration (lower is better)</li>
</ul>

<table>
  <thead>
    <tr>
      <th>Case</th>
      <th>ns/op</th>
      <th>MB/s</th>
      <th>B/op</th>
      <th>allocs/op</th>
    </tr>
  </thead>
  <tbody>
    <tr><td>ReplayLike_MixedRunes</td><td>101,872</td><td>9.93</td><td>224,559</td><td>1,959</td></tr>
    <tr><td>ReplayLike_TinyRunes</td><td>202,833</td><td>4.99</td><td>359,785</td><td>3,102</td></tr>
    <tr><td>Baseline_Fixed64Bytes</td><td>21,545</td><td>46.97</td><td>20,107</td><td>257</td></tr>
    <tr><td>CloseOnly_ReplayLike</td><td>1,677,961</td><td>12.91</td><td>3,239,081</td><td>24,269</td></tr>
    <tr><td>CloseOnly_Baseline128Bytes</td><td>130,438</td><td>166.11</td><td>260,104</td><td>890</td></tr>
  </tbody>
</table>

<details>
  <summary><b>How to interpret these 5 cases</b></summary>
  <ul>
    <li><b>Chunk granularity is the #1 driver</b>: smaller chunks usually mean higher overhead.</li>
    <li><b>Cases 4/5 are extreme stress tests</b>: they model worst-case close-only behavior, not typical production distribution.</li>
    <li><b>Larger chunks help</b>: higher throughput and lower allocation pressure in both regular and stress scenarios.</li>
  </ul>
</details>

<hr />

<p align="center">
  For a quick read: watch the two GIFs, then read “Challenges & Approach” and “Performance”.
</p>
