/**
 * CodexOAITransformer — OpenAI Chat Completions API ↔ Codex (Responses API) 格式转换。
 *
 * 对应 Go 实现:
 *   internal/translator/codex/openai/chat-completions/codex_openai_request.go
 *   internal/translator/codex/openai/chat-completions/codex_openai_response.go
 */

const TOOL_NAME_LIMIT = 64;

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
  const ns = req.tools.map(t => t.function?.name).filter(Boolean);
  return ns.length ? buildShortNameMap(ns) : {};
}

function buildShortToOrig(req) {
  const fwd = buildOrigToShort(req), rev = {};
  for (const [o, s] of Object.entries(fwd)) rev[s] = o;
  return rev;
}

// ═════════════════════════════════════════════════════════════════════════
class CodexOAITransformer {
  name = "codex_oai";
  constructor(options = {}) { this.options = options; }

  // ── Request: OpenAI Chat Completions → Codex Responses API ────────────
  async transformRequestIn(request, provider) {
    const r = request;
    const body = { model: r.model, instructions: "", input: [], stream: r.stream ?? true, store: false };

    // reasoning
    const effort = r.reasoning_effort || "medium";
    body.reasoning = { effort, summary: "auto" };
    body.parallel_tool_calls = true;
    body.include = ["reasoning.encrypted_content"];

    // 构建工具名映射
    const tm = buildOrigToShort(r);

    // messages → input
    if (Array.isArray(r.messages)) {
      for (const msg of r.messages) {
        const role = msg.role;

        if (role === "tool") {
          // tool 消息转为 function_call_output
          body.input.push({
            type: "function_call_output",
            call_id: msg.tool_call_id || "",
            output: msg.content || ""
          });
          continue;
        }

        // 常规消息
        const m = { type: "message", role: role === "system" ? "developer" : role, content: [] };
        const c = msg.content;

        if (typeof c === "string" && c) {
          m.content.push({ type: role === "assistant" ? "output_text" : "input_text", text: c });
        } else if (Array.isArray(c)) {
          for (const item of c) {
            if (item.type === "text") {
              m.content.push({ type: role === "assistant" ? "output_text" : "input_text", text: item.text || "" });
            } else if (item.type === "image_url" && role === "user") {
              m.content.push({ type: "input_image", image_url: item.image_url?.url || "" });
            } else if (item.type === "file" && role === "user") {
              const part = { type: "input_file", file_data: item.file?.file_data || "" };
              if (item.file?.filename) part.filename = item.file.filename;
              m.content.push(part);
            }
          }
        }

        // 只有内容非空才添加消息
        if (m.content.length > 0 || role !== "assistant") {
          body.input.push(m);
        }

        // 处理 assistant 的 tool_calls
        if (role === "assistant" && Array.isArray(msg.tool_calls)) {
          for (const tc of msg.tool_calls) {
            if (tc.type === "function") {
              let name = tc.function?.name || "";
              name = tm[name] || shortenName(name);
              body.input.push({
                type: "function_call",
                call_id: tc.id || "",
                name,
                arguments: tc.function?.arguments || ""
              });
            }
          }
        }
      }
    }

    // response_format
    if (r.response_format) {
      body.text = body.text || {};
      const rf = r.response_format;
      if (rf.type === "text") {
        body.text.format = { type: "text" };
      } else if (rf.type === "json_schema" && rf.json_schema) {
        body.text.format = { type: "json_schema" };
        if (rf.json_schema.name) body.text.format.name = rf.json_schema.name;
        if (rf.json_schema.strict !== undefined) body.text.format.strict = rf.json_schema.strict;
        if (rf.json_schema.schema) body.text.format.schema = rf.json_schema.schema;
      }
      if (r.text?.verbosity) body.text.verbosity = r.text.verbosity;
    } else if (r.text?.verbosity) {
      body.text = { verbosity: r.text.verbosity };
    }

    // tools
    if (Array.isArray(r.tools) && r.tools.length) {
      body.tools = [];
      for (const t of r.tools) {
        if (t.type !== "function") {
          body.tools.push(t);
          continue;
        }
        const fn = t.function || {};
        let name = fn.name || "";
        name = tm[name] || shortenName(name);
        body.tools.push({
          type: "function",
          name,
          description: fn.description,
          parameters: fn.parameters,
          strict: fn.strict
        });
      }
    }

    // tool_choice
    if (r.tool_choice) {
      if (typeof r.tool_choice === "string") {
        body.tool_choice = r.tool_choice;
      } else if (r.tool_choice.type === "function") {
        let name = r.tool_choice.function?.name || "";
        name = tm[name] || shortenName(name);
        body.tool_choice = { type: "function", name };
      } else if (r.tool_choice.type) {
        body.tool_choice = r.tool_choice;
      }
    }

    const baseUrl = (provider.baseUrl || "https://api.openai.com").replace(/\/+$/, "");
    const config = { url: new URL(`${baseUrl}/v1/responses`), headers: { "Content-Type": "application/json" } };
    if (provider.apiKey) config.headers.Authorization = `Bearer ${provider.apiKey}`;
    return { body, config };
  }

  // ── Response: Codex → OpenAI Chat Completions SSE ─────────────────────
  async transformResponseOut(response, context) {
    const origReq = context.req?.body || {};
    const rev = buildShortToOrig(origReq);
    const ct = response.headers.get("Content-Type") || "";

    if (ct.includes("application/json")) {
      const json = await response.json();
      return new Response(JSON.stringify(this._nonStream(json, rev)), {
        status: response.status, statusText: response.statusText, headers: response.headers });
    }

    if (!response.body) return response;
    const dec = new TextDecoder(), enc = new TextEncoder(), self = this;
    const st = { respId: "", created: 0, model: "", funcIdx: -1, gotArgDelta: false, announced: false };
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
    const chunks = this._ev(ev, st, rev);
    for (const c of chunks) if (c) ctrl.enqueue(enc.encode(`data: ${JSON.stringify(c)}\n\n`));
  }

