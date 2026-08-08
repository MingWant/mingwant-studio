import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import copyToClipboard from "copy-to-clipboard";
import { Bot, BookOpenText, Copy, Cpu, Focus, History, PanelRightClose, Plus, RotateCcw, Settings2, Square, Trash2, X } from "lucide-react";
import { App, Button, Modal, Segmented, Select, Switch, Tooltip } from "antd";
import { motion } from "motion/react";

import { encodeChannelModel, hasSystemModelPrice, modelDisplayName, modelOptionName, normalizeModelOptionValue, resolveModelChannel, resolveModelRequestConfig, selectableModelsByCapability, useConfigStore, useEffectiveConfig, type AiConfig } from "@/stores/use-config-store";
import { canvasThemes } from "@/lib/canvas-theme";
import { nanoid } from "nanoid";
import { requestToolResponse, type ResponseFunctionTool, type ResponseInputMessage, type ResponseToolCall, type ToolResponseResult } from "@/services/api/image";
import { imageToDataUrl } from "@/services/image-storage";
import { useAssetStore } from "@/stores/use-asset-store";
import { useThemeStore } from "@/stores/use-theme-store";
import { useUserStore } from "@/stores/use-user-store";
import { imageReferenceLabel } from "@/lib/image-reference-prompt";
import { navigateToSettings } from "@/lib/settings-navigation";
import { ChannelProbeButton, channelProbeModels } from "@/components/channel-probe-button";
import { onlineAgentToolChoiceReason, resolveOnlineAgentToolChoice } from "@/lib/agent-tool-response";
import { onlineAgentFailureMessage } from "@/lib/agent-error";
import { cinematicAgentSessionOpsJson, createCinematicAgentSession, isAgentSessionPollingAbort, isAgentSessionTrackingError, resumeCinematicAgentSession } from "@/lib/canvas/canvas-agent-session";
import { backendAgentProviderConfig, cinematicCreationMessageId, cinematicRequestConfigIdentity, cinematicSessionMessageId } from "@/lib/canvas/canvas-cinematic-request";
import { summarizeCanvasContext } from "@/lib/canvas/canvas-context-summary";
import { normalizeAgentStoryboardMetadata } from "@/lib/canvas/canvas-project-domain";
import { prefersShortCinematicDelivery, resolveChannelProbeReadiness } from "@/lib/channel-probe-readiness";
import { AgentChatComposer, AgentChatMessage, AgentModeSwitch, AgentPanelTabs, AgentWorkingMessage, type CanvasAgentChatMessage, type CanvasAgentMode } from "./canvas-agent-chat-ui";
import { CanvasLocalAgentPanel } from "./canvas-local-agent-panel";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { CanvasNodeType, type CanvasAssistantMessage, type CanvasAssistantPendingBackendSession, type CanvasAssistantReference, type CanvasAssistantSession, type CanvasNodeData } from "@/types/canvas";
import { useCanvasAgentStore } from "@/stores/canvas/use-canvas-agent-store";
import { MANUAL_DELIVERY_VIDEO_MESSAGE, previewCanvasAgentOps, summarizeCanvasAgentOps, type CanvasAgentOp, type CanvasAgentOperationImpact, type CanvasAgentSnapshot } from "@/lib/canvas/canvas-agent-ops";
import {
    ONLINE_AGENT_DEFAULT_MODEL_CALLS,
    ONLINE_AGENT_MAX_MODEL_CALLS,
    onlineAgentBudgetMessage,
    onlineAgentStoppedMessage,
    useCanvasAgentCostControl,
    type OnlineAgentCallBudgetStatus,
} from "./canvas-agent-cost-control";
import { OnlineAgentRequestStoppedError, useOnlineAgentRequestLifecycle, type OnlineAgentProtectedPhase } from "./use-online-agent-request-lifecycle";

export const CANVAS_AGENT_PANEL_MOTION_MS = 500;
const PANEL_MOTION_SECONDS = CANVAS_AGENT_PANEL_MOTION_MS / 1000;
const ONLINE_AGENT_PROMPT = [
    "你是明想 MingWant Studio 网页内置在线画布助手。当前画布 JSON 会随用户消息提供。首轮必须调用工具：只读问题调用 canvas_get_state，需要改动画布时调用和本地 Agent 一致的 infinite-canvas 工具。",
    "用户要求搭建短剧工作流、把故事拆成完整分镜或创建影视项目时，调用 canvas_create_cinematic_session，让后端负责结构化分镜；如果首轮结构异常，网页会另外询问是否允许一次付费修复，未确认不得补发第二次模型请求。不要先调用 canvas_generate_text 要求另一个模型返回 JSON。其他生成任务直接调用 canvas_generate_text、canvas_generate_image、canvas_generate_video、canvas_generate_audio 或 canvas_create_generation_flow。",
    "图片节点带有 imageAnnotation 时先调用 canvas_get_image_annotations 理解标记和指令，再用 canvas_edit_image_annotation 执行，不能把彩色标记当成结果内容。需要精确批量操作时调用 canvas_apply_ops；如果当前工具列表没有某个单项创建/移动工具，也用 canvas_apply_ops 完成同一操作。电商广告内容必须服从肖像授权、商品事实和广告宣称边界。不要输出 JSON ops，不要编造执行结果。工具参数涉及已有节点时必须使用当前画布 JSON 中真实存在的 id；缺少必要 id 或用户意图不明确时直接说明需要用户明确选择或说明，不要猜测。工具返回结果后，再根据真实结果回答用户。",
].join("\n");
const JSON_RECORD_SCHEMA = { type: "object", additionalProperties: true };
const POSITION_SCHEMA = { type: "object", properties: { x: { type: "number" }, y: { type: "number" } }, required: ["x", "y"], additionalProperties: false };
const VIEWPORT_SCHEMA = { type: "object", properties: { x: { type: "number" }, y: { type: "number" }, k: { type: "number" } }, required: ["x", "y", "k"], additionalProperties: false };
const NODE_TYPE_SCHEMA = { type: "string", enum: ["image", "text", "script", "skill", "config", "video", "audio"] };
const GENERATION_MODE_SCHEMA = { type: "string", enum: ["text", "image", "video", "audio"] };
const GENERATION_OPTION_PROPERTIES = {
    model: { type: "string" },
    size: { type: "string" },
    quality: { type: "string" },
    transparentBackground: { type: "string", enum: ["true", "false"] },
    count: { type: "number" },
    seconds: { type: "string" },
    vquality: { type: "string" },
    generateAudio: { type: "string" },
    watermark: { type: "string" },
    audioVoice: { type: "string" },
    audioFormat: { type: "string" },
    audioSpeed: { type: "string" },
    audioInstructions: { type: "string" },
};
const CANVAS_OP_SCHEMA = {
    type: "object",
    properties: {
        type: { type: "string", enum: ["add_node", "update_node", "delete_node", "delete_connections", "connect_nodes", "set_viewport", "select_nodes", "run_generation", "run_image_annotation"] },
        id: { type: "string" },
        ids: { type: "array", items: { type: "string" } },
        nodeType: NODE_TYPE_SCHEMA,
        title: { type: "string" },
        x: { type: "number" },
        y: { type: "number" },
        width: { type: "number" },
        height: { type: "number" },
        position: POSITION_SCHEMA,
        metadata: JSON_RECORD_SCHEMA,
        patch: JSON_RECORD_SCHEMA,
        all: { type: "boolean" },
        fromNodeId: { type: "string" },
        toNodeId: { type: "string" },
        viewport: VIEWPORT_SCHEMA,
        nodeId: { type: "string" },
        annotationNodeId: { type: "string" },
        mode: GENERATION_MODE_SCHEMA,
        prompt: { type: "string" },
    },
    required: ["type"],
    additionalProperties: false,
};
const ONLINE_READ_TOOLS = new Set(["canvas_get_state", "canvas_get_selection", "canvas_get_image_annotations", "canvas_export_snapshot"]);

function toolDefinition(name: string, description: string, properties: Record<string, unknown>, required: string[] = [], strict = false): ResponseFunctionTool {
    return {
        type: "function",
        function: {
            name,
            description,
            // 空 required 在部分兼容网关会被错误当成无效 JSON Schema；没有必填参数时省略，
            // 真实约束仍由工具执行层校验，避免测活成功后首个带工具请求被参数校验拒绝。
            parameters: { type: "object", properties, ...(required.length ? { required } : {}), additionalProperties: false },
            // Kimi K3 省略 strict 时默认启用严格 Schema；画布工具包含可选字段和
            // metadata 扩展，必须显式关闭严格校验，否则普通测活通过、首个带工具请求会被 400 拒绝。
            strict,
        },
    };
}

function generationToolDefinition(name: string, description: string, mode?: "text" | "image" | "video" | "audio") {
    return toolDefinition(
        name,
        description,
        { prompt: { type: "string" }, title: { type: "string" }, x: { type: "number" }, y: { type: "number" }, referenceNodeIds: { type: "array", items: { type: "string" } }, ...(mode ? {} : { mode: GENERATION_MODE_SCHEMA }), autoRun: { type: "boolean" }, ...GENERATION_OPTION_PROPERTIES },
        ["prompt"],
    );
}

const ONLINE_AGENT_TOOLS: ResponseFunctionTool[] = [
    toolDefinition("canvas_get_state", "读取当前网页画布的节点、连线、选区和视口。", {}),
    toolDefinition("canvas_get_selection", "读取当前网页画布选中的节点。", {}),
    toolDefinition("canvas_get_image_annotations", "读取当前选区或指定节点关联的图片标注、原图来源和修改指令。", { nodeId: { type: "string" } }),
    toolDefinition("canvas_export_snapshot", "导出当前画布快照，用于理解布局。", {}),
    toolDefinition("canvas_apply_ops", "批量操作当前网页画布。ops 支持 add_node、update_node、delete_node、delete_connections、connect_nodes、set_viewport、select_nodes、run_generation、run_image_annotation。", { ops: { type: "array", items: CANVAS_OP_SCHEMA } }, ["ops"], false),
    toolDefinition("canvas_create_node", "创建画布节点：text、image、script、config、video、audio 或 skill。分镜脚本优先使用 script 并提供 storyboard.rows；适合创建占位图、媒体占位、配置节点或自定义 metadata 节点。", { nodeType: NODE_TYPE_SCHEMA, title: { type: "string" }, x: { type: "number" }, y: { type: "number" }, width: { type: "number" }, height: { type: "number" }, metadata: JSON_RECORD_SCHEMA }, ["nodeType"]),
    toolDefinition("canvas_create_text_node", "在当前画布创建单个文本节点。", { text: { type: "string" }, x: { type: "number" }, y: { type: "number" }, title: { type: "string" }, width: { type: "number" }, height: { type: "number" } }),
    toolDefinition("canvas_create_text_nodes", "批量创建文本节点，适合生成标题、段落、脚本、说明等内容块。", { items: { type: "array", minItems: 1, items: { type: "object", properties: { text: { type: "string" }, title: { type: "string" }, x: { type: "number" }, y: { type: "number" }, width: { type: "number" }, height: { type: "number" } }, required: ["text"], additionalProperties: false } }, x: { type: "number" }, y: { type: "number" }, gap: { type: "number" }, direction: { type: "string", enum: ["row", "column"] } }, ["items"]),
    toolDefinition("canvas_create_cinematic_session", "把自然语言创作指令提交给后端影视 Agent 会话；网页会先确认一次分镜请求，首轮结构校验失败时再单独询问是否允许一次可能收费的修复请求，未确认不得补发。", { prompt: { type: "string" } }, ["prompt"]),
    toolDefinition("canvas_create_config_node", "创建生成配置节点，可指定 text/image/video/audio 模式和生成参数，可选择立即触发生成。", { prompt: { type: "string" }, mode: GENERATION_MODE_SCHEMA, title: { type: "string" }, x: { type: "number" }, y: { type: "number" }, width: { type: "number" }, height: { type: "number" }, autoRun: { type: "boolean" }, ...GENERATION_OPTION_PROPERTIES }),
    toolDefinition("canvas_create_image_prompt_flow", "创建提示词文本节点和图片生成配置节点，并自动连线，可选择立即触发生图。", { prompt: { type: "string" }, x: { type: "number" }, y: { type: "number" }, autoRun: { type: "boolean" }, ...GENERATION_OPTION_PROPERTIES }, ["prompt"]),
    generationToolDefinition("canvas_create_generation_flow", "创建通用生成流程：提示词文本节点、生成配置节点、参考节点连线，可用于文案、生图、视频或音频。"),
    generationToolDefinition("canvas_generate_text", "创建通用文本生成流程并立即触发生成。", "text"),
    generationToolDefinition("canvas_generate_image", "创建通用图片生成流程并立即触发生成。", "image"),
    generationToolDefinition("canvas_generate_video", "创建通用视频生成流程并立即通过画布已配置的视频模型触发生成。", "video"),
    generationToolDefinition("canvas_generate_audio", "创建通用音频生成流程并立即触发生成。", "audio"),
    toolDefinition("canvas_update_node", "更新节点基础字段或 metadata。", { id: { type: "string" }, patch: JSON_RECORD_SCHEMA, metadata: JSON_RECORD_SCHEMA }, ["id"]),
    toolDefinition("canvas_update_node_text", "更新文本节点内容和标题。", { id: { type: "string" }, text: { type: "string" }, title: { type: "string" } }, ["id", "text"]),
    toolDefinition("canvas_move_nodes", "移动一个或多个节点，支持绝对坐标或 dx/dy 偏移。", { items: { type: "array", minItems: 1, items: { type: "object", properties: { id: { type: "string" }, x: { type: "number" }, y: { type: "number" }, dx: { type: "number" }, dy: { type: "number" } }, required: ["id"], additionalProperties: false } } }, ["items"]),
    toolDefinition("canvas_resize_node", "调整节点尺寸。", { id: { type: "string" }, width: { type: "number" }, height: { type: "number" }, freeResize: { type: "boolean" } }, ["id", "width", "height"]),
    toolDefinition("canvas_delete_nodes", "删除指定节点及相关连线。", { ids: { type: "array", items: { type: "string" }, minItems: 1 } }, ["ids"]),
    toolDefinition("canvas_connect_nodes", "批量连接节点。", { connections: { type: "array", minItems: 1, items: { type: "object", properties: { fromNodeId: { type: "string" }, toNodeId: { type: "string" } }, required: ["fromNodeId", "toNodeId"], additionalProperties: false } } }, ["connections"]),
    toolDefinition("canvas_select_nodes", "设置当前选中节点。", { ids: { type: "array", items: { type: "string" } } }, ["ids"]),
    toolDefinition("canvas_set_viewport", "调整画布视口。", { viewport: VIEWPORT_SCHEMA }, ["viewport"]),
    toolDefinition("canvas_run_generation", "触发指定节点生成，通常用于配置节点或文本/图片/视频/音频节点。", { nodeId: { type: "string" }, mode: GENERATION_MODE_SCHEMA, prompt: { type: "string" } }, ["nodeId"]),
    toolDefinition("canvas_edit_image_annotation", "按图片标注节点保存的干净原图、修改指令和遮罩执行局部编辑。prompt 可覆盖原指令。", { annotationNodeId: { type: "string" }, prompt: { type: "string" } }, ["annotationNodeId"]),
];
// Kimi/Kimi 兼容网关经常支持单个 Function Calling 探针，却在一次提交二十多个
// 深层工具 Schema 时返回 400 或迟迟不产出首个工具调用。精简档仍保留读取、批量
// 画布操作、影视入口、生成和图片标注；其它模型继续使用完整工具集，避免削弱通用
// Agent 能力。批量操作本身覆盖创建、更新、删除、连线和触发生成。
const KIMI_COMPACT_TOOL_NAMES = new Set([
    "canvas_get_state",
    "canvas_get_selection",
    "canvas_get_image_annotations",
    "canvas_export_snapshot",
    "canvas_apply_ops",
    "canvas_create_cinematic_session",
    "canvas_create_generation_flow",
    "canvas_generate_text",
    "canvas_generate_image",
    "canvas_generate_video",
    "canvas_generate_audio",
    "canvas_edit_image_annotation",
]);

// 短剧入口的当前画布快照会由浏览器直接交给后台影视任务；模型首轮只需要
// 选择这一个入口工具。把读取状态的四个 Schema 一起发送会放大兼容网关的
// Schema 校验和推理耗时，出现“文本测活成功、创作台首轮 524”的假象。
const KIMI_CINEMATIC_TOOL_NAMES = new Set(["canvas_create_cinematic_session"]);

// Kimi 兼容网关常能通过单个函数探针，但一次提交十几个画布工具时会在
// Schema 校验或首包阶段超时。按用户意图再压缩一档：普通写操作交给
// canvas_apply_ops，生成请求只保留对应的通用生成工具，仍保留读取工具。
const KIMI_WRITE_TOOL_NAMES = new Set([
    ...ONLINE_READ_TOOLS,
    "canvas_apply_ops",
    "canvas_edit_image_annotation",
]);
const KIMI_GENERATION_TOOL_NAMES = new Set([
    ...ONLINE_READ_TOOLS,
    "canvas_apply_ops",
    "canvas_create_generation_flow",
    "canvas_generate_text",
    "canvas_generate_image",
    "canvas_generate_video",
    "canvas_generate_audio",
    "canvas_edit_image_annotation",
]);

// 保留一个最小手动工具档兼容旧会话；新手动入口默认走无工具纯文本解析，
// 避免把文本测活成功但 Function Calling 不兼容的模型挡在创作台之外。
const MANUAL_STORYBOARD_TOOL_NAMES = new Set([
    "canvas_create_node",
]);

type OnlineAgentToolIntent = "read" | "cinematic" | "manual_storyboard" | "write" | "generation" | "full";

function onlineAgentToolIntent(prompt: string): OnlineAgentToolIntent {
    const value = prompt.trim();
    // “创建一个小故事”本身就是影视/分镜意图；若漏掉这类自然说法，
    // 请求会先落入通用 Tool Loop，额外消耗模型调用后才发现没有可执行工具。
    if (
        (/(短剧|分镜|影视项目|镜头脚本)/.test(value) || /(?:生成|创建|搭建|制作|写|编写|构思|策划|做).{0,24}(?:小?故事|短片|短视频)/.test(value)) &&
        !/(图片|视频|音频)生成/.test(value)
    ) return "cinematic";
    if (
        /^(请|帮我|麻烦)?\s*(读取|查看|看看|列出|总结|分析|解释|导出|检查|确认|告诉我)/.test(value) &&
        !/(创建|生成|修改|更新|删除|移动|连接|连线|调整|执行|运行|搭建|制作|写入|批量|整理|布局|排版|归类|补全|修复|优化)/.test(value)
    ) return "read";
    if (/(生成|生图|出图|文案|图片|视频|音频|配音)/.test(value)) return "generation";
    if (/(创建|修改|更新|删除|移动|连接|连线|调整|执行|运行|搭建|制作|写入|批量|整理|布局|排版|归类|补全|修复|优化)/.test(value)) return "write";
    return "full";
}

function onlineAgentToolsForRequest(config: Pick<OnlineAgentRequestConfig, "model" | "interfaceType">, intent: OnlineAgentToolIntent = "full") {
    if (intent === "manual_storyboard") return ONLINE_AGENT_TOOLS.filter((tool) => MANUAL_STORYBOARD_TOOL_NAMES.has(tool.function.name));
    if (config.interfaceType !== "chat-completion") return ONLINE_AGENT_TOOLS;
    const model = modelOptionName(config.model).trim().toLowerCase().replace(/[/:_]/g, "-");
    if (!model.includes("kimi") && !model.includes("moonshot")) return ONLINE_AGENT_TOOLS;
    const names = intent === "read"
        ? ONLINE_READ_TOOLS
        : intent === "cinematic"
            ? KIMI_CINEMATIC_TOOL_NAMES
            : intent === "write"
                ? KIMI_WRITE_TOOL_NAMES
                : intent === "generation"
                    ? KIMI_GENERATION_TOOL_NAMES
                    : KIMI_COMPACT_TOOL_NAMES;
    return ONLINE_AGENT_TOOLS.filter((tool) => names.has(tool.function.name));
}

function onlineAgentToolProfile(config: Pick<OnlineAgentRequestConfig, "model" | "interfaceType">, intent: OnlineAgentToolIntent = "full") {
    if (intent === "manual_storyboard") return "manual-storyboard";
    if (config.interfaceType !== "chat-completion") return "full";
    const model = modelOptionName(config.model).trim().toLowerCase().replace(/[/:_]/g, "-");
    if (!model.includes("kimi") && !model.includes("moonshot")) return "full";
    if (intent === "read") return "kimi-read";
    if (intent === "cinematic") return "kimi-cinematic";
    if (intent === "write") return "kimi-write";
    if (intent === "generation") return "kimi-generation";
    return "kimi-compact";
}

