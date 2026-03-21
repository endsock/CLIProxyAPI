const os = require("os");
const path = require("path");
const fs = require("fs/promises");
const crypto = require("crypto");

//const OAUTH_FILE = path.join("C:\\Users\\kimsky\\.claude-code-router", "oauth_creds.json");

// Type enum equivalent in JavaScript
const Type = {
  TYPE_UNSPECIFIED: "TYPE_UNSPECIFIED",
  STRING: "STRING",
  NUMBER: "NUMBER",
  INTEGER: "INTEGER",
  BOOLEAN: "BOOLEAN",
  ARRAY: "ARRAY",
  OBJECT: "OBJECT",
  NULL: "NULL",
};


/**
 * 工具 schema 清洗逻辑（1:1 复刻 internal/util/gemini_schema.go 的 Antigravity 分支语义）
 */

const placeholderReasonDescription = "Brief explanation of why you are calling this tool";

const unsupportedConstraints = [
  "minLength",
  "maxLength",
  "exclusiveMinimum",
  "exclusiveMaximum",
  "pattern",
  "minItems",
  "maxItems",
  "format",
  "default",
  "examples",
];

const unsupportedDeleteKeys = new Set([
  ...unsupportedConstraints,
  "$schema",
  "$defs",
  "definitions",
  "const",
  "$ref",
  "additionalProperties",
  "propertyNames",
]);

const TOOL_ALLOWED_KEYS = new Set([
  "name",
  "description",
  "behavior",
  "parameters",
  "response",
  "responseJsonSchema",
]);

function isPlainObject(value) {
  return value != null && typeof value === "object" && !Array.isArray(value);
}

function hasOwn(obj, key) {
  return Object.prototype.hasOwnProperty.call(obj, key);
}

function deepClone(value) {
  if (value == null) {
    return value;
  }
  return JSON.parse(JSON.stringify(value));
}

function appendHint(description, hint) {
  if (!hint) {
    return description || "";
  }
  const existing = typeof description === "string" ? description : "";
  if (!existing) {
    return hint;
  }
  return `${existing} (${hint})`;
}

function toHintString(value) {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return String(value);
}

function mergeDescriptionRaw(schemaObj, parentDesc) {
  const childDesc = typeof schemaObj.description === "string" ? schemaObj.description : "";
  if (childDesc === "") {
    schemaObj.description = parentDesc;
    return schemaObj;
  }
  if (childDesc === parentDesc) {
    return schemaObj;
  }
  schemaObj.description = `${parentDesc} (${childDesc})`;
  return schemaObj;
}

function traverseSchema(node, transform, ctx = {}) {
  const {
    inPropertiesMap = false,
    isRoot = false,
    parentSchema = null,
    propertyName = "",
  } = ctx;

  if (Array.isArray(node)) {
    return node.map((item) =>
      traverseSchema(item, transform, {
        inPropertiesMap: false,
        isRoot: false,
        parentSchema: null,
        propertyName: "",
      })
    );
  }

  if (!isPlainObject(node)) {
    return node;
  }

  if (inPropertiesMap) {
    const mapped = {};
    for (const [propName, propSchema] of Object.entries(node)) {
      mapped[propName] = traverseSchema(propSchema, transform, {
        inPropertiesMap: false,
        isRoot: false,
        parentSchema,
        propertyName: propName,
      });
    }
    return mapped;
  }

  const current = node;
  for (const [key, value] of Object.entries(current)) {
    if (key === "properties" && isPlainObject(value)) {
      current[key] = traverseSchema(value, transform, {
        inPropertiesMap: true,
        isRoot: false,
        parentSchema: current,
        propertyName: "",
      });
      continue;
    }

    if (Array.isArray(value)) {
      current[key] = value.map((item) =>
        traverseSchema(item, transform, {
          inPropertiesMap: false,
          isRoot: false,
          parentSchema: null,
          propertyName: "",
        })
      );
      continue;
    }

    if (isPlainObject(value)) {
      current[key] = traverseSchema(value, transform, {
        inPropertiesMap: false,
        isRoot: false,
        parentSchema: null,
        propertyName: "",
      });
    }
  }

  const transformed = transform(current, {
    inPropertiesMap,
    isRoot,
    parentSchema,
    propertyName,
  });

  return transformed === undefined ? current : transformed;
}

function applySchemaPass(schema, transform) {
  return traverseSchema(schema, transform, {
    inPropertiesMap: false,
    isRoot: true,
    parentSchema: null,
    propertyName: "",
  });
}

function transformConvertRefsToHints(node) {
  if (typeof node.$ref !== "string" || node.$ref === "") {
    return node;
  }

  const refVal = node.$ref;
  const idx = refVal.lastIndexOf("/");
  const defName = idx >= 0 ? refVal.slice(idx + 1) : refVal;
  const hint = appendHint(node.description, `See: ${defName}`);

  return {
    type: "object",
    description: hint,
  };
}

function transformConvertConstToEnum(node) {
  if (node.const !== undefined && !Array.isArray(node.enum)) {
    node.enum = [node.const];
  }
  return node;
}

function transformConvertEnumValuesToStrings(node) {
  if (!Array.isArray(node.enum)) {
    return node;
  }

  node.enum = node.enum.map((item) => String(item));
  node.type = "string";
  return node;
}

