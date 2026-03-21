/**
 * CodexTransformer — Claude Code API ↔ Codex (Responses API) 格式转换。
 *
 * 对应 Go 实现:
 *   internal/translator/codex/claude/codex_claude_request.go
 *   internal/translator/codex/claude/codex_claude_response.go
 *
 * 主要功能:
 *   1. transformRequestIn: 将 Claude Code API 请求转换为 Codex (OpenAI Responses API) 格式
 *   2. transformResponseOut: 将 Codex 响应转换回 Claude Code SSE 格式
 */

// 工具名称最大长度限制 (Codex/Responses API 限制)
const TOOL_NAME_LIMIT = 64;

// 已知的思考级别后缀 (从模型名中解析)
const KNOWN_LEVELS = new Set([
  "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max",
]);

/**
 * 从模型名解析思考级别后缀
 *
 * 例: "gpt-5.4-xhigh" → { model: "gpt-5.4", level: "xhigh" }
 *     "gpt-5.4-low"   → { model: "gpt-5.4", level: "low" }
 *     "gpt-5.4"       → { model: "gpt-5.4", level: null }  (无后缀)
 *
 * @param {string} model - 模型名称
 * @returns {{model: string, level: string|null}} 解析后的模型名和级别
 */
function parseModelSuffix(model) {
  const idx = model.lastIndexOf("-");
  if (idx > 0) {
    const suffix = model.slice(idx + 1).toLowerCase();
    if (KNOWN_LEVELS.has(suffix)) {
      return { model: model.slice(0, idx), level: suffix };
    }
  }
  return { model, level: null };
}

/**
 * 缩短工具名称以符合 Codex API 的 64 字符限制
 *
 * 对于 mcp__ 开头的名称，保留前缀并截断后面的部分
 * 例: "mcp__very_long_server_name__some_tool_name" → "mcp__some_tool_name"
 *
 * @param {string} name - 原始工具名称
 * @returns {string} 缩短后的名称
 */
function shortenName(name) {
  if (name.length <= TOOL_NAME_LIMIT) return name;
  if (name.startsWith("mcp__")) {
    const idx = name.lastIndexOf("__");
    if (idx > 0) {
      const c = "mcp__" + name.slice(idx + 2);
      return c.length > TOOL_NAME_LIMIT ? c.slice(0, TOOL_NAME_LIMIT) : c;
    }
  }
  return name.slice(0, TOOL_NAME_LIMIT);
}

/**
 * 构建名称映射表 (原始名称 → 缩短名称)
 *
 * 处理名称冲突：如果多个工具名称截断后相同，会添加数字后缀区分
 * 例: ["tool_very_long_name_1", "tool_very_long_name_2"]
 *     → { "tool_very_long_name_1": "tool_very_long_name_1", "tool_very_long_name_2": "tool_very_long_name_2_2" }
 *
 * @param {string[]} names - 工具名称数组
 * @returns {Object} 原始名称到缩短名称的映射
 */
function buildShortNameMap(names) {
  const used = new Set(), m = {};  // used: 已使用的缩短名称, m: 映射表

  // base: 获取基础缩短名称
  const base = (n) => {
    if (n.length <= TOOL_NAME_LIMIT) return n;
    if (n.startsWith("mcp__")) {
      const i = n.lastIndexOf("__");
      if (i > 0) {
        let c = "mcp__" + n.slice(i + 2);
        return c.length > TOOL_NAME_LIMIT ? c.slice(0, TOOL_NAME_LIMIT) : c;
      }
    }
    return n.slice(0, TOOL_NAME_LIMIT);
  };

  // uniq: 确保名称唯一，冲突时添加数字后缀
  const uniq = (c) => {
    if (!used.has(c)) return c;
    for (let i = 1; ; i++) {
      const s = "_" + i;
      let a = TOOL_NAME_LIMIT - s.length;
      if (a < 0) a = 0;
      const t = (c.length > a ? c.slice(0, a) : c) + s;
      if (!used.has(t)) return t;
    }
  };

  // 为每个名称生成唯一的缩短名称
  for (const n of names) {
    const u = uniq(base(n));
    used.add(u);
    m[n] = u;
  }
  return m;
}