function persistedOnlineToolIntent(value: unknown): OnlineAgentToolIntent | undefined {
    return value === "read" || value === "cinematic" || value === "manual_storyboard" || value === "write" || value === "generation" || value === "full" ? value : undefined;
}

type OnlineAgentTab = "setup" | "chat" | "history" | "log";
type OnlineAgentLog = { id: string; time: string; title: string; data?: unknown };
type OnlineAgentLogContext = { model: string; running: boolean; confirmTools: boolean; messages: number; nodes: number; connections: number };
// Agent 一轮开始后必须冻结测活时解析出的渠道、协议和模型；只保留 AiConfig
// 会在同名模型跨渠道时重新命中第一条渠道，导致“测活成功、创作台失败”。
type OnlineAgentRequestConfig = ReturnType<typeof resolveModelRequestConfig>;
type PersistedOnlineAgentRequest = {
    channelId: string;
    model: string;
    configIdentity: string;
};
type OnlineLoopContext = {
    step: number;
    maxCalls: number;
    budgetMessageId: string;
    model: string;
    requestConfig: OnlineAgentRequestConfig;
    toolIntent?: OnlineAgentToolIntent;
    /** 手动交付可在非流式测活结果上退回一次短 JSON/文本请求；普通 Agent 始终要求 SSE。 */
    streamingReadinessState?: "stream" | "non_stream" | "failed" | "stale" | "unverified";
    channelProbeTaskId?: string;
    toolProbeTaskId?: string;
};
type OnlineToolResult = { ok: true; message: string; data?: unknown } | { ok: false; message: string };
type OnlineExecutedToolCall = { toolCallId: string; name: string; result: OnlineToolResult };
type PendingOnlineToolContext = { messages: ResponseInputMessage[]; toolCalls: ResponseToolCall[]; assistantId: string; loop: OnlineLoopContext; reasoningContent?: string; assistantContent?: string };

async function onlineAgentRequestIdentity(config: AiConfig, requestConfig: OnlineAgentRequestConfig): Promise<PersistedOnlineAgentRequest> {
    return {
        channelId: requestConfig.resolvedChannelId,
        model: requestConfig.model,
        // 配置指纹包含协议、端点和凭据版本；不把 API Key 写入会话消息。
        configIdentity: await cinematicRequestConfigIdentity(config, requestConfig),
    };
}

async function restorePersistedOnlineAgentRequest(config: AiConfig, detail: Record<string, unknown>): Promise<{ requestConfig?: OnlineAgentRequestConfig; reason?: string }> {
    const channelId = stringOptional(detail.requestChannelId);
    const model = stringOptional(detail.model);
    const expectedIdentity = stringOptional(detail.requestConfigIdentity);
    if (!channelId || !model || !expectedIdentity) {
        return { reason: "页面刷新后原工具上下文缺少模型配置指纹，本次没有执行工具或发送下一轮请求；请重新发送消息。" };
    }
    const channel = config.channels.find((item) => item.id === channelId);
    if (!channel || !channel.models.some((item) => modelOptionName(item) === model)) {
        return { reason: "页面刷新后原模型或渠道已不存在，本次没有执行工具或发送下一轮请求；请重新选择模型后发送消息。" };
    }
    const requestConfig = resolveModelRequestConfig(config, encodeChannelModel(channel.id, model));
    try {
        const currentIdentity = await cinematicRequestConfigIdentity(config, requestConfig);
        if (currentIdentity !== expectedIdentity) {
            return { reason: "页面刷新后模型、渠道、协议或凭据已经变化，本次没有执行旧工具或发送下一轮请求；请确认新配置后重新发送消息。" };
        }
    } catch {
        return { reason: "页面刷新后无法核对原模型配置，本次没有执行工具或发送下一轮请求；请重新发送消息。" };
    }
    return { requestConfig };
}

class OnlineAgentModelCallError extends Error {
    readonly callNumber: number;

    constructor(callNumber: number, cause: unknown) {
        super(cause instanceof Error ? cause.message : String(cause || "在线 Agent 请求失败"));
        this.name = "OnlineAgentModelCallError";
        this.callNumber = callNumber;
    }
}

class CinematicSessionCreationError extends Error {
    readonly requestKey: string;

    constructor(requestKey: string, cause: unknown) {
        super(cause instanceof Error ? cause.message : String(cause || "影视项目创建失败"));
        this.name = "CinematicSessionCreationError";
        this.requestKey = requestKey;
    }
}

function isCinematicSessionCreationError(error: unknown): error is CinematicSessionCreationError {
    return error instanceof CinematicSessionCreationError;
}

type RunCinematicSessionOptions = {
    requestKey?: string;
    configIdentity?: string;
    channelProbeTaskId?: string;
    toolProbeTaskId?: string;
    allowPaidStructureRepair?: boolean;
    onCreated?: (backendSessionId: string) => void;
};

function onlineAgentFailureCallNumber(error: unknown, fallback: number) {
    return error instanceof OnlineAgentModelCallError ? error.callNumber : fallback;
}

function normalizeOnlineAgentMaxCalls(value: unknown) {
    const parsed = Math.floor(Number(value));
    return Number.isFinite(parsed) ? Math.max(1, Math.min(ONLINE_AGENT_MAX_MODEL_CALLS, parsed)) : ONLINE_AGENT_DEFAULT_MODEL_CALLS;
}

function onlineAgentIdempotencyKey(sessionId: string, loop: OnlineLoopContext) {
    return `canvas-agent:${sessionId}:${loop.budgetMessageId}:${loop.step}`;
}

type CanvasAssistantPanelProps = {
    nodes: CanvasNodeData[];
    selectedNodeIds: Set<string>;
    snapshot: CanvasAgentSnapshot;
    projectId: string;
    manualDelivery?: boolean;
    sessions: CanvasAssistantSession[];
    activeSessionId: string | null;
    onSelectNodeIds: (ids: Set<string>) => void;
    onSessionsChange: (sessions: CanvasAssistantSession[], activeSessionId: string | null) => void;
    onPersistSessionsNow: (sessions: CanvasAssistantSession[], activeSessionId: string | null) => Promise<void>;
    onApplyOps: (ops?: CanvasAgentOp[]) => CanvasAgentSnapshot;
    canUndoOps: boolean;
    undoOpsCount: number;
    onUndoOps: () => CanvasAgentSnapshot | null;
    onPasteImage: (file: File) => void;
    agentMode: CanvasAgentMode;
    onAgentModeChange: (mode: CanvasAgentMode) => void;
    autoConnectLocal?: boolean;
    closing: boolean;
    onCollapse: () => void;
    onCloseBlockedChange?: (blocked: boolean) => void;
    cinematicEntry?: boolean;
    onCinematicEntryConsumed?: () => void;
};

