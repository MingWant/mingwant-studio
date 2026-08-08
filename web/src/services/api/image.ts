import axios from "axios";

import { buildApiUrl, isSystemProxyBaseUrl, resolveBackendApiUrl, resolveModelRequestConfig, type AiConfig, type ModelChannel } from "@/stores/use-config-store";
import { nanoid } from "nanoid";
import { chatToolAssistantMessage, onlineAgentChatReasoningOptions, requireUsableAgentToolResponse, resolveOnlineAgentToolChoice, type AgentToolChoice } from "@/lib/agent-tool-response";
import { createClientId } from "@/lib/client-id";
import { confirmsProviderBillingUncertain, confirmsProviderWasNotCalled, isProviderBillingUncertainStatus, providerBillingUncertainMessage, providerConnectionUncertainMessage } from "@/lib/provider-request-error";
import { channelRequest } from "@/services/api/custom-channel-relay";

export type AiTextMessage = {
    role: "system" | "user" | "assistant";
    content: string | Array<{ type: "text"; text: string } | { type: "image_url"; image_url: { url: string } }>;
};

export type ResponseToolCall = {
    id: string;
    type: "function";
    function: { name: string; arguments: string };
    thoughtSignature?: string;
};

export type ResponseInputMessage =
    | AiTextMessage
    | { type: "function_call"; call_id: string; name: string; arguments: string; thoughtSignature?: string; reasoningContent?: string; assistantContent?: string }
    | { role: "tool"; tool_call_id: string; name?: string; content: string };

export type ResponseFunctionTool = {
    type: "function";
    function: {
        name: string;
        description?: string;
        parameters: Record<string, unknown>;
        strict?: boolean;
    };
};

export type ToolResponseResult = {
    content: string;
    toolCalls: ResponseToolCall[];
    reasoningContent?: string;
    truncated?: boolean;
    incompleteReason?: string;
};

type ToolChoice = AgentToolChoice;
type ResponseMessageContent = AiTextMessage["content"] | string;
type ResponseInputContent = { type: "input_text"; text: string } | { type: "input_image"; image_url: string };
type ResponseInputItem =
    | { role: "system" | "user" | "assistant"; content: string | ResponseInputContent[] }
    | { type: "function_call"; call_id: string; name: string; arguments: string }
    | { type: "function_call_output"; call_id: string; output: string };
type ResponseApiToolDefinition = {
    type: "function";
    name: string;
    description?: string;
    parameters: Record<string, unknown>;
    strict?: boolean;
};
type ResponseApiOutputItem =
    | { type?: "message"; content?: Array<{ type?: string; text?: string }> }
    | { type?: "function_call"; id?: string; call_id?: string; name?: string; arguments?: unknown };
type ResponseApiPayload = {
    id?: string;
    output?: ResponseApiOutputItem[];
    output_text?: string;
    status?: string;
    incomplete_details?: { reason?: string } | null;
    error?: { message?: string };
    code?: number;
    msg?: string;
    data?: Record<string, unknown>;
};
type ResponseStreamToolCall = { id: string; name: string; arguments: string };
type ResponseStreamState = { buffer: string; text: string; payload?: ResponseApiPayload; toolCalls: Map<string, ResponseStreamToolCall>; completed: boolean; error?: string };
type ChatCompletionToolCall = {
    id?: string;
    call_id?: string;
    type?: "function";
    index?: number;
    name?: string;
    arguments?: unknown;
    function?: { name?: string; arguments?: unknown };
};
type ChatCompletionPayload = {
    choices?: Array<{ finish_reason?: string | null; finishReason?: string | null; message?: { content?: string | null; reasoning_content?: string | null; reasoningContent?: string | null; tool_calls?: ChatCompletionToolCall[]; toolCalls?: ChatCompletionToolCall[]; function_call?: ChatCompletionToolCall; functionCall?: ChatCompletionToolCall }; delta?: { content?: string | null; reasoning_content?: string | null; reasoningContent?: string | null; tool_calls?: ChatCompletionToolCall[]; toolCalls?: ChatCompletionToolCall[]; function_call?: ChatCompletionToolCall; functionCall?: ChatCompletionToolCall } }>;
    error?: { message?: string };
    code?: number;
    msg?: string;
    data?: Record<string, unknown>;
    done?: boolean;
    status?: string;
    type?: string;
};
type ChatCompletionStreamToolCall = { id: string; name: string; arguments: string };
type ChatCompletionStreamState = { buffer: string; text: string; reasoningContent: string; finishReason: string; toolCalls: Map<number, ChatCompletionStreamToolCall>; completed: boolean; error?: string };

type GeminiPart = {
    text?: string;
    thought?: boolean;
    inlineData?: { mimeType?: string; data?: string };
    inline_data?: { mime_type?: string; mimeType?: string; data?: string };
    fileData?: { mimeType?: string; fileUri?: string };
    functionCall?: GeminiFunctionCall;
    function_call?: GeminiFunctionCall;
    functionResponse?: { id?: string; name?: string; response?: Record<string, unknown> };
    thoughtSignature?: string;
    thought_signature?: string;
};
type GeminiFunctionCall = { id?: string; name?: string; args?: Record<string, unknown>; arguments?: unknown };
type GeminiContent = { role?: "user" | "model"; parts: GeminiPart[] };
type GeminiPayload = {
    candidates?: Array<{ content?: { parts?: GeminiPart[] }; finishReason?: string; finish_reason?: string }>;
    models?: Array<{ name?: string }>;
    done?: boolean;
    type?: string;
    error?: { message?: string };
    promptFeedback?: { blockReason?: string };
};
type GeminiStreamState = { buffer: string; text: string; toolCalls: ResponseToolCall[]; truncated: boolean; completed: boolean; error?: string; incompleteReason?: string };
type RequestOptions = { signal?: AbortSignal; scene?: string; idempotencyKey?: string; stream?: boolean };
type ToolRequestConfig = AiConfig & { interfaceType?: string };

function readAxiosError(error: unknown, fallback: string, billable = true) {
    if (axios.isCancel(error)) return "请求已取消";
    if (axios.isAxiosError<{ error?: { message?: string }; msg?: string; code?: number }>(error)) {
        if (billable && !error.response) return providerConnectionUncertainMessage();
        const responseData = error.response?.data;
        const detail = responseData?.msg || responseData?.error?.message || "";
        if (detail && confirmsProviderWasNotCalled(detail)) return detail;
        if (billable && detail && confirmsProviderBillingUncertain(detail)) return detail;
        if (billable && isProviderBillingUncertainStatus(error.response?.status)) return readStatusError(error.response?.status, fallback, true);
        return detail || readStatusError(error.response?.status, fallback, billable);
    }
    if (error instanceof DOMException && error.name === "AbortError") return "请求已取消";
    if (billable && error instanceof TypeError && /(?:fetch|network|load failed|连接)/i.test(error.message)) return providerConnectionUncertainMessage();
    return error instanceof Error ? error.message : fallback;
}

function readStatusError(status: number | undefined, fallback: string, billable = true) {
    if (status === 401 || status === 403) return "鉴权失败，请检查 API Key、套餐权限或模型权限";
    if (status === 429) return "请求被限流或额度不足，请稍后重试";
    if (status === 404) return "接口不存在（404），请检查 Base URL 是否填写供应商根地址或 /v1、/v1beta 版本根地址，并确认模型协议与 Postman 一致；不要把完整接口路径填入 Base URL";
    if (status === 405) return "上游不支持当前请求方法（405），请确认模型协议和接口路径与供应商文档一致";
    if ((status === 400 || status === 422) && billable) return "上游拒绝了请求格式，请确认模型协议（Chat Completions / Responses / Gemini）和 Function Calling 兼容性；普通文本能在 Postman 通过，不代表创作台工具调用也可用";
    if (billable && isProviderBillingUncertainStatus(status)) return providerBillingUncertainMessage(status);
    return status ? `${fallback}：${status}` : fallback;
}