function transformAddEnumHints(node) {
  if (!Array.isArray(node.enum)) {
    return node;
  }

  if (node.enum.length <= 1 || node.enum.length > 10) {
    return node;
  }

  const vals = node.enum.map((item) => String(item));
  node.description = appendHint(node.description, `Allowed: ${vals.join(", ")}`);
  return node;
}

function transformAddAdditionalPropertiesHints(node) {
  if (node.additionalProperties === false) {
    node.description = appendHint(node.description, "No extra properties allowed");
  }
  return node;
}

function transformMoveConstraintsToDescription(node) {
  for (const key of unsupportedConstraints) {
    if (!hasOwn(node, key)) {
      continue;
    }

    const value = node[key];
    if (value === undefined || value === null) {
      continue;
    }
    if (isPlainObject(value) || Array.isArray(value)) {
      continue;
    }

    node.description = appendHint(node.description, `${key}: ${toHintString(value)}`);
  }
  return node;
}

function transformMergeAllOf(node) {
  if (!Array.isArray(node.allOf)) {
    return node;
  }

  for (const item of node.allOf) {
    if (!isPlainObject(item)) {
      continue;
    }

    if (isPlainObject(item.properties)) {
      if (!isPlainObject(node.properties)) {
        node.properties = {};
      }
      for (const [key, value] of Object.entries(item.properties)) {
        node.properties[key] = value;
      }
    }

    if (Array.isArray(item.required)) {
      const currentRequired = Array.isArray(node.required) ? node.required.slice() : [];
      for (const requiredField of item.required) {
        const requiredKey = String(requiredField);
        if (!currentRequired.includes(requiredKey)) {
          currentRequired.push(requiredKey);
        }
      }
      node.required = currentRequired;
    }
  }

  delete node.allOf;
  return node;
}

function selectBest(items) {
  let bestIdx = 0;
  let bestScore = -1;
  const types = [];

  for (let i = 0; i < items.length; i++) {
    const item = isPlainObject(items[i]) ? items[i] : {};
    let t = typeof item.type === "string" ? item.type : "";
    let score = 0;

    if (t === "object" || hasOwn(item, "properties")) {
      score = 3;
      if (!t) {
        t = "object";
      }
    } else if (t === "array" || hasOwn(item, "items")) {
      score = 2;
      if (!t) {
        t = "array";
      }
    } else if (t !== "" && t !== "null") {
      score = 1;
    } else {
      if (!t) {
        t = "null";
      }
    }

    if (t !== "") {
      types.push(t);
    }

    if (score > bestScore) {
      bestScore = score;
      bestIdx = i;
    }
  }

  return { bestIdx, types };
}

function transformFlattenAnyOfOneOf(node) {
  for (const key of ["anyOf", "oneOf"]) {
    if (!Array.isArray(node[key]) || node[key].length === 0) {
      continue;
    }

    const items = node[key];
    const parentDesc = typeof node.description === "string" ? node.description : "";
    const { bestIdx, types } = selectBest(items);

    let selected = deepClone(items[bestIdx]);
    if (!isPlainObject(selected)) {
      return selected;
    }

    if (parentDesc !== "") {
      selected = mergeDescriptionRaw(selected, parentDesc);
    }

    if (types.length > 1) {
      selected.description = appendHint(selected.description, `Accepts: ${types.join(" | ")}`);
    }

    return selected;
  }

  return node;
}

function transformFlattenTypeArrays(node, ctx) {
  if (!Array.isArray(node.type) || node.type.length === 0) {
    return node;
  }

  let hasNull = false;
  const nonNullTypes = [];

  for (const item of node.type) {
    const typeName = String(item);
    if (typeName === "null") {
      hasNull = true;
    } else if (typeName !== "") {
      nonNullTypes.push(typeName);
    }
  }

  const firstType = nonNullTypes.length > 0 ? nonNullTypes[0] : "string";
  node.type = firstType;

  if (nonNullTypes.length > 1) {
    node.description = appendHint(node.description, `Accepts: ${nonNullTypes.join(" | ")}`);
  }

  if (hasNull && ctx.parentSchema && typeof ctx.propertyName === "string" && ctx.propertyName !== "") {
    node.description = appendHint(node.description, "(nullable)");

    if (Array.isArray(ctx.parentSchema.required)) {
      ctx.parentSchema.required = ctx.parentSchema.required.filter(
        (requiredField) => String(requiredField) !== ctx.propertyName
      );
    }
  }

  return node;
}

function transformRemoveUnsupportedKeywords(node) {
  for (const key of Object.keys(node)) {
    if (key.startsWith("x-")) {
      delete node[key];
      continue;
    }

    if (unsupportedDeleteKeys.has(key)) {
      delete node[key];
    }
  }
  return node;
}

function transformRemoveNullableAndTitle(node) {
  delete node.nullable;
  delete node.title;
  return node;
}

