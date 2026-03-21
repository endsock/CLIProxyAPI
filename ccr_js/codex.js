/**
 * CodexTransformer — Claude Code API ↔ Codex (Responses API) 格式转换。
 *
 * 对应 Go 实现:
 *   internal/translator/codex/claude/codex_claude_request.go
 *   internal/translator/codex/claude/codex_claude_response.go
 */

const TOOL_NAME_LIMIT = 64;

// 已知的思考级别后缀
const KNOWN_LEVELS = new Set([
  "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max",
]);

// 从模型名解析思考级别后缀
// 例: "gpt-5.4-xhigh" → { model: "gpt-5.4", level: "xhigh" }
//     "gpt-5.4-low"   → { model: "gpt-5.4", level: "low" }
//     "gpt-5.4"       → { model: "gpt-5.4", level: null }  (无后缀)
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

function buildShortNameMap(names) {
  const used = new Set(), m = {};
  const base = (n) => {
    if (n.length <= TOOL_NAME_LIMIT) return n;
    if (n.startsWith("mcp__")) {
      const i = n.lastIndexOf("__");
      if (i > 0) { let c = "mcp__" + n.slice(i + 2); return c.length > TOOL_NAME_LIMIT ? c.slice(0, TOOL_NAME_LIMIT) : c; }
    }
    return n.slice(0, TOOL_NAME_LIMIT);
  };
  const uniq = (c) => {
    if (!used.has(c)) return c;
    for (let i = 1; ; i++) { const s = "_" + i; let a = TOOL_NAME_LIMIT - s.length; if (a < 0) a = 0; const t = (c.length > a ? c.slice(0, a) : c) + s; if (!used.has(t)) return t; }
  };
  for (const n of names) { const u = uniq(base(n)); used.add(u); m[n] = u; }
  return m;
}

function buildOrigToShort(req) {
  if (!Array.isArray(req?.tools)) return {};
  const ns = req.tools.map(t => t.name).filter(Boolean);
  return ns.length ? buildShortNameMap(ns) : {};
}

function buildShortToOrig(req) {
  const fwd = buildOrigToShort(req), rev = {};
  for (const [o, s] of Object.entries(fwd)) rev[s] = o;
  return rev;
}

let _idCnt = 0;
function sanitizeToolID(id) {
  let s = (id || "").replace(/[^a-zA-Z0-9_-]/g, "_");
  if (!s) s = `toolu_${Date.now()}_${++_idCnt}`;
  return s;
}

function normParams(schema) {
  if (!schema || typeof schema !== "object") return { type: "object", properties: {} };
  const o = { ...schema }; delete o.$schema;
  if (!o.type) o.type = "object";
  if (o.type === "object" && !o.properties) o.properties = {};
  return o;
}

function extractUsage(u) {
  if (!u) return { inp: 0, out: 0, cached: 0 };
  let inp = u.input_tokens || 0;
  const out = u.output_tokens || 0;
  const cached = u.input_tokens_details?.cached_tokens || 0;
  if (cached > 0) inp = inp >= cached ? inp - cached : 0;
  return { inp, out, cached };
}

// ═════════════════════════════════════════════════════════════════════════
class CodexTransformer {
  name = "codex";
  constructor(options = {}) { this.options = options; }