/**
 * 构建原始名称到缩短名称的映射表
 *
 * 从请求的 tools 数组中提取所有工具名称，生成映射
 *
 * @param {Object} req - Claude Code API 请求对象
 * @returns {Object} 原始名称 → 缩短名称的映射
 */
function buildOrigToShort(req) {
  if (!Array.isArray(req?.tools)) return {};
  const ns = req.tools.map(t => t.name).filter(Boolean);
  return ns.length ? buildShortNameMap(ns) : {};
}

/**
 * 构建缩短名称到原始名称的反向映射表
 *
 * @param {Object} req - Claude Code API 请求对象
 * @returns {Object} 缩短名称 → 原始名称的映射
 */
function buildShortToOrig(req) {
  const fwd = buildOrigToShort(req), rev = {};
  for (const [o, s] of Object.entries(fwd)) rev[s] = o;
  return rev;
}

// 工具 ID 计数器 (用于生成唯一 ID)
let _idCnt = 0;

/**
 * 清理/规范化工具 ID
 *
 * 将 ID 中非法字符替换为下划线，确保符合要求
 * 如果 ID 为空，则生成一个唯一的工具 ID
 *
 * @param {string} id - 原始工具 ID
 * @returns {string} 清理后的工具 ID
 */
function sanitizeToolID(id) {
  let s = (id || "").replace(/[^a-zA-Z0-9_-]/g, "_");
  if (!s) s = `toolu_${Date.now()}_${++_idCnt}`;
  return s;
}

/**
 * 规范化工具参数 schema
 *
 * 移除 $schema 字段，确保有 type 和 properties 字段
 *
 * @param {Object} schema - 原始 JSON Schema
 * @returns {Object} 规范化后的 schema
 */
function normParams(schema) {
  if (!schema || typeof schema !== "object") return { type: "object", properties: {} };
  const o = { ...schema };
  delete o.$schema;  // 移除 $schema 字段
  if (!o.type) o.type = "object";
  if (o.type === "object" && !o.properties) o.properties = {};
  return o;
}

/**
 * 提取并计算使用量信息
 *
 * 处理缓存 token 的计算: 缓存的 token 不计入输入 token
 *
 * @param {Object} u - 使用量对象
 * @returns {{inp: number, out: number, cached: number}}
 *   - inp: 实际输入 token (扣除缓存)
 *   - out: 输出 token
 *   - cached: 缓存的 token
 */
function extractUsage(u) {
  if (!u) return { inp: 0, out: 0, cached: 0 };
  let inp = u.input_tokens || 0;
  const out = u.output_tokens || 0;
  const cached = u.input_tokens_details?.cached_tokens || 0;
  // 缓存 token 需要从输入中扣除
  if (cached > 0) inp = inp >= cached ? inp - cached : 0;
  return { inp, out, cached };
}

// ═════════════════════════════════════════════════════════════════════════
/**
 * CodexTransformer 类
 *
 * 负责 Claude Code API 与 Codex (Responses API) 之间的格式转换
 */
class CodexTransformer {
  name = "codex";  // 转换器名称

  /**
   * 构造函数
   * @param {Object} options - 选项配置
   */
  constructor(options = {}) {
    this.options = options;
  }

  // ── Request: Claude Code API → Codex Responses API ────────────────────