function transformRemovePlaceholderFields(node) {
  if (!isPlainObject(node.properties)) {
    return node;
  }

  if (hasOwn(node.properties, "_")) {
    delete node.properties._;
    if (Array.isArray(node.required)) {
      const filtered = node.required.filter((field) => String(field) !== "_");
      if (filtered.length === 0) {
        delete node.required;
      } else {
        node.required = filtered;
      }
    }
  }

  if (hasOwn(node.properties, "reason") && Object.keys(node.properties).length === 1) {
    const reasonSchema = node.properties.reason;
    const reasonDesc = isPlainObject(reasonSchema) ? reasonSchema.description : undefined;
    if (reasonDesc === placeholderReasonDescription) {
      delete node.properties.reason;
      if (Array.isArray(node.required)) {
        const filtered = node.required.filter((field) => String(field) !== "reason");
        if (filtered.length === 0) {
          delete node.required;
        } else {
          node.required = filtered;
        }
      }
    }
  }

  return node;
}

function transformCleanupRequired(node) {
  if (!Array.isArray(node.required) || !isPlainObject(node.properties)) {
    return node;
  }

  const valid = [];
  for (const requiredField of node.required) {
    const key = String(requiredField);
    if (hasOwn(node.properties, key)) {
      valid.push(key);
    }
  }

  if (valid.length !== node.required.length) {
    if (valid.length === 0) {
      delete node.required;
    } else {
      node.required = valid;
    }
  }

  return node;
}

function transformAddEmptySchemaPlaceholder(node, ctx) {
  if (String(node.type) !== "object") {
    return node;
  }

  const hasPropertiesField = hasOwn(node, "properties");
  const propsVal = node.properties;
  const reqVal = node.required;
  const hasRequiredProperties = Array.isArray(reqVal) && reqVal.length > 0;

  let needsPlaceholder = false;
  if (!hasPropertiesField) {
    needsPlaceholder = true;
  } else if (isPlainObject(propsVal) && Object.keys(propsVal).length === 0) {
    needsPlaceholder = true;
  }

  if (needsPlaceholder) {
    if (!isPlainObject(node.properties)) {
      node.properties = {};
    }

    node.properties.reason = {
      type: "string",
      description: placeholderReasonDescription,
    };
    node.required = ["reason"];
    return node;
  }

  if (isPlainObject(node.properties) && !hasRequiredProperties) {
    if (ctx.isRoot) {
      return node;
    }

    if (!hasOwn(node.properties, "_")) {
      node.properties._ = { type: "boolean" };
    }
    node.required = ["_"];
  }

  return node;
}

function cleanJSONSchema(jsonSchema, addPlaceholder = true) {
  if (!isPlainObject(jsonSchema)) {
    return jsonSchema;
  }

  let schema = deepClone(jsonSchema);

  // Phase 1: Convert and add hints
  schema = applySchemaPass(schema, transformConvertRefsToHints);
  schema = applySchemaPass(schema, transformConvertConstToEnum);
  schema = applySchemaPass(schema, transformConvertEnumValuesToStrings);
  schema = applySchemaPass(schema, transformAddEnumHints);
  schema = applySchemaPass(schema, transformAddAdditionalPropertiesHints);
  schema = applySchemaPass(schema, transformMoveConstraintsToDescription);

  // Phase 2: Flatten complex structures
  schema = applySchemaPass(schema, transformMergeAllOf);
  schema = applySchemaPass(schema, transformFlattenAnyOfOneOf);
  schema = applySchemaPass(schema, transformFlattenTypeArrays);

  // Phase 3: Cleanup
  schema = applySchemaPass(schema, transformRemoveUnsupportedKeywords);

  if (!addPlaceholder) {
    schema = applySchemaPass(schema, transformRemoveNullableAndTitle);
    schema = applySchemaPass(schema, transformRemovePlaceholderFields);
  }

  schema = applySchemaPass(schema, transformCleanupRequired);

  // Phase 4: Add placeholder for empty object schemas (Claude VALIDATED mode requirement)
  if (addPlaceholder) {
    schema = applySchemaPass(schema, transformAddEmptySchemaPlaceholder);
  }

  return schema;
}

/**
 * Process a JSON schema to make it compatible with Antigravity VALIDATED mode
 * @param {Object} jsonSchema - The JSON schema to process
 * @returns {Object} - The processed schema
 */
function processJsonSchema(jsonSchema) {
  return cleanJSONSchema(jsonSchema, true);
}

/**
 * Transform tool declarations
 * @param {Object[]} tool - The tool object array to transform
 * @returns {Object[]} - The transformed tool array
 */
