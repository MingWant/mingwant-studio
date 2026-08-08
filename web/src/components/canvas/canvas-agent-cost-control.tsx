import { useCallback } from "react";
import { Alert, App, Segmented } from "antd";

import { longTextStreamingAdvisory, type ChannelProbeReadiness } from "@/lib/channel-probe-readiness";
import { onlineAgentToolChoiceReason } from "@/lib/agent-tool-response";
import type { CanvasAssistantMessage } from "@/types/canvas";

export const ONLINE_AGENT_MAX_MODEL_CALLS = 4;
export const ONLINE_AGENT_DEFAULT_MODEL_CALLS = 2;

export type OnlineAgentCallBudgetStatus = "running" | "waiting_tool" | "completed" | "failed" | "stopped";

type AgentCostContext = {
    model: string;
    channel: string;
    systemChannel: boolean;
    protocol?: string;
    streamingReadiness?: ChannelProbeReadiness;
    /** 手动交付只需要一次脚本整理请求，不能让用户误选多轮总结。 */
    singleCall?: boolean;
    /** 影视分镜工具依赖 Function Calling；文本 SSE 通过时仍需单独确认工具能力。 */
    requireToolCalling?: boolean;
};

type OnlineAgentActiveRequestContext = {
    callNumber: number;
    model: string;
};

type OnlineAgentBlockedAction = "collapse" | "navigate";

type OnlineAgentProtectedPhase = "running" | "tool_approval";

type OnlineAgentBudgetMessageInput = {
    id: string;
    model: string;
    usedCalls: number;
    maxCalls: number;
    status: OnlineAgentCallBudgetStatus;
    note?: string;
};