function aiApiUrl(config: AiConfig, path: string) {
    return buildApiUrl(config.baseUrl, path);
}

function systemProxyIdempotencyKey(value?: string) {
    // 旧版浏览器或非安全本地上下文可能没有 randomUUID；幂等键缺失时仍要生成
    // 合法随机值，不能让系统渠道请求在真正建连前因 API 不存在而失败。
    return value?.trim() || createClientId();
}

function aiHeaders(config: AiConfig, contentType?: string, scene = "image", idempotencyKey?: string) {
    return {
        Authorization: `Bearer ${config.apiKey}`,
        ...(contentType ? { "Content-Type": contentType } : {}),
        ...(isSystemProxyBaseUrl(config.baseUrl) ? { "X-Canvas-Scene": scene, "X-Idempotency-Key": systemProxyIdempotencyKey(idempotencyKey) } : {}),
    };
}

function geminiBaseUrl(config: Pick<AiConfig, "baseUrl">) {
    // 与 buildApiUrl 一致，必须在把相对系统代理解析成 Backend 绝对地址前保留
    // 系统代理身份；前后端不同端口时不能把代理路径误当成供应商根地址并追加 /v1beta。
    const systemProxy = isSystemProxyBaseUrl(config.baseUrl);
    const normalizedBaseUrl = resolveBackendApiUrl(config.baseUrl).replace(/\/+$/, "");
    const lowerBaseUrl = normalizedBaseUrl.toLowerCase();
    return systemProxy || isSystemProxyBaseUrl(normalizedBaseUrl) || lowerBaseUrl.endsWith("/v1") || lowerBaseUrl.endsWith("/v1beta") ? normalizedBaseUrl : `${normalizedBaseUrl}/v1beta`;
}