function tTool(tool) {
  if (!Array.isArray(tool) || tool.length === 0) {
    return tool;
  }

  for (const item of tool) {
    if (!item || !Array.isArray(item.functionDeclarations)) {
      continue;
    }

    item.functionDeclarations = item.functionDeclarations.map((functionDeclaration) => {
      if (!isPlainObject(functionDeclaration)) {
        return functionDeclaration;
      }

      const declaration = deepClone(functionDeclaration);

      // 与 Go 侧 Claude translator 对齐：支持 input_schema -> parametersJsonSchema
      if (isPlainObject(declaration.input_schema)) {
        declaration.parametersJsonSchema = processJsonSchema(declaration.input_schema);
        delete declaration.input_schema;
      }

      if (declaration.parametersJsonSchema !== undefined) {
        declaration.parameters = declaration.parametersJsonSchema;
        delete declaration.parametersJsonSchema;
      }

      if (isPlainObject(declaration.parameters)) {
        declaration.parameters = processJsonSchema(declaration.parameters);
      }

      if (isPlainObject(declaration.response)) {
        declaration.response = processJsonSchema(declaration.response);
      }

      if (isPlainObject(declaration.responseJsonSchema)) {
        declaration.responseJsonSchema = processJsonSchema(declaration.responseJsonSchema);
      }

      // 与 Go 侧 allowedToolKeys 行为对齐：仅保留允许字段
      const sanitizedDeclaration = {};
      for (const key of Object.keys(declaration)) {
        if (TOOL_ALLOWED_KEYS.has(key)) {
          sanitizedDeclaration[key] = declaration[key];
        }
      }

      return sanitizedDeclaration;
    });
  }

  return tool;
}




class AntigravityTransformer {
  conversationIds = new Map();
  name = "antigravity";
  thoughtSignatureMap = new Map();
  thoughtSignatureTimeMap = new Map();
  printedMcpTools = new Set();
  REQ_COUNT = 0;
  claudeCount = 0;
  geminiProCount = 0;
  gemini3FlashCount = 0;
  gemini25FlashCount = 0;
  constructor(options) {
    this.options = options;
    this.refreshTokenPromise = null; // 用于防止 refreshToken 并发重入
    try {
      this.oauth_path = options.oauth_path;
      //path.join("C:\\Users\\kimsky\\.claude-code-router", "oauth_creds.json");
      this.oauth_creds = require(options.oauth_path);
      this.oauth_num = this.oauth_creds.length;
    } catch { }
  }

  generateUUID() {
    return crypto.randomUUID();
  }

  getConversationId(credIndex) {
    const now = Date.now();
    // 8分钟 = 480000毫秒
    if (!this.conversationIds.has(credIndex)) {
      this.conversationIds.set(credIndex, {
        id: this.generateUUID(),
        timestamp: now,
        index : 0
      });
    }

    const conversationData = this.conversationIds.get(credIndex);
    if (now - conversationData.timestamp > 480000) {
      conversationData.id = this.generateUUID();
      conversationData.timestamp = now;
      conversationData.index = 0;
    }
    conversationData.index += 1;
    return `agent/${now}/${conversationData.id}/${conversationData.index}`;
  }