export function useCanvasAgentCostControl() {
    const { modal } = App.useApp();

    const confirmOnlineAgentTurn = useCallback((context: AgentCostContext) => new Promise<number | null>((resolve) => {
        const toolCalling = context.streamingReadiness?.toolCalling;
        // 测活只给管理员提供诊断信息，不能成为普通用户的调用授权门禁。
        // 失败/过期/未验证时仍允许一次明确确认的请求，但收窄本轮预算，避免
        // 未知协议自动扩展成多轮付费链路；真实结果交给调用链处理。
        const knownStreamingRisk = context.streamingReadiness?.state === "non_stream" || context.streamingReadiness?.state === "failed";
        const unknownToolRisk = toolCalling !== "supported";
        // 工具能力未验证时允许短 Agent 试一次，但不让它自动扩展成多轮付费链路。
        const defaultMaxCalls = context.singleCall || knownStreamingRisk || unknownToolRisk ? 1 : ONLINE_AGENT_DEFAULT_MODEL_CALLS;
        let selectedMaxCalls = defaultMaxCalls;
        const billing = context.systemChannel
            ? "系统渠道会按每一轮模型请求分别创建积分计费记录。"
            : "自定义 API Key 可能由供应商按每一轮请求分别收费。";
        modal.confirm({
            title: "确认发送给在线 Agent",
            content: (
                <div className="space-y-2 text-sm leading-6">
                    <p>模型：{context.model}；渠道：{context.channel}。</p>
                    <StreamingReadinessAlert readiness={context.streamingReadiness} model={context.model} protocol={context.protocol} singleCall={context.singleCall} />
                    <p>本条消息会先调用一次模型，工具执行后可能继续推理。请选择这轮最多允许的独立文本模型请求数：{billing}{context.singleCall ? " 手动交付只整理一次分镜脚本，不会发送总结轮次。" : unknownToolRisk ? " 最近一次工具诊断未通过、已过期或尚未完成，系统默认只允许 1 次；本次仍会正常尝试，结果由上游实际响应决定。" : ""}</p>
                    <Segmented
                        block
                        value={context.singleCall ? 1 : undefined}
                        defaultValue={context.singleCall ? undefined : defaultMaxCalls}
                        options={context.singleCall ? [{ label: "1 次（手动交付）", value: 1 }] : [{ label: "1 次", value: 1 }, { label: "2 次（推荐）", value: 2 }, { label: "4 次（复杂任务）", value: ONLINE_AGENT_MAX_MODEL_CALLS }]}
                        onChange={(value) => { selectedMaxCalls = Number(value); }}
                    />
                    <p>选 1 次时，首轮工具执行后直接展示工具结果，不再让模型总结。生图、视频、音频或分镜等工具会另行产生生成任务，不计入这里的文本推理预算。</p>
                    <p>在线 Agent 不再由网页固定限制单轮输出 token，模型会按自身能力和渠道策略决定长度；Kimi 工具编排仍使用 low 推理强度。供应商若明确返回截断或未完整状态，本轮会停止且不会执行不完整的工具参数；较长响应可能增加等待时间或触发上游 524。</p>
                    <p>524、连接中断或响应不确定时会立即停止本轮，不会自动发送下一次请求。</p>
                </div>
            ),
            okText: "确认并发送",
            cancelText: "取消",
            centered: true,
            onOk: () => resolve(selectedMaxCalls),
            onCancel: () => resolve(null),
        });
    }), [modal]);

    const confirmCinematicTask = useCallback((context: AgentCostContext) => new Promise<boolean>((resolve) => {
        const probeAdvisory = longTextStreamingAdvisory(context.streamingReadiness || { state: "unverified" }, context.model, !context.systemChannel, context.requireToolCalling === true);
        const billing = context.systemChannel
            ? "用户仍按当前分镜任务的积分价格结算，但平台上游可能收到两次文本请求。"
            : "自定义 API Key 可能因此产生最多两次供应商调用费用。";
        modal.confirm({
            title: "确认创建影视项目",
            content: (
                <div className="space-y-2 text-sm leading-6">
                    <p>模型：{context.model}；渠道：{context.channel}。</p>
                    <StreamingReadinessAlert readiness={context.streamingReadiness} model={context.model} protocol={context.protocol} />
                    {probeAdvisory ? <p className="text-amber-600">测活诊断提示：{probeAdvisory}。测活仅供管理员判断渠道状态，本次仍可继续调用；若上游实际失败，系统会按真实请求状态处理，用户可在确认费用后重试。</p> : null}
                    <p>后台先发起一次分镜请求；只有首轮已返回但结构校验失败时，才会使用一次已授权修复。{billing}</p>
                    <p>连续两轮无效时直接失败；524 或连接中断不会触发修复。渠道若明确拒绝流式，系统不会补发非流式请求。</p>
                </div>
            ),
            okText: "确认并允许一次修复",
            cancelText: "取消",
            centered: true,
            onOk: () => resolve(true),
            onCancel: () => resolve(false),
        });
    }), [modal]);

    const confirmStopOnlineAgentRequest = useCallback((context: OnlineAgentActiveRequestContext) => new Promise<boolean>((resolve) => {
        modal.confirm({
            title: "停止等待在线 Agent？",
            content: (
                <div className="space-y-2 text-sm leading-6">
                    <p>正在等待模型 {context.model} 的第 {context.callNumber} 次回复。</p>
                    <p>停止只会中断当前网页连接，不能保证上游供应商停止执行；这次请求仍可能继续运行并产生费用。</p>
                    <p>停止后本轮不会自动重试，也不会发送下一次模型请求。请先到供应商后台或系统请求明细核对状态和账单。</p>
                </div>
            ),
            okText: "停止等待",
            okButtonProps: { danger: true },
            cancelText: "继续等待",
            centered: true,
            onOk: () => resolve(true),
            onCancel: () => resolve(false),
        });
    }), [modal]);

    const warnOnlineAgentActionBlocked = useCallback((action: OnlineAgentBlockedAction, phase: OnlineAgentProtectedPhase, context: OnlineAgentActiveRequestContext | null) => {
        const title = action === "collapse" ? "暂不能收起在线 Agent" : "暂不能离开当前画布";
        const requestText = context
            ? `模型 ${context.model} 的第 ${context.callNumber} 次请求仍在等待。请继续等待，或先使用红色“停止”按钮明确中断本页连接。`
            : phase === "tool_approval"
                ? "当前有工具调用等待确认。请先批准或拒绝，避免丢失本轮工具上下文。"
                : "在线 Agent 正在执行工具或整理本轮结果。请等待当前步骤完成后再离开。";
        modal.warning({
            title,
            content: (
                <div className="space-y-2 text-sm leading-6">
                    <p>{requestText}</p>
                    {context ? <p>直接关闭页面仍可能让供应商继续执行并计费。</p> : null}
                </div>
            ),
            okText: "返回处理",
            centered: true,
        });
    }, [modal]);

    return { confirmOnlineAgentTurn, confirmCinematicTask, confirmStopOnlineAgentRequest, warnOnlineAgentActionBlocked };
}

export function onlineAgentBudgetMessage({ id, model, usedCalls, maxCalls, status, note }: OnlineAgentBudgetMessageInput): CanvasAssistantMessage {
    const progress = `${Math.max(0, usedCalls)} / ${maxCalls}`;
    const statusText: Record<OnlineAgentCallBudgetStatus, string> = {
        running: `正在等待第 ${Math.max(1, usedCalls)} 次模型回复`,
        waiting_tool: `已完成 ${usedCalls} 次模型调用，等待工具确认或执行`,
        completed: `本轮已完成，共使用 ${usedCalls} 次模型调用`,
        failed: `本轮在第 ${Math.max(1, usedCalls)} 次模型调用时停止`,
        stopped: `本轮已停止，已发起或完成 ${usedCalls} 次模型调用`,
    };
    return {
        id,
        role: "system",
        title: "本轮 Agent 文本推理预算",
        text: `${statusText[status]}。调用预算：${progress}；模型：${model}。${note ? ` ${note}` : ""}`.trim(),
        meta: `${progress} 次`,
        detail: { kind: "online_agent_call_budget", model, usedCalls, maxCalls, status, note },
    };
}

