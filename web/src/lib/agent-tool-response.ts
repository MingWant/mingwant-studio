// 在线 Agent 不再由网页给模型注入固定输出上限；不同模型的上下文和工具参数
// 需求差异很大，硬编码 2048/4096 会在工具调用尚未闭合时截断合法请求。供应商
// 明确返回 length/MAX_TOKENS/incomplete 时仍由响应完整性门禁停止本轮。

export type AgentToolChoice = "auto" | "required" | { type: "function"; name: string };

function normalizedAgentModel(model: string) {
    return model.trim().toLowerCase().replace(/[/:_]/g, "-");
}

function isDeepSeekV4Model(model: string) {
    return /(?:^|-)deepseek-v4(?:-|$)/.test(normalizedAgentModel(model));
}

/** DeepSeek V4 思考模式使用上游默认 auto；即使显式发送 auto，部分兼容层也会拒绝整个字段。 */
export function onlineAgentChatOmitsToolChoice(model: string, interfaceType: string | undefined) {
    return interfaceType === "chat-completion" && isDeepSeekV4Model(model);
}

/** 只向明确要求续传推理状态的 Chat 协议回传 reasoning_content，避免污染普通兼容网关。 */
export function onlineAgentChatPreservesReasoningContent(model: string) {
    const normalizedModel = normalizedAgentModel(model);
    return normalizedModel.includes("kimi") || normalizedModel.includes("moonshot") || isDeepSeekV4Model(model);
}

/**
 * Kimi 的 tool_choice 能力按模型区分；DeepSeek V4 思考模式则拒绝整个字段。
 * 对这些已知不兼容族使用上游默认 auto，避免网关先因参数拒绝整次请求；
 * 本地首轮校验仍会阻止没有工具调用的回答。
 */
export function resolveOnlineAgentToolChoice(model: string, interfaceType: string | undefined, requested: AgentToolChoice): AgentToolChoice {
    if (requested !== "required" || interfaceType !== "chat-completion") return requested;
    if (onlineAgentChatOmitsToolChoice(model, interfaceType)) return "auto";
    const normalizedModel = normalizedAgentModel(model);
    if (
        normalizedModel.includes("kimi-k3-ls") ||
        normalizedModel.includes("kimi-k2.7-code") ||
        normalizedModel.includes("kimi-k2-7-code") ||
        normalizedModel.includes("kimi-k27-code") ||
        normalizedModel.includes("kimi-k2.6") ||
        normalizedModel.includes("kimi-k2-6") ||
        normalizedModel.includes("kimi-k26")
    ) return "auto";
    return requested;
}

export function onlineAgentToolChoiceReason(model: string, interfaceType: string | undefined, requested: AgentToolChoice) {
    const resolved = resolveOnlineAgentToolChoice(model, interfaceType, requested);
    if (resolved === requested) return "";
    if (onlineAgentChatOmitsToolChoice(model, interfaceType)) return "当前 DeepSeek V4 思考模式拒绝 tool_choice 参数，已省略该字段并由本地首轮校验确保必须返回工具调用";
    const normalizedModel = normalizedAgentModel(model);
    if (normalizedModel.includes("kimi-k3-ls")) return "当前 Kimi K3-LS 是第三方兼容别名，未声明 tool_choice=required，已改用 auto 并由本地首轮校验确保必须返回工具调用";
    return "当前 Kimi K2.7 Code/K2.6 兼容接口可能不接受 tool_choice=required，已改用 auto 并由本地首轮校验确保必须返回工具调用";
}

export type ChatToolCallInput = {
    call_id: string;
    name: string;
    arguments: string;
    reasoningContent?: string;
    assistantContent?: string;
};

export function onlineAgentChatReasoningOptions(model: string): Record<string, string> {
    // Kimi K3 默认 max 推理强度；工具路由不需要长推理，低档可显著减少首个工具调用前的无输出等待。
    return /(?:^|[/:_-])kimi-k3(?:$|[/:_-])/i.test(model.trim()) ? { reasoning_effort: "low" } : {};
}