  async transformRequestIn(request, provider) {
    // 清理过期的thoughtSignature条目（30分钟前）
    const currentTime = +new Date();
    const expiredKeys = [];
    for (const [key, timestamp] of this.thoughtSignatureTimeMap.entries()) {
      if (currentTime - timestamp > 30 * 60 * 1000) { // 30分钟 = 30 * 60 * 1000毫秒
        expiredKeys.push(key);
      }
    }
    for (const key of expiredKeys) {
      this.thoughtSignatureMap.delete(key);
      this.thoughtSignatureTimeMap.delete(key);
    }

    if (this.oauth_creds && Array.isArray(this.oauth_creds)) {
      // 循环检查数组中的每个凭证是否过期
      for (let i = 0; i < this.oauth_creds.length; i++) {
        const cred = this.oauth_creds[i];
        if (cred.expiry_date < +new Date()) {
          await this.refreshToken(i);
        }
      }
    }

    const tools = [];
    let toolsArr = [];
    let toolsWhiteList = [  "Task",  "TaskOutput",  "Bash",  "Glob",  "Grep",  "ExitPlanMode",  "Read",  "Edit",  "Write",  "NotebookEdit",  "WebFetch",  "TodoWrite",  "WebSearch",  "KillShell",  "AskUserQuestion",  "Skill",  "EnterPlanMode","apply_diff","ask_followup_question","attempt_completion","execute_command","fetch_instructions","list_files","new_task","read_file","search_files","switch_mode","update_todo_list","write_to_file","BashOutput","SlashCommand","ListMcpResourcesTool","ReadMcpResourceTool","LS","MultiEdit","KillBash","ToolSearch"]; //"TaskStop","TaskCreate","TaskGet","TaskUpdate","TaskList",

    if (request.tools && Array.isArray(request.tools)) {
      for (let i = 0; i < request.tools.length; i++) {
        const tool = request.tools[i];
        // 只有在白名单中的工具才会被添加
        if (toolsWhiteList.includes(tool.function.name)) {
          toolsArr.push({
            functionDeclarations: [{
              name: tool.function.name,
              description: tool.function.description,
              parameters: tool.function.parameters,
            }]
          });
        } else if (tool.function.name.startsWith("mcp__")){
          if (!this.printedMcpTools.has(tool.function.name)) {
            this.printedMcpTools.add(tool.function.name);
            console.log(tool.function.name);
          }
          if (tool.function.name.includes("google-search-mcp") || tool.function.name.includes("chrome-server-mcp")) {
            toolsArr.push({
              functionDeclarations: [{
                name: tool.function.name,
                description: tool.function.description,
                parameters: tool.function.parameters,
              }]
            });
          }
        } else {
          if (tool.function.name != "LSP") {
            console.log("tool not in claude white: " + tool.function.name);
          }
        }
        
      }
    }

    if (toolsArr.length) {
      const newToolsArr = tTool(toolsArr);
      for (let i = 0; i < newToolsArr.length; i++) {
        tools.push(newToolsArr[i]);
      }
    }

    //遍历tool_calls，生成一个map。key是id，val是name
    const toolCallsMap = {};
    if (request.messages && Array.isArray(request.messages)) {
      for (let i = 0; i < request.messages.length; i++) {
        const message = request.messages[i];
        if (Array.isArray(message.tool_calls)) {
          for (let j = 0; j < message.tool_calls.length; j++) {
            const toolCall = message.tool_calls[j];
            toolCallsMap[toolCall.id] = toolCall.function.name;
          }
        }
      }
    }

    const contents = [];
    if (request.messages && Array.isArray(request.messages)) {
      for (let i = 0; i < request.messages.length; i++) {
        const message = request.messages[i];
        let role;
        let parts = [];

        if (message.role === "tool") {
          if (request.model.includes("claude")){
            role = "user";
          } else {
            role = "model";
          }

          if (message.tool_call_id && typeof message.content === "string") {
            const toolName = toolCallsMap[message.tool_call_id] || "unknown_function";
            let funResp = {
              functionResponse: {
                id: message.tool_call_id,
                name: toolName,
                response: {
                  output: message.content
                }
              }
            }
            parts.push(funResp);
          }
        } else {
          if (message.role === "assistant") {
            role = "model";
          } else if (["user", "system"].includes(message.role)) {
            role = "user";
          } else {
            role = "user";
          }

          if (typeof message.content === "string") {
            parts.push({
              text: message.content || "<NULL>",
            });
          } else if (Array.isArray(message.content)) {
            for (let j = 0; j < message.content.length; j++) {
              const content = message.content[j];
              if (content.type === "text") {
                parts.push({
                  text: content.text || "<NULL>",
                });
              } else if (content.type === "image_url") {
                // 如果不是最后一条消息，就忽略 image_url
                if (i < request.messages.length - 1) {
                  continue;
                }
                if (content.image_url.url.startsWith("http")) {
                  parts.push({
                    file_data: {
                      mime_type: content.media_type,
                      file_uri: content.image_url.url,
                    },
                  });
                } else {
                  let mime_type = ""
                  if (content.media_type){
                    mime_type = content.media_type;
                  } else {
                    mime_type = content.image_url.url
                      ?.split(",")
                      ?.shift()
                      ?.replace(/^data:/, "")
                  }
                  // if (request.model.includes("claude")) {
                  //   mime_type = mime_type.split(';')[0];
                  // }
                  mime_type = mime_type.split(';')[0];
                  parts.push({
                    inlineData: {
                      mime_type: mime_type,
                      data:
                        content.image_url.url?.split(",")?.pop() ||
                        content.image_url.url,
                    },
                  });
                }
              }
            }
          }
        }



        if (Array.isArray(message.tool_calls)) {
          for (let j = 0; j < message.tool_calls.length; j++) {
            const toolCall = message.tool_calls[j];
            let toolDesc = {
              functionCall: {
                id:
                  toolCall.id ||
                  `tool_${Math.random().toString(36).substring(2, 15)}`,
                name: toolCall.function.name,
                args: JSON.parse(toolCall.function.arguments || "{}"),
              },
            }
            if (toolCall.id && this.thoughtSignatureMap.has(toolCall.id)) {
              toolDesc["thoughtSignature"] = this.thoughtSignatureMap.get(toolCall.id)
              //this.thoughtSignatureMap.delete(toolCall.id)
            }
            parts.push(toolDesc);
          }
        }

        contents.push({
          role,
          parts,
        });
        
        if (request.model.includes("gemini-3")){
          contents.push({
            role :"user",
            parts: [{text:"Enter talkative mode, all explanations should be detailed."},{text:"所有的回复必须使用中文."}],
          });
        }
      }
    }

    // const requestUrl = new URL(
    //   `https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:${request.stream ? "streamGenerateContent?alt=sse" : "generateContent"}`
    // );
    request.stream = true;
    const requestUrl = new URL(
      `https://daily-cloudcode-pa.googleapis.com/v1internal:${request.stream ? "streamGenerateContent?alt=sse" : "generateContent"}`
    );


    let thinkingLevel = "high";
    if (request.model === "gemini-3-pro-low") {
      thinkingLevel = "low";
    }

    if (request.model.includes("-pro")) {
      request.model = request.model + "-thinking";
    }

    const generationConfig = {
      maxOutputTokens: 64000,
      temperature: 0.4
    };

    if (request.model.includes("-thinking")) {
      generationConfig.thinkingConfig = {
        includeThoughts: true,
        thinkingLevel: thinkingLevel
      };
      // 删除 model 的 -thinking 后缀
      if (!request.model.includes("claude")) {
        request.model = request.model.replace("-thinking", "");
      }
    }

    const bodyRequest = {
      contents: contents,
      tools: tools.length ? tools : undefined,
      generationConfig: generationConfig,
      systemInstruction: {
        role: "user",
        parts: [
          {
            text: "<identity>\nYou are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.\nYou are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.\nThe USER will send you requests, which you must always prioritize addressing. Along with each USER request, we will attach additional metadata about their current state, such as what files they have open and where their cursor is.\nThis information may or may not be relevant to the coding task, it is up for you to decide.\n</identity>\n\n<tool_calling>\nCall tools as you normally would. The following list provides additional guidance to help you avoid errors:\n  - **Absolute paths only**. When using tools that accept file path arguments, ALWAYS use the absolute file path.\n</tool_calling>\n\n<web_application_development>\n## Technology Stack,\nYour web applications should be built using the following technologies:,\n1. **Core**: Use HTML for structure and Javascript for logic.\n2. **Styling (CSS)**: Use Vanilla CSS for maximum flexibility and control. Avoid using TailwindCSS unless the USER explicitly requests it; in this case, first confirm which TailwindCSS version to use.\n3. **Web App**: If the USER specifies that they want a more complex web app, use a framework like Next.js or Vite. Only do this if the USER explicitly requests a web app.\n4. **New Project Creation**: If you need to use a framework for a new app, use `npx` with the appropriate script, but there are some rules to follow:,\n   - Use `npx -y` to automatically install the script and its dependencies\n   - You MUST run the command with `--help` flag to see all available options first, \n   - Initialize the app in the current directory with `./` (example: `npx -y create-vite-app@latest ./`),\n   - You should run in non-interactive mode so that the user doesn't need to input anything,\n5. **Running Locally**: When running locally, use `npm run dev` or equivalent dev server. Only build the production bundle if the USER explicitly requests it or you are validating the code for correctness.\n\n# Design Aesthetics,\n1. **Use Rich Aesthetics**: The USER should be wowed at first glance by the design. Use best practices in modern web design (e.g. vibrant colors, dark modes, glassmorphism, and dynamic animations) to create a stunning first impression. Failure to do this is UNACCEPTABLE.\n2. **Prioritize Visual Excellence**: Implement designs that will WOW the user and feel extremely premium:\n\t\t- Avoid generic colors (plain red, blue, green). Use curated, harmonious color palettes (e.g., HSL tailored colors, sleek dark modes).\n   - Using modern typography (e.g., from Google Fonts like Inter, Roboto, or Outfit) instead of browser defaults.\n\t\t- Use smooth gradients,\n\t\t- Add subtle micro-animations for enhanced user experience,\n3. **Use a Dynamic Design**: An interface that feels responsive and alive encourages interaction. Achieve this with hover effects and interactive elements. Micro-animations, in particular, are highly effective for improving user engagement.\n4. **Premium Designs**. Make a design that feels premium and state of the art. Avoid creating simple minimum viable products.\n4. **Don't use placeholders**. If you need an image, use your generate_image tool to create a working demonstration.,\n\n## Implementation Workflow,\nFollow this systematic approach when building web applications:,\n1. **Plan and Understand**:,\n\t\t- Fully understand the user's requirements,\n\t\t- Draw inspiration from modern, beautiful, and dynamic web designs,\n\t\t- Outline the features needed for the initial version,\n2. **Build the Foundation**:,\n\t\t- Start by creating/modifying `index.css`,\n\t\t- Implement the core design system with all tokens and utilities,\n3. **Create Components**:,\n\t\t- Build necessary components using your design system,\n\t\t- Ensure all components use predefined styles, not ad-hoc utilities,\n\t\t- Keep components focused and reusable,\n4. **Assemble Pages**:,\n\t\t- Update the main application to incorporate your design and components,\n\t\t- Ensure proper routing and navigation,\n\t\t- Implement responsive layouts,\n5. **Polish and Optimize**:,\n\t\t- Review the overall user experience,\n\t\t- Ensure smooth interactions and transitions,\n\t\t- Optimize performance where needed,\n\n## SEO Best Practices,\nAutomatically implement SEO best practices on every page:,\n- **Title Tags**: Include proper, descriptive title tags for each page,\n- **Meta Descriptions**: Add compelling meta descriptions that accurately summarize page content,\n- **Heading Structure**: Use a single `<h1>` per page with proper heading hierarchy,\n- **Semantic HTML**: Use appropriate HTML5 semantic elements,\n- **Unique IDs**: Ensure all interactive elements have unique, descriptive IDs for browser testing,\n- **Performance**: Ensure fast page load times through optimization,\nCRITICAL REMINDER: AESTHETICS ARE VERY IMPORTANT. If your web app looks simple and basic then you have FAILED!\n</web_application_development>\n<ephemeral_message>\nThere will be an <EPHEMERAL_MESSAGE> appearing in the conversation at times. This is not coming from the user, but instead injected by the system as important information to pay attention to. \nDo not respond to nor acknowledge those messages, but do follow them strictly.\n</ephemeral_message>\n\n\n<communication_style>\n- **Formatting**. Format your responses in github-style markdown to make your responses easier for the USER to parse. For example, use headers to organize your responses and bolded or italicized text to highlight important keywords. Use backticks to format file, directory, function, and class names. If providing a URL to the user, format this in markdown as well, for example `[label](example.com)`.\n- **Proactiveness**. As an agent, you are allowed to be proactive, but only in the course of completing the user's task. For example, if the user asks you to add a new component, you can edit the code, verify build and test statuses, and take any other obvious follow-up actions, such as performing additional research. However, avoid surprising the user. For example, if the user asks HOW to approach something, you should answer their question and instead of jumping into editing a file.\n- **Helpfulness**. Respond like a helpful software engineer who is explaining your work to a friendly collaborator on the project. Acknowledge mistakes or any backtracking you do as a result of new information.\n- **Ask for clarification**. If you are unsure about the USER's intent, always ask for clarification rather than making assumptions.\n</communication_style>"
          }
        ]
      },
      toolConfig: {functionCallingConfig:{mode:"VALIDATED"}}
    };

    const body = {
      request: bodyRequest,
      model: request.model,
    };

    // 获取有效的access_token，按模型类型分别计数
    let currentCount;
    if (request.model.includes("claude")) {
      currentCount = this.claudeCount;
      this.claudeCount += 1;
    } else if (request.model.includes("gemini-3-pro") || request.model.includes("gemini-2.5-pro")) {
      currentCount = this.geminiProCount;
      this.geminiProCount += 1;
    } else if (request.model.includes("gemini-3-flash")) {
      currentCount = this.gemini3FlashCount;
      this.gemini3FlashCount += 1;
    } else if (request.model.includes("gemini-2.5-flash")) {
      currentCount = this.gemini25FlashCount;
      this.gemini25FlashCount += 1;
    } else {
      currentCount = this.REQ_COUNT;
      this.REQ_COUNT += 1;
      console.log("unknow model count++");
    }
    let blance_index = currentCount % this.oauth_num;
    let blance_oauth = this.oauth_creds[blance_index];
    let accessToken = blance_oauth.access_token;
    const config = {
      url: requestUrl,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        "user-agent": "antigravity/1.16.5 windows/amd64",
        "Content-Type":"application/json",
        "Host":"daily-cloudcode-pa.googleapis.com",
      },
      retryMax : 4,
      retryCode : 503,
    };
    body.project = blance_oauth.project;
    body.userAgent = "antigravity";
    body.requestType = "agent";
    body.requestId = this.getConversationId(blance_index);