  /**
   * 转换请求: Claude Code API → Codex Responses API
   *
   * 处理内容:
   * 1. system → developer message
   * 2. messages → input (含 text, image, tool_use, tool_result)
   * 3. tools → function tools
   * 4. thinking level → reasoning effort
   *
   * @param {Object} request - Claude Code API 请求对象
   * @param {Object} provider - provider 配置 (baseUrl, apiKey)
   * @returns {{body: Object, config: Object}} Codex 格式请求体和配置
   */
  async transformRequestIn(request, provider) {
    const r = request;
    // 构建 Codex Responses API 请求体
    const body = { model: r.model, instructions: "", input: [] };

    // ── system 消息处理 ──
    // Claude Code 的 system 消息转换为 Codex 的 developer 消息
    if (r.system) {
      const parts = [];
      // 过滤掉计费相关的 header
      const add = (t) => {
        if (t && !t.startsWith("x-anthropic-billing-header: ")) {
          parts.push({ type: "input_text", text: t });
        }
      };

      if (typeof r.system === "string") {
        add(r.system);
      } else if (Array.isArray(r.system)) {
        for (const s of r.system) {
          if (s.type === "text") add(s.text || "");
        }
      }
      if (parts.length) {
        body.input.push({ type: "message", role: "developer", content: parts });
      }
    }

    // ── messages 处理 ──
    // 将 Claude messages 转换为 Codex input 格式
    const tm = buildOrigToShort(r);  // 工具名称映射 (原始 → 缩短)
    if (Array.isArray(r.messages)) {
      for (const msg of r.messages) {
        const role = msg.role;
        let cur = { type: "message", role, content: [] }, has = false;

        // flush: 刷新当前消息块到 input 数组
        const flush = () => {
          if (has) {
            body.input.push(cur);
            cur = { type: "message", role, content: [] };
            has = false;
          }
        };

        // 添加文本内容
        const addTxt = (t) => {
          cur.content.push({
            type: role === "assistant" ? "output_text" : "input_text",
            text: t
          });
          has = true;
        };

        // 添加图片内容 (转为 base64 data URL)
        const addImg = (u) => {
          cur.content.push({ type: "input_image", image_url: u });
          has = true;
        };

        // 处理消息内容数组
        if (Array.isArray(msg.content)) {
          for (const b of msg.content) {
            if (b.type === "text") {
              // 文本内容
              addTxt(b.text || "");
            } else if (b.type === "image") {
              // 图片内容 - 转换为 base64 data URL
              const src = b.source;
              if (!src) continue;
              const d = src.data || src.base64 || "";
              if (!d) continue;
              addImg(`data:${src.media_type || src.mime_type || "application/octet-stream"};base64,${d}`);
            } else if (b.type === "tool_use") {
              // 工具调用请求 → Codex function_call
              flush();
              let name = b.name || "";
              name = tm[name] || shortenName(name);  // 使用缩短后的名称
              body.input.push({
                type: "function_call",
                call_id: b.id || "",
                name,
                arguments: typeof b.input === "string" ? b.input : JSON.stringify(b.input || {})
              });
            } else if (b.type === "tool_result") {
              // 工具结果 → Codex function_call_output
              flush();
              const fco = { type: "function_call_output", call_id: b.tool_use_id || "" };
              if (Array.isArray(b.content)) {
                const ps = [];
                for (const c of b.content) {
                  if (c.type === "image") {
                    // 工具结果中的图片
                    const src = c.source;
                    if (!src) continue;
                    const d = src.data || src.base64 || "";
                    if (!d) continue;
                    ps.push({
                      type: "input_image",
                      image_url: `data:${src.media_type || src.mime_type || "application/octet-stream"};base64,${d}`
                    });
                  } else if (c.type === "text") {
                    // 工具结果中的文本
                    ps.push({ type: "input_text", text: c.text || "" });
                  }
                }
                fco.output = ps.length ? ps : (typeof b.content === "string" ? b.content : "");
              } else {
                fco.output = typeof b.content === "string" ? b.content : "";
              }
              body.input.push(fco);
            }
          }
          flush();
        } else if (typeof msg.content === "string") {
          // 简单的字符串消息内容
          addTxt(msg.content);
          flush();
        }
      }
    }

    // ── tools 处理 ──
    // 将 Claude tools 转换为 Codex function tools
    if (Array.isArray(r.tools) && r.tools.length) {
      body.tools = [];
      body.tool_choice = "auto";
      const ns = r.tools.map(t => t.name).filter(Boolean);
      const sm = buildShortNameMap(ns);  // 生成工具名称映射

      for (const t of r.tools) {
        if (t.type === "web_search_20250305") {
          // 特殊类型: web_search
          body.tools.push({ type: "web_search" });
          continue;
        }
        let n = t.name || "";
        n = sm[n] || shortenName(n);  // 使用缩短后的名称
        body.tools.push({
          type: "function",
          name: n,
          description: t.description,
          parameters: normParams(t.input_schema),
          strict: false  // 不使用 strict 模式
        });
      }
    }

    // ── 额外配置 ──
    body.parallel_tool_calls = true;  // 启用并行工具调用

    // ── 思考级别处理 ──
    // 从模型名解析思考级别后缀: gpt-5.4-xhigh → level=xhigh, gpt-5.4 → 默认 minimal
    const parsed = parseModelSuffix(body.model);
    body.model = parsed.model;  // 去掉后缀后的真实模型名
    const suffixLevel = parsed.level || "minimal";  // 无后缀默认 minimal

    // thinking → reasoning (后缀优先，覆盖 Claude 请求中的 thinking 配置)
    let effort = suffixLevel;
    body.reasoning = { effort, summary: "auto" };
    body.stream = true;
    body.store = false;
    body.include = ["reasoning.encrypted_content"];

    // ── 构建请求配置 ──
    const baseUrl = (provider.baseUrl || "https://api.openai.com").replace(/\/+$/, "");
    const config = {
      url: new URL(`${baseUrl}/v1/responses`),
      headers: { "Content-Type": "application/json" }
    };
    if (provider.apiKey) config.headers.Authorization = `Bearer ${provider.apiKey}`;

    return { body, config };
  }