function geminiModelName(model: string) {
    return model.trim().replace(/^models\//, "");
}

function geminiApiUrl(config: Pick<AiConfig, "baseUrl" | "model">, action?: "generateContent" | "streamGenerateContent") {
    const baseUrl = geminiBaseUrl(config);
    if (!action) return `${baseUrl}/models`;
    return `${baseUrl}/models/${encodeURIComponent(geminiModelName(config.model))}:${action}`;
}

function geminiHeaders(config: Pick<AiConfig, "apiKey" | "baseUrl">, scene = "image", idempotencyKey?: string) {
    return {
        "x-goog-api-key": config.apiKey,
        "Content-Type": "application/json",
        ...(isSystemProxyBaseUrl(config.baseUrl) ? { "X-Canvas-Scene": scene, "X-Idempotency-Key": systemProxyIdempotencyKey(idempotencyKey) } : {}),
    };
}

function withSystemMessage<T extends ResponseInputMessage>(config: AiConfig, messages: T[]): ResponseInputMessage[] {
    const systemPrompt = config.systemPrompt.trim();
    return systemPrompt ? [{ role: "system" as const, content: systemPrompt }, ...messages] : messages;
}

function toResponseInput(messages: ResponseInputMessage[]): ResponseInputItem[] {
    return messages.flatMap((message): ResponseInputItem[] => {
        if ("type" in message) {
            // Responses 只接受 function_call 的协议字段；Kimi/Gemini 的临时推理签名不能泄漏到下一种协议。
            return [{ type: "function_call", call_id: message.call_id, name: message.name, arguments: message.arguments }];
        }
        if (message.role === "tool") return [{ type: "function_call_output", call_id: message.tool_call_id, output: message.content }];
        return [{ role: message.role, content: toResponseContent(message.content || "") }];
    });
}

function toResponseContent(content: ResponseMessageContent): string | ResponseInputContent[] {
    if (!Array.isArray(content)) return String(content || "");
    return content.map((item) => (item.type === "text" ? { type: "input_text" as const, text: item.text } : { type: "input_image" as const, image_url: item.image_url.url }));
}

function toResponseTool(tool: ResponseFunctionTool): ResponseApiToolDefinition {
    return {
        type: "function",
        name: tool.function.name,
        description: tool.function.description,
        parameters: tool.function.parameters,
        ...(tool.function.strict ? { strict: true } : {}),
    };
}

function toChatCompletionMessages(messages: ResponseInputMessage[], model = "") {
    const result: Array<Record<string, unknown>> = [];
    const toolNameById = new Map<string, string>();
    for (let index = 0; index < messages.length;) {
        const message = messages[index];
        if ("type" in message) {
            const toolCalls: Array<Extract<ResponseInputMessage, { type: "function_call" }>> = [];
            while (index < messages.length && "type" in messages[index]) {
                const call = messages[index] as Extract<ResponseInputMessage, { type: "function_call" }>;
                toolCalls.push(call);
                toolNameById.set(call.call_id, call.name);
                index += 1;
            }
            // reasoning_content 是 Kimi/Moonshot 的临时协议状态；其他兼容网关即使
            // 在首轮返回该字段，也可能拒绝把未知字段带回第二轮 assistant 消息。
            // 工具调用、参数和可见 content 仍完整保留，不影响普通 Chat 多轮上下文。
            const protocolCalls = isKimiChatModel(model)
                ? toolCalls
                : toolCalls.map((call) => ({ call_id: call.call_id, name: call.name, arguments: call.arguments, ...(call.assistantContent !== undefined ? { assistantContent: call.assistantContent } : {}) }));
            result.push(chatToolAssistantMessage(protocolCalls));
            continue;
        }
        if (message.role === "tool") {
            // Kimi 的多轮工具协议要求 tool 消息同时带 name；OpenAI 兼容网关通常忽略该字段，
            // 但缺失时 Kimi 可能在首轮工具成功后拒绝下一轮请求。
            result.push({
                role: "tool",
                tool_call_id: message.tool_call_id,
                ...(message.name || toolNameById.get(message.tool_call_id) ? { name: message.name || toolNameById.get(message.tool_call_id) } : {}),
                content: message.content,
            });
        } else {
            // 许多 OpenAI 兼容网关的文本模型只接受字符串 content；画布消息即使没有图片，
            // 也会统一构造成 [{ type: "text", text: ... }]。纯文本折叠回字符串后，
            // 测活与创作台使用同一种消息形状，同时保留真正带图片的多模态数组。
            result.push({ role: message.role, content: chatMessageContent(message.content) });
        }
        index += 1;
    }
    return result;
}

function toChatCompletionToolChoice(toolChoice: ToolChoice) {
    return typeof toolChoice === "object" ? { type: "function", function: { name: toolChoice.name } } : toolChoice;
}

function isKimiChatModel(model: string) {
    const normalized = model.trim().toLowerCase().replace(/[/:_]/g, "-");
    return normalized.includes("kimi") || normalized.includes("moonshot");
}

function toChatCompletionTools(model: string, tools: ResponseFunctionTool[]) {
    const kimi = isKimiChatModel(model);
    return tools.map((tool) => {
        const functionDefinition = { ...tool.function };
        // Kimi 的省略值等同 strict=true；画布工具故意允许可选字段和 metadata 扩展，
        // 只对 Kimi 显式发送 false。其他 OpenAI 兼容网关可能拒绝未知 strict 字段，
        // 因此不把这个兼容性补丁扩散到所有 Chat 模型。
        if (kimi) functionDefinition.strict = tool.function.strict === true;
        else if (functionDefinition.strict === false) delete functionDefinition.strict;
        return { ...tool, function: functionDefinition };
    });
}

function parseChatCompletionPayload(payload: ChatCompletionPayload): ToolResponseResult {
    const normalized = unwrapChatCompletionPayload(payload);
    if (typeof normalized.code === "number" && normalized.code !== 0) throw new Error(normalized.msg || "请求失败");
    if (normalized.error?.message) throw new Error(normalized.error.message);
    const message = normalized.choices?.[0]?.message;
    const reasoningContent = message?.reasoning_content || message?.reasoningContent || "";
    // 兼容网关有时同时返回 camelCase 与 snake_case；不能因为其中一个字段是空数组
    // 或空对象就把另一种格式的合法工具调用遮掉。
    const rawToolCalls = [...(message?.tool_calls || []), ...(message?.toolCalls || [])];
    const legacyToolCall = [message?.function_call, message?.functionCall]
        .find((call) => Boolean(call?.function?.name || call?.name)) || message?.function_call || message?.functionCall;
    if (legacyToolCall) rawToolCalls.push(legacyToolCall);
    const toolCalls = rawToolCalls
        .map((call, index) => ({
            // 少数兼容网关把 OpenAI Chat 的 id 改成 call_id，旧式 function_call
            // 甚至不返回 id；在完整终态下补稳定本地 ID，保证工具结果能回传，
            // 不把缺少协议元数据误判成“模型没有调用工具”。
            id: call.id || call.call_id || `tool-call-${call.index ?? index}`,
            type: "function" as const,
            function: {
                name: call.function?.name || call.name || "",
                arguments: stringifyToolArguments(call.function?.arguments ?? call.arguments) || "{}",
            },
        }))
        .filter((call) => call.function.name)
        .filter((call, index, calls) => calls.findIndex((item) => item.id === call.id && item.function.name === call.function.name && item.function.arguments === call.function.arguments) === index);
    const finishReason = normalized.choices?.[0]?.finish_reason || normalized.choices?.[0]?.finishReason;
    return {
        content: contentText(message?.content),
        toolCalls,
        ...(reasoningContent.trim() ? { reasoningContent } : {}),
        ...(completionWasTruncated(finishReason) ? { truncated: true } : {}),
        ...(completionWasIncomplete(finishReason) ? { incompleteReason: String(finishReason) } : {}),
    };
}

function chatMessageContent(content: ResponseMessageContent): string | Array<{ type: "text"; text: string } | { type: "image_url"; image_url: { url: string } }> {
    if (!Array.isArray(content)) return content;
    if (content.every((item) => item.type === "text")) return content.map((item) => item.text).join("");
    return content;
}

function parseToolResponse(payload: ResponseApiPayload): ToolResponseResult {
    const normalized = unwrapResponsePayload(payload);
    const output = normalized.output || [];
    const content =
        normalized.output_text ||
        output
            .flatMap((item) => (item.type === "message" ? item.content || [] : []))
            .map((item) => item.text || "")
            .join("");
    const toolCalls = output
        .filter((item): item is Extract<ResponseApiOutputItem, { type?: "function_call" }> => item.type === "function_call")
        .map((item) => ({
            id: item.call_id || item.id || "",
            type: "function" as const,
            function: { name: item.name || "", arguments: stringifyToolArguments(item.arguments) || "{}" },
        }))
        .filter((item) => item.id && item.function.name);
    const incompleteReason = normalized.status === "incomplete" ? normalized.incomplete_details?.reason || "response_incomplete" : "";
    return {
        content,
        toolCalls,
        ...(completionWasTruncated(incompleteReason) ? { truncated: true } : {}),
        ...(incompleteReason && !completionWasTruncated(incompleteReason) ? { incompleteReason } : {}),
    };
}

function completionWasTruncated(value: unknown) {
    return ["length", "max_tokens", "max_completion_tokens", "max_output_tokens"].includes(String(value || "").trim().toLowerCase());
}

function completionWasIncomplete(value: unknown) {
    const reason = String(value || "").trim().toLowerCase();
    return Boolean(reason) && !["stop", "tool_calls", "function_call", "length", "max_tokens", "max_completion_tokens", "max_output_tokens"].includes(reason);
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function responseErrorMessage(value: unknown) {
    return responseErrorMessageAtDepth(value, 0);
}

function responseErrorMessageAtDepth(value: unknown, depth: number): string {
    if (!isRecord(value) || depth > 2) return "";
    const error = isRecord(value.error) ? value.error : undefined;
    if (stringValue(value.msg) || stringValue(error?.message)) return stringValue(value.msg) || stringValue(error?.message);
    for (const key of ["data", "response", "result"]) {
        const nested: unknown = value[key];
        if (nested === value) continue;
        const message = responseErrorMessageAtDepth(nested, depth + 1);
        if (message) return message;
    }
    return "";
}

function stringValue(value: unknown) {
    return typeof value === "string" ? value : "";
}

function contentText(value: unknown): string {
    if (typeof value === "string") return value;
    if (Array.isArray(value)) return value.map((item) => contentText(isRecord(item) ? item.text ?? item.content : item)).join("");
    if (isRecord(value)) return contentText(value.text ?? value.content);
    return "";
}

function stringifyToolArguments(value: unknown): string {
    if (typeof value === "string") return value;
    if (value === undefined || value === null) return "";
    try {
        return JSON.stringify(value);
    } catch {
        return "";
    }
}

function parseStreamingEvent<T>(data: string): T {
    try {
        return JSON.parse(data) as T;
    } catch {
        throw new Error("模型流式响应不完整或已损坏：请求可能已经计费，请勿立即重试，请先核对供应商后台或请求明细");
    }
}

function validateResponsePayload(payload: ResponseApiPayload) {
    const normalized = unwrapResponsePayload(payload);
    if (typeof normalized.code === "number" && normalized.code !== 0) throw new Error(normalized.msg || "请求失败");
    if (normalized.error?.message) throw new Error(normalized.error.message);
}

function unwrapChatCompletionPayload(payload: ChatCompletionPayload | Record<string, unknown>): ChatCompletionPayload {
    let current = payload as Record<string, unknown>;
    for (let depth = 0; depth < 2; depth += 1) {
        const nested = isRecord(current.data) ? current.data : undefined;
        if (!nested || (!Array.isArray(nested.choices) && !isRecord(nested.error) && typeof nested.code !== "number" && typeof nested.msg !== "string" && typeof nested.done !== "boolean" && typeof nested.status !== "string" && typeof nested.type !== "string")) break;
        // 兼容网关可能把 choices 放进 data，却把 done/status 留在外层；两层都要保留。
        current = { ...current, ...nested };
    }
    return current as ChatCompletionPayload;
}

function unwrapResponsePayload(payload: ResponseApiPayload): ResponseApiPayload {
    let current = payload as ResponseApiPayload & { data?: Record<string, unknown> };
    for (let depth = 0; depth < 2; depth += 1) {
        const nested = isRecord(current.data) ? current.data : undefined;
        if (!nested || (!Array.isArray(nested.output) && typeof nested.output_text !== "string" && typeof nested.status !== "string" && !isRecord(nested.error))) break;
        current = { ...current, ...nested } as ResponseApiPayload & { data?: Record<string, unknown> };
    }
    return current;
}

function unwrapResponseStreamEvent(payload: Record<string, unknown>, eventName: string) {
    let current = payload;
    for (let depth = 0; depth < 2; depth += 1) {
        const nested = isRecord(current.data) ? current.data : undefined;
        if (!nested || !hasResponseStreamFields(nested)) break;
        // 终态有时在外层，正文/工具片段在 data 内层；不能只返回内层。
        current = { ...current, ...nested };
    }
    if (!stringValue(current.type) && eventName) return { ...current, type: eventName };
    return current;
}

function hasResponseStreamFields(value: Record<string, unknown>) {
    return ["type", "delta", "text", "response", "output", "item", "item_id", "itemId", "call_id", "callId", "arguments", "status", "done", "error"].some((key) => key in value);
}

function unwrapGeminiPayload(payload: GeminiPayload): GeminiPayload {
    let current = payload as GeminiPayload & { data?: Record<string, unknown> };
    for (let depth = 0; depth < 2; depth += 1) {
        const nested = isRecord(current.data) ? current.data : undefined;
        if (!nested || (!Array.isArray(nested.candidates) && !isRecord(nested.error) && !isRecord(nested.promptFeedback) && typeof nested.done !== "boolean")) break;
        current = { ...current, ...nested } as GeminiPayload & { data?: Record<string, unknown> };
    }
    return current;
}

function validateGeminiPayload(payload: GeminiPayload) {
    const normalized = unwrapGeminiPayload(payload);
    if (normalized.error?.message) throw new Error(normalized.error.message);
    if (normalized.promptFeedback?.blockReason) throw new Error(`Gemini 拒绝了本次请求：${normalized.promptFeedback.blockReason}`);
}

async function readFetchError(response: Response, fallback: string) {
    const text = await response.text();
    let detail = "";
    if (text && !/^\s*(?:<!doctype|<html)/i.test(text)) {
        try {
            detail = responseErrorMessage(JSON.parse(text));
        } catch {
            detail = "";
        }
    }
    if (detail && confirmsProviderWasNotCalled(detail)) return detail;
    if (detail && confirmsProviderBillingUncertain(detail)) return detail;
    if (isProviderBillingUncertainStatus(response.status)) return readStatusError(response.status, fallback, true);
    if (detail) return detail;
    if (!text) return readStatusError(response.status, fallback);
    if (/^\s*(?:<!doctype|<html)/i.test(text)) return readStatusError(response.status, fallback);
    try {
        return responseErrorMessage(JSON.parse(text)) || readStatusError(response.status, fallback);
    } catch {
        return text.slice(0, 300) || readStatusError(response.status, fallback);
    }
}

async function readJsonPayload<T>(response: Response, fallback: string): Promise<T> {
    return parseResponseJsonText(await response.text(), fallback);
}

function parseResponseJsonText<T>(text: string, fallback: string): T {
    try {
        return JSON.parse(text) as T;
    } catch {
        if (/^\s*(?:<!doctype|<html)/i.test(text)) throw new Error("后端代理返回了前端网页，请检查 VITE_CANVAS_BACKEND_URL 和反向代理配置");
        throw new Error(`${fallback}：接口没有返回有效 JSON`);
    }
}

type ResponseBodyReadResult = {
    eventStream: boolean;
    text: string;
};

/**
 * 兼容把 SSE 错标为 application/json 的网关，同时保留真正的渐进读取。
 * 只窥探很短的正文前缀；确认是事件流后立即把已经读到的前缀交给解析器，
 * 不等待完整响应，也不把慢模型重新降级成一次性 JSON。
 */
async function readResponseBodyWithEventStreamDetection(response: Response, onEventStreamChunk: (text: string) => void): Promise<ResponseBodyReadResult> {
    if (!response.body) return { eventStream: false, text: "" };
    const declaredEventStream = (response.headers.get("content-type") || "").toLowerCase().includes("text/event-stream");
    // 即使响应头声明了 SSE 也先看正文首字符，避免“完整 JSON 错标为 SSE”
    // 被交给事件解析器；真正的 SSE 通常在极短前缀内出现 data:/event:。
    let mode: "unknown" | "event-stream" | "json" = "unknown";
    let eventStreamDetected = false;
    let prefix = "";
    const decoder = new TextDecoder();
    const reader = response.body.getReader();
    const consume = (chunk: string) => {
        if (!chunk) return;
        if (mode === "event-stream") {
            onEventStreamChunk(chunk);
            return;
        }
        prefix += chunk;
        if (mode === "json") return;
        const sample = prefix.replace(/^\uFEFF/, "").trimStart();
        if (responseBodyLooksLikeEventStream(sample)) {
            mode = "event-stream";
            eventStreamDetected = true;
            const eventPrefix = prefix.replace(/^\uFEFF/, "");
            prefix = "";
            onEventStreamChunk(eventPrefix);
            return;
        }
        // JSON 的首个非空字符足以区分大多数误标 SSE；未知正文最多窥探 4 KiB，
        // 避免异常网关的长前缀占用内存或让解析迟迟不进入终态。
        if (sample.startsWith("{") || sample.startsWith("[")) mode = "json";
        else if (prefix.length >= 4 * 1024) {
            const fallbackMode: "event-stream" | "json" = declaredEventStream ? "event-stream" : "json";
            mode = fallbackMode;
            if (fallbackMode === "event-stream") {
                eventStreamDetected = true;
                const eventPrefix = prefix.replace(/^\uFEFF/, "");
                prefix = "";
                onEventStreamChunk(eventPrefix);
            }
        }
    };
    try {
        for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            consume(decoder.decode(value, { stream: true }));
        }
        consume(decoder.decode());
    } catch (error) {
        // 事件损坏或已知上游错误时尽快关闭浏览器读取，避免继续等待并把失败误显示成“仍在生成”。
        await reader.cancel().catch(() => undefined);
        throw error;
    }
    if (eventStreamDetected) return { eventStream: true, text: "" };
    return { eventStream: false, text: prefix };
}

function responseBodyLooksLikeEventStream(value: string) {
    return ["data:", "event:", "id:", "retry:", ":"].some((prefix) => value.toLowerCase().startsWith(prefix));
}

function consumeResponseStreamBlock(block: string, state: ResponseStreamState, onDelta?: (text: string) => void) {
    const eventName = sseEventName(block);
    if (["done", "message_stop", "response.completed", "response.incomplete"].includes(eventName)) state.completed = true;
    if (eventName === "response.incomplete") state.payload = { status: "incomplete" };
    const data = block
        .split(/\r\n|\r|\n/)
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).replace(/^ /, ""))
        .join("\n")
        .trim();
    if (!data) return;
    if (data === "[DONE]") {
        state.completed = true;
        return;
    }
    const rawEvent = parseStreamingEvent<Record<string, unknown>>(data);
    const event = unwrapResponseStreamEvent(rawEvent, eventName);
    const type = stringValue(event.type) || eventName;
    const errorMessage = responseErrorMessage(event) || responseErrorMessage(rawEvent);
    if (errorMessage) state.error = errorMessage;
    if (type === "response.failed" && !state.error) state.error = "模型流式响应失败";
    if (type === "response.output_text.delta" && typeof event.delta === "string") {
        state.text += event.delta;
        onDelta?.(state.text);
    }
    if (type === "response.output_text.done" && typeof event.text === "string") {
        mergeResponseStreamText(state, event.text, onDelta);
    }
    collectResponseStreamToolCall(event, state);
    if (type === "response.incomplete") {
        state.completed = true;
        const payload = (isRecord(event.response) ? event.response : event) as ResponseApiPayload;
        state.payload = { ...payload, status: "incomplete" };
    } else if (type === "response.completed") {
        state.completed = true;
        if (isRecord(event.response)) state.payload = event.response as ResponseApiPayload;
        else if (Array.isArray(event.output)) state.payload = event as ResponseApiPayload;
    } else if (Array.isArray(event.output)) {
        state.payload = event as ResponseApiPayload;
    }
    if (type === "done" || stringValue(event.status).toLowerCase() === "completed") state.completed = true;
}