    const result = {
      body: body,
      config: config,
    };
    
    return result;
  }

  async transformResponseOut(response,context) {
    if (response.headers.get("Content-Type")?.includes("application/json")) {
      let jsonResponse = await response.json();
      jsonResponse = jsonResponse.response;
      // 检测并记录 thoughtSignature
      const tool_calls = []
      jsonResponse.candidates[0].content.parts?.forEach(part => {
        if (part.functionCall) {
          let randomId = `tool_${Math.random().toString(36).substring(2, 15)}`
          if (part.thoughtSignature) {
            const id = part.functionCall?.id || randomId;
            this.thoughtSignatureMap.set(id, part.thoughtSignature);
            this.thoughtSignatureTimeMap.set(id, +new Date());
          }
          tool_calls.push({
            id:
              part.functionCall?.id || randomId,
            type: "function",
            function: {
              name: part.functionCall?.name,
              arguments: JSON.stringify(part.functionCall?.args || {}),
            },
          })
        }
      });

      const res = {
        id: jsonResponse.responseId,
        choices: [
          {
            finish_reason:
              jsonResponse.candidates[0].finishReason?.toLowerCase() || null,
            index: 0,
            message: {
              content: jsonResponse.candidates[0].content.parts
                .filter((part) => part.text && !part.thought)
                .map((part) => part.text)
                .join("\n"),
              reasoning: jsonResponse.candidates[0].content.parts
                        ?.filter((part) => part.text && part.thought)
                        ?.map((part) => part.text)
                        ?.join("\n"),
              role: "assistant",
              tool_calls: tool_calls.length > 0 ? tool_calls : undefined,
            },
          },
        ],
        created: parseInt(new Date().getTime() / 1000 + "", 10),
        model: context.req.orgiModel,
        object: "chat.completion",
        usage: {
          completion_tokens: jsonResponse.usageMetadata?.candidatesTokenCount ?? 0,
          prompt_tokens: jsonResponse.usageMetadata?.promptTokenCount ?? 0,
          total_tokens: jsonResponse.usageMetadata?.totalTokenCount ?? 0,
        },
      };
      return new Response(JSON.stringify(res), {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers,
      });
    } else if (response.headers.get("Content-Type")?.includes("stream")) {
      if (!response.body) {
        return response;
      }

      const decoder = new TextDecoder();
      const encoder = new TextEncoder();

      const processLine = (line, controller) => {
        if (line.startsWith("data: ")) {
          const chunkStr = line.slice(6).trim();
          if (chunkStr) {
            this.logger.debug({ chunkStr }, "gemini-cli chunk:");
            try {
              let chunk = JSON.parse(chunkStr);
              chunk = chunk.response;

              const tool_calls = []
              let toolCallIndex = 0;
              chunk.candidates[0].content.parts?.forEach(part => {
                if (part.functionCall) {
                  let randomId = `tool_${Math.random().toString(36).substring(2, 15)}`
                  const id = part.functionCall?.id || randomId;
                  if (part.thoughtSignature) {
                    this.thoughtSignatureMap.set(id, part.thoughtSignature);
                    this.thoughtSignatureTimeMap.set(id, +new Date());
                  }
                  tool_calls.push({
                    index: toolCallIndex++,
                    id: id,
                    type: "function",
                    function: {
                      name: part.functionCall?.name,
                      arguments: JSON.stringify(part.functionCall?.args || {}),
                    },
                  })
                }
              });
              
              let delta = {
                content: chunk.candidates[0].content.parts
                        ?.filter((part) => part.text && !part.thought)
                        ?.map((part) => part.text)
                        ?.join("\n"),
                role: "assistant",
                tool_calls: tool_calls.length > 0 ? tool_calls : undefined,
              }
              
              let reasoning = chunk.candidates[0].content.parts
                        ?.filter((part) => part.text && part.thought)
                        ?.map((part) => part.text)
                        ?.join("\n");
              if (reasoning) {
                delta.reasoning = reasoning;
              }
              const res = {
                choices: [
                  {
                    delta: delta,
                    finish_reason:
                      chunk.candidates[0].finishReason?.toLowerCase() || null,
                    index:
                      chunk.candidates[0].index || tool_calls.length > 0
                        ? 1
                        : 0,
                    logprobs: null,
                  },
                ],
                created: parseInt(new Date().getTime() / 1000 + "", 10),
                id: chunk.responseId || "",
                model: context.req.orgiModel || "",
                object: "chat.completion.chunk",
                system_fingerprint: "fp_a49d71b8a1",
                usage: {
                  completion_tokens: chunk.usageMetadata?.candidatesTokenCount ?? 0,
                  prompt_tokens: chunk.usageMetadata?.promptTokenCount ?? 0,
                  total_tokens: chunk.usageMetadata?.totalTokenCount ?? 0,
                },
              };
              if (
                chunk.candidates[0]?.groundingMetadata?.groundingChunks?.length
              ) {
                res.choices[0].delta.annotations =
                  chunk.candidates[0].groundingMetadata.groundingChunks.map(
                    (groundingChunk, index) => {
                      const support =
                        chunk.candidates[0]?.groundingMetadata?.groundingSupports?.filter(
                          (item) => item.groundingChunkIndices.includes(index)
                        );
                      return {
                        type: "url_citation",
                        url_citation: {
                          url: groundingChunk.web.uri,
                          title: groundingChunk.web.title,
                          content: support?.[0].segment.text,
                          start_index: support?.[0].segment.startIndex,
                          end_index: support?.[0].segment.endIndex,
                        },
                      };
                    }
                  );
              }
              controller.enqueue(
                encoder.encode(`data: ${JSON.stringify(res)}\n\n`)
              );
            } catch (error) {
              this.logger.error(
                { chunkStr, error },
                "Error parsing Gemini stream chunk"
              );
            }
          }
        }
      };

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
                break;
              }

              buffer += decoder.decode(value, { stream: true });
              const lines = buffer.split("\n");

              buffer = lines.pop() || "";

              for (const line of lines) {
                processLine(line, controller);
              }
            }
          } catch (error) {
            controller.error(error);
          } finally {
            controller.close();
          }
        },
      });

      return new Response(stream, {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers,
      });
    }
    return response;
  }

  refreshToken(arrayIndex = null) {
    // 如果已经有正在进行的 refreshToken 请求，直接返回该 Promise
    if (this.refreshTokenPromise) {
      console.log("Token refresh already in progress, waiting for existing request...");
      return this.refreshTokenPromise;
    }

    // 获取要刷新的凭证对象
    let oauthobj;
    if (arrayIndex !== null && Array.isArray(this.oauth_creds)) {
      oauthobj = this.oauth_creds[arrayIndex];
    } else {
      throw new Error("Invalid token refresh parameters");
    }

    // 创建新的 refreshToken Promise
    this.refreshTokenPromise = fetch("https://oauth2.googleapis.com/token", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        client_id: oauthobj.client_id,
        client_secret: oauthobj.client_secret,
        refresh_token: oauthobj.refresh_token,
        grant_type: "refresh_token",
      }),
    })
      .then((response) => response.json())
      .then(async (data) => {
        oauthobj.expiry_date = new Date().getTime() + data.expires_in * 1000 - 1000 * 60;
        oauthobj.access_token = data.access_token;

        // 使用新的 access_token 调用 cloudcode-pa.googleapis.com API
        const cloudCodeResponse = await fetch("https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", {
          method: "POST",
          headers: {
            'Host': 'cloudcode-pa.googleapis.com',
            'User-Agent': 'antigravity/1.11.9 windows/amd64',
            'Authorization': `Bearer ${data.access_token}`,
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({
            "metadata": {
              "ideType": "ANTIGRAVITY"
            }
          })
        });

        if (cloudCodeResponse.ok) {
          const cloudCodeData = await cloudCodeResponse.json();
          if (cloudCodeData.cloudaicompanionProject) {
            oauthobj.project = cloudCodeData.cloudaicompanionProject;
          }
        }

        await fs.writeFile(this.oauth_path, JSON.stringify(this.oauth_creds, null, 2));
      })
      .finally(() => {
        // 请求完成后清除 Promise，允许后续的刷新请求
        this.refreshTokenPromise = null;
      });

    return this.refreshTokenPromise;
  }
}

module.exports = AntigravityTransformer;