export function CanvasAssistantPanel({ nodes, selectedNodeIds, snapshot, projectId, manualDelivery = false, sessions, activeSessionId, onSelectNodeIds, onSessionsChange, onPersistSessionsNow, onApplyOps, canUndoOps, undoOpsCount, onUndoOps, onPasteImage, agentMode, onAgentModeChange, autoConnectLocal, closing, onCollapse, onCloseBlockedChange, cinematicEntry = false, onCinematicEntryConsumed }: CanvasAssistantPanelProps) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const user = useUserStore((state) => state.user);
    const { message } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const cleanupImages = useAssetStore((state) => state.cleanupImages);
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);
    const updateConfig = useConfigStore((state) => state.updateConfig);
    const confirmTools = useCanvasAgentStore((state) => state.confirmTools);
    const setAgentState = useCanvasAgentStore((state) => state.setAgentState);
    const { confirmOnlineAgentTurn, confirmCinematicTask, confirmStopOnlineAgentRequest, warnOnlineAgentActionBlocked } = useCanvasAgentCostControl();
    const [width, setWidth] = useState(520);
    const [view, setView] = useState<OnlineAgentTab>("chat");
    const [prompt, setPrompt] = useState("");
    const [cinematicEntryActive, setCinematicEntryActive] = useState(cinematicEntry);
    const [isRunning, setIsRunning] = useState(false);
    const [onlineTurnActive, setOnlineTurnActive] = useState(false);
    const [deleteChatIds, setDeleteChatIds] = useState<string[]>([]);
    const [onlineLogs, setOnlineLogs] = useState<OnlineAgentLog[]>([]);
    const [resizing, setResizing] = useState(false);
    const [removedReferenceIds, setRemovedReferenceIds] = useState<Set<string>>(new Set());
    const manualDeliveryRef = useRef(manualDelivery);
    manualDeliveryRef.current = manualDelivery;
    const [localSessions, setLocalSessions] = useState<CanvasAssistantSession[]>(() => (sessions.length ? sessions : [createSession()]));
    const [localActiveSessionId, setLocalActiveSessionId] = useState<string | null>(activeSessionId);
    const localSessionsRef = useRef(localSessions);
    const applyingExternalSessionsRef = useRef(false);
    const chatListRef = useRef<HTMLDivElement>(null);
    const snapshotRef = useRef(snapshot);
    const pendingToolContextRef = useRef(new Map<string, PendingOnlineToolContext>());
    const cinematicSessionControllersRef = useRef(new Map<string, AbortController>());
    const cinematicCreationRecoveriesRef = useRef(new Set<string>());
    const cinematicAutoRecoveryAttemptsRef = useRef(new Set<string>());

    const replaceLocalSessions = (value: CanvasAssistantSession[] | ((current: CanvasAssistantSession[]) => CanvasAssistantSession[])) => {
        const next = typeof value === "function" ? value(localSessionsRef.current) : value;
        localSessionsRef.current = next;
        setLocalSessions(next);
        return next;
    };

    useEffect(() => {
        if (!sessions.length) return;
        if (sessions === localSessions && activeSessionId === localActiveSessionId) return;
        applyingExternalSessionsRef.current = true;
        localSessionsRef.current = sessions;
        setLocalSessions(sessions);
        setLocalActiveSessionId(activeSessionId);
    }, [activeSessionId, sessions]);

    useEffect(() => {
        snapshotRef.current = snapshot;
    }, [snapshot]);

    useEffect(() => () => {
        // 收起面板或刷新页面时只停止前端查询，后台任务由下次挂载根据持久化 ID 继续接管。
        cinematicSessionControllersRef.current.forEach((controller) => controller.abort());
        cinematicSessionControllersRef.current.clear();
        cinematicCreationRecoveriesRef.current.clear();
        cinematicAutoRecoveryAttemptsRef.current.clear();
    }, []);

    useEffect(() => {
        if (applyingExternalSessionsRef.current) {
            applyingExternalSessionsRef.current = false;
            return;
        }
        if (sessions === localSessions && activeSessionId === localActiveSessionId) return;
        onSessionsChange(localSessions, localActiveSessionId);
    }, [activeSessionId, localActiveSessionId, localSessions, onSessionsChange, sessions]);

    const safeSessions = localSessions.length ? localSessions : [createSession()];
    const activeSession = useMemo(() => safeSessions.find((session) => session.id === localActiveSessionId) || safeSessions[0] || null, [localActiveSessionId, safeSessions]);
    const historySessions = safeSessions.filter((session) => session.messages.length > 0);
    const messages = activeSession?.messages || [];
    const hasMessages = messages.length > 0;
    const pendingToolApproval = safeSessions.some((session) => session.messages.some((message) => message.role === "tool" && objectDetail(message.detail).status === "pending"));
    const pendingBackendSession = safeSessions.some((session) => Boolean(session.pendingBackendSession));
    const agentWorking = isRunning || pendingBackendSession;
    const agentBusy = agentWorking || pendingToolApproval;
    const protectedPhase: OnlineAgentProtectedPhase = pendingToolApproval ? "tool_approval" : onlineTurnActive ? "running" : null;
    const {
        activeRequest: activeOnlineRequest,
        allowCollapse: allowOnlineAgentCollapse,
        runRequest: runOnlineAgentRequest,
        stopRequest: stopOnlineAgentRequest,
    } = useOnlineAgentRequestLifecycle({
        protectedPhase,
        confirmStop: confirmStopOnlineAgentRequest,
        warnBlocked: warnOnlineAgentActionBlocked,
    });

    useLayoutEffect(() => {
        onCloseBlockedChange?.(Boolean(protectedPhase));
        return () => onCloseBlockedChange?.(false);
    }, [onCloseBlockedChange, protectedPhase]);
    const activeModel = effectiveConfig.textModel || effectiveConfig.model;
    const selectedNodeKey = useMemo(() => Array.from(selectedNodeIds).sort().join(","), [selectedNodeIds]);
    const allSelectedReferences = useMemo(() => buildAssistantReferences(nodes, selectedNodeIds), [nodes, selectedNodeIds]);
    const selectedReferences = useMemo(() => allSelectedReferences.filter((item) => !removedReferenceIds.has(item.id)), [allSelectedReferences, removedReferenceIds]);
    const contextSummary = useMemo(() => summarizeCanvasContext(nodes, selectedNodeIds), [nodes, selectedNodeIds]);
    const iconButtonStyle = { color: theme.node.muted };

    useEffect(() => {
        if (agentMode !== "online" || view !== "chat") return;
        const frame = requestAnimationFrame(() => chatListRef.current?.scrollTo({ top: chatListRef.current.scrollHeight }));
        return () => cancelAnimationFrame(frame);
    }, [agentBusy, agentMode, localActiveSessionId, messages, view]);

    useEffect(() => {
        setRemovedReferenceIds(new Set());
    }, [selectedNodeKey]);

    const updateSession = (sessionId: string, updater: (session: CanvasAssistantSession) => CanvasAssistantSession) => {
        return replaceLocalSessions((current) => current.map((session) => (session.id === sessionId ? updater(session) : session)));
    };

    const appendMessage = (sessionId: string, message: CanvasAssistantMessage) => {
        updateSession(sessionId, (session) => ({
            ...session,
            title: session.messages.length ? session.title : message.text.slice(0, 18) || "新对话",
            messages: [...session.messages, message],
            updatedAt: new Date().toISOString(),
        }));
    };
    const addOnlineLog = (title: string, data?: unknown) => setOnlineLogs((prev) => [{ id: nanoid(), time: new Date().toLocaleTimeString(), title, data }, ...prev].slice(0, 80));

    const upsertMessage = (sessionId: string, message: CanvasAssistantMessage) => {
        updateSession(sessionId, (session) => {
            const exists = session.messages.some((item) => item.id === message.id);
            return {
                ...session,
                title: session.messages.length ? session.title : message.text.slice(0, 18) || "新对话",
                messages: exists ? session.messages.map((item) => (item.id === message.id ? { ...item, ...message } : item)) : [...session.messages, message],
                updatedAt: new Date().toISOString(),
            };
        });
    };

    const updateOnlineAgentBudget = (sessionId: string, loop: OnlineLoopContext, status: OnlineAgentCallBudgetStatus, note?: string) => {
        if (!loop.budgetMessageId) return;
        upsertMessage(sessionId, onlineAgentBudgetMessage({ id: loop.budgetMessageId, model: loop.model, usedCalls: loop.step, maxCalls: loop.maxCalls, status, note }));
    };

    const recordOnlineAgentError = (sessionId: string, loop: OnlineLoopContext, error: unknown) => {
        if (error instanceof OnlineAgentRequestStoppedError) {
            const stoppedLoop = { ...loop, step: error.callNumber };
            const notice = onlineAgentStoppedMessage(error.callNumber);
            updateOnlineAgentBudget(sessionId, stoppedLoop, "stopped", notice);
            addOnlineLog("用户停止等待，费用状态待核对", { step: error.callNumber, model: loop.model });
            appendMessage(sessionId, { id: nanoid(), role: "error", title: "已停止等待（费用待核对）", text: notice });
            return;
        }
        const failedStep = onlineAgentFailureCallNumber(error, loop.step);
        const failure = onlineAgentFailureMessage(error, failedStep);
        updateOnlineAgentBudget(sessionId, { ...loop, step: failedStep }, "failed", failure);
        addOnlineLog("请求失败并停止本轮", { step: failedStep, error: error instanceof Error ? error.message : error });
        appendMessage(sessionId, { id: nanoid(), role: "error", title: "在线 Agent 已停止", text: failure });
    };

    const failCinematicCreation = (sessionId: string, requestKey: string, error: unknown) => {
        updateSession(sessionId, (session) => {
            const pending = session.pendingBackendSession;
            if (pending?.status !== "creating" || pending.requestKey !== requestKey) return session;
            const failedAt = new Date().toISOString();
            const text = error instanceof Error ? error.message : "影视项目创建失败";
            return {
                ...session,
                pendingBackendSession: undefined,
                messages: upsertAssistantMessage(session.messages, {
                    id: pending.messageId,
                    role: "error",
                    title: "影视项目创建失败",
                    text,
                    detail: { kind: "cinematic", status: "failed", failedAt },
                }),
                updatedAt: failedAt,
            };
        });
    };

    const detachCinematicPending = (sessionId: string, pending: CanvasAssistantPendingBackendSession, text: string) => {
        updateSession(sessionId, (session) => {
            if (session.pendingBackendSession?.messageId !== pending.messageId) return session;
            const detachedAt = new Date().toISOString();
            return {
                ...session,
                pendingBackendSession: undefined,
                messages: upsertAssistantMessage(session.messages, {
                    id: pending.messageId,
                    role: "error",
                    title: "未自动接管影视项目",
                    text,
                    detail: { kind: "cinematic", status: "detached", detachedAt },
                }),
                updatedAt: detachedAt,
            };
        });
    };

    const setPendingCinematicCreation = async (sessionId: string, requestKey: string, text: string, configIdentity: string, channelProbeTaskId?: string, toolProbeTaskId?: string, allowPaidStructureRepair = false) => {
        const startedAt = new Date().toISOString();
        const pending: CanvasAssistantPendingBackendSession = {
            kind: "cinematic",
            canvasId: projectId,
            requestKey,
            prompt: text,
            configIdentity,
            channelProbeTaskId,
            toolProbeTaskId,
            allowPaidStructureRepair,
            messageId: cinematicCreationMessageId(requestKey),
            status: "creating",
            startedAt,
        };
        const nextSessions = updateSession(sessionId, (session) => ({
            ...session,
            pendingBackendSession: pending,
            messages: upsertAssistantMessage(session.messages, {
                id: pending.messageId,
                role: "assistant",
                title: "正在确认影视项目创建",
                text: "创建标识已保存，正在联系后端。即使响应丢失或页面刷新，也会复用原标识恢复，不会生成新的创建请求。",
                detail: { kind: "cinematic", status: "creating", startedAt, allowPaidStructureRepair },
            }),
            updatedAt: startedAt,
        }));
        try {
            await onPersistSessionsNow(nextSessions, sessionId);
        } catch (error) {
            const detail = error instanceof Error ? error.message : "浏览器存储不可用";
            const failure = new Error(`无法先保存影视项目创建标识：${detail}。为避免响应丢失后重复计费，系统没有调用后端。`);
            failCinematicCreation(sessionId, requestKey, failure);
            throw new CinematicSessionCreationError(requestKey, failure);
        }
        return pending;
    };

    const setPendingCinematicSession = (sessionId: string, backendSessionId: string, requestKey: string, text: string, configIdentity: string, allowPaidStructureRepair = false) => {
        updateSession(sessionId, (session) => {
            const existing = session.pendingBackendSession;
            const startedAt = existing?.startedAt || new Date().toISOString();
            const pending: CanvasAssistantPendingBackendSession = {
                id: backendSessionId,
                kind: "cinematic",
                canvasId: existing?.canvasId || projectId,
                requestKey,
                prompt: text,
                configIdentity,
                channelProbeTaskId: existing?.channelProbeTaskId,
                toolProbeTaskId: existing?.toolProbeTaskId,
                allowPaidStructureRepair: existing?.allowPaidStructureRepair ?? allowPaidStructureRepair,
                messageId: existing?.messageId || cinematicSessionMessageId(backendSessionId),
                status: "pending",
                startedAt,
            };
            return {
                ...session,
                pendingBackendSession: pending,
                messages: upsertAssistantMessage(session.messages, {
                    id: pending.messageId,
                    role: "assistant",
                    title: "影视项目生成中",
                    text: "后端影视 Agent 正在处理。即使页面刷新，也会在重新进入画布后继续等待结果。",
                detail: { kind: "cinematic", backendSessionId, status: "pending", startedAt, allowPaidStructureRepair: pending.allowPaidStructureRepair === true },
                }),
                updatedAt: new Date().toISOString(),
            };
        });
    };

    const completeCinematicSession = (sessionId: string, backendSessionId: string, ops: CanvasAgentOp[], recovered = false) => {
        updateSession(sessionId, (session) => {
            const pending = session.pendingBackendSession;
            if (pending?.status !== "pending" || pending.id !== backendSessionId) return session;
            const completedAt = new Date().toISOString();
            const summary = summarizeCanvasAgentOps(ops) || "影视项目已写回当前画布。";
            return {
                ...session,
                pendingBackendSession: undefined,
                messages: upsertAssistantMessage(session.messages, {
                    id: pending.messageId,
                    role: "assistant",
                    title: recovered ? "影视项目已恢复并写回" : "影视项目已写回",
                    text: recovered ? `页面重新连接后已恢复后台结果：${summary}` : summary,
                    detail: { kind: "cinematic", backendSessionId, status: "completed", recovered, completedAt },
                }),
                updatedAt: completedAt,
            };
        });
    };

    const failCinematicSession = (sessionId: string, backendSessionId: string, error: unknown) => {
        updateSession(sessionId, (session) => {
            const pending = session.pendingBackendSession;
            if (pending?.status !== "pending" || pending.id !== backendSessionId) return session;
            const failedAt = new Date().toISOString();
            const text = error instanceof Error ? error.message : "影视项目生成失败";
            return {
                ...session,
                pendingBackendSession: undefined,
                messages: upsertAssistantMessage(session.messages, {
                    id: pending.messageId,
                    role: "error",
                    title: "影视项目生成失败",
                    text,
                    detail: { kind: "cinematic", backendSessionId, status: "failed", failedAt },
                }),
                updatedAt: failedAt,
            };
        });
    };

    const pauseCinematicCreationRecovery = (sessionId: string, pending: Extract<CanvasAssistantPendingBackendSession, { status: "creating" }>, text: string) => {
        const session = localSessionsRef.current.find((item) => item.id === sessionId);
        const currentMessage = session?.messages.find((item) => item.id === pending.messageId);
        if (currentMessage?.text === text) return;
        updateSession(sessionId, (item) => ({
            ...item,
            messages: upsertAssistantMessage(item.messages, {
                id: pending.messageId,
                role: "assistant",
                title: "影视项目恢复已暂停",
                text,
                detail: { kind: "cinematic", status: "creating", recoveryState: "paused", startedAt: pending.startedAt },
            }),
            updatedAt: new Date().toISOString(),
        }));
    };

    const pauseCinematicSessionRecovery = (sessionId: string, backendSessionId: string, text: string) => {
        const session = localSessionsRef.current.find((item) => item.id === sessionId);
        if (!session) return;
        const pending = session.pendingBackendSession;
        if (pending?.status !== "pending" || pending.id !== backendSessionId) return;
        const currentMessage = session.messages.find((item) => item.id === pending.messageId);
        if (currentMessage?.text === text) return;
        updateSession(sessionId, (item) => ({
            ...item,
            messages: upsertAssistantMessage(item.messages, {
                id: pending.messageId,
                role: "assistant",
                title: "影视项目跟踪已暂停",
                text,
                detail: { kind: "cinematic", backendSessionId, status: "pending", recoveryState: "paused", startedAt: pending.startedAt },
            }),
            updatedAt: new Date().toISOString(),
        }));
    };

    const runCinematicSession = async (sessionId: string, text: string, current: CanvasAgentSnapshot, config: AiConfig | OnlineAgentRequestConfig, options: RunCinematicSessionOptions = {}) => {
        // 在线 Agent 已经冻结了首轮解析出的渠道；再次按裸模型名解析会让同名多渠道
        // 的影视工具落到另一条端点。直接调用入口仍按当前配置解析一次。
        const configuredRequestConfig = config as Partial<OnlineAgentRequestConfig>;
        const requestConfig = typeof configuredRequestConfig.interfaceType === "string" && configuredRequestConfig.interfaceType.trim()
            ? config as OnlineAgentRequestConfig
            : resolveModelRequestConfig(config, config.textModel || config.model);
        const configIdentity = options.configIdentity || await cinematicRequestConfigIdentity(config, requestConfig);
        const requestKey = options.requestKey || nanoid();
        const allowPaidStructureRepair = options.allowPaidStructureRepair === true;
        const controller = new AbortController();
        const controllerKey = `creating:${requestKey}`;
        let backendSessionId = "";
        // 先占住创建键再异步落盘，避免 React 恢复 effect 在 LocalForage 写入期间并发补发同一请求。
        cinematicSessionControllersRef.current.set(controllerKey, controller);
        try {
            if (!options.requestKey) await setPendingCinematicCreation(sessionId, requestKey, text, configIdentity, options.channelProbeTaskId, options.toolProbeTaskId, allowPaidStructureRepair);
            const detail = await createCinematicAgentSession(
                {
                    requestKey,
                    projectId,
                    prompt: text,
                    canvasSnapshot: compactSnapshot(current) as unknown as Record<string, unknown>,
                    config: backendAgentProviderConfig(requestConfig),
                    channelProbeTaskId: options.channelProbeTaskId,
                    toolProbeTaskId: options.toolProbeTaskId,
                    allowPaidStructureRepair,
                },
                {
                    signal: controller.signal,
                    onCreated: (created) => {
                        backendSessionId = created.session.id;
                        cinematicSessionControllersRef.current.delete(controllerKey);
                        cinematicSessionControllersRef.current.set(backendSessionId, controller);
                        setPendingCinematicSession(sessionId, backendSessionId, requestKey, text, options.configIdentity || configIdentity, allowPaidStructureRepair);
                        addOnlineLog("后端影视 Agent 会话已创建", { backendSessionId });
                        options.onCreated?.(backendSessionId);
                    },
                },
            );
            return { backendSessionId: detail.session.id, ops: requireOps(JSON.parse(cinematicAgentSessionOpsJson(detail))) };
        } catch (error) {
            if (isAgentSessionTrackingError(error)) {
                if (backendSessionId) pauseCinematicSessionRecovery(sessionId, backendSessionId, error.message);
                else {
                    const pending = localSessionsRef.current.find((item) => item.id === sessionId)?.pendingBackendSession;
                    if (pending?.status === "creating" && pending.requestKey === requestKey) pauseCinematicCreationRecovery(sessionId, pending, error.message);
                }
            }
            if (backendSessionId && !isAgentSessionPollingAbort(error) && !isAgentSessionTrackingError(error)) failCinematicSession(sessionId, backendSessionId, error);
            if (!backendSessionId && !isAgentSessionPollingAbort(error) && !isAgentSessionTrackingError(error) && !isCinematicSessionCreationError(error)) {
                failCinematicCreation(sessionId, requestKey, error);
                throw new CinematicSessionCreationError(requestKey, error);
            }
            throw error;
        } finally {
            cinematicSessionControllersRef.current.delete(controllerKey);
            if (backendSessionId) cinematicSessionControllersRef.current.delete(backendSessionId);
        }
    };

    const startChatSession = () => {
        if (activeSession && activeSession.messages.length === 0) {
            setLocalActiveSessionId(activeSession.id);
            return;
        }
        const session = createSession();
        replaceLocalSessions((current) => [session, ...current]);
        setLocalActiveSessionId(session.id);
    };

    const removeSessions = (ids: string[]) => {
        const next = safeSessions.filter((session) => !ids.includes(session.id));
        if (!next.length) {
            const session = createSession();
            replaceLocalSessions([session]);
            setLocalActiveSessionId(session.id);
        } else {
            replaceLocalSessions(next);
            setLocalActiveSessionId(localActiveSessionId && ids.includes(localActiveSessionId) ? next[0].id : localActiveSessionId);
        }
        cleanupImages({ sessions: next });
    };

    const clearSessions = () => {
        const session = createSession();
        replaceLocalSessions([session]);
        setLocalActiveSessionId(session.id);
        cleanupImages({ sessions: [session] });
    };

    const sendMessage = async (text: string, history: CanvasAssistantMessage[], savedReferences?: CanvasAssistantReference[], forcedToolIntent?: OnlineAgentToolIntent) => {
        const requestConfig = { ...effectiveConfig, model: effectiveConfig.textModel || effectiveConfig.model };
        if (!isAiConfigReady(requestConfig, requestConfig.model)) {
            navigateToSettings({ continueCreation: true });
            return false;
        }
        const resolvedRequestConfig = resolveModelRequestConfig(effectiveConfig, requestConfig.model);
        // 测活与真正发送必须使用同一个已解析渠道；resolvedRequestConfig.model
        // 已去掉渠道前缀，直接拿裸模型名重新查找会在多渠道重名时误命中第一条。
        const channel = channelForResolvedRequest(resolvedRequestConfig);
        const modelLabel = modelOptionName(resolvedRequestConfig.model) || resolvedRequestConfig.model;
        if (channel.scope === "system" && !hasSystemModelPrice(channel, modelLabel)) {
            const detail = `系统渠道模型“${modelLabel}”尚未配置用户积分价格；LLM 测活本身不扣积分，但创作台正式调用需要先完成定价。本次没有创建 Agent 会话或调用供应商，请联系管理员在模型管理中设置价格后再试。`;
            message.error(detail);
            addOnlineLog("系统模型缺少用户积分价格，未调用 Agent", { model: modelLabel, channelId: channel.id });
            return false;
        }
        const streamingReadiness = resolveChannelProbeReadiness(channel, modelLabel, resolvedRequestConfig.interfaceType);
        const maxCalls = await confirmOnlineAgentTurn({ model: modelLabel, channel: channel.name, systemChannel: channel.scope === "system", protocol: resolvedRequestConfig.interfaceType, streamingReadiness, singleCall: forcedToolIntent === "manual_storyboard" });
        if (!maxCalls) return false;

        const session = activeSession || createSession();
        if (!activeSession) {
            replaceLocalSessions([session]);
            setLocalActiveSessionId(session.id);
        }

        const refs = savedReferences || selectedReferences;
        const userMessage: CanvasAssistantMessage = { id: nanoid(), role: "user", text, references: refs };
        const assistantId = nanoid();
        const budgetMessageId = nanoid();
        appendMessage(session.id, userMessage);
        appendMessage(session.id, onlineAgentBudgetMessage({
            id: budgetMessageId,
            model: modelLabel,
            usedCalls: 0,
            maxCalls,
            status: "running",
            note: forcedToolIntent === "manual_storyboard"
                ? "本轮使用精简手动分镜路径，只写入 script 节点，不创建视频任务。"
                : `已确认本轮最多 ${maxCalls} 次独立文本模型请求。`,
        }));
        const loop: OnlineLoopContext = {
            step: 1,
            maxCalls,
            budgetMessageId,
            model: modelLabel,
            requestConfig: resolvedRequestConfig,
            toolIntent: forcedToolIntent || onlineAgentToolIntent(text),
            streamingReadinessState: streamingReadiness.state,
            // 影视工具实际依赖 Function Calling；探针结果只用于本轮风险提示和预算收窄，
            // 不作为普通用户的调用授权。真实工具响应仍必须经过本地完整性校验。
            channelProbeTaskId: streamingReadiness.probeTaskId,
            toolProbeTaskId: streamingReadiness.toolProbeTaskId,
        };
        // 在线 Agent 也必须先把用户消息、预算和本轮渠道绑定落盘；否则浏览器在发出供应商请求后刷新，
        // 页面可能丢失调用进度，既无法准确恢复也容易让用户误以为可以重新发送。
        try {
            await onPersistSessionsNow(localSessionsRef.current, session.id);
        } catch (error) {
            const detail = error instanceof Error ? error.message : "浏览器存储不可用";
            const failure = `无法保存在线 Agent 的调用预算与进度：${detail}。为避免响应丢失后重复计费，本次没有调用模型。`;
            upsertMessage(session.id, onlineAgentBudgetMessage({ id: budgetMessageId, model: modelLabel, usedCalls: 0, maxCalls, status: "failed", note: failure }));
            addOnlineLog("在线 Agent 进度保存失败，未调用模型", { model: modelLabel, channelId: resolvedRequestConfig.channelId });
            appendMessage(session.id, { id: nanoid(), role: "error", title: "在线 Agent 未发送", text: failure });
            return false;
        }
        addOnlineLog("已确认并发送请求", { text, model: modelLabel, modelKey: requestConfig.model, protocol: resolvedRequestConfig.interfaceType || "未声明", apiFormat: resolvedRequestConfig.apiFormat, endpoint: agentEndpointLabel(resolvedRequestConfig), channel: channel.name, channelId: channel.id, maxModelCalls: maxCalls, selectedNodeIds: snapshotRef.current.selectedNodeIds, nodeCount: snapshotRef.current.nodes.length, connectionCount: snapshotRef.current.connections.length });
        setPrompt("");
        setIsRunning(true);
        setOnlineTurnActive(true);
        void runOnlineAgentStep(session.id, assistantId, history, userMessage, loop);
        return true;
    };

    const runOnlineAgentStep = async (sessionId: string, assistantId: string, history: CanvasAssistantMessage[], userMessage: CanvasAssistantMessage, loop: OnlineLoopContext) => {
        const requestConfig = loop.requestConfig;
        try {
            setIsRunning(true);
            setOnlineTurnActive(true);
            updateOnlineAgentBudget(sessionId, loop, "running");
            const messages = await buildToolAgentMessages(snapshotRef.current, history, userMessage, manualDelivery, loop.toolIntent);
            const loopRequestConfig = requestConfig;
            // 手动交付只需要把文本整理成可编辑分镜，不能把 Function Calling
            // 当成唯一入口：很多上游文本测活成功，但工具参数协议并不兼容。
            // 该路径改为无工具的一次文本请求，结果由本地解析后写入 script 节点。
            const manualTextPath = loop.toolIntent === "manual_storyboard";
            const agentTools = manualTextPath ? [] : onlineAgentToolsForRequest(loopRequestConfig, loop.toolIntent);
            const requestedToolChoice = manualTextPath ? ("auto" as const) : ("required" as const);
            const toolChoice = resolveOnlineAgentToolChoice(loopRequestConfig.model, loopRequestConfig.interfaceType, requestedToolChoice);
            // 已明确测得非流式时，短 Agent 也不能继续强塞 stream=true：允许它在一轮预算内
            // 用完整 JSON 返回工具调用；其它测活结论只影响风险提示和预算，不阻止请求。
            const streamRequest = manualTextPath
                ? loop.streamingReadinessState === "stream"
                : loop.streamingReadinessState !== "non_stream";
            addOnlineLog(`Agent ${manualTextPath ? "文本整理" : "Tool Loop"} ${loop.step} 开始`, { toolChoiceRequested: requestedToolChoice, toolChoiceSent: toolChoice, toolChoiceReason: onlineAgentToolChoiceReason(loopRequestConfig.model, loopRequestConfig.interfaceType, requestedToolChoice), outputTokenLimit: "provider", model: loopRequestConfig.model, channelId: loopRequestConfig.channelId, protocol: loopRequestConfig.interfaceType || "未声明", apiFormat: loopRequestConfig.apiFormat, endpoint: agentEndpointLabel(loopRequestConfig), toolProfile: onlineAgentToolProfile(loopRequestConfig, loop.toolIntent), toolCount: agentTools.length, manualTextPath, streamRequested: streamRequest, messageCount: messages.length });
            let streamed = "";
            let result: ToolResponseResult;
            try {
                result = await runOnlineAgentRequest<ToolResponseResult>(
                    { sessionId, callNumber: loop.step, model: loop.model },
                    (signal) => requestToolResponse({ ...requestConfig, systemPrompt: "" }, messages, agentTools, toolChoice, (text) => {
                        streamed = text;
                        if (text.trim()) upsertMessage(sessionId, { id: assistantId, role: "assistant", text });
                    }, { signal, idempotencyKey: onlineAgentIdempotencyKey(sessionId, loop), stream: streamRequest }),
                );
            } catch (error) {
                if (error instanceof OnlineAgentRequestStoppedError) throw error;
                throw new OnlineAgentModelCallError(loop.step, error);
            }
            addOnlineLog(manualTextPath ? "模型分镜文本回复" : "模型工具回复", { content: result.content, toolCalls: result.toolCalls });
            if (manualTextPath && result.toolCalls.length) {
                // 手动交付请求明确不发送工具 Schema；兼容网关若仍返回函数调用，不能
                // 把它当成普通 Agent 继续执行，否则会绕过“只交付提示词”的产品边界。
                throw new OnlineAgentModelCallError(loop.step, new Error("模型在手动分镜路径返回了工具调用；本次未执行任何画布或视频操作。请重试一次短文本分镜整理，或切换支持纯文本 Chat 的模型"));
            }
            if (manualTextPath) {
                const ops = manualStoryboardTextToOps(result.content, snapshotRef.current);
                if (!ops.length) throw new OnlineAgentModelCallError(loop.step, new Error("模型返回了文本，但没有识别出可用的分镜镜头；本次没有写入画布。请把需求拆成 3-5 个镜头后再试。"));
                const execution = executeOps(ops);
                const createdNode = ops.find((op): op is Extract<CanvasAgentOp, { type: "add_node" }> => op.type === "add_node");
                const rowCount = createdNode?.metadata?.storyboard?.rows?.length || 0;
                const summary = execution.changed
                    ? `已创建可编辑分镜脚本（${rowCount || "若干"} 个镜头）。现在可以复制每行的图片提示词和视频动作提示词，到网页工作台逐镜生成。`
                    : "模型返回了分镜内容，但画布状态没有变化；请查看 Agent 日志后重试。";
                upsertMessage(sessionId, { id: assistantId, role: "assistant", text: summary });
                updateOnlineAgentBudget(sessionId, loop, execution.changed ? "completed" : "failed", summary);
                addOnlineLog("手动分镜文本已写入画布", { changed: execution.changed, rowCount, contentLength: result.content.length });
                try {
                    await onPersistSessionsNow(localSessionsRef.current, sessionId);
                } catch (error) {
                    const detail = error instanceof Error ? error.message : "浏览器存储不可用";
                    appendMessage(sessionId, { id: nanoid(), role: "error", title: "分镜已写入，但会话状态未完整保存", text: `画布内容已经保留；无法保存本轮预算：${detail}` });
                }
                return;
            }
            if (result.toolCalls.length) {
                const writableCalls = result.toolCalls.filter(isWritableToolCall);
                if ((confirmTools || requiresExplicitToolConfirmation(result.toolCalls)) && writableCalls.length) {
                    upsertMessage(sessionId, { id: assistantId, role: "assistant", text: result.content || streamed || "准备执行工具，等待确认。" });
                    const toolMessageId = nanoid();
                    pendingToolContextRef.current.set(toolMessageId, { messages, toolCalls: result.toolCalls, assistantId, loop, reasoningContent: result.reasoningContent, assistantContent: result.content });
                    const requestIdentity = await onlineAgentRequestIdentity(effectiveConfig, loop.requestConfig).catch(() => undefined);
                    const toolMessage: CanvasAssistantMessage = { id: toolMessageId, role: "tool", title: "确认工具调用", text: summarizeToolCalls(result.toolCalls), detail: { status: "pending", step: loop.step, maxCalls: loop.maxCalls, budgetMessageId: loop.budgetMessageId, model: loop.model, toolCalls: result.toolCalls, ...(loop.toolIntent ? { toolIntent: loop.toolIntent } : {}), reasoningStateRequired: Boolean(result.reasoningContent?.trim()), ...(requestIdentity ? { requestChannelId: requestIdentity.channelId, requestConfigIdentity: requestIdentity.configIdentity } : {}), impact: previewOnlineToolCalls(result.toolCalls, snapshotRef.current, loop.requestConfig) } };
                    appendMessage(sessionId, toolMessage);
                    updateOnlineAgentBudget(sessionId, loop, "waiting_tool");
                    addOnlineLog("等待用户确认", result.toolCalls);
                    return;
                }
                await continueOnlineToolLoop(sessionId, assistantId, messages, result, loop);
            } else {
                const detail = toolChoice === "auto"
                    ? "当前模型接口不支持强制 tool_choice=required，系统已改用 auto 并在本地要求首轮必须返回工具调用；本次仍未返回工具调用，画布操作未执行，本轮不会继续。"
                    : "模型没有按首轮 required 要求返回工具调用；画布操作未执行，本轮不会继续。";
                throw new OnlineAgentModelCallError(loop.step, new Error(detail));
            }
        } catch (error) {
            recordOnlineAgentError(sessionId, loop, error);
        } finally {
            setOnlineTurnActive(false);
            setIsRunning(false);
        }
    };

    const continueOnlineToolLoop = async (sessionId: string, assistantId: string, messages: ResponseInputMessage[], result: ToolResponseResult, loop: OnlineLoopContext) => {
        const toolResults = await executeOnlineToolCalls(sessionId, result.toolCalls, loop);
        addOnlineLog("工具执行结果", toolResults);
        appendMessage(sessionId, {
            id: nanoid(),
            role: "tool",
            title: "工具自动执行完成",
            text: toolResults.map((item) => toolResultText(item.result)).join("\n"),
            detail: { status: "completed", step: loop.step, toolCalls: result.toolCalls, results: toolResults },
        });
        await continueOnlineToolLoopAfterResults(sessionId, assistantId, messages, result.toolCalls, toolResults, loop, result.reasoningContent, result.content);
    };

    const continueOnlineToolLoopAfterResults = async (sessionId: string, assistantId: string, messages: ResponseInputMessage[], toolCalls: ResponseToolCall[], toolResults: OnlineExecutedToolCall[], loop: OnlineLoopContext, reasoningContent?: string, assistantContent?: string) => {
        const nextMessages: ResponseInputMessage[] = [
            ...messages,
            ...toolCalls.map((call, index) => toolCallToResponseInput(call, reasoningContent, index === 0 ? assistantContent : undefined)),
            ...toolResults.map((item) => ({ role: "tool" as const, tool_call_id: item.toolCallId, content: JSON.stringify(item.result) })),
        ];
        if (toolResults.some((item) => isManualDeliveryBlockedResult(item.result))) {
            // 手动交付模式的拒绝是产品边界，不是给模型继续纠错的普通工具失败；
            // 停在已生成的分镜图和视频提示词处，避免再发一轮付费请求。
            const notice = `${MANUAL_DELIVERY_VIDEO_MESSAGE}本轮不会再发送下一次模型请求。`;
            upsertMessage(sessionId, { id: assistantId, role: "assistant", text: notice });
            updateOnlineAgentBudget(sessionId, loop, "stopped", notice);
            addOnlineLog("手动交付模式已停止视频工具后的下一轮请求", { step: loop.step, model: loop.model });
            try {
                await onPersistSessionsNow(localSessionsRef.current, sessionId);
            } catch (error) {
                const detail = error instanceof Error ? error.message : "浏览器存储不可用";
                appendMessage(sessionId, { id: nanoid(), role: "error", title: "在线 Agent 状态未完整保存", text: `已阻止下一轮模型请求，但无法保存停止状态：${detail}。请先确认画布中的分镜结果。` });
            }
            return;
        }
        if (loop.step >= loop.maxCalls) {
            upsertMessage(sessionId, { id: assistantId, role: "assistant", text: toolResults.map((item) => toolResultText(item.result)).join("\n") || "工具已执行。" });
            updateOnlineAgentBudget(sessionId, loop, "completed", "已达到本轮授权上限，未再发送模型请求。");
            addOnlineLog("Agent Tool Loop 达到调用预算上限", { maxModelCalls: loop.maxCalls });
            try {
                await onPersistSessionsNow(localSessionsRef.current, sessionId);
            } catch (error) {
                const detail = error instanceof Error ? error.message : "浏览器存储不可用";
                const failure = `工具已经执行，但无法保存在线 Agent 的完成进度：${detail}。本轮没有再发送模型请求，请先确认画布和会话状态后再继续。`;
                upsertMessage(sessionId, onlineAgentBudgetMessage({ id: loop.budgetMessageId, model: loop.model, usedCalls: loop.step, maxCalls: loop.maxCalls, status: "failed", note: failure }));
                addOnlineLog("在线 Agent 完成进度保存失败", { error: detail, providerCalls: loop.step });
                appendMessage(sessionId, { id: nanoid(), role: "error", title: "在线 Agent 状态未完整保存", text: failure });
            }
            return;
        }
        const requestConfig = loop.requestConfig;
        const nextLoop = { ...loop, step: loop.step + 1 };
        updateOnlineAgentBudget(sessionId, nextLoop, "running");
        // 工具已经执行但下一轮模型尚未发送；先保存工具结果和新的轮次预算，
        // 存储失败时停止在此处，避免刷新后无法判断是否已经产生第二次供应商费用。
        try {
            await onPersistSessionsNow(localSessionsRef.current, sessionId);
        } catch (error) {
            const detail = error instanceof Error ? error.message : "浏览器存储不可用";
            const failure = `无法保存在线 Agent 第 ${nextLoop.step} 次调用的进度：${detail}。工具结果已保留，但本次没有发送新的模型请求。`;
            upsertMessage(sessionId, onlineAgentBudgetMessage({ id: nextLoop.budgetMessageId, model: nextLoop.model, usedCalls: loop.step, maxCalls: nextLoop.maxCalls, status: "failed", note: failure }));
            addOnlineLog("在线 Agent 下一轮进度保存失败，未调用模型", { step: nextLoop.step, providerCalls: loop.step });
            appendMessage(sessionId, { id: nanoid(), role: "error", title: "在线 Agent 已停止", text: failure });
            return;
        }
        let streamed = "";
        let next: ToolResponseResult;
        try {
            const agentTools = onlineAgentToolsForRequest(nextLoop.requestConfig, nextLoop.toolIntent);
            next = await runOnlineAgentRequest<ToolResponseResult>(
                { sessionId, callNumber: nextLoop.step, model: nextLoop.model },
                (signal) => requestToolResponse({ ...requestConfig, systemPrompt: "" }, nextMessages, agentTools, "auto", (text) => {
                    streamed = text;
                    if (text.trim()) upsertMessage(sessionId, { id: assistantId, role: "assistant", text });
                }, { signal, idempotencyKey: onlineAgentIdempotencyKey(sessionId, nextLoop), stream: nextLoop.toolIntent === "manual_storyboard" ? nextLoop.streamingReadinessState === "stream" : nextLoop.streamingReadinessState !== "non_stream" }),
            );
        } catch (error) {
            if (error instanceof OnlineAgentRequestStoppedError) throw error;
            throw new OnlineAgentModelCallError(nextLoop.step, error);
        }
        addOnlineLog(`Agent Tool Loop ${nextLoop.step} 回复`, { content: next.content, toolCalls: next.toolCalls });
        if (next.toolCalls.length) {
            const writableCalls = next.toolCalls.filter(isWritableToolCall);
            if ((confirmTools || requiresExplicitToolConfirmation(next.toolCalls)) && writableCalls.length) {
                upsertMessage(sessionId, { id: assistantId, role: "assistant", text: next.content || streamed || "准备执行工具，等待确认。" });
                const toolMessageId = nanoid();
                pendingToolContextRef.current.set(toolMessageId, { messages: nextMessages, toolCalls: next.toolCalls, assistantId, loop: nextLoop, reasoningContent: next.reasoningContent, assistantContent: next.content });
                const requestIdentity = await onlineAgentRequestIdentity(effectiveConfig, nextLoop.requestConfig).catch(() => undefined);
                appendMessage(sessionId, { id: toolMessageId, role: "tool", title: "确认工具调用", text: summarizeToolCalls(next.toolCalls), detail: { status: "pending", step: nextLoop.step, maxCalls: nextLoop.maxCalls, budgetMessageId: nextLoop.budgetMessageId, model: nextLoop.model, toolCalls: next.toolCalls, ...(nextLoop.toolIntent ? { toolIntent: nextLoop.toolIntent } : {}), reasoningStateRequired: Boolean(next.reasoningContent?.trim()), ...(requestIdentity ? { requestChannelId: requestIdentity.channelId, requestConfigIdentity: requestIdentity.configIdentity } : {}), impact: previewOnlineToolCalls(next.toolCalls, snapshotRef.current, nextLoop.requestConfig) } });
                updateOnlineAgentBudget(sessionId, nextLoop, "waiting_tool");
                addOnlineLog("等待用户确认", next.toolCalls);
                return;
            }
            await continueOnlineToolLoop(sessionId, assistantId, nextMessages, next, nextLoop);
            return;
        }
        if (!next.content.trim()) {
            throw new OnlineAgentModelCallError(nextLoop.step, new Error("模型在工具结果之后没有返回可用文本或下一项工具调用；本轮已停止，不会把旧工具结果伪装成新的模型回复。"));
        }
        upsertMessage(sessionId, { id: assistantId, role: "assistant", text: next.content || streamed });
        updateOnlineAgentBudget(sessionId, nextLoop, "completed");
    };

    const executeOps = (ops: CanvasAgentOp[]) => {
        const beforeSnapshot = snapshotRef.current;
        const before = snapshotSignature(beforeSnapshot);
        const next = onApplyOps(ops);
        snapshotRef.current = next;
        const ranGeneration = ops.some((op) => (op.type === "run_generation" && Boolean(op.nodeId)) || (op.type === "run_image_annotation" && Boolean(op.annotationNodeId)));
        const changed = before !== snapshotSignature(next) || ranGeneration;
        const noopReason = changed ? "" : explainNoop(ops, beforeSnapshot);
        return { changed, ops, ranGeneration, noopReason, before: JSON.parse(before), after: JSON.parse(snapshotSignature(next)) };
    };

    const executeOnlineTool = async (sessionId: string, name: string, args: Record<string, unknown>, loop: OnlineLoopContext): Promise<OnlineToolResult> => {
            const current = snapshotRef.current;
            const requestConfig = loop.requestConfig;
            try {
            if (name === "canvas_get_state") return { ok: true, message: describeCanvasSnapshot(current), data: compactSnapshot(current) };
            if (name === "canvas_export_snapshot") return { ok: true, message: describeCanvasSnapshot(current), data: compactSnapshot(current) };
            if (name === "canvas_get_selection") {
                const ids = new Set(current.selectedNodeIds || []);
                return { ok: true, message: `当前选中 ${ids.size} 个节点。`, data: { nodes: compactSnapshot({ ...current, nodes: current.nodes.filter((node) => ids.has(node.id)) }).nodes } };
            }
            if (name === "canvas_get_image_annotations") {
                const annotations = imageAnnotationsFromSnapshot(current, stringOptional(args.nodeId));
                return { ok: true, message: `读取到 ${annotations.length} 个图片标注。`, data: { annotations } };
            }
            if (name === "canvas_create_cinematic_session") {
                const channel = channelForResolvedRequest(requestConfig);
                const resolvedRequestConfig = requestConfig;
                const modelLabel = modelOptionName(requestConfig.model) || requestConfig.model;
                const streamingReadiness = resolveChannelProbeReadiness(channel, modelLabel, resolvedRequestConfig.interfaceType);
                if (!await confirmCinematicTask({ model: modelLabel, channel: channel.name, systemChannel: channel.scope === "system", protocol: resolvedRequestConfig.interfaceType, streamingReadiness, requireToolCalling: true })) {
                    return { ok: false, message: "用户未授权影视分镜的结构修复预算，未创建后台任务，也未调用供应商。" };
                }
                const cinematic = await runCinematicSession(sessionId, requireString(args.prompt, "prompt"), current, requestConfig, {
                    channelProbeTaskId: loop.channelProbeTaskId,
                    toolProbeTaskId: loop.toolProbeTaskId,
                    allowPaidStructureRepair: true,
                });
                try {
                    const result = executeOps(cinematic.ops);
                    completeCinematicSession(sessionId, cinematic.backendSessionId, cinematic.ops);
                    return { ok: result.changed, message: result.changed ? summarizeCanvasAgentOps(cinematic.ops) || "后端影视 Agent 已写回画布。" : result.noopReason, data: result };
                } catch (error) {
                    failCinematicSession(sessionId, cinematic.backendSessionId, error);
                    throw error;
                }
            }
            if (manualDeliveryRef.current && isManualDeliveryVideoToolCall(name, args)) {
                return { ok: false, message: MANUAL_DELIVERY_VIDEO_MESSAGE };
            }
            const ops = onlineToolToOps(name, args, current, requestConfig);
            const result = executeOps(ops);
            return { ok: result.changed, message: result.changed ? summarizeCanvasAgentOps(ops) || "画布操作已执行。" : result.noopReason, data: result };
        } catch (error) {
            if (isAgentSessionPollingAbort(error) || isAgentSessionTrackingError(error)) throw error;
            return { ok: false, message: error instanceof Error ? error.message : "工具执行失败" };
        }
    };

    const executeOnlineToolCall = async (sessionId: string, toolCall: ResponseToolCall, loop: OnlineLoopContext): Promise<OnlineExecutedToolCall> => {
        try {
            const result = await executeOnlineTool(sessionId, toolCall.function.name, parseToolArguments(toolCall.function.arguments), loop);
            return { toolCallId: toolCall.id, name: toolCall.function.name, result };
        } catch (error) {
            if (isAgentSessionPollingAbort(error) || isAgentSessionTrackingError(error)) throw error;
            return { toolCallId: toolCall.id, name: toolCall.function.name, result: { ok: false, message: error instanceof Error ? error.message : "工具参数错误" } };
        }
    };

    const executeOnlineToolCalls = async (sessionId: string, toolCalls: ResponseToolCall[], loop: OnlineLoopContext) => {
        const results: OnlineExecutedToolCall[] = [];
        let stopped = false;
        for (const toolCall of toolCalls) {
            if (stopped) {
                results.push({ toolCallId: toolCall.id, name: toolCall.function.name, result: { ok: false, message: "前一个工具调用失败，未继续执行。" } });
                continue;
            }
            const result = await executeOnlineToolCall(sessionId, toolCall, loop);
            results.push(result);
            if (!result.result.ok) stopped = true;
        }
        return results;
    };

    const approveOnlineTool = async (messageId: string) => {
        const message = safeSessions.flatMap((session) => session.messages).find((item) => item.id === messageId);
        const detail = objectDetail(message?.detail);
        const pendingContext = pendingToolContextRef.current.get(messageId);
        const toolCalls = pendingContext?.toolCalls || toolCallsFromDetail(detail);
        const previousMessages = pendingContext?.messages || [];
        const session = safeSessions.find((session) => session.messages.some((item) => item.id === messageId));
        addOnlineLog("批准工具", { messageId, toolCalls });
        if (!session) return;
        const assistantId = pendingContext?.assistantId || "";
        const restoredRequest = pendingContext ? undefined : await restorePersistedOnlineAgentRequest(effectiveConfig, detail);
        if (!pendingContext && !restoredRequest?.requestConfig) {
            pendingToolContextRef.current.delete(messageId);
            const stoppedLoop: OnlineLoopContext = {
                step: Number(detail.step) || 1,
                maxCalls: normalizeOnlineAgentMaxCalls(detail.maxCalls),
                budgetMessageId: stringOptional(detail.budgetMessageId) || "",
                model: stringOptional(detail.model) || modelOptionName(activeModel) || activeModel,
                requestConfig: resolveModelRequestConfig(effectiveConfig, effectiveConfig.textModel || effectiveConfig.model),
                toolIntent: persistedOnlineToolIntent(detail.toolIntent),
            };
            const reason = restoredRequest?.reason || "页面刷新后工具上下文无法恢复，本次没有继续模型请求。";
            upsertMessage(session.id, { id: messageId, role: "tool", title: "工具上下文已失效", text: reason, detail: { ...detail, status: "failed" } });
            updateOnlineAgentBudget(session.id, stoppedLoop, "stopped", reason);
            return;
        }
        const loop = pendingContext?.loop || {
            step: Number(detail.step) || 1,
            maxCalls: normalizeOnlineAgentMaxCalls(detail.maxCalls),
            budgetMessageId: stringOptional(detail.budgetMessageId) || "",
            model: stringOptional(detail.model) || modelOptionName(activeModel) || activeModel,
            requestConfig: restoredRequest?.requestConfig || resolveModelRequestConfig(effectiveConfig, effectiveConfig.textModel || effectiveConfig.model),
            toolIntent: persistedOnlineToolIntent(detail.toolIntent),
        };
        if (!pendingContext && detail.reasoningStateRequired === true) {
            // Kimi 等协议的 reasoning_content 只允许保存在内存；刷新后不能省略它再发一笔付费请求。
            pendingToolContextRef.current.delete(messageId);
            upsertMessage(session.id, { id: messageId, role: "tool", title: "工具上下文已失效", text: "页面刷新后模型要求的临时推理上下文已丢失，本次没有执行工具或发送下一轮请求。请重新发送消息。", detail: { ...detail, status: "failed" } });
            updateOnlineAgentBudget(session.id, loop, "stopped", "临时推理上下文已失效，未继续发送模型请求。");
            return;
        }
        if (!toolCalls.length || !previousMessages.length || !assistantId || !loop.budgetMessageId) {
            pendingToolContextRef.current.delete(messageId);
            upsertMessage(session.id, { id: messageId, role: "tool", title: "工具执行失败", text: "工具上下文不完整，无法执行。", detail: { ...detail, status: "failed" } });
            updateOnlineAgentBudget(session.id, loop, "stopped", "工具上下文不完整，未继续发送模型请求。");
            return;
        }
        let toolsCompleted = false;
        try {
            setIsRunning(true);
            setOnlineTurnActive(true);
            updateOnlineAgentBudget(session.id, loop, "waiting_tool", "正在执行已批准的工具。");
            const results = await executeOnlineToolCalls(session.id, toolCalls, loop);
            addOnlineLog("工具执行结果", results);
            upsertMessage(session.id, { id: messageId, role: "tool", title: "工具执行完成", text: results.map((item) => toolResultText(item.result)).join("\n"), detail: { ...detail, results, status: "completed" } });
            toolsCompleted = true;
            pendingToolContextRef.current.delete(messageId);
            await continueOnlineToolLoopAfterResults(session.id, assistantId, previousMessages, toolCalls, results, loop, pendingContext?.reasoningContent, pendingContext?.assistantContent);
        } catch (error) {
            pendingToolContextRef.current.delete(messageId);
            if (!toolsCompleted) {
                upsertMessage(session.id, { id: messageId, role: "tool", title: "工具执行失败", text: error instanceof Error ? error.message : "工具执行失败", detail: { ...detail, status: "failed" } });
            }
            recordOnlineAgentError(session.id, loop, error);
        } finally {
            setOnlineTurnActive(false);
            setIsRunning(false);
        }
    };

    const rejectOnlineTool = (messageId: string) => {
        const session = safeSessions.find((session) => session.messages.some((item) => item.id === messageId));
        const detail = objectDetail(session?.messages.find((item) => item.id === messageId)?.detail);
        const pendingContext = pendingToolContextRef.current.get(messageId);
        const loop = pendingContext?.loop || {
            step: Number(detail.step) || 1,
            maxCalls: normalizeOnlineAgentMaxCalls(detail.maxCalls),
            budgetMessageId: stringOptional(detail.budgetMessageId) || "",
            model: stringOptional(detail.model) || modelOptionName(activeModel) || activeModel,
            requestConfig: resolveModelRequestConfig(effectiveConfig, effectiveConfig.textModel || effectiveConfig.model),
        };
        addOnlineLog("拒绝工具", { messageId });
        pendingToolContextRef.current.delete(messageId);
        if (session) {
            upsertMessage(session.id, { id: messageId, role: "tool", title: "已拒绝执行", text: "工具调用已取消", detail: { ...detail, status: "rejected" } });
            updateOnlineAgentBudget(session.id, loop, "stopped", "用户拒绝工具调用，未继续发送模型请求。");
        }
    };

    const undoLastOnlineBatch = () => {
        const restored = onUndoOps();
        if (!restored) return;
        snapshotRef.current = restored;
        if (activeSession) appendMessage(activeSession.id, { id: nanoid(), role: "tool", title: "已撤销 Agent 批次", text: "已恢复到本次写回前的画布状态", detail: { status: "completed", remainingUndoCount: Math.max(0, undoOpsCount - 1) } });
    };

    const submit = async () => {
        const text = prompt.trim();
        if (!text || agentBusy) return;
        // 已知的影视入口不再先付费调用一次在线 Agent 来“猜工具”。
        // 直接走持久化影视会话，保留同一套测活、费用确认、幂等和结构修复边界，
        // 避免出现“测活成功，但首轮工具调用被网关拒绝”而无法开始创作。
        if (isDirectCinematicPrompt(text)) {
            if (manualDelivery) {
                // 手动交付的终点是“画布整理提示词 → 网页工作台逐镜生成”，
                // 即使测活观察到渐进 SSE，也不应再创建后台长分镜任务；长任务
                // 只会增加等待和 524 风险，却不会替用户提交视频。
                await sendMessage(text, messages, undefined, "manual_storyboard");
                return;
            }
            await submitCinematicProject(text);
            return;
        }
        await sendMessage(text, messages);
    };

    useEffect(() => {
        if (!cinematicEntry) return;
        setCinematicEntryActive(true);
        setView("chat");
        setPrompt("");
        onCinematicEntryConsumed?.();
    }, [cinematicEntry, onCinematicEntryConsumed]);

    const submitCinematicProject = async (text: string) => {
        const value = text.trim();
        if (!value || agentBusy) return;
        if (manualDelivery) {
            // 空画布的“一句话生成影视项目”入口不经过 submit；手动交付仍必须
            // 停在可编辑脚本节点，不能从这个快捷入口绕回后台长分镜任务。
            if (await sendMessage(value, messages, undefined, "manual_storyboard")) setCinematicEntryActive(false);
            return;
        }
        const requestConfig = { ...effectiveConfig, model: effectiveConfig.textModel || effectiveConfig.model };
        if (!isAiConfigReady(requestConfig, requestConfig.model)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        const resolvedRequestConfig = resolveModelRequestConfig(effectiveConfig, requestConfig.model);
        // 影视入口同样要沿用测活时解析出的渠道和模型，不能按已去前缀的裸模型名重新猜渠道。
        const channel = channelForResolvedRequest(resolvedRequestConfig);
        const modelLabel = modelOptionName(resolvedRequestConfig.model) || resolvedRequestConfig.model;
        if (channel.scope === "system" && !hasSystemModelPrice(channel, modelLabel)) {
            const detail = `系统渠道模型“${modelLabel}”尚未配置用户积分价格；LLM 测活本身不扣积分，但影视/手动分镜正式调用需要先完成定价。本次没有创建任务或调用供应商，请联系管理员在模型管理中设置价格后再试。`;
            message.error(detail);
            addOnlineLog("系统模型缺少用户积分价格，未调用影视任务", { model: modelLabel, channelId: channel.id });
            return;
        }
        const streamingReadiness = resolveChannelProbeReadiness(channel, modelLabel, resolvedRequestConfig.interfaceType);
        const prefersShortDelivery = prefersShortCinematicDelivery(resolvedRequestConfig.model, resolvedRequestConfig.interfaceType);
        if (prefersShortDelivery) {
            // 兼容别名优先交付一轮有界的可编辑分镜文本，降低已知网关的等待和 524 风险；
            // 这只是兼容策略，不是测活状态对普通用户的调用门禁。
            addOnlineLog("慢速兼容模型改走短分镜交付", { model: modelLabel, channelId: channel.id, interfaceType: resolvedRequestConfig.interfaceType, reason: "兼容模型别名" });
            if (await sendMessage(value, messages, undefined, "manual_storyboard")) setCinematicEntryActive(false);
            return;
        }
        if (!await confirmCinematicTask({ model: modelLabel, channel: channel.name, systemChannel: channel.scope === "system", protocol: resolvedRequestConfig.interfaceType, streamingReadiness, requireToolCalling: true })) return;
        const session = activeSession || createSession();
        if (!activeSession) {
            replaceLocalSessions([session]);
            setLocalActiveSessionId(session.id);
        }
        appendMessage(session.id, { id: nanoid(), role: "user", text: value });
        addOnlineLog("已确认影视项目费用边界", { model: modelLabel, maxProviderCalls: 2 });
        setPrompt("");
        setIsRunning(true);
        let backendSessionId = "";
        try {
            // 直接入口要复用刚完成测活的同一条渠道、协议和模型；再次传入裸 AiConfig
            // 会在多渠道同名模型时重新猜渠道，造成“测活成功、创作台失败”。
            const cinematic = await runCinematicSession(session.id, value, snapshotRef.current, resolvedRequestConfig, {
                channelProbeTaskId: streamingReadiness.probeTaskId,
                toolProbeTaskId: streamingReadiness.toolProbeTaskId,
                allowPaidStructureRepair: true,
                onCreated: (createdId) => {
                    backendSessionId = createdId;
                },
            });
            const next = onApplyOps(cinematic.ops);
            snapshotRef.current = next;
            completeCinematicSession(session.id, cinematic.backendSessionId, cinematic.ops);
            setCinematicEntryActive(false);
        } catch (error) {
            if (isAgentSessionPollingAbort(error)) return;
            if (isAgentSessionTrackingError(error)) {
                addOnlineLog("影视项目状态跟踪中断，已保留后台会话", error.message);
                return;
            }
            if (isCinematicSessionCreationError(error)) {
                addOnlineLog("影视项目创建失败，未留下待重试请求", error.message);
                return;
            }
            if (backendSessionId) failCinematicSession(session.id, backendSessionId, error);
            else appendMessage(session.id, { id: nanoid(), role: "error", title: "影视项目生成失败", text: error instanceof Error ? error.message : "影视项目生成失败" });
        } finally {
            setIsRunning(false);
        }
    };

    const resumePendingCinematicCreation = async (sessionId: string, pending: Extract<CanvasAssistantPendingBackendSession, { status: "creating" }>) => {
        const controllerKey = `creating:${pending.requestKey}`;
        if (cinematicSessionControllersRef.current.has(controllerKey) || cinematicCreationRecoveriesRef.current.has(pending.requestKey)) return;
        cinematicCreationRecoveriesRef.current.add(pending.requestKey);
        let started = false;
        try {
            const requestConfig = { ...effectiveConfig, model: effectiveConfig.textModel || effectiveConfig.model };
            if (!isAiConfigReady(requestConfig, requestConfig.model)) {
                const text = "原创建标识仍已保留，但当前文本模型配置不可用。请恢复原模型配置后重新打开画布；不要重新提交影视项目。";
                pauseCinematicCreationRecovery(sessionId, pending, text);
                addOnlineLog("影视项目创建恢复等待模型配置", { requestKey: pending.requestKey });
                return;
            }
            const resolvedRequestConfig = resolveModelRequestConfig(effectiveConfig, requestConfig.model);
            let currentIdentity: string;
            try {
                currentIdentity = await cinematicRequestConfigIdentity(effectiveConfig, resolvedRequestConfig);
            } catch (error) {
                const detail = error instanceof Error ? error.message : "无法计算配置指纹";
                pauseCinematicCreationRecovery(sessionId, pending, `原创建标识仍已保留，但无法核对原模型配置：${detail}。系统没有补发请求，请勿重新提交。`);
                return;
            }
            if (currentIdentity !== pending.configIdentity) {
                const text = "原创建标识仍已保留，但模型、渠道或凭据版本已经变化。为避免用未经确认的新配置产生费用，系统没有补发请求；请恢复原配置或从任务中心核对原任务。";
                pauseCinematicCreationRecovery(sessionId, pending, text);
                addOnlineLog("影视项目创建恢复因配置变化暂停", { requestKey: pending.requestKey });
                return;
            }
            started = true;
            setIsRunning(true);
            addOnlineLog("恢复尚未确认 ID 的影视项目创建", { requestKey: pending.requestKey });
            // 恢复创建也必须沿用身份校验时解析出的请求配置，不能在恢复阶段按裸模型名重选渠道。
            const cinematic = await runCinematicSession(sessionId, pending.prompt, snapshotRef.current, resolvedRequestConfig, {
                requestKey: pending.requestKey,
                configIdentity: pending.configIdentity,
                channelProbeTaskId: pending.channelProbeTaskId,
                toolProbeTaskId: pending.toolProbeTaskId,
                allowPaidStructureRepair: pending.allowPaidStructureRepair === true,
            });
            executeOps(cinematic.ops);
            completeCinematicSession(sessionId, cinematic.backendSessionId, cinematic.ops, true);
            addOnlineLog("影视项目创建恢复并写回完成", { backendSessionId: cinematic.backendSessionId });
        } catch (error) {
            if (!isAgentSessionPollingAbort(error)) {
                if (isAgentSessionTrackingError(error)) {
                    addOnlineLog("影视项目创建结果仍无法确认，继续保留原标识", error.message);
                } else if (isCinematicSessionCreationError(error)) {
                    addOnlineLog("影视项目恢复创建明确失败", error.message);
                } else {
                    failCinematicCreation(sessionId, pending.requestKey, error);
                    addOnlineLog("影视项目恢复创建失败", error instanceof Error ? error.message : error);
                }
            }
        } finally {
            cinematicCreationRecoveriesRef.current.delete(pending.requestKey);
            if (started && cinematicSessionControllersRef.current.size === 0) setIsRunning(false);
        }
    };

    const resumePendingCinematicSession = async (sessionId: string, pending: Extract<CanvasAssistantPendingBackendSession, { status: "pending" }>) => {
        if (cinematicSessionControllersRef.current.has(pending.id)) return;
        const controller = new AbortController();
        cinematicSessionControllersRef.current.set(pending.id, controller);
        setIsRunning(true);
        addOnlineLog("恢复后端影视 Agent 会话", { backendSessionId: pending.id });
        try {
            const detail = await resumeCinematicAgentSession(pending.id, { signal: controller.signal });
            const ops = requireOps(JSON.parse(cinematicAgentSessionOpsJson(detail)));
            executeOps(ops);
            completeCinematicSession(sessionId, pending.id, ops, true);
            addOnlineLog("后端影视 Agent 会话恢复完成", { backendSessionId: pending.id });
        } catch (error) {
            if (!isAgentSessionPollingAbort(error)) {
                if (isAgentSessionTrackingError(error)) {
                    pauseCinematicSessionRecovery(sessionId, pending.id, error.message);
                    addOnlineLog("后端影视 Agent 状态仍无法查询，保留会话等待下次恢复", error.message);
                } else {
                    failCinematicSession(sessionId, pending.id, error);
                    addOnlineLog("后端影视 Agent 会话恢复失败", error instanceof Error ? error.message : error);
                }
            }
        } finally {
            if (cinematicSessionControllersRef.current.get(pending.id) === controller) cinematicSessionControllersRef.current.delete(pending.id);
            if (cinematicSessionControllersRef.current.size === 0) setIsRunning(false);
        }
    };

    useEffect(() => {
        localSessions.forEach((session) => {
            const pending = session.pendingBackendSession;
            if (!pending || pending.kind !== "cinematic") return;
            const recoveryKey = pending.status === "creating" ? `creating:${pending.requestKey}` : `pending:${pending.id}`;
            // 每次挂载只自动接管一次；状态未知后要等用户重新打开画布，不能在同一页面循环补发。
            if (cinematicAutoRecoveryAttemptsRef.current.has(recoveryKey)) return;
            cinematicAutoRecoveryAttemptsRef.current.add(recoveryKey);
            if (pending.canvasId && pending.canvasId !== projectId) {
                detachCinematicPending(session.id, pending, "该记录来自另一个画布副本，系统不会在当前画布自动接管或重新提交原影视项目。请回到原画布或在任务中心核对。");
                return;
            }
            if (pending.status === "creating") void resumePendingCinematicCreation(session.id, pending);
            else void resumePendingCinematicSession(session.id, pending);
        });
    }, [localSessions, projectId]);

    const addImagesToCanvas = (files: FileList | File[] | null) => {
        const file = Array.from(files || []).find((item) => item.type.startsWith("image/"));
        if (file) onPasteImage(file);
    };

    const startResize = () => {
        const move = (event: MouseEvent) => setWidth(Math.min(760, Math.max(320, window.innerWidth - event.clientX)));
        const stop = () => {
            setResizing(false);
            document.body.style.cursor = "";
            document.body.style.userSelect = "";
            document.removeEventListener("mousemove", move);
            document.removeEventListener("mouseup", stop);
        };
        setResizing(true);
        document.body.style.cursor = "col-resize";
        document.body.style.userSelect = "none";
        document.addEventListener("mousemove", move);
        document.addEventListener("mouseup", stop);
    };

    const collapse = () => {
        if (!allowOnlineAgentCollapse()) return;
        onCollapse();
    };

    const onlineContent = (
        <>
            <AgentPanelTabs
                value={view}
                theme={theme}
                items={[
                    { value: "setup", label: "连接配置", icon: <Settings2 className="size-3.5" /> },
                    { value: "chat", label: "对话" },
                    { value: "history", label: "历史", icon: <History className="size-3.5" />, count: historySessions.length },
                    { value: "log", label: "日志", count: onlineLogs.length },
                ]}
                onChange={setView}
                right={
                    <>
                        {view === "history" ? (
                            <Tooltip title="删除全部">
                                <Button type="text" shape="circle" className="!h-8 !w-8 !min-w-8" style={iconButtonStyle} icon={<X className="size-4" />} disabled={!historySessions.length || agentBusy} onClick={() => setDeleteChatIds(historySessions.map((session) => session.id))} />
                            </Tooltip>
                        ) : null}
                        <Tooltip title="新对话">
                            <Button
                                type="text"
                                shape="circle"
                                className="!h-8 !w-8 !min-w-8"
                                style={iconButtonStyle}
                                icon={<Plus className="size-4" />}
                                disabled={!hasMessages || agentBusy}
                                onClick={() => {
                                    startChatSession();
                                    setView("chat");
                                }}
                            />
                        </Tooltip>
                        <Tooltip title="配置">
                            <Button type="text" shape="circle" className="!h-8 !w-8 !min-w-8" style={iconButtonStyle} icon={<Settings2 className="size-4" />} disabled={agentBusy} onClick={() => navigateToSettings()} />
                        </Tooltip>
                    </>
                }
            />

            {view === "setup" ? (
                <OnlineAgentSetupView theme={theme} activeModel={activeModel} disabled={agentBusy} onOpenConfig={() => navigateToSettings({ continueCreation: true })} />
            ) : (
                <div ref={chatListRef} className="thin-scrollbar min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-4">
                    {view === "history" ? (
                        <AssistantHistory
                            sessions={historySessions}
                            activeSession={activeSession}
                            disabled={agentBusy}
                            onOpen={(id) => {
                                setLocalActiveSessionId(id);
                                setView("chat");
                            }}
                            onDelete={(id) => setDeleteChatIds([id])}
                        />
                    ) : view === "log" ? (
                        <OnlineAgentLogView logs={onlineLogs} theme={theme} context={{ model: activeModel, running: agentBusy, confirmTools, messages: messages.length, nodes: snapshot.nodes.length, connections: snapshot.connections.length }} onClear={() => setOnlineLogs([])} />
                    ) : messages.length ? (
                        <>
                            {messages.map((message) => (
                                <div key={message.id} className="space-y-2">
                                    <AgentChatMessage item={assistantMessageToChatMessage(message)} theme={theme} user={user} onRejectTool={rejectOnlineTool} onApproveTool={approveOnlineTool} />
                                    {message.references?.length ? <MessageReferences message={message} /> : null}
                                </div>
                            ))}
                            {agentWorking ? <AgentWorkingMessage theme={theme} /> : null}
                        </>
                    ) : (
                        <div className="flex h-full flex-col items-center justify-center px-3 text-center">
                            <span className="grid size-11 place-items-center rounded-lg border" style={{ background: theme.accent.primarySoft, borderColor: theme.spatial.glowStrong, color: theme.accent.primary }}><Bot className="size-5" /></span>
                            <div className="mt-3 text-sm font-semibold" style={{ color: theme.node.text }}>Agent</div>
                            <div className="mt-5 grid w-full max-w-[360px] grid-cols-2 gap-2">
                                {["搭建短剧工作流", "整理当前画布", "生成镜头分镜", "检查节点连线"].map((suggestion) => (
                                    <button key={suggestion} type="button" className="h-9 rounded-md border px-2 text-xs font-medium transition hover:-translate-y-0.5" style={{ background: theme.spatial.surface, borderColor: theme.toolbar.border, color: theme.node.text }} onClick={() => setPrompt(suggestion)}>{suggestion}</button>
                                ))}
                            </div>
                        </div>
                    )}
                </div>
            )}

            {view === "chat" ? (
                <>
                    {selectedReferences.length ? (
                        <div className="thin-scrollbar flex max-w-full gap-1.5 overflow-x-auto px-3 pb-1">
                            {selectedReferences.map((item, index) => (
                                <AssistantReferenceChip
                                    key={item.id}
                                    item={item}
                                    label={assistantImageReferenceLabel(selectedReferences, index)}
                                    onRemove={() => {
                                        setRemovedReferenceIds((prev) => new Set(prev).add(item.id));
                                        if (selectedNodeIds.has(item.id)) onSelectNodeIds(new Set(Array.from(selectedNodeIds).filter((nodeId) => nodeId !== item.id)));
                                    }}
                                />
                            ))}
                        </div>
                    ) : null}
                    <AgentChatComposer
                        prompt={prompt}
                        sending={agentBusy}
                        placeholder={cinematicEntryActive ? "一句话描述题材、角色和核心冲突" : "描述你想让 Agent 如何操作画布"}
                        theme={theme}
                        onPromptChange={setPrompt}
                        onSubmit={cinematicEntryActive ? () => submitCinematicProject(prompt) : submit}
                        onStop={activeOnlineRequest ? () => void stopOnlineAgentRequest() : undefined}
                        onAddFiles={addImagesToCanvas}
                        left={
                            <>
                                <AgentTextModelPicker config={effectiveConfig} value={effectiveConfig.textModel} disabled={agentBusy} onChange={(model) => updateConfig("textModel", model)} />
                                {cinematicEntryActive ? <span className="ml-2 inline-flex h-6 items-center rounded-md border px-2 text-[10px] font-medium" style={{ borderColor: theme.node.stroke, color: theme.node.muted }}>影视项目</span> : null}
                            </>
                        }
                    />
                </>
            ) : null}

            <Modal
                title="删除对话记录？"
                open={deleteChatIds.length > 0}
                centered
                onCancel={() => setDeleteChatIds([])}
                footer={
                    <>
                        <Button onClick={() => setDeleteChatIds([])}>取消</Button>
                        <Button
                            danger
                            type="primary"
                            onClick={() => {
                                deleteChatIds.length === historySessions.length ? clearSessions() : removeSessions(deleteChatIds);
                                setDeleteChatIds([]);
                            }}
                        >
                            删除
                        </Button>
                    </>
                }
            >
                <p className="text-sm opacity-60">将删除 {deleteChatIds.length} 条对话记录，此操作不可撤销。</p>
            </Modal>
        </>
    );

    return (
        <motion.div
            className="flex shrink-0"
            initial={{ width: 0, opacity: 0 }}
            animate={{ width: closing ? 0 : width + 9, opacity: closing ? 0 : 1 }}
            transition={{ duration: resizing ? 0 : PANEL_MOTION_SECONDS, ease: [0.22, 1, 0.36, 1] }}
            style={{ overflow: "clip", pointerEvents: closing ? "none" : undefined }}
        >
            <motion.aside
                className="relative my-2 mr-2 flex shrink-0 flex-col overflow-hidden rounded-lg border"
                initial={{ x: 48 }}
                animate={{ x: closing ? 28 : 0 }}
                transition={{ duration: resizing ? 0 : PANEL_MOTION_SECONDS, ease: [0.22, 1, 0.36, 1] }}
                style={{ width, background: theme.spatial.elevated, borderColor: theme.node.stroke, color: theme.node.text, boxShadow: `0 24px 72px ${theme.spatial.shadow}` }}
            >
                <button type="button" className="absolute inset-y-0 left-0 z-40 w-4 -translate-x-1/2 cursor-col-resize" onMouseDown={startResize} aria-label="调整右侧面板宽度" />
                <header className="flex h-14 items-center justify-between border-b px-4" style={{ borderColor: theme.node.stroke }}>
                    <div className="flex min-w-0 items-center gap-2">
                        <span className="grid size-8 place-items-center rounded-md" style={{ background: theme.accent.primarySoft, color: theme.accent.primary }}>
                            <Bot className="size-4" />
                        </span>
                        <div className="min-w-0">
                            <div className="text-base font-semibold leading-5">Agent</div>
                            <div className="truncate text-xs" style={{ color: theme.node.muted }}>
                                画布助手
                            </div>
                        </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                        {activeOnlineRequest ? (
                            <Tooltip title="停止等待；供应商可能仍在执行并计费">
                                <Button danger size="small" className="!h-8 !px-2" icon={<Square className="size-3 fill-current" />} onClick={() => void stopOnlineAgentRequest()}>
                                    停止
                                </Button>
                            </Tooltip>
                        ) : null}
                        {agentMode === "online" ? <Button type="text" shape="circle" className="!h-8 !w-8 !min-w-8" disabled={!canUndoOps || agentBusy} icon={<RotateCcw className="size-3.5" />} onClick={undoLastOnlineBatch} aria-label="撤销最近一批 Agent 写回" title={undoOpsCount ? `可撤销最近 ${undoOpsCount} 批` : "没有可撤销的 Agent 写回"} /> : null}
                        <AgentModeSwitch value={agentMode} theme={theme} disabled={agentBusy} onChange={onAgentModeChange} />
                        <label className="flex items-center gap-1.5 text-xs" style={{ color: theme.node.muted }}>
                            <Switch size="small" checked={confirmTools} disabled={agentBusy} onChange={(confirmTools) => setAgentState({ confirmTools })} />
                            工具确认
                        </label>
                        <Tooltip title="收起对话">
                            <Button type="text" shape="circle" className="!h-8 !w-8 !min-w-8" style={iconButtonStyle} icon={<PanelRightClose className="size-4" />} onClick={collapse} />
                        </Tooltip>
                    </div>
                </header>
                <div className="mx-3 mt-2 flex min-h-8 flex-wrap items-center gap-x-2 gap-y-1 rounded-md border px-2.5 py-1.5 text-[10px]" style={{ borderColor: theme.node.stroke, background: theme.spatial.surface, color: theme.node.muted }}>
                    <span className="font-semibold" style={{ color: theme.node.text }}>将读取</span>
                    <span>当前画布 {contextSummary.nodeCount} 个节点</span>
                    {contextSummary.selectedCount ? <span className="inline-flex items-center gap-1"><Focus className="size-3" />选区 {contextSummary.selectedCount} 个</span> : null}
                    {contextSummary.chapterLabel ? <span className="inline-flex min-w-0 items-center gap-1"><BookOpenText className="size-3 shrink-0" /><span className="max-w-32 truncate">{contextSummary.chapterLabel}</span>{contextSummary.shotLabel ? ` · ${contextSummary.shotLabel}` : ""}</span> : null}
                    {selectedReferences.length ? <span>{selectedReferences.length} 个参考节点</span> : null}
                    {undoOpsCount ? <span className="ml-auto tabular-nums">可撤销 {undoOpsCount} 批</span> : null}
                </div>
                {agentMode === "local" ? (
                    <CanvasLocalAgentPanel
                        embedded
                        snapshot={snapshot}
                        canUndoOps={canUndoOps}
                        undoOpsCount={undoOpsCount}
                        onApplyOps={onApplyOps}
                        onUndoOps={onUndoOps}
                        autoConnect={autoConnectLocal}
                    />
                ) : (
                    onlineContent
                )}
            </motion.aside>
        </motion.div>
    );
}