  // ── Request: Claude Code API → Codex Responses API ────────────────────
  async transformRequestIn(request, provider) {
    const r = request;
    const body = { model: r.model, instructions: "", input: [] };

    // system → developer message
    if (r.system) {
      const parts = [];
      const add = (t) => { if (t && !t.startsWith("x-anthropic-billing-header: ")) parts.push({ type: "input_text", text: t }); };
      if (typeof r.system === "string") add(r.system);
      else if (Array.isArray(r.system)) for (const s of r.system) { if (s.type === "text") add(s.text || ""); }
      if (parts.length) body.input.push({ type: "message", role: "developer", content: parts });
    }

    // messages → input
    const tm = buildOrigToShort(r);
    if (Array.isArray(r.messages)) {
      for (const msg of r.messages) {
        const role = msg.role;
        let cur = { type: "message", role, content: [] }, has = false;
        const flush = () => { if (has) { body.input.push(cur); cur = { type: "message", role, content: [] }; has = false; } };
        const addTxt = (t) => { cur.content.push({ type: role === "assistant" ? "output_text" : "input_text", text: t }); has = true; };
        const addImg = (u) => { cur.content.push({ type: "input_image", image_url: u }); has = true; };

        if (Array.isArray(msg.content)) {
          for (const b of msg.content) {
            if (b.type === "text") { addTxt(b.text || ""); }
            else if (b.type === "image") {
              const src = b.source; if (!src) continue;
              const d = src.data || src.base64 || ""; if (!d) continue;
              addImg(`data:${src.media_type || src.mime_type || "application/octet-stream"};base64,${d}`);
            } else if (b.type === "tool_use") {
              flush();
              let name = b.name || ""; name = tm[name] || shortenName(name);
              body.input.push({ type: "function_call", call_id: b.id || "", name, arguments: typeof b.input === "string" ? b.input : JSON.stringify(b.input || {}) });
            } else if (b.type === "tool_result") {
              flush();
              const fco = { type: "function_call_output", call_id: b.tool_use_id || "" };
              if (Array.isArray(b.content)) {
                const ps = [];
                for (const c of b.content) {
                  if (c.type === "image") {
                    const src = c.source; if (!src) continue;
                    const d = src.data || src.base64 || ""; if (!d) continue;
                    ps.push({ type: "input_image", image_url: `data:${src.media_type || src.mime_type || "application/octet-stream"};base64,${d}` });
                  } else if (c.type === "text") { ps.push({ type: "input_text", text: c.text || "" }); }
                }
                fco.output = ps.length ? ps : (typeof b.content === "string" ? b.content : "");
              } else { fco.output = typeof b.content === "string" ? b.content : ""; }
              body.input.push(fco);
            }
          }
          flush();
        } else if (typeof msg.content === "string") { addTxt(msg.content); flush(); }
      }
    }

    // tools
    if (Array.isArray(r.tools) && r.tools.length) {
      body.tools = []; body.tool_choice = "auto";
      const ns = r.tools.map(t => t.name).filter(Boolean);
      const sm = buildShortNameMap(ns);
      for (const t of r.tools) {
        if (t.type === "web_search_20250305") { body.tools.push({ type: "web_search" }); continue; }
        let n = t.name || ""; n = sm[n] || shortenName(n);
        body.tools.push({ type: "function", name: n, description: t.description, parameters: normParams(t.input_schema), strict: false });
      }
    }

    // extra
    body.parallel_tool_calls = true;

    // 从模型名解析思考级别后缀: gpt-5.4-xhigh → level=xhigh, gpt-5.4 → 默认 minimal
    const parsed = parseModelSuffix(body.model);
    body.model = parsed.model; // 去掉后缀后的真实模型名
    const suffixLevel = parsed.level || "minimal"; // 无后缀默认 minimal

    // thinking → reasoning (后缀优先，覆盖 Claude 请求中的 thinking 配置)
    let effort = suffixLevel;
    body.reasoning = { effort, summary: "auto" };
    body.stream = true; body.store = false;
    body.include = ["reasoning.encrypted_content"];

    const baseUrl = (provider.baseUrl || "https://api.openai.com").replace(/\/+$/, "");
    const config = { url: new URL(`${baseUrl}/v1/responses`), headers: { "Content-Type": "application/json" } };
    if (provider.apiKey) config.headers.Authorization = `Bearer ${provider.apiKey}`;
    return { body, config };
  }

  // ── Response: Codex → Claude Code SSE ─────────────────────────────────
  async transformResponseOut(response, context) {
    const cReq = context.req?.originalBody || {};
    const rev = buildShortToOrig(cReq);
    const ct = response.headers.get("Content-Type") || "";

    if (ct.includes("application/json")) {
      const json = await response.json();
      return new Response(JSON.stringify(this._nonStream(json, rev, context)), {
        status: response.status, statusText: response.statusText, headers: response.headers });
    }

    if (!response.body) return response;
    const dec = new TextDecoder(), enc = new TextEncoder(), self = this;
    const st = { hasTool: false, bi: 0, gotArgDelta: false };
    const stream = new ReadableStream({
      async start(ctrl) {
        const rd = response.body.getReader(); let buf = "";
        try {
          while (true) {
            const { done, value } = await rd.read();
            if (done) { if (buf.trim()) self._line(buf, st, rev, ctrl, enc); break; }
            buf += dec.decode(value, { stream: true });
            const ls = buf.split("\n"); buf = ls.pop() || "";
            for (const l of ls) if (l.trim()) self._line(l, st, rev, ctrl, enc);
          }
        } catch (e) { ctrl.error(e); } finally { ctrl.close(); }
      }
    });
    return new Response(stream, {
      status: response.status, statusText: response.statusText,
      headers: new Headers({ "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" }) });
  }

  _line(line, st, rev, ctrl, enc) {
    if (!line.startsWith("data:")) return;
    const raw = line.slice(5).trim();
    if (!raw || raw === "[DONE]") return;
    let ev; try { ev = JSON.parse(raw); } catch { return; }
    const o = this._ev(ev, ev.type, st, rev);
    if (o) ctrl.enqueue(enc.encode(o));
  }