export function onlineAgentStoppedMessage(callNumber: number) {
    return `你已停止等待在线 Agent 第 ${Math.max(1, callNumber)} 次模型回复。本页连接已中断，当前已显示的增量内容可能不完整；供应商也可能仍在服务端执行并产生费用。本轮不会自动重试或发送下一次请求，请先核对供应商后台或系统请求明细。`;
}

function StreamingReadinessAlert({ readiness, model, protocol, singleCall = false }: { readiness?: ChannelProbeReadiness; model?: string; protocol?: string; singleCall?: boolean }) {
    const toolChoiceCompatibility = model ? onlineAgentToolChoiceReason(model, protocol, "required") : "";
    const toolChoiceNote = toolChoiceCompatibility ? `${toolChoiceCompatibility}。` : "";
    const toolCallingNote = readiness?.toolCalling === "supported"
        ? "最近一次无副作用工具诊断已通过。"
        : readiness?.toolCalling === "failed"
            ? singleCall ? "最近一次工具诊断未通过；手动交付本轮不使用工具，改为纯文本整理。" : "最近一次无副作用工具诊断未通过；本次仍会尝试，工具能否执行以真实模型响应为准。"
            : readiness?.toolCalling === "stale"
                ? "工具调用诊断已过期；本次仍可调用，建议管理员重新运行“测试工具调用”。"
                : "尚未完成工具调用诊断；文本测活通过不代表 Function Calling 可用，但不影响本次用户尝试。";
    if (readiness?.state === "stream") {
        return <Alert type="success" showIcon message="最近一次测活确认完整 SSE 已渐进到达" description={<span>{probeReadinessMeta(readiness)}。{toolCallingNote}{toolChoiceNote}{singleCall ? "手动交付走一次纯文本整理，不依赖工具调用。" : "若首轮没有返回工具调用，系统会停止且不会执行画布操作。"}</span>} />;
    }
    if (readiness?.state === "non_stream") {
        if (readiness.transport === "stream-unverified") {
            return <Alert type="warning" showIcon message="SSE 格式完整，但未证实渐进分片" description={`${probeReadinessMeta(readiness)}。响应可能过快被网络合并，也可能由网关缓冲到最后才返回；这是管理员诊断提示，不会阻止用户调用，本轮默认选择 1 次文本调用。`} />;
        }
        return <Alert type="warning" showIcon message="该模型已测得非流式返回，长推理很容易遇到 524" description={`${probeReadinessMeta(readiness)}。这是管理员诊断提示；用户仍可调用，本轮默认选择 1 次文本调用。`} />;
    }
    if (readiness?.state === "failed") {
        return <Alert type="warning" showIcon message="该模型最近一次测活未通过" description={`${probeReadinessMeta(readiness)}。测活仅供管理员判断渠道状态；${singleCall ? "手动交付仍只尝试一次短文本整理；若返回无法识别的内容，画布不会写入。" : "本次仍可正常调用，本轮默认选择 1 次文本调用。"}`} />;
    }
    if (readiness?.state === "stale") {
        return <Alert type="warning" showIcon message="流式能力测活已过期或检查时间异常" description={`${probeReadinessMeta(readiness)}。供应商行为无法确认，但测活不会阻止用户调用；管理员可重新测活更新诊断。`} />;
    }
    const nearby = readiness?.nearbyProbe;
    return <Alert type="warning" showIcon message={nearby ? "最近一次测活不是当前模型/协议" : "尚未验证该模型是否真正流式返回"} description={nearby ? `当前模型尚未完成测活；最近一次测活对象是 ${nearby.model}（${nearby.protocol}，${new Date(nearby.checkedAt).toLocaleString()}）。这只是管理员诊断提示；${singleCall ? "手动交付仍可在一次预算内尝试纯文本整理，" : "用户仍可在明确预算下调用，"}长分镜也不会因测活状态被阻止。` : `${singleCall ? "手动交付只需一次短文本整理；" : "建议管理员先在渠道或模型管理中运行 LLM 测活；"}仅设置 stream=true 不能证明供应商会持续发送 SSE，但不会阻止用户尝试。`} />;
}

function probeReadinessMeta(readiness: ChannelProbeReadiness) {
    const parts = [readiness.checkedAt ? new Date(readiness.checkedAt).toLocaleString() : "未知时间"];
    if (readiness.durationMs) parts.push(`${Math.round(readiness.durationMs / 1_000)} 秒`);
    if (readiness.transport && readiness.transport !== "stream") parts.push(readiness.transport === "stream-unverified" ? "SSE 渐进分片未证实" : readiness.transport === "non-stream-compatible" ? "完整 JSON 非流式" : readiness.transport === "non-stream-fallback" ? "已回退非流式" : "非流式");
    return parts.join(" · ");
}