function AgentTextModelPicker({ config, value, disabled, onChange }: { config: AiConfig; value: string; disabled?: boolean; onChange: (model: string) => void }) {
    const options = useMemo(() => Array.from(new Set([value, ...selectableModelsByCapability(config, "text")].filter(Boolean))), [config, value]);
    const [probeRevision, setProbeRevision] = useState(0);
    const current = value || "";
    const requestConfig = useMemo(() => resolveModelRequestConfig(config, current), [config, current]);
    const toolChoiceReason = current ? onlineAgentToolChoiceReason(requestConfig.model, requestConfig.interfaceType, "required") : "";
    const requestChannel = current ? channelForResolvedRequest(requestConfig) : undefined;
    const probeModels = useMemo(() => {
        if (!current || !requestChannel) return undefined;
        const currentModel = modelOptionName(requestConfig.model);
        const models = channelProbeModels(requestChannel);
        return [...models.filter((item) => item.model === currentModel), ...models.filter((item) => item.model !== currentModel)];
    }, [current, requestChannel, requestConfig.model]);
    const probeReadiness = useMemo(
        () => current && requestChannel
            ? resolveChannelProbeReadiness(requestChannel, modelOptionName(requestConfig.model) || requestConfig.model, requestConfig.interfaceType)
            : undefined,
        [current, requestChannel, requestConfig.interfaceType, requestConfig.model, probeRevision],
    );
    const probeMismatchReason = probeReadiness?.nearbyProbe ? `最近一次测活的是 ${probeReadiness.nearbyProbe.model}（${probeReadiness.nearbyProbe.protocol}），当前 Agent 使用 ${modelOptionName(requestConfig.model)}（${requestConfig.interfaceType || "未声明协议"}）；管理员可重新测活当前模型` : "";
    const toolProbeStatus = probeReadiness?.toolCalling;
    const toolProbeLabel = toolProbeStatus === "supported" ? "工具通过" : toolProbeStatus === "failed" ? "工具未通过" : toolProbeStatus === "stale" ? "工具需重测" : "需测工具";
    const toolProbeReason = toolProbeStatus === "supported"
        ? "最近一次无副作用工具诊断已通过，创作台可以继续验证具体工具参数。"
        : toolProbeStatus === "failed"
            ? "最近一次工具诊断未通过；测活只供管理员诊断，本次仍可尝试，工具能否执行以真实模型响应为准。"
            : toolProbeStatus === "stale"
                ? "工具诊断已过期，请打开测活窗口重新运行“测试工具调用”。"
                : "文本测活不等于 Function Calling；短 Agent 仍会在明确预算下尝试，没有工具结论时可能直接停止。管理员可在同一窗口点击“测试工具调用”更新诊断。";
    return (
        <div className="flex min-w-0 max-w-[320px] items-center gap-1" onMouseDown={(event) => event.stopPropagation()} onPointerDown={(event) => event.stopPropagation()}>
            <div className="min-w-0 flex-1">
                <Select<string>
                    size="small"
                    variant="borderless"
                    value={current || undefined}
                    disabled={disabled}
                    className="agent-text-model-select w-full"
                    popupMatchSelectWidth={288}
                    options={options.map((model) => ({ value: model, label: `${modelDisplayName(config, model)} ${modelOptionName(model)} ${resolveModelChannel(config, model).name}` }))}
                    notFoundContent={<span className="block py-2 text-center text-xs text-foreground/48">暂无文本模型</span>}
                    optionRender={(option) => {
                        const model = String(option.value);
                        return <span className="flex min-w-0 items-center gap-2"><AgentModelIcon model={model} /><span className="min-w-0 flex-1"><span className="block truncate">{modelDisplayName(config, model)}</span><span className="block truncate text-[10px] opacity-45">{modelOptionName(model)}</span></span><span className="shrink-0 text-xs opacity-55">{resolveModelChannel(config, model).name}</span></span>;
                    }}
                    labelRender={() => <span className="flex min-w-0 items-center gap-1.5"><AgentModelIcon model={current} /><span className="min-w-0 truncate">{current ? modelDisplayName(config, current) : "选择文本模型"}</span>{current ? <span className="shrink-0 opacity-55">{resolveModelChannel(config, current).name}</span> : null}{toolChoiceReason ? <Tooltip title={toolChoiceReason}><span className="shrink-0 rounded border border-amber-500/30 px-1 text-[9px] text-amber-600">工具兼容</span></Tooltip> : null}{current && toolProbeStatus ? <Tooltip title={toolProbeReason}><span className={`shrink-0 rounded border px-1 text-[9px] ${toolProbeStatus === "supported" ? "border-emerald-500/30 text-emerald-600" : toolProbeStatus === "failed" ? "border-red-500/30 text-red-600" : "border-amber-500/30 text-amber-600"}`}>{toolProbeLabel}</span></Tooltip> : null}{probeMismatchReason ? <Tooltip title={probeMismatchReason}><span className="shrink-0 rounded border border-red-500/30 px-1 text-[9px] text-red-600">需重测</span></Tooltip> : null}</span>}
                    onChange={onChange}
                    aria-label="选择 Agent 文本模型"
                    title={current ? `${modelDisplayName(config, current)} · ${modelOptionName(current)} · ${resolveModelChannel(config, current).name}` : "选择文本模型"}
                />
            </div>
            {current && requestChannel ? <ChannelProbeButton channel={requestChannel} models={probeModels} label="测活" size="small" className="shrink-0" disabled={disabled} onCompleted={() => setProbeRevision((revision) => revision + 1)} onToolCompleted={() => setProbeRevision((revision) => revision + 1)} /> : null}
        </div>
    );
}