  // ── Response: Codex → Claude Code SSE ─────────────────────────────────

  /**
   * 转换响应: Codex SSE 流 → Claude Code SSE 格式
   *
   * 支持两种模式:
   * 1. 流式响应: 将 Codex SSE 事件转换为 Claude SSE 事件
   * 2. 非流式响应: 将 Codex JSON 响应转换为 Claude 格式
   *
   * @param {Response} response - Codex 响应对象
   * @param {Object} context - 上下文 (包含原始请求等)
   * @returns {Response} Claude Code 格式响应
   */
  async transformResponseOut(response, context) {
    const cReq = context.req?.originalBody || {};
    const rev = buildShortToOrig(cReq);  // 反向映射 (缩短名称 → 原始名称)
    const ct = response.headers.get("Content-Type") || "";

    // ── 非流式响应处理 ──
    if (ct.includes("application/json")) {
      const json = await response.json();
      return new Response(JSON.stringify(this._nonStream(json, rev, context)), {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers
      });
    }

    // ── 流式响应处理 ──
    if (!response.body) return response;

    const dec = new TextDecoder(), enc = new TextEncoder(), self = this;
    const st = { hasTool: false, bi: 0, gotArgDelta: false };

    // 构建 SSE 流
    const stream = new ReadableStream({
      async start(ctrl) {
        const rd = response.body.getReader();
        let buf = "";
        try {
          while (true) {
            const { done, value } = await rd.read();
            if (done) {
              if (buf.trim()) self._line(buf, st, rev, ctrl, enc);
              break;
            }
            buf += dec.decode(value, { stream: true });
            const ls = buf.split("\n");
            buf = ls.pop() || "";
            for (const l of ls)
              if (l.trim()) self._line(l, st, rev, ctrl, enc);
          }
        } catch (e) {
          ctrl.error(e);
        } finally {
          ctrl.close();
        }
      }
    });

    return new Response(stream, {
      status: response.status,
      statusText: response.statusText,
      headers: new Headers({
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive"
      })
    });
  }

  /**
   * 处理单行 SSE 数据
   *
   * 解析 "data: ..." 行，提取 JSON 并转换为 Claude SSE 格式
   *
   * @param {string} line - 原始 SSE 行
   * @param {Object} st - 状态对象
   * @param {Object} rev - 名称反向映射
   * @param {ReadableStreamDefaultController} ctrl - 流控制器
   * @param {TextEncoder} enc - 文本编码器
   */
  _line(line, st, rev, ctrl, enc) {
    if (!line.startsWith("data:")) return;
    const raw = line.slice(5).trim();
    if (!raw || raw === "[DONE]") return;  // [DONE] 表示流结束

    let ev;
    try {
      ev = JSON.parse(raw);
    } catch {
      return;  // 解析失败，跳过
    }

    const o = this._ev(ev, ev.type, st, rev);
    if (o) ctrl.enqueue(enc.encode(o));
  }

