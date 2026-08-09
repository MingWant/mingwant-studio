import { describe, expect, test } from "bun:test";

import { chatToolAssistantMessage, onlineAgentChatOmitsToolChoice, onlineAgentChatPreservesReasoningContent, onlineAgentChatReasoningOptions, requireUsableAgentToolResponse, resolveOnlineAgentToolChoice } from "../src/lib/agent-tool-response";

const toolCall = (name: string, id = "call-1") => ({ id, function: { name } });

describe("online agent semantic response boundary", () => {
    test("auto accepts either text or a complete tool call", () => {
        expect(requireUsableAgentToolResponse({ content: "完成", toolCalls: [] }, "auto").content).toBe("完成");
        expect(requireUsableAgentToolResponse({ content: "", toolCalls: [toolCall("canvas_get_state")] }, "auto").toolCalls).toHaveLength(1);
    });

    test("auto rejects a semantically empty completed response", () => {
        expect(() => requireUsableAgentToolResponse({ content: "  ", toolCalls: [] }, "auto")).toThrow("没有返回可用文本或完整工具调用");
    });

    test("all tool choices reject output-limit truncation before using partial data", () => {
        expect(() => requireUsableAgentToolResponse({ content: "partial", toolCalls: [toolCall("canvas_get_state")], truncated: true }, "required")).toThrow("供应商已明确返回输出截断状态");
        expect(() => requireUsableAgentToolResponse({ content: "partial", toolCalls: [], truncated: true }, "auto")).toThrow("返回的文本或工具参数可能被截断");
    });

    test("provider-declared incomplete results stop before tools or another model call", () => {
        expect(() => requireUsableAgentToolResponse({ content: "partial", toolCalls: [], incompleteReason: "content_filter" }, "auto")).toThrow("明确以未完整状态结束");
    });

    test("required rejects text without a tool call", () => {
        expect(() => requireUsableAgentToolResponse({ content: "我来处理", toolCalls: [] }, "required")).toThrow("没有按要求返回工具调用");
    });

    test("required accepts a complete tool call and rejects incomplete calls", () => {
        expect(requireUsableAgentToolResponse({ content: "", toolCalls: [toolCall("canvas_get_state")] }, "required").toolCalls).toHaveLength(1);
        expect(() => requireUsableAgentToolResponse({ content: "", toolCalls: [toolCall("", "")] }, "required")).toThrow("没有按要求返回工具调用");
    });

    test("named choice requires the requested function", () => {
        expect(requireUsableAgentToolResponse({ content: "", toolCalls: [toolCall("canvas_get_state")] }, { type: "function", name: "canvas_get_state" }).toolCalls).toHaveLength(1);
        expect(() => requireUsableAgentToolResponse({ content: "", toolCalls: [toolCall("canvas_export_snapshot")] }, { type: "function", name: "canvas_get_state" })).toThrow("没有返回指定工具 canvas_get_state");
    });

    test("online agent does not impose a fixed output token budget", () => {
        expect(() => requireUsableAgentToolResponse({ content: "partial", toolCalls: [], truncated: true }, "auto")).toThrow("供应商已明确返回输出截断状态");
    });

    test("Kimi K3 tool routing uses low reasoning effort without changing other models", () => {
        expect(onlineAgentChatReasoningOptions("kimi-k3-ls")).toEqual({ reasoning_effort: "low" });
        expect(onlineAgentChatReasoningOptions("vendor/kimi-k3")).toEqual({ reasoning_effort: "low" });
        expect(onlineAgentChatReasoningOptions("gpt-4o-mini")).toEqual({});
    });

    test("known thinking models that reject required use provider auto", () => {
        expect(resolveOnlineAgentToolChoice("kimi-k2.7-code", "chat-completion", "required")).toBe("auto");
        expect(resolveOnlineAgentToolChoice("vendor/kimi-k2.6", "chat-completion", "required")).toBe("auto");
        expect(resolveOnlineAgentToolChoice("kimi-k3-ls", "chat-completion", "required")).toBe("auto");
        expect(resolveOnlineAgentToolChoice("kimi-k3", "chat-completion", "required")).toBe("required");
        expect(resolveOnlineAgentToolChoice("vendor/moonshot-kimi", "chat-completion", "required")).toBe("required");
        expect(resolveOnlineAgentToolChoice("deepseek-v4-pro", "chat-completion", "required")).toBe("auto");
        expect(resolveOnlineAgentToolChoice("vendor/deepseek-v4-flash", "chat-completion", "required")).toBe("auto");
        expect(resolveOnlineAgentToolChoice("kimi-k3-ls", "gemini-content", "required")).toBe("required");
        expect(resolveOnlineAgentToolChoice("gpt-4o-mini", "chat-completion", "required")).toBe("required");
    });

    test("DeepSeek V4 omits tool_choice and preserves reasoning state only on its Chat protocol", () => {
        expect(onlineAgentChatOmitsToolChoice("vendor/deepseek-v4-pro", "chat-completion")).toBe(true);
        expect(onlineAgentChatOmitsToolChoice("deepseek-v4-pro", "openai-response")).toBe(false);
        expect(onlineAgentChatOmitsToolChoice("deepseek-v3.2", "chat-completion")).toBe(false);
        expect(onlineAgentChatPreservesReasoningContent("deepseek-v4-flash")).toBe(true);
        expect(onlineAgentChatPreservesReasoningContent("kimi-k3")).toBe(true);
        expect(onlineAgentChatPreservesReasoningContent("generic-model")).toBe(false);
    });

    test("chat tool follow-up preserves provider reasoning content without exposing it as visible content", () => {
        expect(chatToolAssistantMessage([
            { call_id: "call-1", name: "canvas_get_state", arguments: "{}", reasoningContent: "internal state" },
        ])).toEqual({
            role: "assistant",
            content: "",
            reasoning_content: "internal state",
            tool_calls: [{ id: "call-1", type: "function", function: { name: "canvas_get_state", arguments: "{}" } }],
        });
    });

    test("chat tool follow-up preserves visible assistant content when the provider returns it with tool calls", () => {
        expect(chatToolAssistantMessage([
            { call_id: "call-1", name: "canvas_get_state", arguments: "{}", assistantContent: "我先读取当前画布。" },
            { call_id: "call-2", name: "canvas_get_selection", arguments: "{}" },
        ])).toMatchObject({
            role: "assistant",
            content: "我先读取当前画布。",
        });
    });
});