function collectResponseStreamToolCall(event: Record<string, unknown>, state: ResponseStreamState) {
    const item = isRecord(event.item) ? event.item : undefined;
    const itemType = stringValue(item?.type || event.type).toLowerCase();
    // Responses 标准流把 function_call 的 `id`（item_id）和真正用于回传结果的
    // `call_id` 分开：首个 output_item 事件通常同时给出两者，后续参数分片只带
    // item_id。没有显式 call_id 的分片不能覆盖之前已经收到的 call_id。
    const itemId = stringValue(item?.id || event.item_id || event.itemId);
    const explicitCallId = stringValue(item?.call_id || event.call_id || event.callId);
    const name = stringValue(item?.name || event.name);
    const key = itemId || explicitCallId;
    const isFunctionCall = itemType === "function_call" || typeIsFunctionCallEvent(stringValue(event.type));
    if (!isFunctionCall || !key) return;
    const current = state.toolCalls.get(key) || { id: explicitCallId || itemId || key, name: "", arguments: "" };
    const argumentDelta = stringifyToolArguments(event.delta);
    const fullArguments = stringifyToolArguments(item?.arguments ?? event.arguments);
    // 兼容网关有时会先把占位 `{}` 放入 output_item，再发送真正的 delta；
    // 只取 fullArguments 会覆盖已累计参数，随后交给工具解析的正文就会损坏。
    const nextArguments = mergeToolArgumentFragments(current.arguments, argumentDelta || fullArguments, Boolean(fullArguments && !argumentDelta));
    state.toolCalls.set(key, {
        id: explicitCallId || current.id || itemId || key,
        name: name || current.name,
        arguments: nextArguments,
    });
}