  /**
   * 将 Codex 事件转换为 Claude SSE 事件
   *
   * Codex 事件类型映射:
   *   - response.created → message_start
   *   - response.reasoning_summary_part.added → content_block_start (thinking)
   *   - response.reasoning_summary_text.delta → content_block_delta (thinking_delta)
   *   - response.reasoning_summary_part.done → content_block_stop
   *   - response.content_part.added → content_block_start (text)
   *   - response.output_text.delta → content_block_delta (text_delta)
   *   - response.content_part.done → content_block_stop
   *   - response.completed → message_delta + message_stop
   *   - response.output_item.added → content_block_start (tool_use)
   *   - response.output_item.done → content_block_stop (tool_use)
   *   - response.function_call_arguments.delta → content_block_delta (input_json_delta)
   *   - response.function_call_arguments.done → content_block_delta (完成工具参数)
   *
   * @param {Object} ev - Codex 事件对象
   * @param {string} type - 事件类型
   * @param {Object} st - 状态对象
   * @param {Object} rev - 名称反向映射
   * @returns {string} Claude SSE 格式字符串
   */
  _ev(ev, type, st, rev) {
    const J = JSON.stringify;

    // ── 消息开始 ──
    if (type === "response.created") {
      const r = ev.response || {};
      return `event: message_start\ndata: ${J({
        type: "message_start",
        message: {
          id: r.id || "",
          type: "message",
          role: "assistant",
          model: r.model || "",
          stop_sequence: null,
          usage: { input_tokens: 0, output_tokens: 0 },
          content: [],
          stop_reason: null
        }
      })}\n\n`;
    }

    // ── 思考/推理内容 ──
    if (type === "response.reasoning_summary_part.added") {
      // 思考开始
      return `event: content_block_start\ndata: ${J({
        type: "content_block_start",
        index: st.bi,
        content_block: { type: "thinking", thinking: "" }
      })}\n\n`;
    }

    if (type === "response.reasoning_summary_text.delta") {
      // 思考内容增量
      return `event: content_block_delta\ndata: ${J({
        type: "content_block_delta",
        index: st.bi,
        delta: { type: "thinking_delta", thinking: ev.delta || "" }
      })}\n\n`;
    }

    if (type === "response.reasoning_summary_part.done") {
      // 思考结束
      const o = `event: content_block_stop\ndata: ${J({ type: "content_block_stop", index: st.bi })}\n\n`;
      st.bi++;
      return o;
    }

    // ── 文本内容 ──
    if (type === "response.content_part.added") {
      // 文本块开始
      return `event: content_block_start\ndata: ${J({
        type: "content_block_start",
        index: st.bi,
        content_block: { type: "text", text: "" }
      })}\n\n`;
    }

    if (type === "response.output_text.delta") {
      // 文本内容增量
      return `event: content_block_delta\ndata: ${J({
        type: "content_block_delta",
        index: st.bi,
        delta: { type: "text_delta", text: ev.delta || "" }
      })}\n\n`;
    }

    if (type === "response.content_part.done") {
      // 文本块结束
      const o = `event: content_block_stop\ndata: ${J({ type: "content_block_stop", index: st.bi })}\n\n`;
      st.bi++;
      return o;
    }

    // ── 消息完成 ──
    if (type === "response.completed") {
      const r = ev.response || {};
      // 判断停止原因: 如果有工具调用则用 tool_use，否则用模型返回的 stop_reason
      const stop = st.hasTool ? "tool_use" : (r.stop_reason === "max_tokens" || r.stop_reason === "stop" ? r.stop_reason : "end_turn");
      const { inp, out: ot, cached } = extractUsage(r.usage);
      const usage = { input_tokens: inp, output_tokens: ot };
      if (cached > 0) usage.cache_read_input_tokens = cached;

      return `event: message_delta\ndata: ${J({
        type: "message_delta",
        delta: { stop_reason: stop, stop_sequence: null },
        usage
      })}\n\nevent: message_stop\ndata: {"type":"message_stop"}\n\n`;
    }

    // ── 工具调用 ──
    if (type === "response.output_item.added") {
      const it = ev.item || {};
      if (it.type === "function_call") {
        st.hasTool = true;
        st.gotArgDelta = false;
        // 将缩短的名称转换回原始名称
        let n = it.name || "";
        if (rev[n]) n = rev[n];

        return `event: content_block_start\ndata: ${J({
          type: "content_block_start",
          index: st.bi,
          content_block: { type: "tool_use", id: sanitizeToolID(it.call_id || ""), name: n, input: {} }
        })}\n\nevent: content_block_delta\ndata: ${J({
          type: "content_block_delta",
          index: st.bi,
          delta: { type: "input_json_delta", partial_json: "" }
        })}\n\n`;
      }
    }

    if (type === "response.output_item.done") {
      if ((ev.item || {}).type === "function_call") {
        // 工具调用块结束
        const o = `event: content_block_stop\ndata: ${J({ type: "content_block_stop", index: st.bi })}\n\n`;
        st.bi++;
        return o;
      }
    }

    // ── 工具参数 ──
    if (type === "response.function_call_arguments.delta") {
      // 工具参数增量
      st.gotArgDelta = true;
      return `event: content_block_delta\ndata: ${J({
        type: "content_block_delta",
        index: st.bi,
        delta: { type: "input_json_delta", partial_json: ev.delta || "" }
      })}\n\n`;
    }

    if (type === "response.function_call_arguments.done") {
      // 工具参数完成 (有些 API 会一次性返回完整参数)
      if (!st.gotArgDelta && ev.arguments)
        return `event: content_block_delta\ndata: ${J({
          type: "content_block_delta",
          index: st.bi,
          delta: { type: "input_json_delta", partial_json: ev.arguments }
        })}\n\n`;
    }

    return "";  // 未知事件类型，返回空
  }