function AgentModelIcon({ model }: { model: string }) {
    const icon = resolveModelIcon(modelOptionName(model));
    return icon ? <img src={icon} alt="" className="size-4 shrink-0 dark:invert" /> : <Cpu className="size-4 shrink-0 opacity-70" />;
}

function resolveModelIcon(model: string) {
    // 与模型选择器保持同一套厂商图标规则。
    const name = model.toLowerCase();
    if (name.includes("claude") || name.includes("anthropic")) return "/icons/claude.svg";
    if (
        name.includes("gemini") ||
        name.includes("google") ||
        name.includes("nano banana") ||
        name.includes("nanobanana") ||
        name.includes("imagen") ||
        name.includes("veo") ||
        name.includes("omni flash") ||
        name.includes("omni-flash")
    ) {
        return "/icons/gemini.svg";
    }
    if (name.includes("gpt") || name.includes("openai") || name.includes("dall-e") || name.includes("dalle")) return "/icons/openai.svg";
    if (name.includes("grok")) return "/icons/grok.svg";
    if (name.includes("deepseek")) return "/icons/deepseek.svg";
    if (name.includes("glm") || name.includes("chatglm")) return "/icons/glm.svg";
    return "";
}

function AssistantHistory({
    sessions,
    activeSession,
    disabled,
    onOpen,
    onDelete,
}: {
    sessions: CanvasAssistantSession[];
    activeSession: CanvasAssistantSession | null;
    disabled?: boolean;
    onOpen: (id: string) => void;
    onDelete: (id: string) => void;
}) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];

    return (
        <div className="space-y-3">
            <div className="text-sm" style={{ color: theme.node.muted }}>
                {sessions.length ? `${sessions.length} 条历史` : "暂无历史"}
            </div>
            {sessions.map((session) => (
                <div key={session.id} className="rounded-lg border px-2.5 py-1.5 transition" style={{ borderColor: session.id === activeSession?.id ? theme.node.text : theme.node.stroke, background: "transparent", color: theme.node.text }}>
                    <div className="flex items-center gap-2">
                        <div className="min-w-0 flex-1">
                            <div className="flex min-w-0 items-center gap-1.5">
                                {session.id === activeSession?.id ? <span className="shrink-0 text-[10px] font-medium" style={{ color: theme.node.text }}>当前</span> : null}
                                <div className="truncate text-sm font-medium leading-5">{session.title}</div>
                            </div>
                            <div className="truncate text-[11px] leading-4 opacity-65">{sessionPreview(session)}</div>
                        </div>
                        <div className="flex shrink-0 items-center gap-1">
                            <span className="text-[10px] opacity-55">{formatSessionTime(session.updatedAt || session.createdAt)}</span>
                            <Button size="small" className="!h-6 !px-2" onClick={() => onOpen(session.id)}>
                                进入
                            </Button>
                            <Tooltip title="删除记录">
                                <Button size="small" danger type="text" className="!h-6 !w-6 !min-w-6" disabled={disabled} icon={<Trash2 className="size-3.5" />} onClick={() => onDelete(session.id)} />
                            </Tooltip>
                        </div>
                    </div>
                </div>
            ))}
            {!sessions.length ? (
                <div className="px-3 py-8 text-center text-sm" style={{ color: theme.node.muted }}>
                    网站 Agent 的对话记录会显示在这里
                </div>
            ) : null}
        </div>
    );
}