function mergeResponseStreamText(state: ResponseStreamState, value: string, onDelta?: (text: string) => void) {
    if (!value || value.length <= state.text.length) return;
    state.text = value;
    onDelta?.(state.text);
}

function typeIsFunctionCallEvent(type: string) {
    return type === "response.output_item.added" || type === "response.output_item.done" || type === "response.function_call_arguments.delta" || type === "response.function_call_arguments.done";
}

function consumeResponseStreamText(state: ResponseStreamState, text: string, onDelta?: (text: string) => void, flush = false) {
    state.buffer += text;
    for (;;) {
        const match = state.buffer.match(/(?:\r\n|\r|\n){2}/);
        if (!match) break;
        const index = match.index ?? 0;
        consumeResponseStreamBlock(state.buffer.slice(0, index), state, onDelta);
        state.buffer = state.buffer.slice(index + match[0].length);
    }
    if (flush && state.buffer.trim()) {
        consumeResponseStreamBlock(state.buffer, state, onDelta);
        state.buffer = "";
    }
}

async function requestStreamingResponse(config: AiConfig, body: Record<string, unknown>, onDelta?: (text: string) => void, options?: RequestOptions): Promise<ToolResponseResult> {
    const streaming = options?.stream !== false;
    const request = channelRequest(config, aiApiUrl(config, "/responses"), { ...aiHeaders(config, "application/json", options?.scene || "text", options?.idempotencyKey), Accept: streaming ? "text/event-stream" : "application/json" });
    const response = await fetch(request.url, {
        method: "POST",
        headers: request.headers,
        body: JSON.stringify({ ...body, ...(streaming ? { stream: true } : {}) }),
        signal: options?.signal,
        credentials: request.credentials,
    });
    if (!response.ok) throw new Error(await readFetchError(response, "请求失败"));
    const state: ResponseStreamState = { buffer: "", text: "", toolCalls: new Map(), completed: false };
    const bodyResult = await readResponseBodyWithEventStreamDetection(response, (chunk) => {
        consumeResponseStreamText(state, chunk, onDelta);
        if (state.error) throw new Error(state.error);
    });
    if (!bodyResult.eventStream) {
        // 部分兼容网关会忽略 stream=true 并返回完整 JSON；这里仍解析结果，但测活会把该模型标为非流式风险。
        const payload = parseResponseJsonText<ResponseApiPayload>(bodyResult.text, "请求失败");
        validateResponsePayload(payload);
        return parseToolResponse(payload);
    }

    consumeResponseStreamText(state, "", onDelta, true);
    if (state.error) throw new Error(state.error);
    // HTTP 200 或自然 EOF 不等于模型完成；没有终态时不能把已收到的工具片段交给画布。
    if (!state.completed) throw new Error("模型流式响应缺少完成标记，结果可能不完整且费用状态不确定；请勿立即重试");
    if (!state.payload) {
        return { content: state.text, toolCalls: responseStreamToolCalls(state.toolCalls) };
    }
    validateResponsePayload(state.payload);
    const result = parseToolResponse(state.payload);
    const streamedToolCalls = responseStreamToolCalls(state.toolCalls);
    const content = result.content.length >= state.text.length ? result.content : state.text;
    return { ...result, toolCalls: mergeResponseToolCalls(result.toolCalls, streamedToolCalls), content };
}

function responseStreamToolCalls(calls: Map<string, ResponseStreamToolCall>): ResponseToolCall[] {
    return Array.from(calls.values())
        .map((call) => ({ id: call.id, type: "function" as const, function: { name: call.name, arguments: call.arguments || "{}" } }))
        .filter((call) => call.id && call.function.name);
}

function mergeResponseToolCalls(primary: ResponseToolCall[], streamed: ResponseToolCall[]) {
    const merged = new Map<string, ResponseToolCall>();
    [...primary, ...streamed].forEach((call) => {
        if (!call.id || !call.function.name) return;
        const current = merged.get(call.id);
        merged.set(call.id, current && current.function.arguments.length >= call.function.arguments.length ? current : call);
    });
    return Array.from(merged.values());
}

function consumeChatCompletionStreamBlock(block: string, state: ChatCompletionStreamState, onDelta?: (text: string) => void) {
    const eventName = sseEventName(block);
    if (["done", "message_stop", "response.completed", "response.incomplete"].includes(eventName)) state.completed = true;
    if (eventName === "response.incomplete") state.finishReason = "response.incomplete";
    const data = block
        .split(/\r\n|\r|\n/)
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).replace(/^ /, ""))
        .join("\n")
        .trim();
    if (!data) return;
    if (data === "[DONE]") {
        state.completed = true;
        return;
    }
    const event = parseStreamingEvent<Record<string, unknown>>(data);
    const normalizedEvent = unwrapChatCompletionPayload(event);
    const errorMessage = responseErrorMessage(normalizedEvent) || responseErrorMessage(event);
    if (errorMessage) state.error = errorMessage;
    const choices = Array.isArray(normalizedEvent.choices) ? normalizedEvent.choices : [];
    const choice = isRecord(choices[0]) ? choices[0] : undefined;
    const finishReason = stringValue(choice?.finish_reason) || stringValue(choice?.finishReason);
    if (finishReason) {
        state.finishReason = finishReason;
        state.completed = true;
    }
    if (normalizedEvent.done === true || ["done", "message_stop", "completed"].includes(stringValue(normalizedEvent.status).trim().toLowerCase()) || ["done", "message_stop", "response.incomplete"].includes(stringValue(normalizedEvent.type).trim().toLowerCase())) state.completed = true;
    const message = choice && isRecord(choice.message) ? choice.message : undefined;
    const delta = choice && isRecord(choice.delta) ? choice.delta : undefined;
    if (!delta) {
        // 一些兼容网关的最后一个事件只返回完整 message.tool_calls 和 message.content，
        // 没有 delta；必须把这段可见正文保留下来，后续 Kimi 工具消息才能复现首轮 choice.message。
        mergeChatCompletionMessageContent(state, contentText(message?.content), onDelta);
        appendReasoningContent(state, stringValue(message?.reasoning_content) || stringValue(message?.reasoningContent));
        collectChatCompletionStreamToolCalls(message?.tool_calls, state, true);
        collectChatCompletionStreamToolCalls(message?.toolCalls, state, true);
        collectChatCompletionStreamToolCalls(message?.function_call, state, true);
        collectChatCompletionStreamToolCalls(message?.functionCall, state, true);
        return;
    }
    const deltaContent = contentText(delta.content);
    if (deltaContent) {
        state.text += deltaContent;
        onDelta?.(state.text);
    }
    // 若网关同时给出空 delta 和完整 message，允许补齐；有真实 delta 时不能重复追加同一正文。
    if (!deltaContent) mergeChatCompletionMessageContent(state, contentText(message?.content), onDelta);
    appendReasoningContent(state, stringValue(delta.reasoning_content) || stringValue(delta.reasoningContent));
    collectChatCompletionStreamToolCalls(delta.tool_calls, state, false);
    collectChatCompletionStreamToolCalls(delta.toolCalls, state, false);
    collectChatCompletionStreamToolCalls(delta.function_call, state, false);
    collectChatCompletionStreamToolCalls(delta.functionCall, state, false);
    collectChatCompletionStreamToolCalls(message?.tool_calls, state, true);
    collectChatCompletionStreamToolCalls(message?.toolCalls, state, true);
    collectChatCompletionStreamToolCalls(message?.function_call, state, true);
    collectChatCompletionStreamToolCalls(message?.functionCall, state, true);
}