  /**
   * 处理非流式响应
   *
   * 将 Codex JSON 响应转换为 Claude Code 格式
   *
   * @param {Object} json - Codex JSON 响应
   * @param {Object} rev - 名称反向映射
   * @param {Object} ctx - 上下文
   * @returns {Object} Claude 格式响应对象
   */
  _nonStream(json, rev, ctx) {
    const rd = json.response || json;  // 兼容不同响应格式

    const o = {
      id: rd.id || "",
      type: "message",
      role: "assistant",
      model: rd.model || ctx.req?.orgiModel || "",
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage: { input_tokens: 0, output_tokens: 0 }
    };

    // 提取使用量
    const { inp, out: ot, cached } = extractUsage(rd.usage);
    o.usage.input_tokens = inp;
    o.usage.output_tokens = ot;
    if (cached > 0) o.usage.cache_read_input_tokens = cached;

    let hasTool = false;

    // 处理 output 数组
    if (Array.isArray(rd.output)) {
      for (const it of rd.output) {
        if (it.type === "reasoning") {
          // 思考/推理内容
          let txt = "";
          const sm = it.summary;
          if (sm) {
            txt = Array.isArray(sm) ? sm.map(p => p.text ?? String(p)).join("") : String(sm);
          }
          if (!txt && it.content) {
            const ct = it.content;
            txt = Array.isArray(ct) ? ct.map(p => p.text ?? String(p)).join("") : String(ct);
          }
          if (txt) o.content.push({ type: "thinking", thinking: txt });

        } else if (it.type === "message") {
          // 文本消息
          const ct = it.content;
          if (Array.isArray(ct)) {
            for (const p of ct) {
              if (p.type === "output_text" && p.text) {
                o.content.push({ type: "text", text: p.text });
              }
            }
          } else if (typeof ct === "string" && ct) {
            o.content.push({ type: "text", text: ct });
          }

        } else if (it.type === "function_call") {
          // 工具调用
          hasTool = true;
          let n = it.name || "";
          if (rev[n]) n = rev[n];  // 转换回原始名称

          let inp = {};
          try {
            const p = JSON.parse(it.arguments || "");
            if (p && typeof p === "object" && !Array.isArray(p)) inp = p;
          } catch {}

          o.content.push({
            type: "tool_use",
            id: sanitizeToolID(it.call_id || ""),
            name: n,
            input: inp
          });
        }
      }
    }

    // 确定 stop_reason
    o.stop_reason = rd.stop_reason || (hasTool ? "tool_use" : "end_turn");
    if (rd.stop_sequence) o.stop_sequence = rd.stop_sequence;

    return o;
  }
}

module.exports = CodexTransformer;