function OnlineAgentSetupView({ theme, activeModel, disabled, onOpenConfig }: { theme: (typeof canvasThemes)[keyof typeof canvasThemes]; activeModel: string; disabled?: boolean; onOpenConfig: () => void }) {
    return (
        <div className="thin-scrollbar min-h-0 flex-1 overflow-y-auto p-4">
            <div className="space-y-4">
                <div>
                    <div className="text-base font-semibold leading-6">连接配置</div>
                    <div className="mt-1 text-xs leading-5" style={{ color: theme.node.muted }}>
                        网站 Agent 直接使用当前网页配置的文本模型和 API。
                    </div>
                </div>
                <div className="rounded-lg border p-3" style={{ borderColor: theme.node.stroke }}>
                    <div className="flex flex-wrap items-start justify-between gap-3">
                        <div className="min-w-0 flex-1">
                            <div className="text-sm font-medium leading-5">文本模型</div>
                            <div className="mt-1 truncate text-xs leading-5" style={{ color: theme.node.muted }}>
                                {activeModel || "未配置模型"}
                            </div>
                        </div>
                        <Button className="!h-8 !px-3" type="primary" disabled={disabled} icon={<Settings2 className="size-4" />} onClick={onOpenConfig}>
                            配置
                        </Button>
                    </div>
                </div>
            </div>
        </div>
    );
}

function OnlineAgentLogView({ logs, theme, context, onClear }: { logs: OnlineAgentLog[]; theme: (typeof canvasThemes)[keyof typeof canvasThemes]; context: OnlineAgentLogContext; onClear: () => void }) {
    const [mode, setMode] = useState<"text" | "json">("text");
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const content = mode === "text" ? formatOnlineLogText(logs, context) : formatOnlineLogJson(logs, context);
    const lastError = [...logs].reverse().find((item) => /错误|失败|error/i.test(`${item.title}\n${stringifyLog(item.data)}`));
    const copy = async (value = content) => {
        if (await copyToClipboard(value)) return;
        textareaRef.current?.focus();
        textareaRef.current?.select();
    };
    return (
        <div className="flex min-h-full flex-col gap-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
                <Segmented size="small" value={mode} onChange={(value) => setMode(value as "text" | "json")} options={[{ label: "排查日志", value: "text" }, { label: "原始 JSON", value: "json" }]} />
                <div className="flex items-center gap-2">
                    <span className="text-xs" style={{ color: theme.node.muted }}>{logs.length} 条</span>
                    <Button size="small" icon={<Copy className="size-3.5" />} disabled={!logs.length} onClick={() => void copy()}>复制</Button>
                    <Button size="small" disabled={!lastError} onClick={() => lastError && void copy(formatOnlineLogText([lastError], context))}>最近错误</Button>
                    <Button size="small" danger type="text" icon={<Trash2 className="size-3.5" />} disabled={!logs.length} onClick={onClear}>清空</Button>
                </div>
            </div>
            <textarea
                ref={textareaRef}
                readOnly
                value={content}
                className="thin-scrollbar min-h-[360px] flex-1 resize-none rounded-lg border bg-transparent p-3 font-mono text-xs leading-5 outline-none"
                style={{ borderColor: theme.node.stroke, color: theme.node.text }}
                onFocus={(event) => event.currentTarget.select()}
            />
        </div>
    );
}

function MessageReferences({ message }: { message: CanvasAssistantMessage }) {
    return (
        <div className={`flex max-w-[88%] flex-wrap gap-2 ${message.role === "user" ? "ml-auto justify-end" : "ml-11 justify-start"}`}>
            {message.references?.map((item, index, references) => (
                <AssistantReferenceChip key={item.id} item={item} label={assistantImageReferenceLabel(references, index)} />
            ))}
        </div>
    );
}

function AssistantReferenceChip({ item, label, onRemove }: { item: CanvasAssistantReference; label?: string; onRemove?: () => void }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const text = (item.text || item.title).replace(/\s+/g, " ").trim().slice(0, 1) || "文";
    return (
        <div className="group/chip relative inline-flex h-8 max-w-[150px] shrink-0 items-center gap-1.5 rounded-lg text-sm" style={{ color: theme.node.text }}>
            {item.dataUrl ? (
                <span className="relative block size-8 shrink-0">
                    <img src={item.dataUrl} alt="" className="size-8 rounded-lg object-cover" />
                    {label ? <span className="absolute left-0.5 top-0.5 rounded bg-black/60 px-1 py-0.5 text-[8px] font-medium leading-none text-white">{label}</span> : null}
                </span>
            ) : (
                <span className="grid size-8 place-items-center rounded-lg border text-sm font-medium" style={{ background: theme.node.panel, borderColor: theme.node.activeStroke }}>
                    {text}
                </span>
            )}
            {onRemove ? (
                <button
                    type="button"
                    className="absolute -right-1 -top-1 grid size-4 place-items-center rounded-full border opacity-0 shadow-sm transition group-hover/chip:opacity-100"
                    style={{ background: theme.toolbar.panel, borderColor: theme.node.stroke }}
                    onClick={onRemove}
                    aria-label="移除引用"
                >
                    <X className="size-3" />
                </button>
            ) : null}
        </div>
    );
}

function assistantImageReferenceLabel(references: CanvasAssistantReference[], index: number) {
    if (!references[index]?.dataUrl) return undefined;
    const imageIndex = references.slice(0, index + 1).filter((item) => item.dataUrl).length - 1;
    return imageIndex >= 0 ? imageReferenceLabel(imageIndex) : undefined;
}

function assistantMessageToChatMessage(message: CanvasAssistantMessage): CanvasAgentChatMessage {
    return { id: message.id, role: message.role, title: message.title, text: message.text, meta: message.meta, detail: message.detail };
}

function formatSessionTime(value?: string) {
    return value ? new Date(value).toLocaleString() : "";
}

function sessionPreview(session: CanvasAssistantSession) {
    return session.messages.at(-1)?.text || `${session.messages.length} 条消息`;
}

function objectDetail(value: unknown) {
    return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
}