function mergeChatCompletionMessageContent(state: ChatCompletionStreamState, value: string, onDelta?: (text: string) => void) {
    if (!value || value.length <= state.text.length) return;
    state.text = value;
    onDelta?.(state.text);
}

function appendReasoningContent(state: ChatCompletionStreamState, value: string) {
    if (!value) return;
    const current = state.reasoningContent;
    if (!current || value === current || value.startsWith(current)) {
        state.reasoningContent = value;
        return;
    }
    if (current.endsWith(value)) return;
    state.reasoningContent += value;
}

function collectChatCompletionStreamToolCalls(value: unknown, state: ChatCompletionStreamState, replaceArguments: boolean) {
    const items = Array.isArray(value) ? value : isRecord(value) ? [value] : [];
    items.forEach((item, fallbackIndex) => {
        if (!isRecord(item)) return;
        const callIndex = typeof item.index === "number" ? item.index : fallbackIndex;
        const current = state.toolCalls.get(callIndex) || { id: `tool-call-${callIndex}`, name: "", arguments: "" };
        // 兼容旧式 function_call 及将 call_id/name/arguments 放在顶层的网关，
        // 仍然只在完整流终态后交给工具执行。
        const fn = isRecord(item.function) ? item.function : item;
        const nextArguments = stringifyToolArguments(fn.arguments);
        const mergedArguments = mergeToolArgumentFragments(current.arguments, nextArguments, replaceArguments);
        state.toolCalls.set(callIndex, {
            id: stringValue(item.id) || stringValue(item.call_id) || current.id,
            name: stringValue(fn.name) || stringValue(item.name) || current.name,
            arguments: mergedArguments || current.arguments,
        });
    });
}

/**
 * 兼容两类 OpenAI 兼容网关：标准实现发送参数增量，部分实现却在每个 SSE
 * 事件重复发送截至当前的完整参数。两者混用时不能盲目拼接，否则合法 JSON
 * 会变成 `{"a":1}{"a":1}`，创作台首个工具调用会在本地解析阶段失败。
 */
function mergeToolArgumentFragments(current: string, incoming: string, replace = false) {
    const previous = current === "{}" ? "" : current;
    if (!incoming) return previous;
    if (replace) return incoming.length >= previous.length ? incoming : previous;
    if (!previous) return incoming;
    if (incoming === previous || incoming.startsWith(previous)) return incoming;
    if (previous.startsWith(incoming)) return previous;
    return previous + incoming;
}

function consumeChatCompletionStreamText(state: ChatCompletionStreamState, text: string, onDelta?: (text: string) => void, flush = false) {
    state.buffer += text;
    for (;;) {
        const match = state.buffer.match(/(?:\r\n|\r|\n){2}/);
        if (!match) break;
        const index = match.index ?? 0;
        consumeChatCompletionStreamBlock(state.buffer.slice(0, index), state, onDelta);
        state.buffer = state.buffer.slice(index + match[0].length);
    }
    if (flush && state.buffer.trim()) {
        consumeChatCompletionStreamBlock(state.buffer, state, onDelta);
        state.buffer = "";
    }
}

async function requestStreamingChatCompletion(config: AiConfig, body: Record<string, unknown>, onDelta?: (text: string) => void, options?: RequestOptions): Promise<ToolResponseResult> {
    const streaming = options?.stream !== false;
    const request = channelRequest(config, aiApiUrl(config, "/chat/completions"), { ...aiHeaders(config, "application/json", options?.scene || "text", options?.idempotencyKey), Accept: streaming ? "text/event-stream" : "application/json" });
    const response = await fetch(request.url, {
        method: "POST",
        headers: request.headers,
        body: JSON.stringify({ ...body, ...(streaming ? { stream: true } : {}) }),
        signal: options?.signal,
        credentials: request.credentials,
    });
    if (!response.ok) throw new Error(await readFetchError(response, "请求失败"));
    const state: ChatCompletionStreamState = { buffer: "", text: "", reasoningContent: "", finishReason: "", toolCalls: new Map(), completed: false };
    const bodyResult = await readResponseBodyWithEventStreamDetection(response, (chunk) => {
        consumeChatCompletionStreamText(state, chunk, onDelta);
        if (state.error) throw new Error(state.error);
    });
    if (!bodyResult.eventStream) return parseChatCompletionPayload(parseResponseJsonText<ChatCompletionPayload>(bodyResult.text, "请求失败"));
    consumeChatCompletionStreamText(state, "", onDelta, true);
    if (state.error) throw new Error(state.error);
    // Chat 工具参数可能跨多个 delta，必须等 finish_reason/[DONE] 才能确认参数闭合。
    if (!state.completed) throw new Error("模型流式响应缺少完成标记，工具参数可能不完整且费用状态不确定；请勿立即重试");
    const toolCalls = Array.from(state.toolCalls.entries())
        .sort(([left], [right]) => left - right)
        .map(([, call]) => ({ id: call.id, type: "function" as const, function: { name: call.name, arguments: call.arguments || "{}" } }))
        .filter((call) => call.id && call.function.name);
    return {
        content: state.text,
        toolCalls,
        ...(state.reasoningContent.trim() ? { reasoningContent: state.reasoningContent } : {}),
        ...(completionWasTruncated(state.finishReason) ? { truncated: true } : {}),
        ...(completionWasIncomplete(state.finishReason) ? { incompleteReason: state.finishReason } : {}),
    };
}

function toGeminiBody(config: AiConfig, messages: ResponseInputMessage[], extra?: Record<string, unknown>) {
    const systemText = [
        config.systemPrompt.trim(),
        ...messages.flatMap((message) => (!("type" in message) && message.role === "system" ? [geminiTextContent(message.content)] : [])),
    ]
        .filter(Boolean)
        .join("\n\n");
    const contents = toGeminiContents(messages.filter((message) => ("type" in message ? true : message.role !== "system")));
    return {
        contents,
        ...(systemText ? { systemInstruction: { parts: [{ text: systemText }] } } : {}),
        ...extra,
    };
}

function toGeminiContents(messages: ResponseInputMessage[]): GeminiContent[] {
    const callNameById = new Map<string, string>();
    return messages.flatMap((message): GeminiContent[] => {
        if ("type" in message) {
            callNameById.set(message.call_id, message.name);
            return [{ role: "model", parts: [{ functionCall: { id: message.call_id, name: message.name, args: jsonObject(message.arguments) }, ...(message.thoughtSignature ? { thoughtSignature: message.thoughtSignature } : {}) }] }];
        }
        if (message.role === "tool") {
            const name = callNameById.get(message.tool_call_id) || "tool_result";
            return [{ role: "user", parts: [{ functionResponse: { id: message.tool_call_id, name, response: { result: jsonValue(message.content) } } }] }];
        }
        return [{ role: message.role === "assistant" ? "model" : "user", parts: toGeminiParts(message.content) }];
    });
}

function toGeminiParts(content: ResponseMessageContent): GeminiPart[] {
    if (!Array.isArray(content)) return [{ text: String(content || "") }];
    return content.map((item) => (item.type === "text" ? { text: item.text } : toGeminiImagePart(item.image_url.url)));
}

function toGeminiImagePart(url: string): GeminiPart {
    const match = url.match(/^data:([^;,]+);base64,(.+)$/);
    if (match) return { inlineData: { mimeType: match[1], data: match[2] } };
    return { fileData: { fileUri: url, mimeType: "image/png" } };
}

