/**
 * CodexOAITransformer — OpenAI Chat Completions API ↔ Codex (Responses API) 格式转换。
 *
 * 对应 Go 实现:
 *   internal/translator/codex/openai/chat-completions/codex_openai_request.go
 *   internal/translator/codex/openai/chat-completions/codex_openai_response.go
 *
 * 这个 JS 版本主要用于在运行时把：
 * 1. OpenAI Chat Completions 请求改写为 Codex Responses 请求；
 * 2. Codex 返回的 JSON / SSE 结果再还原为 OpenAI Chat Completions 兼容格式。
 */

// Codex 对工具名长度有限制；超过时需要截断或改写。
const TOOL_NAME_LIMIT = 64;

// 将工具名截断到 Codex 可接受的长度上限。
// 对 mcp__xxx__tool 这类名字，优先保留最后一段工具名，尽量提高可读性。
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

// 为一组原始工具名构建“原名 -> 短名”映射。
// 如果多个长名称截断后冲突，会自动追加 _1、_2 等后缀保证唯一。
function buildShortNameMap(names) {
  const used = new Set(), m = {};

  // 生成候选短名：
  // - 长度足够短时直接使用原名；
  // - MCP 工具优先保留尾部工具名；
  // - 其他情况直接按长度截断。
  const base = (n) => {
    if (n.length <= TOOL_NAME_LIMIT) return n;
    if (n.startsWith("mcp__")) {
      const i = n.lastIndexOf("__");
      if (i > 0) { let c = "mcp__" + n.slice(i + 2); return c.length > TOOL_NAME_LIMIT ? c.slice(0, TOOL_NAME_LIMIT) : c; }
    }
    return n.slice(0, TOOL_NAME_LIMIT);
  };

  // 保证短名唯一。
  // 如果 base 已经被占用，则在尾部追加 _1 / _2 / ...，必要时继续裁剪主体部分。
  const uniq = (c) => {
    if (!used.has(c)) return c;
    for (let i = 1; ; i++) { const s = "_" + i; let a = TOOL_NAME_LIMIT - s.length; if (a < 0) a = 0; const t = (c.length > a ? c.slice(0, a) : c) + s; if (!used.has(t)) return t; }
  };

  for (const n of names) {
    const u = uniq(base(n));
    used.add(u);
    m[n] = u;
  }
  return m;
}

// 从原始请求中的 tools 提取工具名，并生成原名到短名的映射。
function buildOrigToShort(req) {
  if (!Array.isArray(req?.tools)) return {};
  const ns = req.tools.map(t => t.function?.name).filter(Boolean);
  return ns.length ? buildShortNameMap(ns) : {};
}

// 构建短名到原名的反向映射，供响应阶段把工具名还原回 OpenAI 侧名称。
function buildShortToOrig(req) {
  const fwd = buildOrigToShort(req), rev = {};
  for (const [o, s] of Object.entries(fwd)) rev[s] = o;
  return rev;
}

// ═════════════════════════════════════════════════════════════════════════
// 转换器职责：
// 1. 入站：把 OpenAI Chat Completions 请求改写成 Codex Responses API 请求。
// 2. 出站：把 Codex 的 JSON / SSE 响应再转换回 OpenAI Chat Completions 兼容格式。
class CodexOAITransformer {
  name = "codex_oai";
  constructor(options = {}) { this.options = options; }