function stringifyLog(value: unknown) {
    return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

function formatOnlineLogText(logs: OnlineAgentLog[], context: OnlineAgentLogContext) {
    const head = [
        "明想 MingWant Studio 网站 Agent 诊断日志",
        `model: ${context.model || "none"}`,
        `running: ${context.running}`,
        `confirmTools: ${context.confirmTools}`,
        `messages: ${context.messages}`,
        `nodes: ${context.nodes}`,
        `connections: ${context.connections}`,
        `logs: ${logs.length}`,
    ].join("\n");
    const body = logs.map((log, index) => [`#${index + 1} ${log.time} ${log.title}`, log.data === undefined ? "" : stringifyLog(log.data)].filter(Boolean).join("\n")).join("\n\n---\n\n");
    return [head, body || "暂无事件日志"].join("\n\n");
}

function formatOnlineLogJson(logs: OnlineAgentLog[], context: OnlineAgentLogContext) {
    return JSON.stringify({ context, logs: logs.map(({ time, title, data }) => ({ time, title, data })) }, null, 2);
}

function describeCanvasSnapshot(snapshot: CanvasAgentSnapshot) {
    const counts = snapshot.nodes.reduce<Record<string, number>>((acc, node) => {
        acc[node.type] = (acc[node.type] || 0) + 1;
        return acc;
    }, {});
    return `当前画布有 ${snapshot.nodes.length} 个节点、${snapshot.connections.length} 条连线。背板 ${counts[CanvasNodeType.Frame] || 0} 个，文本 ${counts[CanvasNodeType.Text] || 0} 个，绘图 ${counts[CanvasNodeType.Drawing] || 0} 个，分镜脚本 ${counts[CanvasNodeType.Script] || 0} 个，技能 ${counts[CanvasNodeType.Skill] || 0} 个，图片 ${counts[CanvasNodeType.Image] || 0} 个，生成配置 ${counts[CanvasNodeType.Config] || 0} 个，视频 ${counts[CanvasNodeType.Video] || 0} 个，音频 ${counts[CanvasNodeType.Audio] || 0} 个。`;
}

function parseToolArguments(value: string) {
    try {
        const parsed = JSON.parse(value || "{}");
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("工具参数必须是 JSON 对象");
        return parsed as Record<string, unknown>;
    } catch {
        throw new Error("工具参数不是合法 JSON 对象");
    }
}

function onlineToolToOps(name: string, input: Record<string, unknown>, snapshot: CanvasAgentSnapshot, config: AiConfig): CanvasAgentOp[] {
    if (name === "canvas_apply_ops") return requireOps(input.ops);
    if (name === "canvas_create_node") {
        const nodeType = requireNodeType(input.nodeType);
        const x = numberOr(input.x, nextCanvasX(snapshot));
        const y = numberOr(input.y, 0);
        const metadata = recordOptional(input.metadata);
        if (nodeType === CanvasNodeType.Config) return [configNodeOp(stringOptional(input.id) || `config-${nanoid()}`, { ...metadata, ...input }, x, y, config)];
        const nodeMetadata = nodeType === CanvasNodeType.Script ? normalizeAgentStoryboardMetadata(metadata) : metadata as CanvasNodeData["metadata"];
        return [{ type: "add_node", nodeType, title: stringOptional(input.title), position: { x, y }, width: numberOptional(input.width), height: numberOptional(input.height), metadata: nodeMetadata }];
    }
    if (name === "canvas_create_text_node") return [textNodeOp(input, numberOr(input.x, nextCanvasX(snapshot)), numberOr(input.y, 0))];
    if (name === "canvas_create_text_nodes") {
        const items = requireRecordArray(input.items, "items");
        const x = numberOr(input.x, nextCanvasX(snapshot));
        const y = numberOr(input.y, 0);
        const gap = numberOr(input.gap, 40);
        const direction = input.direction === "row" ? "row" : "column";
        return items.map((item, index) => textNodeOp({ ...item, text: requireString(item.text, "text") }, numberOr(item.x, direction === "row" ? x + index * (NODE_DEFAULT_SIZE[CanvasNodeType.Text].width + gap) : x), numberOr(item.y, direction === "row" ? y : y + index * (NODE_DEFAULT_SIZE[CanvasNodeType.Text].height + gap))));
    }
    if (name === "canvas_create_image_prompt_flow") return generationFlowOps({ ...input, mode: "image" }, snapshot, config);
    if (name === "canvas_create_config_node") {
        const configId = `config-${nanoid()}`;
        const mode = generationMode(input.mode);
        return [configNodeOp(configId, input, numberOr(input.x, nextCanvasX(snapshot)), numberOr(input.y, 0), config), ...(input.autoRun ? [runGenerationOp(configId, mode, stringOptional(input.prompt))] : [])];
    }
    if (name === "canvas_create_generation_flow") return generationFlowOps(input, snapshot, config);
    if (name === "canvas_generate_text") return generationFlowOps({ ...input, mode: "text", autoRun: true }, snapshot, config);
    if (name === "canvas_generate_image") return generationFlowOps({ ...input, mode: "image", autoRun: true }, snapshot, config);
    if (name === "canvas_generate_video") return generationFlowOps({ ...input, mode: "video", autoRun: true }, snapshot, config);
    if (name === "canvas_generate_audio") return generationFlowOps({ ...input, mode: "audio", autoRun: true }, snapshot, config);
    if (name === "canvas_update_node") return [{ type: "update_node", id: requireString(input.id, "id"), patch: recordOptional(input.patch) as Partial<CanvasNodeData> | undefined, metadata: recordOptional(input.metadata) as CanvasNodeData["metadata"] }];
    if (name === "canvas_update_node_text") return [{ type: "update_node", id: requireString(input.id, "id"), patch: stringOptional(input.title) ? { title: stringOptional(input.title) } : undefined, metadata: { content: requireString(input.text, "text"), status: "success" } }];
    if (name === "canvas_move_nodes") {
        return requireRecordArray(input.items, "items").map((item) => {
            const id = requireString(item.id, "id");
            const current = snapshot.nodes.find((node) => node.id === id);
            return { type: "update_node", id, patch: { position: { x: numberOr(item.x, (current?.position.x || 0) + numberOr(item.dx, 0)), y: numberOr(item.y, (current?.position.y || 0) + numberOr(item.dy, 0)) } } };
        });
    }
    if (name === "canvas_resize_node") return [{ type: "update_node", id: requireString(input.id, "id"), patch: { width: requireNumber(input.width, "width"), height: requireNumber(input.height, "height") }, metadata: typeof input.freeResize === "boolean" ? { freeResize: input.freeResize } : undefined }];
    if (name === "canvas_delete_nodes") return [{ type: "delete_node", ids: requireStringArray(input.ids, "ids") }];
    if (name === "canvas_connect_nodes") return requireRecordArray(input.connections, "connections").map((connection) => ({ type: "connect_nodes", fromNodeId: requireString(connection.fromNodeId, "fromNodeId"), toNodeId: requireString(connection.toNodeId, "toNodeId") }));
    if (name === "canvas_select_nodes") return [{ type: "select_nodes", ids: requireStringArray(input.ids, "ids") }];
    if (name === "canvas_set_viewport") return [{ type: "set_viewport", viewport: requireViewport(input.viewport) }];
    if (name === "canvas_run_generation") return [runGenerationOp(requireString(input.nodeId, "nodeId"), generationMode(input.mode), stringOptional(input.prompt))];
    if (name === "canvas_edit_image_annotation") return [{ type: "run_image_annotation", annotationNodeId: requireString(input.annotationNodeId, "annotationNodeId"), prompt: stringOptional(input.prompt) }];
    throw new Error(`不支持的工具：${name}`);
}

function generationFlowOps(input: Record<string, unknown>, snapshot: CanvasAgentSnapshot, config: AiConfig): CanvasAgentOp[] {
    const mode = generationMode(input.mode);
    const prompt = requireString(input.prompt, "prompt");
    const x = numberOr(input.x, nextCanvasX(snapshot));
    const y = numberOr(input.y, 0);
    const textId = `text-${nanoid()}`;
    const configId = `config-${nanoid()}`;
    const referenceNodeIds = Array.isArray(input.referenceNodeIds) ? input.referenceNodeIds.filter((id): id is string => typeof id === "string") : [];
    const tokens = [`@[node:${textId}]`, ...referenceNodeIds.map((id) => `@[node:${id}]`)];
    return [
        textNodeOp({ id: textId, text: prompt, title: stringOptional(input.title) || "提示词" }, x, y),
        configNodeOp(configId, { ...input, prompt: tokens.join("\n") }, x + NODE_DEFAULT_SIZE[CanvasNodeType.Text].width + 80, y, config),
        { type: "connect_nodes", fromNodeId: textId, toNodeId: configId },
        ...referenceNodeIds.map((fromNodeId) => ({ type: "connect_nodes" as const, fromNodeId, toNodeId: configId })),
        { type: "select_nodes", ids: [configId] },
        ...(input.autoRun ? [runGenerationOp(configId, mode, tokens.join("\n"))] : []),
    ];
}

function textNodeOp(input: Record<string, unknown>, x: number, y: number): CanvasAgentOp {
    return { type: "add_node", id: stringOptional(input.id), nodeType: CanvasNodeType.Text, title: stringOptional(input.title), position: { x, y }, width: numberOptional(input.width), height: numberOptional(input.height), metadata: { content: stringOptional(input.text), status: "success", fontSize: 14 } };
}

function configNodeOp(id: string, input: Record<string, unknown>, x: number, y: number, config: AiConfig): CanvasAgentOp {
    const mode = generationMode(input.mode);
    const prompt = stringOptional(input.prompt);
    return {
        type: "add_node",
        id,
        nodeType: CanvasNodeType.Config,
        title: stringOptional(input.title) || generationTitle(mode),
        position: { x, y },
        width: numberOptional(input.width),
        height: numberOptional(input.height),
        metadata: cleanRecord({
            generationMode: mode,
            composerContent: prompt,
            prompt,
            status: "idle",
            model: resolveGenerationModel(config, mode, stringOptional(input.model)),
            size: stringOptional(input.size) || config.size,
            quality: stringOptional(input.quality) || config.quality,
            transparentBackground: stringOptional(input.transparentBackground) || config.transparentBackground,
            count: numberOptional(input.count) ?? generationCount(mode === "image" ? config.canvasImageCount || config.count : config.count),
            seconds: stringOptional(input.seconds) || config.videoSeconds,
            vquality: stringOptional(input.vquality) || config.vquality,
            generateAudio: stringOptional(input.generateAudio) || config.videoGenerateAudio,
            watermark: stringOptional(input.watermark) || config.videoWatermark,
            audioVoice: stringOptional(input.audioVoice) || config.audioVoice,
            audioFormat: stringOptional(input.audioFormat) || config.audioFormat,
            audioSpeed: stringOptional(input.audioSpeed) || config.audioSpeed,
            audioInstructions: stringOptional(input.audioInstructions) || config.audioInstructions,
        }) as CanvasNodeData["metadata"],
    };
}

function runGenerationOp(nodeId: string, mode: "text" | "image" | "video" | "audio", prompt?: string): CanvasAgentOp {
    return { type: "run_generation", nodeId, mode, prompt };
}

function isWritableToolCall(call: ResponseToolCall) {
    return !ONLINE_READ_TOOLS.has(call.function.name);
}

function requiresExplicitToolConfirmation(calls: ResponseToolCall[]) {
    return calls.some((call) => call.function.name === "canvas_create_cinematic_session");
}

function toolCallsFromDetail(detail: Record<string, unknown>): ResponseToolCall[] {
    return Array.isArray(detail.toolCalls) ? (detail.toolCalls.filter(isResponseToolCall) as ResponseToolCall[]) : [];
}

function isResponseToolCall(value: unknown): value is ResponseToolCall {
    const item = objectDetail(value);
    const fn = objectDetail(item.function);
    return typeof item.id === "string" && item.type === "function" && typeof fn.name === "string" && typeof fn.arguments === "string";
}

function toolCallToResponseInput(call: ResponseToolCall, reasoningContent?: string, assistantContent?: string): ResponseInputMessage {
    return {
        type: "function_call",
        call_id: call.id,
        name: call.function.name,
        arguments: call.function.arguments,
        ...(call.thoughtSignature ? { thoughtSignature: call.thoughtSignature } : {}),
        ...(reasoningContent ? { reasoningContent } : {}),
        ...(typeof assistantContent === "string" ? { assistantContent } : {}),
    };
}

function summarizeToolCalls(calls: ResponseToolCall[]) {
    return calls.map((call) => toolCallLabel(call.function.name)).join("，") || "工具调用";
}

function previewOnlineToolCalls(calls: ResponseToolCall[], snapshot: CanvasAgentSnapshot, config: AiConfig): CanvasAgentOperationImpact {
    const ops: CanvasAgentOp[] = [];
    let deferredCinematicCount = 0;
    calls.filter(isWritableToolCall).forEach((call) => {
        if (call.function.name === "canvas_create_cinematic_session") {
            deferredCinematicCount += 1;
            return;
        }
        try {
            ops.push(...onlineToolToOps(call.function.name, parseToolArguments(call.function.arguments), snapshot, config));
        } catch {
            // 参数错误会在真正执行时显式失败；预览阶段只展示可确定的影响。
        }
    });
    const impact = previewCanvasAgentOps(ops, snapshot);
    if (!deferredCinematicCount) return impact;
    return {
        ...impact,
        operationCount: impact.operationCount + deferredCinematicCount,
        items: [...impact.items, "启动影视 Agent，会话完成后将剧本、分镜和生成节点写回当前画布"].slice(0, 8),
        warning: [impact.warning, "影视 Agent 先发起 1 次文本请求；结构校验失败时，真正创建会话前还会单独询问是否允许最多 1 次修复请求。自定义 API Key 可能因此产生两次供应商费用，具体写回范围将在后端完成拆解后确定。"].filter(Boolean).join(" "),
    };
}

function toolCallLabel(name: string) {
    if (name === "canvas_apply_ops") return "画布操作";
    if (name === "canvas_get_state") return "读取画布";
    if (name === "canvas_get_selection") return "读取选区";
    if (name === "canvas_export_snapshot") return "导出快照";
    if (name === "canvas_create_cinematic_session") return "创建影视项目";
    if (name === "canvas_create_node") return "创建节点";
    if (name === "canvas_create_text_node") return "创建文本";
    if (name === "canvas_create_text_nodes") return "批量创建文本";
    if (name === "canvas_create_config_node") return "创建生成配置";
    if (name === "canvas_create_image_prompt_flow") return "创建生图流程";
    if (name === "canvas_create_generation_flow") return "创建生成流程";
    if (name === "canvas_generate_text") return "生成文本";
    if (name === "canvas_generate_image") return "生成图片";
    if (name === "canvas_generate_video") return "生成视频";
    if (name === "canvas_generate_audio") return "生成音频";
    if (name === "canvas_update_node") return "更新节点";
    if (name === "canvas_update_node_text") return "更新文本";
    if (name === "canvas_move_nodes") return "移动节点";
    if (name === "canvas_resize_node") return "调整节点尺寸";
    if (name === "canvas_delete_nodes") return "删除节点";
    if (name === "canvas_connect_nodes") return "连接节点";
    if (name === "canvas_select_nodes") return "选择节点";
    if (name === "canvas_set_viewport") return "调整视口";
    if (name === "canvas_run_generation") return "触发生成";
    if (name === "canvas_get_image_annotations") return "读取图片标注";
    if (name === "canvas_edit_image_annotation") return "执行标注改图";
    return name;
}

function toolResultText(result: OnlineToolResult) {
    return result.message;
}

function requireStringArray(value: unknown, field: string): string[] {
    if (!Array.isArray(value)) throw new Error(`${field} 必须是字符串数组`);
    if (!value.every((item) => typeof item === "string" && Boolean(item))) throw new Error(`${field} 必须只包含非空字符串`);
    return value as string[];
}

function requireOps(value: unknown): CanvasAgentOp[] {
    if (!Array.isArray(value)) throw new Error("ops 必须是数组");
    return value.map(toCanvasAgentOp);
}

function toCanvasAgentOp(value: unknown): CanvasAgentOp {
    const item = objectDetail(value);
    const type = item.type;
    if (type === "add_node") {
        const metadata = recordOptional(item.metadata);
        const nodeType = item.nodeType ? requireNodeType(item.nodeType) : undefined;
        const nodeMetadata = nodeType === CanvasNodeType.Script ? normalizeAgentStoryboardMetadata(metadata) : metadata as CanvasNodeData["metadata"];
        return {
            type,
            id: stringOptional(item.id),
            nodeType,
            title: stringOptional(item.title),
            position: recordOptional(item.position) ? { x: requireNumber(objectDetail(item.position).x, "position.x"), y: requireNumber(objectDetail(item.position).y, "position.y") } : undefined,
            x: numberOptional(item.x),
            y: numberOptional(item.y),
            width: numberOptional(item.width),
            height: numberOptional(item.height),
            metadata: nodeMetadata,
        };
    }
    if (type === "update_node") return { type, id: requireString(item.id, "id"), patch: recordOptional(item.patch) as Partial<CanvasNodeData> | undefined, metadata: recordOptional(item.metadata) as CanvasNodeData["metadata"] };
    if (type === "delete_node") return { type, id: stringOptional(item.id), ids: Array.isArray(item.ids) ? requireStringArray(item.ids, "ids") : undefined };
    if (type === "delete_connections") return { type, id: stringOptional(item.id), ids: Array.isArray(item.ids) ? requireStringArray(item.ids, "ids") : undefined, all: typeof item.all === "boolean" ? item.all : undefined };
    if (type === "connect_nodes") return { type, id: stringOptional(item.id), fromNodeId: requireString(item.fromNodeId, "fromNodeId"), toNodeId: requireString(item.toNodeId, "toNodeId") };
    if (type === "set_viewport") return { type, viewport: requireViewport(item.viewport) };
    if (type === "select_nodes") return { type, ids: requireStringArray(item.ids, "ids") };
    if (type === "run_generation") return { type, nodeId: requireString(item.nodeId, "nodeId"), mode: generationMode(item.mode), prompt: stringOptional(item.prompt) };
    if (type === "run_image_annotation") return { type, annotationNodeId: requireString(item.annotationNodeId, "annotationNodeId"), prompt: stringOptional(item.prompt) };
    throw new Error("不支持的画布操作类型");
}

function requireRecordArray(value: unknown, field: string): Record<string, unknown>[] {
    if (!Array.isArray(value)) throw new Error(`${field} 必须是数组`);
    return value.map((item) => {
        const record = objectDetail(item);
        if (!Object.keys(record).length) throw new Error(`${field} 必须只包含对象`);
        return record;
    });
}

function requireString(value: unknown, field: string) {
    if (typeof value !== "string" || !value) throw new Error(`${field} 必须是非空字符串`);
    return value;
}

function requireNumber(value: unknown, field: string) {
    if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`${field} 必须是数字`);
    return value;
}

function requireNodeType(value: unknown): CanvasNodeType {
    if (Object.values(CanvasNodeType).includes(value as CanvasNodeType)) return value as CanvasNodeType;
    throw new Error("节点类型必须是 text、image、script、config、video、audio 或 skill");
}

function requireViewport(value: unknown) {
    const item = objectDetail(value);
    return { x: requireNumber(item.x, "viewport.x"), y: requireNumber(item.y, "viewport.y"), k: requireNumber(item.k, "viewport.k") };
}

function recordOptional(value: unknown) {
    return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function stringOptional(value: unknown) {
    return typeof value === "string" ? value : "";
}

function numberOptional(value: unknown) {
    return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function numberOr(value: unknown, fallback: number) {
    return numberOptional(value) ?? fallback;
}

function nextCanvasX(snapshot: CanvasAgentSnapshot) {
    return snapshot.nodes.length ? Math.max(...snapshot.nodes.map((node) => node.position.x + node.width)) + 80 : 0;
}

function generationMode(value: unknown): "text" | "image" | "video" | "audio" {
    return value === "text" || value === "video" || value === "audio" ? value : "image";
}

function generationTitle(mode: "text" | "image" | "video" | "audio") {
    if (mode === "text") return "文本生成";
    if (mode === "video") return "视频生成";
    if (mode === "audio") return "音频生成";
    return "图片生成";
}

function isDirectCinematicPrompt(value: string) {
    const text = value.trim();
    if (/搭建短剧工作流|生成镜头分镜|拆成完整分镜|创建影视项目/.test(text)) return true;
    // 用户常会输入“帮我把这个故事拆成短剧分镜”等自然变体；创作意图明确时
    // 直接走持久化影视会话，避免先付费调用在线 Agent 猜工具。
    return /(?:生成|创建|搭建|拆成|拆分|规划|制作|设计|做|写|编写|构思|策划).{0,24}(?:短剧|分镜|影视项目|镜头脚本|小?故事|短片|短视频)/.test(text);
}

function defaultGenerationModel(config: AiConfig, mode: "text" | "image" | "video" | "audio") {
    if (mode === "image") return config.imageModel || config.model;
    if (mode === "video") return config.videoModel || config.model;
    if (mode === "audio") return config.audioModel || config.model;
    return config.textModel || config.model;
}

function agentEndpointLabel(config: ReturnType<typeof resolveModelRequestConfig>) {
    if (config.apiFormat === "gemini") return "streamGenerateContent";
    return config.interfaceType === "chat-completion" ? "chat/completions" : "responses";
}

function channelForResolvedRequest(config: OnlineAgentRequestConfig) {
    // resolvedChannelId 是本轮开始时记录的内部绑定；不能用已降为裸名称的 model
    // 再次命中第一条同名渠道。
    return (config.resolvedChannelId && config.channels.find((channel) => channel.id === config.resolvedChannelId)) || resolveModelChannel(config, config.model);
}

function resolveGenerationModel(config: AiConfig, mode: "text" | "image" | "video" | "audio", model?: string) {
    const normalized = normalizeModelOptionValue(model, config.channels);
    return normalized && selectableModelsByCapability(config, mode).includes(normalized) ? normalized : defaultGenerationModel(config, mode);
}

function generationCount(value: string) {
    return Math.max(1, Math.min(15, Math.floor(Math.abs(Number(value)) || 1)));
}

function cleanRecord(value: Record<string, unknown>) {
    return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined && item !== ""));
}

function snapshotSignature(snapshot: CanvasAgentSnapshot) {
    return JSON.stringify({ nodes: snapshot.nodes, connections: snapshot.connections, selectedNodeIds: snapshot.selectedNodeIds, viewport: snapshot.viewport });
}

function explainNoop(ops: CanvasAgentOp[], snapshot: CanvasAgentSnapshot) {
    if (!ops.length) return "模型没有返回可执行的画布操作。";
    const nodeIds = new Set(snapshot.nodes.map((node) => node.id));
    const connectionIds = new Set(snapshot.connections.map((conn) => conn.id));
    const deleteConnectionOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "delete_connections" }> => op.type === "delete_connections");
    const connectOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "connect_nodes" }> => op.type === "connect_nodes");
    const deleteNodeOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "delete_node" }> => op.type === "delete_node");
    const updateOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "update_node" }> => op.type === "update_node");
    const selectOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "select_nodes" }> => op.type === "select_nodes");
    const generationOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "run_generation" }> => op.type === "run_generation");
    const annotationOps = ops.filter((op): op is Extract<CanvasAgentOp, { type: "run_image_annotation" }> => op.type === "run_image_annotation");
    if (deleteConnectionOps.length && !snapshot.connections.length) return "画布当前没有连线可删除。";
    if (deleteConnectionOps.length && deleteConnectionOps.every((op) => !op.all && [...(op.ids || []), ...(op.id ? [op.id] : [])].every((id) => !connectionIds.has(id)))) return "没有找到要删除的连线。";
    if (connectOps.length && connectOps.every((op) => snapshot.connections.some((conn) => conn.fromNodeId === op.fromNodeId && conn.toNodeId === op.toNodeId))) return "这些节点已经存在对应连线，无需重复连接。";
    if (connectOps.length && connectOps.every((op) => !nodeIds.has(op.fromNodeId) || !nodeIds.has(op.toNodeId))) return "没有找到要连接的节点。";
    if (deleteNodeOps.length && deleteNodeOps.every((op) => op.nodeType === CanvasNodeType.Config) && !snapshot.nodes.some((node) => node.type === CanvasNodeType.Config)) return "画布当前没有生成配置节点可删除。";
    if (deleteNodeOps.length && deleteNodeOps.every((op) => [...(op.ids || []), ...(op.id ? [op.id] : [])].every((id) => !nodeIds.has(id)))) return "没有找到要删除的节点。";
    if (updateOps.length && updateOps.every((op) => !nodeIds.has(op.id))) return "没有找到要更新的节点。";
    if (selectOps.length && selectOps.every((op) => !(op.ids || []).some((id) => nodeIds.has(id)))) return "没有找到要选择的节点。";
    if (generationOps.length && generationOps.every((op) => !nodeIds.has(op.nodeId))) return "没有找到要触发生成的节点。";
    if (annotationOps.length && annotationOps.every((op) => !nodeIds.has(op.annotationNodeId))) return "没有找到要执行的图片标注节点。";
    if (ops.every((op) => op.type === "set_viewport")) return "视图已经是目标状态。";
    if (selectOps.length && selectOps.every((op) => JSON.stringify(op.ids || []) === JSON.stringify(snapshot.selectedNodeIds))) return "选区已经是目标状态。";
    return "工具已执行，但画布状态没有变化；请在日志 tab 查看工具参数和执行前后状态。";
}