function geminiTextContent(content: ResponseMessageContent) {
    if (!Array.isArray(content)) return String(content || "");
    return content.map((item) => (item.type === "text" ? item.text : item.image_url.url)).join("\n");
}

function jsonObject(value: string): Record<string, unknown> {
    const parsed = jsonValue(value);
    return isRecord(parsed) ? parsed : {};
}

function jsonValue(value: string): unknown {
    try {
        return JSON.parse(value);
    } catch {
        return value;
    }
}

function toGeminiToolOptions(tools: ResponseFunctionTool[], toolChoice: ToolChoice) {
    if (!tools.length) return {};
    const functionDeclarations = tools.map((tool) => ({
        name: tool.function.name,
        description: tool.function.description,
        // Gemini GenerateContent 只接受 OpenAPI Schema 子集；OpenAI 工具定义里的
        // additionalProperties、oneOf 等字段原样透传会让“文本测活成功”的模型在工具请求阶段被拒绝。
        parameters: toGeminiSchema(tool.function.parameters),
    }));
    const functionCallingConfig =
        typeof toolChoice === "object"
            ? { mode: "ANY", allowedFunctionNames: [toolChoice.name] }
            : { mode: toolChoice === "required" ? "ANY" : "AUTO" };
    return {
        tools: [{ functionDeclarations }],
        toolConfig: { functionCallingConfig },
    };
}

async function requestGeminiStreamingResponse(config: AiConfig, body: Record<string, unknown>, onDelta?: (text: string) => void, options?: RequestOptions): Promise<ToolResponseResult> {
    const streaming = options?.stream !== false;
    const action = streaming ? "streamGenerateContent" : "generateContent";
    const request = channelRequest(config, `${geminiApiUrl(config, action)}${streaming ? "?alt=sse" : ""}`, geminiHeaders(config, options?.scene || "text", options?.idempotencyKey));
    const response = await fetch(request.url, {
        method: "POST",
        headers: request.headers,
        body: JSON.stringify(body),
        signal: options?.signal,
        credentials: request.credentials,
    });
    if (!response.ok) throw new Error(await readFetchError(response, "请求失败"));
    const state: GeminiStreamState = { buffer: "", text: "", toolCalls: [], truncated: false, completed: false };
    const bodyResult = await readResponseBodyWithEventStreamDetection(response, (chunk) => {
        consumeGeminiStreamText(state, chunk, onDelta);
        if (state.error) throw new Error(state.error);
    });
    if (!bodyResult.eventStream) {
        // 兼容网关忽略 alt=sse 时按完整 JSON 读取；测活与真实系统调用会把该渠道标为非流式风险。
        return parseGeminiToolResponse(parseResponseJsonText<GeminiPayload>(bodyResult.text, "请求失败"));
    }
    consumeGeminiStreamText(state, "", onDelta, true);
    if (state.error) throw new Error(state.error);
    // Gemini 的 functionCall 也可能在终态前只返回部分 args，缺少 finishReason 时禁止执行。
    if (!state.completed) throw new Error("Gemini 流式响应缺少完成标记，工具参数可能不完整且费用状态不确定；请勿立即重试");
    return { content: state.text, toolCalls: state.toolCalls, ...(state.truncated ? { truncated: true } : {}), ...(state.incompleteReason ? { incompleteReason: state.incompleteReason } : {}) };
}

function consumeGeminiStreamText(state: GeminiStreamState, text: string, onDelta?: (text: string) => void, flush = false) {
    state.buffer += text;
    for (;;) {
        const match = state.buffer.match(/(?:\r\n|\r|\n){2}/);
        if (!match) break;
        const index = match.index ?? 0;
        consumeGeminiStreamBlock(state.buffer.slice(0, index), state, onDelta);
        state.buffer = state.buffer.slice(index + match[0].length);
    }
    if (flush && state.buffer.trim()) {
        consumeGeminiStreamBlock(state.buffer, state, onDelta);
        state.buffer = "";
    }
}

function consumeGeminiStreamBlock(block: string, state: GeminiStreamState, onDelta?: (text: string) => void) {
    const eventName = sseEventName(block);
    if (["done", "message_stop", "response.completed", "response.incomplete"].includes(eventName)) state.completed = true;
    if (eventName === "response.incomplete") state.incompleteReason = "response_incomplete";
    const data = block
        .split(/\r\n|\r|\n/)
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).replace(/^ /, ""))
        .join("\n")
        .trim();
    if (!data) return;
    if (data === "[DONE]") {
        state.completed = true;
        return;
    }
    const payload = unwrapGeminiPayload(parseStreamingEvent<GeminiPayload>(data));
    const result = parseGeminiToolResponse(payload);
    if (result.content) {
        state.text += result.content;
        onDelta?.(state.text);
    }
    mergeGeminiStreamToolCalls(state.toolCalls, result.toolCalls);
    state.truncated = state.truncated || Boolean(result.truncated);
    state.incompleteReason = state.incompleteReason || result.incompleteReason;
    if (stringValue(isRecord(payload) ? payload.type : "").toLowerCase() === "response.incomplete") state.incompleteReason = "response_incomplete";
    state.completed = state.completed || geminiPayloadIsTerminal(payload);
}

// Gemini 某些兼容网关会把同一个 functionCall 分在多个 SSE chunk 中，且每个 chunk
// 都不带稳定 id。按“函数名 + 当前唯一候选”合并，避免同一工具被重复执行；同名并行
// 调用仍保留为多个候选，只有参数相同的重复 chunk 才会被折叠。
function mergeGeminiStreamToolCalls(target: ResponseToolCall[], incoming: ResponseToolCall[]) {
    incoming.forEach((call) => {
        const sameIdIndex = call.id ? target.findIndex((item) => item.id === call.id) : -1;
        const sameNameIndexes = target.reduce<number[]>((indexes, item, index) => {
            if (item.function.name === call.function.name) indexes.push(index);
            return indexes;
        }, []);
        const exactIndex = sameNameIndexes.find((index) => target[index].function.arguments === call.function.arguments) ?? -1;
        const index = sameIdIndex >= 0 ? sameIdIndex : sameNameIndexes.length === 1 ? sameNameIndexes[0] : exactIndex;
        if (index < 0) {
            target.push(call);
            return;
        }
        const current = target[index];
        target[index] = {
            ...call,
            id: current.id || call.id || nanoid(),
            function: {
                ...call.function,
                arguments: call.function.arguments.length >= current.function.arguments.length ? call.function.arguments : current.function.arguments,
            },
            ...(call.thoughtSignature || current.thoughtSignature ? { thoughtSignature: call.thoughtSignature || current.thoughtSignature } : {}),
        };
    });
}

function sseEventName(block: string) {
    const line = block.split(/\r\n|\r|\n/).find((value) => value.startsWith("event:"));
    return line ? line.slice(6).trim().toLowerCase() : "";
}

function geminiPayloadIsTerminal(payload: GeminiPayload) {
    const normalized = unwrapGeminiPayload(payload);
    if (normalized.done === true) return true;
    if (stringValue(isRecord(normalized) ? normalized.type : "").toLowerCase() === "response.incomplete") return true;
    return (normalized.candidates || []).some((candidate) => Boolean(geminiCandidateFinishReason(candidate)));
}

function geminiCandidateFinishReason(candidate: { finishReason?: string; finish_reason?: string }) {
    return (candidate.finishReason || candidate.finish_reason || "").trim();
}

