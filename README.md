<h1 align="center">Stream JSON Partial Parser</h1>
<p align="center">
  <a href="./README.md"><b>简体中文</b></a> |
  <a href="./README_EN.md">English</a>
</p>
<p align="center">
  面向 LLM 流式输出的 <b>JSON 增量解析 + 事件化输出</b> Demo
</p>

<p align="center">
  <a href="#项目展示">项目展示</a> ·
  <a href="#它解决什么问题">它解决什么问题</a> ·
  <a href="#难点与思路精简版">难点与思路（精简版）</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#性能概览">性能概览</a>
</p>

<hr />

<h2 id="项目展示">项目展示</h2>

<p><b>GIF 1</b>：展示“原始 LLM 一行一个 chunk”与“解析后给客户端的一行一个 chunk”对比。</p>
<figure>
  <img src="assets/demo/streaming_output.gif" alt="Streaming Output: raw llm chunks vs parsed chunks" width="100%" />
  <figcaption><code>Streaming Output</code>：左侧原始流，右侧解析后事件流。</figcaption>
</figure>

<p><b>GIF 2</b>：展示 <code>Report Preview</code> 页签中用户最终看到的效果。</p>
<figure>
  <img src="assets/demo/report_preview.gif" alt="Report Preview demo" width="100%" />
  <figcaption><code>Report Preview</code>：用户侧结构化报告实时预览。</figcaption>
</figure>

<hr />

<h2 id="它解决什么问题">它解决什么问题</h2>

<p>
  LLM 的输出是按 chunk 到达的，通常不是完整 JSON。<br />
  这个项目的目标是：<b>在 JSON 尚未闭合时，也能稳定输出可消费的增量事件</b>，同时避免中文乱码和字段串台。
</p>

<div>
  <b>一句话：</b>边收、边解析、边输出，而不是等完整 JSON 再一次性处理。
</div>

<hr />

<h2 id="难点与思路精简版">难点与思路（精简版）</h2>

<h3>主要难点</h3>
<ul>
  <li>chunk 边界不对齐：可能切在字符串中间、甚至 UTF-8 字符中间。</li>
  <li>业务字段语义不一致：有的字段可以实时发，有的必须完整发（如 ID）。</li>
  <li>性能波动大：chunk 越碎，状态切换和内存分配越重。</li>
</ul>

<h3>实现方式（概括）</h3>
<ol>
  <li>字节级状态机：按字节推进 JSON 语法状态。</li>
  <li>路径映射：把 JSON 路径映射为业务事件类型（如 <code>topic[0].name</code>）。</li>
  <li>输出策略分层：普通字段增量发，close-only 字段闭合后发。</li>
  <li>UTF-8 尾巴缓存：半个字符先缓存，下一 chunk 补齐再输出。</li>
</ol>

<hr />

<h2 id="快速开始">快速开始</h2>

<h3>1) 配置 <code>.env</code></h3>
<p>项目需要以下配置：</p>
<ul>
  <li><code>api_base</code>（必填）</li>
  <li><code>api_key</code>（必填）</li>
  <li><code>model</code>（必填）</li>
  <li><code>summary_lang</code>（可选，默认 <code>zh</code>，可设 <code>en</code>）</li>
</ul>

<pre><code>api_base=https://your-api-base/v1
api_key=your_api_key
model=your_model_name
summary_lang=zh
</code></pre>

<h3>2) 启动</h3>
<pre><code>go run web_server.go
</code></pre>

<p>默认地址：<code>http://127.0.0.1:8080</code></p>

<p>自定义端口示例：</p>
<pre><code>WEB_ADDR=:8081 go run web_server.go
</code></pre>

<hr />

<h2 id="性能概览">性能概览</h2>

<p>
  本地样例数据（命令：<code>go test -bench . -benchmem ./sdk/lexer</code>）。<br />
  环境：<code>darwin/arm64, Apple M4 Pro</code>
</p>

<p><b>Tip（列含义）</b></p>
<ul>
  <li><code>ns/op</code>：每次迭代平均耗时（越小越好）</li>
  <li><code>MB/s</code>：吞吐（越大越好）</li>
  <li><code>B/op</code>：每次迭代平均分配字节（越小越好）</li>
  <li><code>allocs/op</code>：每次迭代平均分配次数（越小越好）</li>
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
  <summary><b>如何理解这 5 组结果</b></summary>
  <ul>
    <li><b>chunk 粒度是第一影响因子</b>：同一常规 payload 下，切得越碎，性能越差。</li>
    <li><b>case 4/5 是极端压力测试</b>：用于观察 close-only 最坏上限，不代表常规业务分布。</li>
    <li><b>大 chunk 更友好</b>：吞吐更高、分配更低，这在常规和极端场景都成立。</li>
  </ul>
</details>

<hr />

<p align="center">
  如果你只想快速看核心：先看上面的两个 GIF，再看“难点与思路”和“性能概览”即可。
</p>