  _ev(ev, type, st, rev) {
    const J = JSON.stringify;
    if (type === "response.created") {
      const r = ev.response || {};
      return `event: message_start\ndata: ${J({ type: "message_start", message: { id: r.id || "", type: "message", role: "assistant", model: r.model || "", stop_sequence: null, usage: { input_tokens: 0, output_tokens: 0 }, content: [], stop_reason: null }})}\n\n`;
    }
    if (type === "response.reasoning_summary_part.added")
      return `event: content_block_start\ndata: ${J({ type: "content_block_start", index: st.bi, content_block: { type: "thinking", thinking: "" }})}\n\n`;
    if (type === "response.reasoning_summary_text.delta")
      return `event: content_block_delta\ndata: ${J({ type: "content_block_delta", index: st.bi, delta: { type: "thinking_delta", thinking: ev.delta || "" }})}\n\n`;
    if (type === "response.reasoning_summary_part.done") {
      const o = `event: content_block_stop\ndata: ${J({ type: "content_block_stop", index: st.bi })}\n\n`;
      st.bi++; return o;
    }
    if (type === "response.content_part.added")
      return `event: content_block_start\ndata: ${J({ type: "content_block_start", index: st.bi, content_block: { type: "text", text: "" }})}\n\n`;
    if (type === "response.output_text.delta")
      return `event: content_block_delta\ndata: ${J({ type: "content_block_delta", index: st.bi, delta: { type: "text_delta", text: ev.delta || "" }})}\n\n`;
    if (type === "response.content_part.done") {
      const o = `event: content_block_stop\ndata: ${J({ type: "content_block_stop", index: st.bi })}\n\n`;
      st.bi++; return o;
    }
    if (type === "response.completed") {
      const r = ev.response || {};
      const stop = st.hasTool ? "tool_use" : (r.stop_reason === "max_tokens" || r.stop_reason === "stop" ? r.stop_reason : "end_turn");
      const { inp, out: ot, cached } = extractUsage(r.usage);
      const usage = { input_tokens: inp, output_tokens: ot };
      if (cached > 0) usage.cache_read_input_tokens = cached;
      return `event: message_delta\ndata: ${J({ type: "message_delta", delta: { stop_reason: stop, stop_sequence: null }, usage })}\n\nevent: message_stop\ndata: {"type":"message_stop"}\n\n`;
    }
    if (type === "response.output_item.added") {
      const it = ev.item || {};
      if (it.type === "function_call") {
        st.hasTool = true; st.gotArgDelta = false;
        let n = it.name || ""; if (rev[n]) n = rev[n];
        return `event: content_block_start\ndata: ${J({ type: "content_block_start", index: st.bi, content_block: { type: "tool_use", id: sanitizeToolID(it.call_id || ""), name: n, input: {} }})}\n\nevent: content_block_delta\ndata: ${J({ type: "content_block_delta", index: st.bi, delta: { type: "input_json_delta", partial_json: "" }})}\n\n`;
      }
    }
    if (type === "response.output_item.done") {
      if ((ev.item || {}).type === "function_call") {
        const o = `event: content_block_stop\ndata: ${J({ type: "content_block_stop", index: st.bi })}\n\n`;
        st.bi++; return o;
      }
    }
    if (type === "response.function_call_arguments.delta") {
      st.gotArgDelta = true;
      return `event: content_block_delta\ndata: ${J({ type: "content_block_delta", index: st.bi, delta: { type: "input_json_delta", partial_json: ev.delta || "" }})}\n\n`;
    }
    if (type === "response.function_call_arguments.done") {
      if (!st.gotArgDelta && ev.arguments)
        return `event: content_block_delta\ndata: ${J({ type: "content_block_delta", index: st.bi, delta: { type: "input_json_delta", partial_json: ev.arguments }})}\n\n`;
    }
    return "";
  }

  _nonStream(json, rev, ctx) {
    const rd = json.response || json;
    const o = {
      id: rd.id || "", type: "message", role: "assistant",
      model: rd.model || ctx.req?.orgiModel || "",
      content: [], stop_reason: null, stop_sequence: null,
      usage: { input_tokens: 0, output_tokens: 0 }
    };
    const { inp, out: ot, cached } = extractUsage(rd.usage);
    o.usage.input_tokens = inp; o.usage.output_tokens = ot;
    if (cached > 0) o.usage.cache_read_input_tokens = cached;

    let hasTool = false;
    if (Array.isArray(rd.output)) for (const it of rd.output) {
      if (it.type === "reasoning") {
        let txt = "";
        const sm = it.summary;
        if (sm) txt = Array.isArray(sm) ? sm.map(p => p.text ?? String(p)).join("") : String(sm);
        if (!txt && it.content) {
          const ct = it.content;
          txt = Array.isArray(ct) ? ct.map(p => p.text ?? String(p)).join("") : String(ct);
        }
        if (txt) o.content.push({ type: "thinking", thinking: txt });
      } else if (it.type === "message") {
        const ct = it.content;
        if (Array.isArray(ct)) for (const p of ct) { if (p.type === "output_text" && p.text) o.content.push({ type: "text", text: p.text }); }
        else if (typeof ct === "string" && ct) o.content.push({ type: "text", text: ct });
      } else if (it.type === "function_call") {
        hasTool = true;
        let n = it.name || ""; if (rev[n]) n = rev[n];
        let inp = {};
        try { const p = JSON.parse(it.arguments || ""); if (p && typeof p === "object" && !Array.isArray(p)) inp = p; } catch {}
        o.content.push({ type: "tool_use", id: sanitizeToolID(it.call_id || ""), name: n, input: inp });
      }
    }
    o.stop_reason = rd.stop_reason || (hasTool ? "tool_use" : "end_turn");
    if (rd.stop_sequence) o.stop_sequence = rd.stop_sequence;
    return o;
  }
}

module.exports = CodexTransformer;
