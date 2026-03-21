class OllamaTransformer {
  name = "ollama";

  constructor(options) {
    console.log("ollama init:---");
    this.think = options.think; //true,false
    this.options = options;
  }

  async transformRequestIn(request, provider) {
    const messages = [];

    // 转换消息格式
    if (request.messages && Array.isArray(request.messages)) {
      for (const message of request.messages) {
        let role = message.role;

        // Ollama 不支持 system role，需要转换为 user
        if (role === "system") {
          role = "user";
        }

        // 处理 tool role
        if (role === "tool") {
          // Ollama 将 tool 结果作为 user 消息
          role = "user";
          messages.push({
            role: role,
            content: `Tool result: ${message.content}`
          });
          continue;
        }

        // 处理消息内容
        if (typeof message.content === "string") {
          const msg = {
            role: role,
            content: message.content
          };

          // 如果有 tool_calls，添加到消息中
          if (Array.isArray(message.tool_calls)) {
            msg.tool_calls = message.tool_calls.map(tc => ({
              id: tc.id,
              type: tc.type,
              function: {
                name: tc.function.name,
                arguments: typeof tc.function.arguments === 'string'
                  ? JSON.parse(tc.function.arguments)
                  : tc.function.arguments
              }
            }));
          }

          messages.push(msg);
        } else if (Array.isArray(message.content)) {
          // 处理多模态内容
          const textParts = [];
          const images = [];

          for (const content of message.content) {
            if (content.type === "text") {
              textParts.push(content.text);
            } else if (content.type === "image_url") {
              // 提取 base64 图片数据
              let imageData = content.image_url.url;
              if (imageData.startsWith("data:")) {
                // 移除 data:image/xxx;base64, 前缀
                imageData = imageData.split(",").pop();
              }
              images.push(imageData);
            }
          }

          const msg = {
            role: role,
            content: textParts.join("\n")
          };

          if (images.length > 0) {
            msg.images = images;
          }

          messages.push(msg);
        }
      }
    }

    // 转换工具定义
    let tools = undefined;
    if (request.tools && Array.isArray(request.tools)) {
      tools = request.tools.map(tool => ({
        type: "function",
        function: {
          name: tool.function.name,
          description: tool.function.description,
          parameters: tool.function.parameters
        }
      }));
    }

    // 构建 Ollama 请求体
    const bodyRequest = {
      model: request.model,
      messages: messages,
      stream: request.stream !== false, // 默认为 true
      options: {
        temperature: request.temperature || 0.7,
        top_p: request.top_p,
        top_k: request.top_k,
        num_predict: request.max_tokens
      }
    };

    // 添加工具定义
    if (tools && tools.length > 0) {
      bodyRequest.tools = tools;
    }

    // 构建请求配置
    const requestUrl = new URL(
      `${provider.baseUrl || "https://ollama.com"}/api/chat`
    );

    const config = {
      url: requestUrl,
      headers: {
        "Content-Type": "application/json"
      }
    };

    // 如果提供了 API key，添加到 headers
    if (provider.apiKey) {
      config.headers.Authorization = `Bearer ${provider.apiKey}`;
    }

    return {
      body: bodyRequest,
      config: config
    };
  }

  async transformResponseOut(response,context) {
    if (response.headers.get("Content-Type")?.includes("application/json")) {
      // 非流式响应
      const jsonResponse = await response.json();

      // 构建 message 对象
      const message = {
        role: "assistant",
        content: jsonResponse.message?.content || ""
      };

      // 处理 tool_calls，将 arguments 序列化为字符串
      if (jsonResponse.message?.tool_calls) {
        message.tool_calls = jsonResponse.message.tool_calls.map(tc => ({
          ...tc,
          function: {
            ...tc.function,
            arguments: typeof tc.function.arguments === 'string'
              ? tc.function.arguments
              : JSON.stringify(tc.function.arguments)
          }
        }));
      }

      // 如果存在 thinking 字段，将其转换为 reasoning
      if (jsonResponse.message?.thinking) {
        message.reasoning = jsonResponse.message.thinking;
      }

      const res = {
        id: `chatcmpl-${Date.now()}`,
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model: context.req.orgiModel,
        choices: [
          {
            index: 0,
            message: message,
            finish_reason: jsonResponse.done ? "stop" : null
          }
        ],
        usage: {
          prompt_tokens: jsonResponse.prompt_eval_count || 0,
          completion_tokens: jsonResponse.eval_count || 0,
          total_tokens: (jsonResponse.prompt_eval_count || 0) + (jsonResponse.eval_count || 0)
        }
      };

      return new Response(JSON.stringify(res), {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers
      });
    } else {
      // 流式响应
      if (!response.body) {
        return response;
      }

      const decoder = new TextDecoder();
      const encoder = new TextEncoder();

      const stream = new ReadableStream({
        async start(controller) {
          const reader = response.body.getReader();
          let buffer = "";

          try {
            while (true) {
              const { done, value } = await reader.read();
              if (done) {
                if (buffer) {
                  processLine(buffer, controller);
                }
                // 发送结束标记
                controller.enqueue(encoder.encode("data: [DONE]\n\n"));
                break;
              }

              buffer += decoder.decode(value, { stream: true });
              const lines = buffer.split("\n");
              buffer = lines.pop() || "";

              for (const line of lines) {
                if (line.trim()) {
                  processLine(line, controller);
                }
              }
            }
          } catch (error) {
            controller.error(error);
          } finally {
            controller.close();
          }
        }
      });

      function processLine(line, controller) {
        try {
          const chunk = JSON.parse(line);

          // 构建 delta 对象
          const delta = {
            role: chunk.message?.role,
            content: chunk.message?.content || ""
          };

          // 处理 tool_calls，将 arguments 序列化为字符串
          if (chunk.message?.tool_calls) {
            delta.tool_calls = chunk.message.tool_calls.map(tc => ({
              ...tc,
              function: {
                ...tc.function,
                arguments: typeof tc.function.arguments === 'string'
                  ? tc.function.arguments
                  : JSON.stringify(tc.function.arguments)
              }
            }));
          }

          // 如果存在 thinking 字段，将其转换为 reasoning
          if (chunk.message?.thinking) {
            delta.reasoning = chunk.message.thinking;
          }

          const res = {
            id: `chatcmpl-${Date.now()}`,
            object: "chat.completion.chunk",
            created: Math.floor(Date.now() / 1000),
            model: context.req.orgiModel,
            choices: [
              {
                index: 0,
                delta: delta,
                finish_reason: chunk.done ? "stop" : null
              }
            ]
          };

          controller.enqueue(
            encoder.encode(`data: ${JSON.stringify(res)}\n\n`)
          );
        } catch (error) {
          // 忽略解析错误
        }
      }

      return new Response(stream, {
        status: response.status,
        statusText: response.statusText,
        headers: new Headers({
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          "Connection": "keep-alive"
        })
      });
    }
  }
}

module.exports = OllamaTransformer;