function nodeToReference(node: CanvasNodeData): CanvasAssistantReference | null {
    if (node.type === CanvasNodeType.Image && node.metadata?.content) {
        const annotation = node.metadata.imageAnnotation;
        return { id: node.id, type: node.type, title: node.title, dataUrl: node.metadata.content, storageKey: node.metadata.storageKey, text: annotation ? `图片标注指令：${annotation.instruction}\n原图节点：${annotation.sourceNodeId}` : undefined };
    }
    if (node.type === CanvasNodeType.Text && node.metadata?.content) {
        return { id: node.id, type: node.type, title: node.title, text: node.metadata.content };
    }
    if (node.type === CanvasNodeType.Skill && node.metadata?.skillSnapshot) {
        return { id: node.id, type: node.type, title: node.title, text: [node.metadata.skillSnapshot.name, node.metadata.skillSnapshot.template, node.metadata.skillSnapshot.outputContract].filter(Boolean).join("\n\n") };
    }
    return null;
}

function buildAssistantReferences(nodes: CanvasNodeData[], selectedNodeIds: Set<string>) {
    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    return Array.from(selectedNodeIds)
        .map((id) => nodeById.get(id))
        .filter((node): node is CanvasNodeData => Boolean(node))
        .map(nodeToReference)
        .filter((item): item is CanvasAssistantReference => Boolean(item));
}

async function buildToolAgentMessages(snapshot: CanvasAgentSnapshot, history: CanvasAssistantMessage[], userMessage: CanvasAssistantMessage, manualDelivery = false, toolIntent?: OnlineAgentToolIntent): Promise<ResponseInputMessage[]> {
    const refs = userMessage.references || [];
    const workspaceInstruction = toolIntent === "manual_storyboard"
        ? "当前影视入口选择精简手动交付路径：本轮只做一次短文本整理，默认生成 3 个镜头（用户明确指定数量时再调整），每个镜头必须包含画面提示词和视频动作提示词；提示词用短句，不重复整段风格说明，不要创建后台分镜或视频任务。网页会把返回文本解析为可编辑 script 节点，用户再复制提示词到网页工作台逐镜生成视频。"
        : manualDelivery
        ? "当前画布处于手动交付模式：只生成分镜脚本、分镜图和视频提示词；不要调用任何会提交视频任务的工具（包括 canvas_generate_video、canvas_run_generation 的 video 模式或自动运行的视频流程）。视频由用户复制提示词后在网页工作台逐镜提交。"
        : "";
    const canvasContext = toolIntent === "cinematic"
        ? `当前画布用于承接影视结果：${snapshot.title || "未命名画布"}，共有 ${snapshot.nodes.length} 个节点，当前选中 ${snapshot.selectedNodeIds?.length || 0} 个节点。完整画布快照会由后台影视任务接收，首轮无需读取或复述节点。`
        : toolIntent === "manual_storyboard"
            ? `当前画布用于手动交付：${snapshot.title || "未命名画布"}，共有 ${snapshot.nodes.length} 个节点，当前选中 ${snapshot.selectedNodeIds?.length || 0} 个节点。网页会把本轮短文本整理结果写入下一个空闲位置的 script 节点，不需要读取完整画布快照。`
        : `当前画布：${JSON.stringify(compactSnapshot(snapshot))}`;
    const systemInstruction = toolIntent === "cinematic"
        ? "你是明想 MingWant Studio 的影视入口助手。用户要求短剧、分镜或影视项目时，首轮只能调用 canvas_create_cinematic_session，把用户原始需求作为 prompt 交给后台影视 Agent；不要调用读取工具、不要输出 JSON 或解释过程。"
        : toolIntent === "manual_storyboard"
            ? "你是明想 MingWant Studio 的精简手动分镜助手。本轮不依赖工具调用，请直接返回 3 个镜头的可读分镜文本（用户明确指定数量时遵循用户）；每个镜头都要有简短画面描述、图片生成提示词、视频动作提示词和时长，提示词各控制在约 120 个中文字符内，不要重复风格说明。优先使用‘镜头 1：……\n画面：……\n图片提示词：……\n视频动作提示词：……\n时长：6 秒’格式，不要写长篇解释。"
        : ONLINE_AGENT_PROMPT;
    return [
        { role: "system", content: [systemInstruction, workspaceInstruction].filter(Boolean).join("\n") },
        ...history
            .filter((message) => (message.role === "user" || message.role === "assistant" || message.role === "system") && objectDetail(message.detail).kind !== "online_agent_call_budget")
            .slice(-8)
            .map((message): ResponseInputMessage => ({ role: message.role as "system" | "user" | "assistant", content: message.text })),
        {
            role: "user",
            content: [
                ...refs.flatMap((item) => (item.text ? [{ type: "text" as const, text: `选中节点 ${item.title}：${item.text}` }] : [])),
                { type: "text", text: `${canvasContext}\n\n用户需求：${userMessage.text}` },
                ...(await Promise.all(refs.filter((item) => item.dataUrl).map(async (item) => ({ type: "image_url" as const, image_url: { url: await imageToDataUrl(item) } })))),
            ],
        },
    ];
}

/**
 * 手动交付不要求上游支持 Function Calling。模型只返回短分镜文本时，
 * 浏览器把常见 JSON、Markdown 和“镜头/画面/动作”格式统一收敛为 script 节点，
 * 让测活成功但工具协议不兼容的模型也能直接产出可复制的提示词。
 */
function manualStoryboardTextToOps(content: string, snapshot: CanvasAgentSnapshot): CanvasAgentOp[] {
    const rows = parseManualStoryboardRows(content);
    if (!rows.length) return [];
    const metadata = normalizeAgentStoryboardMetadata({
        status: "success",
        composerContent: content.trim(),
        storyboard: {
            rows,
            visibleColumns: ["shotNumber", "durationSeconds", "plotDescription", "dialogue", "camera", "motion", "imageGenerationPrompt", "videoMotionPrompt", "negativePrompt"],
            referenceNodeIds: [],
        },
    });
    return [{
        type: "add_node",
        nodeType: CanvasNodeType.Script,
        title: "手动交付 · 分镜脚本",
        position: { x: nextCanvasX(snapshot), y: 0 },
        metadata,
    }];
}

function parseManualStoryboardRows(content: string) {
    const parsed = parseEmbeddedJson(content);
    const parsedRecord = recordOptional(parsed);
    const nestedStoryboard = recordOptional(parsedRecord?.storyboard);
    const jsonRows = Array.isArray(parsed)
        ? parsed
        : Array.isArray(parsedRecord?.rows)
            ? parsedRecord.rows
            : Array.isArray(parsedRecord?.shots)
                ? parsedRecord.shots
                : Array.isArray(nestedStoryboard?.rows)
                    ? nestedStoryboard.rows
                    : [];
    if (jsonRows.length) return jsonRows.map((value, index) => manualStoryboardRow(value, index)).filter(Boolean).slice(0, 8);

    const tableRows = parseManualStoryboardTable(content);
    if (tableRows.length) return tableRows.map((value, index) => manualStoryboardRow(value, index)).filter(Boolean).slice(0, 8);

    const blocks = splitManualStoryboardBlocks(content);
    return blocks.map((block, index) => manualStoryboardRowFromText(block, index)).filter(Boolean).slice(0, 8);
}

function parseManualStoryboardTable(content: string) {
    const lines = content.split(/\r?\n/).map((line) => line.trim()).filter((line) => line.includes("|") && line.replace(/[|\s:-]/g, "").length > 0);
    if (lines.length < 2) return [];
    const cells = (line: string) => line.replace(/^\||\|$/g, "").split("|").map((value) => value.trim());
    const headers = cells(lines[0]);
    if (!headers.some((header) => /镜头|分镜|镜号|shot|画面|图片|视频|动作|时长/i.test(header))) return [];
    return lines.slice(1).filter((line) => !cells(line).every((cell) => /^:?-{2,}:?$/.test(cell))).map((line) => {
        const values = cells(line);
        return values.reduce<Record<string, string>>((record, value, index) => {
            const header = headers[index] || "";
            if (/首帧|分镜图|图片|图像|画面提示|image(?:_|\s)?(?:generation)?(?:_|\s)?prompt/i.test(header)) record.imageGenerationPrompt = value;
            else if (/视频|动作|运镜|运动|video(?:_|\s)?(?:motion|generation)?(?:_|\s)?prompt/i.test(header)) record.videoMotionPrompt = value;
            else if (/时长|duration(?:_|\s)?(?:seconds?|sec)?|seconds?/i.test(header)) record.durationSeconds = value;
            else if (/台词|对白|dialogue/i.test(header)) record.dialogue = value;
            else if (/镜头|分镜|镜号|shot|场景|画面|描述|内容|scene|visual/i.test(header)) record.plotDescription = value;
            return record;
        }, {});
    });
}

function manualStoryboardRow(value: unknown, index: number) {
    const item = recordOptional(value);
    if (!item) return null;
    const plotDescription = firstText(item, ["plotDescription", "sceneDescription", "scene_description", "description", "scene", "visual", "画面描述", "镜头画面", "画面", "场景", "内容"]);
    const imageGenerationPrompt = firstText(item, ["imageGenerationPrompt", "image_generation_prompt", "imagePrompt", "image_prompt", "firstFramePrompt", "first_frame_prompt", "首帧图片提示词", "首帧提示词", "分镜图提示词", "图片生成提示词", "图片提示词", "画面提示词"]);
    const videoMotionPrompt = firstText(item, ["videoMotionPrompt", "video_motion_prompt", "videoPrompt", "video_prompt", "videoGenerationPrompt", "video_generation_prompt", "motionPrompt", "motion_prompt", "视频动作提示词", "视频动作", "视频生成提示词", "视频提示词", "动作提示词", "动作", "运镜"]);
    const fallback = plotDescription || imageGenerationPrompt || videoMotionPrompt;
    if (!fallback) return null;
    return {
        shotNumber: index + 1,
        durationSeconds: manualDuration(firstText(item, ["durationSeconds", "duration_seconds", "duration", "durationSec", "duration_sec", "seconds", "时长", "秒数"])),
        plotDescription: plotDescription || fallback,
        dialogue: firstText(item, ["dialogue", "台词", "对白"]),
        camera: firstText(item, ["camera", "镜头", "景别"]),
        motion: firstText(item, ["motion", "cameraMotion", "运镜", "镜头运动"]),
        imageGenerationPrompt: imageGenerationPrompt || fallback,
        videoMotionPrompt: videoMotionPrompt || firstText(item, ["motion", "cameraMotion", "运镜"]) || fallback,
        negativePrompt: firstText(item, ["negativePrompt", "negative_prompt", "负面提示词", "负面"]),
    };
}

function manualStoryboardRowFromText(block: string, index: number) {
    const lines = block.split(/\r?\n/).map((line) => line.replace(/^\s*[-*•]\s*/, "").trim()).filter(Boolean);
    const field = (labels: string[]) => {
        const labelPattern = labels.join("|");
        const line = lines.find((value) => new RegExp(`^(?:${labelPattern})\\s*[：:：-]`, "i").test(value));
        return line ? line.replace(new RegExp(`^(?:${labelPattern})\\s*[：:：-]\\s*`, "i"), "").trim() : "";
    };
    const description = field(["画面描述", "镜头画面", "画面", "场景", "内容", "scene", "visual"]) || lines.find((line) => !/^(?:首帧|分镜图|图片|图像|画面)?提示词|^(?:视频)?(?:生成)?动作|^视频生成提示词|^运镜|^(?:镜头)?运动|^(?:时长|duration|seconds|秒数)|^(?:台词|对白)|^负面/i.test(line)) || "";
    const imageGenerationPrompt = field(["首帧图片提示词", "首帧提示词", "分镜图提示词", "图片生成提示词", "图像生成提示词", "图片提示词", "图像提示词", "画面提示词", "image prompt", "image_prompt"]) || description;
    const videoMotionPrompt = field(["视频动作提示词", "视频动作", "视频生成提示词", "视频提示词", "动作提示词", "动作", "运镜", "镜头运动", "video prompt", "video_prompt"]) || field(["运动"]) || description;
    if (!description && !imageGenerationPrompt && !videoMotionPrompt) return null;
    return {
        shotNumber: index + 1,
        durationSeconds: manualDuration(field(["时长", "duration", "seconds", "秒数"])),
        plotDescription: description || imageGenerationPrompt || videoMotionPrompt,
        dialogue: field(["台词", "对白", "dialogue"]),
        camera: field(["景别", "镜头", "camera"]),
        motion: field(["镜头运动", "运镜", "motion"]),
        imageGenerationPrompt,
        videoMotionPrompt,
        negativePrompt: field(["负面提示词", "负面", "negative prompt", "negative_prompt"]),
    };
}

function splitManualStoryboardBlocks(content: string) {
    const text = content.replace(/```[\s\S]*?```/g, (value) => value.replace(/```(?:text|markdown)?/gi, "").replace(/```/g, "")).trim();
    const marker = /(?:^|\n)\s*(?:(?:镜头|分镜|shot)\s*\d{0,2}|\d{1,2}[、.)])\s*[:：.)、-]?\s*/gi;
    const matches = Array.from(text.matchAll(marker));
    if (matches.length) {
        return matches.map((match, index) => text.slice(match.index! + match[0].length, matches[index + 1]?.index ?? text.length).trim()).filter(Boolean);
    }
    return text.split(/\n{2,}|\n(?=\s*\d+[、.)])/).map((value) => value.trim()).filter(Boolean).slice(0, 8);
}

function parseEmbeddedJson(content: string): unknown {
    const fenced = content.match(/```(?:json)?\s*([\s\S]*?)```/i)?.[1]?.trim();
    for (const candidate of [fenced, content.trim(), balancedJsonSlice(content)]) {
        if (!candidate) continue;
        try {
            return JSON.parse(candidate);
        } catch {
            // 文本解析路径允许继续尝试下一个候选，不把模型的 Markdown 包裹误判成失败。
        }
    }
    return undefined;
}

function balancedJsonSlice(value: string) {
    const start = value.search(/[\[{]/);
    if (start < 0) return "";
    const stack: string[] = [];
    let quoted = false;
    let escaped = false;
    for (let index = start; index < value.length; index += 1) {
        const char = value[index];
        if (quoted) {
            if (escaped) escaped = false;
            else if (char === "\\") escaped = true;
            else if (char === '"') quoted = false;
            continue;
        }
        if (char === '"') {
            quoted = true;
            continue;
        }
        if (char === "{" || char === "[") stack.push(char === "{" ? "}" : "]");
        else if (char === "}" || char === "]") {
            if (stack.pop() !== char) return "";
            if (!stack.length) return value.slice(start, index + 1);
        }
    }
    return "";
}

function firstText(value: Record<string, unknown>, keys: string[]) {
    for (const key of keys) {
        const raw = value[key];
        const text = typeof raw === "string" ? raw.trim() : typeof raw === "number" && Number.isFinite(raw) ? String(raw) : "";
        if (text) return text;
    }
    return "";
}

function manualDuration(value: string) {
    const match = value.match(/\d+(?:\.\d+)?/);
    const seconds = match ? Number(match[0]) : 6;
    return Math.min(60, Math.max(1, Math.round(Number.isFinite(seconds) ? seconds : 6)));
}

function isManualDeliveryVideoToolCall(name: string, input: Record<string, unknown>) {
    if (name === "canvas_generate_video") return true;
    if (name === "canvas_run_generation") return input.mode === "video";
    if (name === "canvas_create_config_node") return input.mode === "video";
    if (name === "canvas_create_generation_flow") return input.mode === "video";
    if (name === "canvas_create_node") {
        if (input.nodeType === CanvasNodeType.Video) return true;
        const metadata = recordOptional(input.metadata);
        return input.nodeType === CanvasNodeType.Config && metadata?.generationMode === "video";
    }
    if (name === "canvas_apply_ops") {
        return Array.isArray(input.ops) && input.ops.some((value) => {
            if (!value || typeof value !== "object" || Array.isArray(value)) return false;
            const op = value as Record<string, unknown>;
            if (op.type === "run_generation" && op.mode === "video") return true;
            if (op.type === "add_node" && op.nodeType === CanvasNodeType.Video) return true;
            const metadata = recordOptional(op.metadata);
            return op.type === "add_node" && op.nodeType === CanvasNodeType.Config && metadata?.generationMode === "video";
        });
    }
    return false;
}

function isManualDeliveryBlockedResult(result: OnlineToolResult) {
    return !result.ok && result.message === MANUAL_DELIVERY_VIDEO_MESSAGE;
}

function imageAnnotationsFromSnapshot(snapshot: CanvasAgentSnapshot, requestedNodeId?: string) {
    const selectedIds = new Set(snapshot.selectedNodeIds || []);
    if (!requestedNodeId && selectedIds.size === 0) throw new Error("请先选择标注节点或原图节点，或传入 nodeId");
    const annotations = snapshot.nodes.flatMap((node) => {
        const annotation = node.metadata?.imageAnnotation;
        if (node.type !== CanvasNodeType.Image || !annotation) return [];
        const inScope = requestedNodeId
            ? node.id === requestedNodeId || annotation.sourceNodeId === requestedNodeId
            : selectedIds.has(node.id) || selectedIds.has(annotation.sourceNodeId);
        if (!inScope) return [];
        return [{
            nodeId: node.id,
            title: node.title,
            sourceNodeId: annotation.sourceNodeId,
            sourceTitle: snapshot.nodes.find((item) => item.id === annotation.sourceNodeId)?.title,
            instruction: annotation.instruction,
        }];
    });
    if (!annotations.length) throw new Error("当前范围没有图片标注；请先在图片工具栏完成标注并保存节点");
    if (annotations.length > 6) throw new Error("一次最多读取 6 个图片标注，请缩小选区");
                return annotations;
}

function compactSnapshot(snapshot: CanvasAgentSnapshot) {
    return {
        title: snapshot.title,
        viewport: snapshot.viewport,
        selectedNodeIds: snapshot.selectedNodeIds,
        nodes: snapshot.nodes.map((node) => ({
            id: node.id,
            type: node.type,
            title: node.title,
            position: node.position,
            width: node.width,
            height: node.height,
            metadata: compactMetadata(node.metadata || {}),
        })),
        connections: snapshot.connections,
    };
}

function compactMetadata(metadata: CanvasNodeData["metadata"]) {
    return {
        content: String(metadata?.content || "").slice(0, 500),
        prompt: String(metadata?.prompt || metadata?.composerContent || "").slice(0, 500),
        status: metadata?.status,
        skillName: metadata?.skillSnapshot?.name,
        skillVersion: metadata?.skillSnapshot?.version,
        generationMode: metadata?.generationMode,
        model: metadata?.model,
        size: metadata?.size,
        assetTags: metadata?.assetTags,
        workflowKind: metadata?.workflowKind,
        workflowTitle: metadata?.workflowTitle,
        workflowDescription: metadata?.workflowDescription,
        chapterId: metadata?.chapterId,
        chapterTitle: metadata?.chapterTitle,
        shotIndex: metadata?.shotIndex,
        imageAnnotation: metadata?.imageAnnotation ? { sourceNodeId: metadata.imageAnnotation.sourceNodeId, instruction: metadata.imageAnnotation.instruction } : undefined,
        imageAnnotationResultOf: metadata?.imageAnnotationResultOf,
    };
}

function upsertAssistantMessage(messages: CanvasAssistantMessage[], message: CanvasAssistantMessage) {
    const exists = messages.some((item) => item.id === message.id);
    return exists ? messages.map((item) => (item.id === message.id ? { ...item, ...message } : item)) : [...messages, message];
}

function createSession(): CanvasAssistantSession {
    const now = new Date().toISOString();
    return { id: nanoid(), title: "新对话", messages: [], createdAt: now, updatedAt: now };
}