function parseGeminiToolResponse(payload: GeminiPayload): ToolResponseResult {
    const normalized = unwrapGeminiPayload(payload);
    validateGeminiPayload(normalized);
    const parts = normalized.candidates?.flatMap((candidate) => candidate.content?.parts || []) || [];
    // Gemini 的思考片段只用于模型内部推理，不能展示给用户或混入分镜/工具参数解析；
    // 同一 part 仍可能携带 functionCall，因此只过滤 thought 文本，不丢弃函数调用。
    const content = parts.filter((part) => part.thought !== true).map((part) => part.text || "").join("");
    const toolCalls = parts
        .map((part) => ({ part, call: [part.functionCall, part.function_call].find((call) => Boolean(call?.name)) || part.functionCall || part.function_call }))
        .filter((item): item is { part: GeminiPart; call: GeminiFunctionCall } => Boolean(item.call?.name))
        .map(({ part, call }) => {
            const thoughtSignature = part?.thoughtSignature || part?.thought_signature;
            const args = call.args || (typeof call.arguments === "string" ? jsonObject(call.arguments) : call.arguments) || {};
            return {
                id: call.id || nanoid(),
                type: "function" as const,
                function: { name: call.name || "", arguments: JSON.stringify(args) },
                ...(thoughtSignature ? { thoughtSignature } : {}),
            };
        });
    const finishReason = (normalized.candidates || []).map(geminiCandidateFinishReason).find((reason) => reason && !["STOP", "MAX_TOKENS"].includes(reason.toUpperCase()));
    const truncated = (normalized.candidates || []).some((candidate) => completionWasTruncated(geminiCandidateFinishReason(candidate)));
    return {
        content,
        toolCalls,
        ...(truncated ? { truncated: true } : {}),
        ...(finishReason ? { incompleteReason: finishReason } : {}),
    };
}

const GEMINI_SCHEMA_KEYS = new Set([
    "type",
    "format",
    "title",
    "description",
    "nullable",
    "enum",
    "maxItems",
    "minItems",
    "properties",
    "required",
    "items",
    "propertyOrdering",
]);

function toGeminiSchema(value: unknown): Record<string, unknown> {
    if (!isRecord(value)) return { type: "object" };
    const result: Record<string, unknown> = {};
    Object.entries(value).forEach(([key, item]) => {
        if (!GEMINI_SCHEMA_KEYS.has(key)) return;
        if (key === "properties" && isRecord(item)) {
            result.properties = Object.fromEntries(Object.entries(item).map(([name, schema]) => [name, toGeminiSchema(schema)]));
            return;
        }
        if (key === "items") {
            result.items = toGeminiSchema(item);
            return;
        }
        if (key === "required" && Array.isArray(item)) {
            result.required = item.filter((name): name is string => typeof name === "string");
            return;
        }
        result[key] = item;
    });
    return result;
}

export async function requestToolResponse(config: ToolRequestConfig, messages: ResponseInputMessage[], tools: ResponseFunctionTool[], toolChoice: ToolChoice = "auto", onDelta?: (text: string) => void, options?: RequestOptions): Promise<ToolResponseResult> {
    // 画布调用方已经按“渠道 + 模型”解析过配置；再次用裸模型名解析会在多渠道
    // 共用同一模型时落到另一条渠道，造成测活通过、创作台却请求失败。只有未声明
    // 协议的原始配置才走默认解析。
    const requestConfig = config.interfaceType ? config : resolveModelRequestConfig(config, config.model || config.textModel);
    const effectiveToolChoice = resolveOnlineAgentToolChoice(requestConfig.model, requestConfig.interfaceType, toolChoice);
    const requestOptions = { ...options, scene: "canvas_agent" };
    try {
        // 旧画布可能保存了已删除渠道的模型绑定；空地址/密钥必须在建连前明确失败，
        // 不能让它落成模糊的 Invalid URL，更不能被误看成供应商拒绝后立即重试。
        if (!requestConfig.baseUrl.trim() || !requestConfig.apiKey.trim()) throw new Error("当前文本模型的渠道绑定已失效，请重新选择模型或补充渠道配置；本次没有调用供应商");
        let result: ToolResponseResult;
        if (requestConfig.apiFormat === "gemini") {
            result = await requestGeminiStreamingResponse(requestConfig, toGeminiBody(requestConfig, messages, {
                ...toGeminiToolOptions(tools, effectiveToolChoice),
            }), onDelta, requestOptions);
        } else if (requestConfig.interfaceType === "chat-completion") {
            const chatTools = tools.length
                ? { tools: toChatCompletionTools(requestConfig.model, tools), tool_choice: toChatCompletionToolChoice(effectiveToolChoice) }
                : {};
            result = await requestStreamingChatCompletion(requestConfig, {
                model: requestConfig.model,
                messages: toChatCompletionMessages(withSystemMessage(requestConfig, messages), requestConfig.model),
                ...chatTools,
                ...onlineAgentChatReasoningOptions(requestConfig.model),
            }, onDelta, requestOptions);
        } else {
            const responseTools = tools.length
                ? { tools: tools.map(toResponseTool), tool_choice: effectiveToolChoice }
                : {};
            result = await requestStreamingResponse(requestConfig, {
                model: requestConfig.model,
                input: toResponseInput(withSystemMessage(requestConfig, messages)),
                ...responseTools,
            }, onDelta, requestOptions);
        }
        // 不替模型决定输出长度；仍必须拒绝供应商明确标记为截断或未完成的工具参数。
        return requireUsableAgentToolResponse(result, effectiveToolChoice);
    } catch (error) {
        throw new Error(readAxiosError(error, "请求失败"));
    }
}

export async function fetchImageModels(config: Pick<AiConfig, "baseUrl" | "apiKey" | "apiFormat">) {
    try {
        if (config.apiFormat === "gemini") {
            const requestConfig = { ...defaultGeminiConfig, ...config };
            const request = channelRequest(requestConfig, geminiApiUrl(requestConfig), geminiHeaders(requestConfig));
            const response = await axios.get<GeminiPayload>(request.url, { headers: request.headers, withCredentials: request.credentials === "include" });
            validateGeminiPayload(response.data);
            return (response.data.models || [])
                .map((model) => model.name?.replace(/^models\//, ""))
                .filter((id): id is string => Boolean(id))
                .sort((a, b) => a.localeCompare(b));
        }
        const request = channelRequest(config, buildApiUrl(config.baseUrl, "/models"), { Authorization: `Bearer ${config.apiKey}` });
        const response = await axios.get<{ data?: Array<{ id?: string }>; error?: { message?: string } }>(request.url, { headers: request.headers, withCredentials: request.credentials === "include" });
        return (response.data.data || [])
            .map((model) => model.id)
            .filter((id): id is string => Boolean(id))
            .sort((a, b) => a.localeCompare(b));
    } catch (error) {
        throw new Error(readAxiosError(error, "读取模型失败", false));
    }
}

export async function fetchChannelModels(channel: ModelChannel, viaBackend = false) {
    if (!viaBackend) {
        return fetchImageModels({ baseUrl: channel.baseUrl, apiKey: channel.apiKey, apiFormat: channel.apiFormat });
    }
    try {
        // 登录态由同源后端代取模型目录，避免每个 OpenAI 兼容服务分别维护浏览器 CORS 白名单。
        const response = await axios.post<{ code?: number; data?: { models?: string[] }; msg?: string }>(
            resolveBackendApiUrl("/api/ai/models"),
            {
                baseUrl: channel.baseUrl,
                apiKey: channel.apiKey,
                apiFormat: channel.apiFormat,
            },
            { withCredentials: true },
        );
        if (typeof response.data.code === "number" && response.data.code !== 0) {
            throw new Error(response.data.msg || "读取模型失败");
        }
        return Array.from(new Set((response.data.data?.models || []).map((model) => model.trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b));
    } catch (error) {
        throw new Error(readAxiosError(error, "读取模型失败", false));
    }
}

const defaultGeminiConfig: Pick<AiConfig, "baseUrl" | "apiKey" | "apiFormat" | "model" | "systemPrompt"> = {
    baseUrl: "https://generativelanguage.googleapis.com",
    apiKey: "",
    apiFormat: "gemini",
    model: "",
    systemPrompt: "",
};