export function chatToolAssistantMessage(calls: ChatToolCallInput[]): Record<string, unknown> {
    const reasoningContent = calls.find((call) => call.reasoningContent?.trim())?.reasoningContent;
    const assistantContent = calls.find((call) => typeof call.assistantContent === "string")?.assistantContent;
    return {
        role: "assistant",
        // Kimi/DeepSeek 的思考工具协议要求回传首轮 choice.message；content 为空时
        // 仍发送空字符串，也不能丢掉工具调用前附带的可见说明。
        content: assistantContent ?? "",
        tool_calls: calls.map((call) => ({ id: call.call_id, type: "function", function: { name: call.name, arguments: call.arguments } })),
        ...(reasoningContent ? { reasoning_content: reasoningContent } : {}),
    };
}

type AgentToolCallLike = {
    id?: string;
    function?: { name?: string };
};

type AgentToolResponseLike = {
    content: string;
    toolCalls: AgentToolCallLike[];
    truncated?: boolean;
    incompleteReason?: string;
};

function completeToolCalls(result: AgentToolResponseLike) {
    return result.toolCalls.filter((call) => Boolean(call.id?.trim() && call.function?.name?.trim()));
}

function outputTruncationLabel(maxOutputTokens?: number) {
    return typeof maxOutputTokens === "number" && maxOutputTokens > 0
        ? `模型已达到本轮 ${maxOutputTokens} token 输出上限`
        : "供应商已明确返回输出截断状态";
}

// HTTP/SSE 正常结束只代表传输完成；没有可用文本或完整工具调用时不能让 Agent 误入下一轮。
export function requireUsableAgentToolResponse<T extends AgentToolResponseLike>(result: T, toolChoice: AgentToolChoice, maxOutputTokens?: number): T {
    const toolCalls = completeToolCalls(result);
    const hasContent = Boolean(result.content.trim());
    if (result.truncated) {
        throw new Error(
            `${outputTruncationLabel(maxOutputTokens)}，返回的文本或工具参数可能被截断。本次调用已计入 Agent 预算且供应商可能计费；系统没有执行这批工具或发送下一轮。请检查供应商模型的输出策略后重新发送。`,
        );
    }
    if (result.incompleteReason) {
        throw new Error(
            "模型已明确以未完整状态结束，返回的文本或工具参数不能安全使用。本次调用已计入 Agent 预算且供应商可能计费；系统没有执行这批工具或发送下一轮，请检查供应商请求明细和模型兼容性。",
        );
    }
    if (toolChoice === "required" && toolCalls.length === 0) {
        const outputLimitHint = typeof maxOutputTokens === "number" && maxOutputTokens > 0
            ? `也可能是在 ${maxOutputTokens} token 输出上限内没有形成工具调用`
            : "也可能是在供应商自身输出策略内没有形成工具调用";
        throw new Error(
            `模型请求已完整结束，但没有按要求返回工具调用。普通文本测活成功不代表该模型支持 Function Calling / tool_choice=required；${outputLimitHint}。本次调用已计入 Agent 预算且供应商可能计费；系统没有执行画布操作或发送下一轮，请检查模型协议与工具调用兼容性后再重新发送。`,
        );
    }
    if (typeof toolChoice === "object" && !toolCalls.some((call) => call.function?.name?.trim() === toolChoice.name.trim())) {
        throw new Error(
            `模型请求已完整结束，但没有返回指定工具 ${toolChoice.name}。本次调用已计入 Agent 预算且供应商可能计费；系统没有执行画布操作或发送下一轮，请检查模型的 Function Calling 与指定工具兼容性。`,
        );
    }
    if (toolChoice === "auto" && !hasContent && toolCalls.length === 0) {
        throw new Error(
            "模型请求已完整结束，但没有返回可用文本或完整工具调用。本次调用已计入 Agent 预算且供应商可能计费；系统没有发送下一轮，请检查模型协议、Function Calling 兼容性和供应商请求明细。",
        );
    }
    return result;
}