  _ev(ev, st, rev) {
    const type = ev.type;
    const tpl = () => ({ id: st.respId, object: "chat.completion.chunk", created: st.created, model: st.model, choices: [{ index: 0, delta: {}, finish_reason: null }] });

    if (type === "response.created") {
      st.respId = ev.response?.id || "";
      st.created = ev.response?.created_at || 0;
      st.model = ev.response?.model || "";
      return [];
    }

    if (type === "response.reasoning_summary_text.delta") {
      const o = tpl();
      o.choices[0].delta = { role: "assistant", reasoning_content: ev.delta || "" };
      return [o];
    }

    if (type === "response.reasoning_summary_text.done") {
      const o = tpl();
      o.choices[0].delta = { role: "assistant", reasoning_content: "\n\n" };
      return [o];
    }

    if (type === "response.output_text.delta") {
      const o = tpl();
      o.choices[0].delta = { role: "assistant", content: ev.delta || "" };
      return [o];
    }

    if (type === "response.completed") {
      const o = tpl();
      o.choices[0].finish_reason = st.funcIdx !== -1 ? "tool_calls" : "stop";
      if (ev.response?.usage) {
        const u = ev.response.usage;
        o.usage = {
          prompt_tokens: u.input_tokens || 0,
          completion_tokens: u.output_tokens || 0,
          total_tokens: u.total_tokens || 0
        };
        if (u.input_tokens_details?.cached_tokens) {
          o.usage.prompt_tokens_details = { cached_tokens: u.input_tokens_details.cached_tokens };
        }
        if (u.output_tokens_details?.reasoning_tokens) {
          o.usage.completion_tokens_details = { reasoning_tokens: u.output_tokens_details.reasoning_tokens };
        }
      }
      return [o];
    }

    if (type === "response.output_item.added") {
      const it = ev.item || {};
      if (it.type !== "function_call") return [];
      st.funcIdx++;
      st.gotArgDelta = false;
      st.announced = true;
      let name = it.name || "";
      if (rev[name]) name = rev[name];
      const o = tpl();
      o.choices[0].delta = {
        role: "assistant",
        tool_calls: [{ index: st.funcIdx, id: it.call_id || "", type: "function", function: { name, arguments: "" } }]
      };
      return [o];
    }

    if (type === "response.function_call_arguments.delta") {
      st.gotArgDelta = true;
      const o = tpl();
      o.choices[0].delta = { tool_calls: [{ index: st.funcIdx, function: { arguments: ev.delta || "" } }] };
      return [o];
    }

    if (type === "response.function_call_arguments.done") {
      if (st.gotArgDelta) return [];
      const o = tpl();
      o.choices[0].delta = { tool_calls: [{ index: st.funcIdx, function: { arguments: ev.arguments || "" } }] };
      return [o];
    }

    if (type === "response.output_item.done") {
      const it = ev.item || {};
      if (it.type !== "function_call") return [];
      if (st.announced) { st.announced = false; return []; }
      // fallback
      st.funcIdx++;
      let name = it.name || "";
      if (rev[name]) name = rev[name];
      const o = tpl();
      o.choices[0].delta = {
        role: "assistant",
        tool_calls: [{ index: st.funcIdx, id: it.call_id || "", type: "function", function: { name, arguments: it.arguments || "" } }]
      };
      return [o];
    }

    return [];
  }

  _nonStream(json, rev) {
    const rd = json.response || json;
    const o = {
      id: rd.id || "",
      object: "chat.completion",
      created: rd.created_at || Math.floor(Date.now() / 1000),
      model: rd.model || "",
      choices: [{
        index: 0,
        message: { role: "assistant", content: null, reasoning_content: null, tool_calls: null },
        finish_reason: null
      }]
    };

    if (rd.usage) {
      const u = rd.usage;
      o.usage = {
        prompt_tokens: u.input_tokens || 0,
        completion_tokens: u.output_tokens || 0,
        total_tokens: u.total_tokens || 0
      };
      if (u.input_tokens_details?.cached_tokens) {
        o.usage.prompt_tokens_details = { cached_tokens: u.input_tokens_details.cached_tokens };
      }
      if (u.output_tokens_details?.reasoning_tokens) {
        o.usage.completion_tokens_details = { reasoning_tokens: u.output_tokens_details.reasoning_tokens };
      }
    }

    let contentText = "", reasoningText = "";
    const toolCalls = [];

    if (Array.isArray(rd.output)) {
      for (const it of rd.output) {
        if (it.type === "reasoning") {
          const sm = it.summary;
          if (Array.isArray(sm)) {
            for (const p of sm) if (p.type === "summary_text") reasoningText = p.text || "";
          }
        } else if (it.type === "message") {
          const ct = it.content;
          if (Array.isArray(ct)) {
            for (const p of ct) if (p.type === "output_text") contentText = p.text || "";
          }
        } else if (it.type === "function_call") {
          let name = it.name || "";
          if (rev[name]) name = rev[name];
          toolCalls.push({ id: it.call_id || "", type: "function", function: { name, arguments: it.arguments || "" } });
        }
      }
    }

    if (contentText) o.choices[0].message.content = contentText;
    if (reasoningText) o.choices[0].message.reasoning_content = reasoningText;
    if (toolCalls.length) o.choices[0].message.tool_calls = toolCalls;

    o.choices[0].finish_reason = rd.status === "completed" ? "stop" : "stop";
    return o;
  }
}

module.exports = CodexOAITransformer;