  // ── Request: OpenAI Chat Completions → Codex Responses API ────────────
  // 将 OpenAI Chat Completions 请求体映射为 Codex Responses 请求体。
  // 这里会处理消息、工具调用、结构化输出、tool_choice 以及认证头。
  async transformRequestIn(request, provider) {
    const r = request;

    // Codex Responses 的基础请求结构。
    // instructions 目前保持空串，主要上下文通过 input 里的 message / function_call 传递。
    const body = { model: r.model, instructions: "", input: [], stream: r.stream ?? true, store: false };

    // 将 OpenAI 风格的 reasoning_effort 映射到 Codex 的 reasoning 配置。
    const effort = r.reasoning_effort || "medium";
    body.reasoning = { effort, summary: "auto" };

    // 默认开启并行工具调用，并请求返回加密的 reasoning 内容。
    body.parallel_tool_calls = true;
    body.include = ["reasoning.encrypted_content"];

    // 构建工具名映射。
    // 只要请求里声明了 tools，后续所有工具相关字段（历史 tool_calls、tools、tool_choice）
    // 都要使用同一套短名，确保请求前后一致。
    const tm = buildOrigToShort(r);

    // 将 Chat Completions 的 messages 数组转换为 Responses API 的 input 数组。
    if (Array.isArray(r.messages)) {
      for (const msg of r.messages) {
        const role = msg.role;

        if (role === "tool") {
          // tool 消息表示“工具执行结果”，在 Responses API 中对应 function_call_output。
          body.input.push({
            type: "function_call_output",
            call_id: msg.tool_call_id || "",
            output: msg.content || ""
          });
          continue;
        }

        // 普通 message：system -> developer，其余角色保持原语义。
        // Responses API 的 message.content 是分片数组，因此这里统一收敛为 content: []。
        const m = { type: "message", role: role === "system" ? "developer" : role, content: [] };
        const c = msg.content;

        if (typeof c === "string" && c) {
          // assistant 消息中的文本片段使用 output_text；其余角色使用 input_text。
          m.content.push({ type: role === "assistant" ? "output_text" : "input_text", text: c });
        } else if (Array.isArray(c)) {
          // 多模态消息逐项转换。
          for (const item of c) {
            if (item.type === "text") {
              m.content.push({ type: role === "assistant" ? "output_text" : "input_text", text: item.text || "" });
            } else if (item.type === "image_url" && role === "user") {
              // 仅用户消息里的 image_url 转成 input_image。
              m.content.push({ type: "input_image", image_url: item.image_url?.url || "" });
            } else if (item.type === "file" && role === "user") {
              // 文件输入映射为 input_file；如有文件名则一并透传。
              const part = { type: "input_file", file_data: item.file?.file_data || "" };
              if (item.file?.filename) part.filename = item.file.filename;
              m.content.push(part);
            }
          }
        }

        // 只有内容非空才添加消息。
        // assistant 空消息常见于“仅发起 tool_calls”的情况，此时不需要额外写入空 message。
        if (m.content.length > 0 || role !== "assistant") {
          body.input.push(m);
        }

        // assistant 消息里的 tool_calls 会被拆成独立的 function_call 项。
        // 这是 Responses API 对“模型曾经调用过工具”的表达方式。
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

    // 结构化输出：把 Chat Completions 的 response_format 映射到 Responses 的 text.format。
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

      // verbosity 属于 text 配置的一部分；不管是否有 format，都允许透传。
      if (r.text?.verbosity) body.text.verbosity = r.text.verbosity;
    } else if (r.text?.verbosity) {
      body.text = { verbosity: r.text.verbosity };
    }

    // 工具定义：函数工具会写入短名，非 function 类型原样透传。
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

    // tool_choice 同样需要使用短名，确保和 tools / tool_calls 的命名一致。
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

    // 组装上游请求地址与鉴权头，请求最终发往 /v1/responses。
    const baseUrl = (provider.baseUrl || "https://api.openai.com").replace(/\/+$/, "");
    const config = { url: new URL(`${baseUrl}/v1/responses`), headers: { "Content-Type": "application/json" } };
    if (provider.apiKey) config.headers.Authorization = `Bearer ${provider.apiKey}`;
    return { body, config };
  }

  // ── Response: Codex → OpenAI Chat Completions SSE ─────────────────────
  // 将 Codex 的响应转换回 OpenAI Chat Completions 兼容输出。
  // 同时支持非流式 JSON 和流式 SSE 两种模式。
  async transformResponseOut(response, context) {
    const origReq = context.req?.body || {};

    // 基于原始请求重新构造“短名 -> 原名”映射。
    // 这样在响应阶段就能把工具名恢复成 OpenAI 客户端最初看到的名字。
    const rev = buildShortToOrig(origReq);
    const ct = response.headers.get("Content-Type") || "";

    // 非流式响应：直接整体转换为 chat.completion。
    if (ct.includes("application/json")) {
      const json = await response.json();
      return new Response(JSON.stringify(this._nonStream(json, rev)), {
        status: response.status, statusText: response.statusText, headers: response.headers });
    }

    // 流式响应：逐行解析 SSE，再重组为 OpenAI 风格 chunk。
    if (!response.body) return response;
    const dec = new TextDecoder(), enc = new TextEncoder(), self = this;

    // st 用于跨事件保存流式状态：
    // - respId / created / model：公共元信息；
    // - funcIdx：当前累计到第几个 tool_call；
    // - gotArgDelta：是否已经收到 arguments 的增量事件；
    // - announced：是否已经通过 output_item.added 宣布过本次 function_call。
    const st = { respId: "", created: 0, model: "", funcIdx: -1, gotArgDelta: false, announced: false };
    const stream = new ReadableStream({
      async start(ctrl) {
        const rd = response.body.getReader(); let buf = "";
        try {
          while (true) {
            const { done, value } = await rd.read();
            if (done) { if (buf.trim()) self._line(buf, st, rev, ctrl, enc); break; }
            buf += dec.decode(value, { stream: true });

            // SSE 以换行分帧；保留最后一个可能不完整的片段到下一轮继续拼接。
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

  // 处理单行 SSE 数据；只消费 data: 开头的事件负载。
  // 该函数负责：去掉 data: 前缀、解析 JSON、调用 _ev 生成 0..N 个 chunk。
  _line(line, st, rev, ctrl, enc) {
    if (!line.startsWith("data:")) return;
    const raw = line.slice(5).trim();
    if (!raw || raw === "[DONE]") return;
    let ev; try { ev = JSON.parse(raw); } catch { return; }
    const chunks = this._ev(ev, st, rev);
    for (const c of chunks) if (c) ctrl.enqueue(enc.encode(`data: ${JSON.stringify(c)}\n\n`));
  }

  // 将单个 Codex SSE 事件映射为 0..N 个 OpenAI chat.completion.chunk 片段。
  _ev(ev, st, rev) {
    const type = ev.type;

    // OpenAI Chat Completions SSE chunk 的公共模板。
    const tpl = () => ({ id: st.respId, object: "chat.completion.chunk", created: st.created, model: st.model, choices: [{ index: 0, delta: {}, finish_reason: null }] });

    // 初始化流式响应的公共元数据。
    if (type === "response.created") {
      st.respId = ev.response?.id || "";
      st.created = ev.response?.created_at || 0;
      st.model = ev.response?.model || "";
      return [];
    }

    // 推理摘要增量，映射到自定义 reasoning_content 字段。
    if (type === "response.reasoning_summary_text.delta") {
      const o = tpl();
      o.choices[0].delta = { role: "assistant", reasoning_content: ev.delta || "" };
      return [o];
    }

    // 推理摘要结束时补一个空行，便于与后续正文内容分隔。
    if (type === "response.reasoning_summary_text.done") {
      const o = tpl();
      o.choices[0].delta = { role: "assistant", reasoning_content: "\n\n" };
      return [o];
    }

    // 普通文本增量，映射到 assistant.content。
    if (type === "response.output_text.delta") {
      const o = tpl();
      o.choices[0].delta = { role: "assistant", content: ev.delta || "" };
      return [o];
    }

    // 响应结束时补充 finish_reason 与 usage。
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

    // function_call 开始：先发出一个带空 arguments 的 tool_calls 框架。
    // 后续参数内容由 response.function_call_arguments.delta / done 继续补齐。
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

    // function_call 参数流式增量。
    // OpenAI 格式要求把 arguments 作为字符串增量不断拼接。
    if (type === "response.function_call_arguments.delta") {
      st.gotArgDelta = true;
      const o = tpl();
      o.choices[0].delta = { tool_calls: [{ index: st.funcIdx, function: { arguments: ev.delta || "" } }] };
      return [o];
    }

    // 某些上游不会发送 delta，只在 done 里一次性给出完整 arguments。
    // 只有在此前没收到过 delta 时，才在这里补发完整参数，避免重复。
    if (type === "response.function_call_arguments.done") {
      if (st.gotArgDelta) return [];
      const o = tpl();
      o.choices[0].delta = { tool_calls: [{ index: st.funcIdx, function: { arguments: ev.arguments || "" } }] };
      return [o];
    }

    // 兜底逻辑：如果前面没有 output_item.added，这里直接产出完整 tool_call。
    // 某些实现可能只在 done 事件里给出最终 function_call。
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

  // 非流式响应整体转换：把 Responses 的 output 数组收敛成一个 chat.completion。
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

    // 将 Codex usage 字段改写成 Chat Completions 兼容结构。
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

    // 遍历 output，分别提取文本、推理摘要和函数调用。
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

    // 当前统一返回 stop。
    // 如果后续需要更精确区分 tool_calls / length / content_filter 等原因，可在这里继续细化。
    o.choices[0].finish_reason = rd.status === "completed" ? "stop" : "stop";
    return o;
  }
}

module.exports = CodexOAITransformer;
