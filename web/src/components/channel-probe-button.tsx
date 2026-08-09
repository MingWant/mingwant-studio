import { Alert, App, Button, Modal, Select, Spin, Tooltip, type ButtonProps } from "antd";
import { Activity } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { isGeminiModelProtocol, modelProtocolCapability, modelProtocolLabel, type ModelProtocol } from "@/lib/model-protocols";
import { CHANNEL_PROBE_VERIFIER_VERSION, recordChannelProbeReadiness, recordChannelToolProbeReadiness } from "@/lib/channel-probe-readiness";
import { createClientId } from "@/lib/client-id";
import { getActiveUserScope, scopedLocalStorage } from "@/lib/user-scope";
import { createChannelProbe, getChannelProbe, type ChannelProbeStatus } from "@/services/api/channel-probes";
import { defaultTextProtocolForChannel, modelMatchesCapability, modelOptionName, type ModelChannel } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";

export type ChannelProbeModelOption = {
    model: string;
    label?: string;
    protocol: ModelProtocol;
};

export type ChannelToolProbeSummary = {
    model: string;
    status?: string;
    checkedAt?: string;
    verifierVersion?: string;
};

type ChannelProbeButtonProps = {
    channel: ModelChannel;
    models?: ChannelProbeModelOption[];
    label?: string;
    size?: ButtonProps["size"];
    className?: string;
    disabled?: boolean;
    toolProbeSummary?: ChannelToolProbeSummary;
    onCompleted?: (probe: ChannelProbeStatus) => void | Promise<void>;
    onToolCompleted?: (probe: ChannelProbeStatus) => void | Promise<void>;
};

export function ChannelProbeButton({ channel, models, label = "测活", size = "small", className, disabled = false, toolProbeSummary, onCompleted, onToolCompleted }: ChannelProbeButtonProps) {
    const { message } = App.useApp();
    const options = useMemo(() => models || channelProbeModels(channel), [channel, models]);
    const userRole = useUserStore((state) => state.user?.role);
    const [open, setOpen] = useState(false);
    const [selectedModel, setSelectedModel] = useState(options[0]?.model || "");
    const [starting, setStarting] = useState(false);
    const [probe, setProbe] = useState<ChannelProbeStatus | null>(null);
    const [pollError, setPollError] = useState("");
    const [toolProbe, setToolProbe] = useState<ChannelProbeStatus | null>(null);
    const [toolProbeRunning, setToolProbeRunning] = useState(false);
    const [toolProbePollError, setToolProbePollError] = useState("");
    const requestKeysRef = useRef(new Map<string, string>());
    // 测活可能运行数分钟；结果必须写回提交时的 Base URL、协议和凭据版本，
    // 不能因为用户在等待期间编辑渠道就把旧请求标成新配置已通过。
    const probeChannelSnapshotsRef = useRef(new Map<string, ModelChannel>());
    const active = probe?.status === "queued" || probe?.status === "running";
    const disabledReason = channelProbeDisabledReason(channel, options, userRole);

    useEffect(() => {
        if (!options.some((item) => item.model === selectedModel)) setSelectedModel(options[0]?.model || "");
    }, [options, selectedModel]);

    useEffect(() => {
        const selectedOption = options.find((item) => item.model === selectedModel);
        if (!probe || !selectedOption) return;
        const activeProbe = probe.status === "queued" || probe.status === "running";
        if (activeProbe || (probe.model === selectedOption.model && probe.protocol === selectedOption.protocol)) return;
        // 切换模型/协议后不能继续展示上一配置的结果，否则用户会误以为新模型已经测活。
        setProbe(null);
        setPollError("");
        setToolProbe(null);
        setToolProbePollError("");
        setToolProbeRunning(false);
    }, [options, probe?.model, probe?.protocol, probe?.status, selectedModel]);

    useEffect(() => {
        if (!probe?.id || (probe.status !== "queued" && probe.status !== "running")) return;
        let stopped = false;
        let timer = 0;
        const poll = async () => {
            try {
                const next = (await getChannelProbe(probe.id)).probe;
                if (stopped) return;
                setProbe(next);
                setPollError("");
                if (next.status === "queued" || next.status === "running") {
                    // 慢推理可能持续十几分钟；5 秒足够反馈阶段变化，也避免状态查询先于模型完成触发限流。
                    timer = window.setTimeout(() => void poll(), 5_000);
                    return;
                }
                const probeChannel = probeChannelSnapshotsRef.current.get(next.id) || channel;
                await finalizeChannelProbe(probeChannel, next, onCompleted, (content) => message.warning(content));
                probeChannelSnapshotsRef.current.delete(next.id);
                if (next.status === "succeeded" && next.result?.ok) {
                    const option = options.find((item) => item.model === next.model && item.protocol === next.protocol);
                    if (option) void runToolProbe(next, option, probeChannel);
                }
            } catch (error) {
                if (stopped) return;
                setPollError(error instanceof Error ? error.message : "读取测活状态失败");
                timer = window.setTimeout(() => void poll(), 10_000);
            }
        };
        timer = window.setTimeout(() => void poll(), 800);
        return () => {
            stopped = true;
            window.clearTimeout(timer);
        };
    }, [probe?.id]);

    useEffect(() => {
        if (!toolProbe?.id || toolProbe.kind !== "tool" || (toolProbe.status !== "queued" && toolProbe.status !== "running")) {
            if (toolProbe && toolProbeRunning) setToolProbeRunning(false);
            return;
        }
        let stopped = false;
        let timer = 0;
        const poll = async () => {
            try {
                const next = (await getChannelProbe(toolProbe.id)).probe;
                if (stopped) return;
                setToolProbe(next);
                setToolProbePollError("");
                if (next.status === "queued" || next.status === "running") {
                    timer = window.setTimeout(() => void poll(), 5_000);
                    return;
                }
                setToolProbeRunning(false);
                const probeChannel = probeChannelSnapshotsRef.current.get(next.id) || channel;
                notifyToolProbeCompleted(next, (content) => message.success(content), (content) => message.error(content));
                await finalizeChannelToolProbe(probeChannel, next, onToolCompleted, (content) => message.warning(content));
                probeChannelSnapshotsRef.current.delete(next.id);
            } catch (error) {
                if (stopped) return;
                setToolProbePollError(error instanceof Error ? error.message : "读取工具诊断状态失败");
                timer = window.setTimeout(() => void poll(), 10_000);
            }
        };
        timer = window.setTimeout(() => void poll(), 800);
        return () => {
            stopped = true;
            window.clearTimeout(timer);
        };
    }, [toolProbe?.id]);

    const start = async () => {
        const option = options.find((item) => item.model === selectedModel);
        if (!option) {
            message.warning("请选择一个文本模型");
            return;
        }
        setStarting(true);
        setPollError("");
        setToolProbe(null);
        setToolProbePollError("");
        setToolProbeRunning(false);
        const requestIdentity = channelProbeRequestIdentity(channel, option);
        const requestKey = getOrCreateChannelProbeRequestKey(requestKeysRef.current, requestIdentity);
        const channelSnapshot = cloneProbeChannel(channel);
        try {
            const input = channel.scope === "system"
                ? { requestKey, kind: "text" as const, channelId: channel.id, model: option.model }
                : { requestKey, kind: "text" as const, baseUrl: channel.baseUrl, apiKey: channel.apiKey, apiFormat: isGeminiModelProtocol(option.protocol) ? "gemini" as const : "openai" as const, interfaceType: option.protocol, model: option.model };
            const next = (await createChannelProbe(input)).probe;
            releaseChannelProbeRequestKey(requestKeysRef.current, requestIdentity, requestKey);
            probeChannelSnapshotsRef.current.set(next.id, channelSnapshot);
            setProbe(next);
            if (next.reused) {
                const stillActive = next.status === "queued" || next.status === "running";
                message.info(stillActive
                    ? "相同配置已有测活任务进行中，已接管原任务；本次没有创建新的供应商调用"
                    : "同一次测活提交已返回原任务结果；本次没有创建新的供应商调用");
            }
            if (next.status !== "queued" && next.status !== "running") {
                await finalizeChannelProbe(channelSnapshot, next, onCompleted, (content) => message.warning(content));
                probeChannelSnapshotsRef.current.delete(next.id);
                if (next.status === "succeeded" && next.result?.ok) void runToolProbe(next, option, channelSnapshot);
            }
        } catch (error) {
            const errorMessage = error instanceof Error ? error.message : "创建测活任务失败";
            if (errorMessage.includes("提交键已用于另一项渠道配置") || errorMessage.includes("提交键格式无效")) {
                releaseChannelProbeRequestKey(requestKeysRef.current, requestIdentity, requestKey);
            }
            message.error(errorMessage);
        } finally {
            setStarting(false);
        }
    };

    const runToolProbe = async (textProbe = probe, optionOverride?: ChannelProbeModelOption, channelOverride?: ModelChannel) => {
        const option = optionOverride || options.find((item) => item.model === selectedModel);
        if (!option) {
            message.warning("请选择要诊断的文本模型");
            return;
        }
        if (textProbe?.status !== "succeeded" || !textProbe.result?.ok || textProbe.model !== option.model || textProbe.protocol !== option.protocol) {
            message.warning("请先完成当前模型和协议的文本测活，再诊断工具调用");
            return;
        }
        const targetChannel = channelOverride || channel;
        const requestIdentity = channelProbeRequestIdentity(targetChannel, option, "tool");
        const requestKey = getOrCreateChannelProbeRequestKey(requestKeysRef.current, requestIdentity);
        const channelSnapshot = channelOverride || cloneProbeChannel(channel);
        setToolProbeRunning(true);
        setToolProbePollError("");
        try {
            const input = targetChannel.scope === "system"
                ? { requestKey, kind: "tool" as const, channelId: targetChannel.id, model: option.model }
                : { requestKey, kind: "tool" as const, baseUrl: targetChannel.baseUrl, apiKey: targetChannel.apiKey, apiFormat: isGeminiModelProtocol(option.protocol) ? "gemini" as const : "openai" as const, interfaceType: option.protocol, model: option.model };
            const next = (await createChannelProbe(input)).probe;
            releaseChannelProbeRequestKey(requestKeysRef.current, requestIdentity, requestKey);
            if (next.kind !== "tool") {
                setToolProbeRunning(false);
                message.info("当前配置已有文本测活在排队或运行，本次未创建新的工具诊断；请等文本测活结束后再点击");
                return;
            }
            probeChannelSnapshotsRef.current.set(next.id, channelSnapshot);
            setToolProbe(next);
            if (next.reused) {
                const stillActive = next.status === "queued" || next.status === "running";
                message.info(stillActive
                    ? "相同配置已有工具诊断进行中，已接管原任务；本次没有创建新的供应商调用"
                    : "同一次工具诊断提交已返回原任务结果；本次没有创建新的供应商调用");
            }
            if (next.status !== "queued" && next.status !== "running") {
                setToolProbeRunning(false);
                notifyToolProbeCompleted(next, (content) => message.success(content), (content) => message.error(content));
                await finalizeChannelToolProbe(channelSnapshot, next, onToolCompleted, (content) => message.warning(content));
                probeChannelSnapshotsRef.current.delete(next.id);
            }
        } catch (error) {
            const errorMessage = error instanceof Error ? error.message : "创建工具诊断任务失败";
            if (errorMessage.includes("提交键已用于另一项渠道配置") || errorMessage.includes("提交键格式无效")) {
                releaseChannelProbeRequestKey(requestKeysRef.current, requestIdentity, requestKey);
            }
            setToolProbeRunning(false);
            message.error(errorMessage);
        }
    };

    const toolProbeSucceeded = toolProbe?.status === "succeeded" && toolProbe.result?.ok === true;
    const configuredToolProbe = channel.modelCosts?.find((item) => item.model === selectedModel || modelOptionName(item.model) === selectedModel);
    const savedToolProbe = toolProbeSummary?.model === selectedModel && toolProbeSummary.checkedAt
        ? toolProbeSummary
        : configuredToolProbe?.toolProbeCheckedAt
            ? { model: selectedModel, status: configuredToolProbe.toolProbeStatus, checkedAt: configuredToolProbe.toolProbeCheckedAt, verifierVersion: configuredToolProbe.toolProbeVerifierVersion }
            : undefined;

    const trigger = (
        <Button
            className={className}
            size={size}
            icon={<Activity className={`size-3.5 ${active ? "motion-safe:animate-pulse" : ""}`} />}
            disabled={disabled || Boolean(disabledReason)}
            loading={starting}
            onClick={() => setOpen(true)}
        >
            {active ? "测活中" : label}
        </Button>
    );

    return (
        <>
            {disabledReason ? <Tooltip title={disabledReason}><span>{trigger}</span></Tooltip> : trigger}
            <Modal
                title={`${channel.name || "当前渠道"} · LLM 测活`}
                open={open}
                width={560}
                maskClosable={!starting && !toolProbeRunning}
                closable={!starting && !toolProbeRunning}
                onCancel={() => setOpen(false)}
                footer={[
                    <Button key="close" disabled={starting || toolProbeRunning} onClick={() => setOpen(false)}>关闭</Button>,
                    <Button key="run" type="primary" loading={starting} disabled={active || toolProbeRunning || !selectedModel} onClick={() => void start()}>
                        {active ? "测活进行中" : probe ? "重新测活" : "开始测活"}
                    </Button>,
                ]}
            >
                <div className="space-y-4">
                    <Alert
                        type="info"
                        showIcon
                        message="最小真实能力探针"
                        description="仅本次测活探针会执行一次带随机校验码的运维记录信息抽取，不发送 hi 或无意义重复内容；探针输出上限为 256 token，文本通过后自动追加的无副作用工具诊断上限为 512 token。这个上限只约束两次诊断请求，不代表创作台正式调用的输出或模型上下文上限。两次请求都会校验 SSE 事件完整性并观察正文是否分批、跨时间到达；可能产生极少量供应商费用，但不扣平台积分。"
                    />
                    <div>
                        <div className="mb-1.5 text-xs font-medium">文本模型</div>
                        <Select
                            className="w-full"
                            value={selectedModel || undefined}
                            disabled={active || toolProbeRunning}
                            onChange={setSelectedModel}
                            options={options.map((item) => ({ value: item.model, label: `${item.label || item.model} · ${modelProtocolLabel(item.protocol)}` }))}
                        />
                    </div>
                    {probe ? <ChannelProbeState probe={probe} /> : null}
                    {probe?.status === "succeeded" && probe.result?.ok ? (
                        <div className="rounded-md border border-border bg-foreground/[0.02] p-3">
                            <div className="flex flex-wrap items-center justify-between gap-2">
                                <div className="min-w-0">
                                    <div className="text-sm font-medium">创作台工具调用诊断</div>
                                    <div className="mt-1 text-xs leading-5 text-foreground/55">文本测活成功后会自动追加一个可恢复的固定工具诊断任务，带一份代表性画布参数结构（数组、嵌套字段和 metadata），只调用无副作用的 probe_extract_record，不执行画布写操作，输出上限为 512 token。这样“文本通过”不会再被误认为“创作台可用”。</div>
                                </div>
                                <Button size="small" loading={toolProbeRunning} disabled={active || starting || toolProbeRunning || probe.model !== selectedModel || probe.protocol !== options.find((item) => item.model === selectedModel)?.protocol} onClick={() => void runToolProbe()}>
                                    测试工具调用
                                </Button>
                            </div>
                            {toolProbe?.kind === "tool" && (toolProbe.status === "queued" || toolProbe.status === "running") ? <div className="mt-3 flex items-center gap-2 text-xs text-foreground/55"><Spin size="small" />{toolProbe.stage || "工具诊断进行中"}</div> : null}
                            {toolProbe?.kind === "tool" && toolProbe.status !== "queued" && toolProbe.status !== "running" ? <Alert className="mt-3" type={toolProbeSucceeded ? "success" : "error"} showIcon message={toolProbeSucceeded ? "Agent 工具调用通过" : "Agent 工具调用未通过"} description={toolProbeSucceeded ? `已收到 ${toolProbe.result?.toolName || "probe_extract_record"} 工具调用并校验参数；耗时 ${formatProbeDuration(toolProbe.result?.durationMs || 0)}，这条请求没有执行画布操作。` : toolProbe.error || "后台没有返回可校验的工具调用结果"} /> : null}
                            {!toolProbe && savedToolProbe ? <SavedToolProbeState summary={savedToolProbe} className="mt-3" /> : null}
                            {toolProbePollError ? <Alert className="mt-3" type="warning" showIcon message="工具诊断状态刷新暂时失败" description={`${toolProbePollError}；后台任务仍会继续，页面将自动重试。`} /> : null}
                        </div>
                    ) : null}
                    {!probe && savedToolProbe ? <SavedToolProbeState summary={savedToolProbe} /> : null}
                    {pollError ? <Alert type="warning" showIcon message="状态刷新暂时失败" description={`${pollError}；后台任务仍会继续，页面将自动重试。`} /> : null}
                    {active ? <div className="text-xs leading-5 text-foreground/50">当前运行的是低成本测活探针（输出上限 256 token），不是创作台正式请求；模型启动本身较慢时仍会按后台“文本任务超时”继续等待。关闭弹窗不会取消任务，也可以到任务中心查看“渠道测活”。</div> : null}
                </div>
            </Modal>
        </>
    );
}

function notifyToolProbeCompleted(probe: ChannelProbeStatus, success: (content: string) => void, failure: (content: string) => void) {
    if (probe.status === "succeeded" && probe.result?.ok) {
        success("Agent 工具调用诊断已通过，结果已显示在弹窗内");
        return;
    }
    failure("Agent 工具调用诊断未通过，请查看弹窗内的失败原因");
}

function SavedToolProbeState({ summary, className }: { summary: ChannelToolProbeSummary; className?: string }) {
    const checkedTime = Date.parse(summary.checkedAt || "");
    const age = Date.now() - checkedTime;
    const stale = !Number.isFinite(checkedTime)
        || age > 7 * 24 * 60 * 60 * 1_000
        || age < -5 * 60 * 1_000
        || summary.verifierVersion !== CHANNEL_PROBE_VERIFIER_VERSION;
    const checkedAt = Number.isFinite(checkedTime) ? new Date(checkedTime).toLocaleString("zh-CN") : "检查时间异常";
    if (stale) {
        return <Alert className={className} type="warning" showIcon message="最近一次工具诊断需要重测" description={`后台保存的结果已过期或版本不匹配；最近检查：${checkedAt}。`} />;
    }
    const succeeded = summary.status === "succeeded";
    return <Alert className={className} type={succeeded ? "success" : "error"} showIcon message={succeeded ? "最近一次 Agent 工具调用通过" : "最近一次 Agent 工具调用未通过"} description={`这是后台保存的最近一次诊断结论；检查时间：${checkedAt}。重新测试后会在这里更新。`} />;
}

function cloneProbeChannel(channel: ModelChannel): ModelChannel {
    return {
        ...channel,
        models: [...channel.models],
        modelCosts: channel.modelCosts?.map((item) => ({ ...item })),
    };
}

async function finalizeChannelProbe(
    channel: ModelChannel,
    probe: ChannelProbeStatus,
    onCompleted: ChannelProbeButtonProps["onCompleted"],
    warn: (content: string) => void,
) {
    if (probe.status === "succeeded" || probe.status === "failed") {
        try {
            recordChannelProbeReadiness(channel, {
                probeTaskId: probe.id,
                verifierVersion: probe.result?.verifierVersion,
                model: probe.model,
                protocol: probe.protocol,
                status: probe.status,
                transport: probe.result?.transport,
                durationMs: probe.result?.durationMs,
                checkedAt: probe.result?.checkedAt,
                completedAt: probe.completedAt,
                updatedAt: probe.updatedAt,
            });
        } catch {
            warn("测活已完成，但浏览器未能保存发送前风险提示状态");
        }
    }
    try {
        await onCompleted?.(probe);
    } catch {
        warn("测活已完成，但模型列表状态刷新失败，请手动刷新");
    }
}

async function finalizeChannelToolProbe(channel: ModelChannel, probe: ChannelProbeStatus, onCompleted: ChannelProbeButtonProps["onToolCompleted"], warn: (content: string) => void) {
    if (probe.kind !== "tool" || (probe.status !== "succeeded" && probe.status !== "failed")) return;
    try {
        recordChannelToolProbeReadiness(channel, {
            model: probe.model,
            protocol: probe.protocol,
            status: probe.status,
            toolCalling: probe.result?.toolCalling,
            checkedAt: probe.result?.checkedAt,
            completedAt: probe.completedAt,
            updatedAt: probe.updatedAt,
            probeTaskId: probe.id,
            verifierVersion: probe.result?.verifierVersion,
        });
    } catch {
        warn("工具诊断已完成，但浏览器未能保存工具调用结论；创作台会按未验证处理");
    }
    try {
        await onCompleted?.(probe);
    } catch {
        warn("工具诊断已完成，但模型列表状态刷新失败，请手动刷新");
    }
}

function channelProbeRequestIdentity(channel: ModelChannel, option: ChannelProbeModelOption, kind: "text" | "tool" = "text") {
    // 提交键永久绑定后端用户；内存 Map 也必须带作用域，避免切换账号后复用旧账号的键。
    // 连接配置变化后必须使用新的身份，否则响应丢失时会把旧提交键带到新
    // Base URL/凭据版本，后端只能拒绝并迫使用户再点一次测活。
    const configured = channel.modelCosts?.find((item) => item.model === option.model);
    const values = [
        getActiveUserScope(),
        channel.scope,
        channel.id || channel.name || "unsaved",
        channel.probeCredentialVersion || "initial",
        channel.apiFormat,
        configured?.probeCheckedAt || configured?.toolProbeCheckedAt || "",
        option.model,
        option.protocol,
    ];
    if (kind === "tool") values.unshift("tool");
    return values.map((value: string | undefined) => encodeURIComponent(value ?? "")).join(":");
}

function getOrCreateChannelProbeRequestKey(memory: Map<string, string>, identity: string) {
    const cached = memory.get(identity);
    if (cached) return cached;
    const storageKey = `mingwant:channel-probe-request:${identity}`;
    try {
        const stored = scopedLocalStorage.getItem(storageKey)?.trim();
        if (stored) {
            memory.set(identity, stored);
            return stored;
        }
    } catch {
        // 浏览器禁用持久存储时仍保留当前组件内的响应丢失保护。
    }
    const requestKey = createClientId();
    memory.set(identity, requestKey);
    try {
        scopedLocalStorage.setItem(storageKey, requestKey);
    } catch {
        // 内存键仍能覆盖双击和当前页面内的网络重发。
    }
    return requestKey;
}

function releaseChannelProbeRequestKey(memory: Map<string, string>, identity: string, requestKey: string) {
    if (memory.get(identity) === requestKey) memory.delete(identity);
    const storageKey = `mingwant:channel-probe-request:${identity}`;
    try {
        if (scopedLocalStorage.getItem(storageKey) === requestKey) scopedLocalStorage.removeItem(storageKey);
    } catch {
        // 清理失败只会让下一次点击先安全取回原任务，不会创建重复调用。
    }
}

function ChannelProbeState({ probe }: { probe: ChannelProbeStatus }) {
    if (probe.status === "queued" || probe.status === "running") {
        return <div className="flex items-center gap-3 rounded-md border border-border bg-foreground/[0.02] p-3"><Spin size="small" /><div><div className="text-sm font-medium">{probe.stage || "模型正在处理"}</div><div className="mt-1 text-xs text-foreground/45">{probe.model}</div></div></div>;
    }
    if (probe.status === "succeeded" && probe.result?.ok) {
        const streamed = probe.result.transport === "stream";
        const unverifiedStream = probe.result.transport === "stream-unverified";
        return (
            <Alert
                type={streamed ? "success" : "warning"}
                showIcon
                message={streamed ? "文本链路通过：已观察到渐进式 SSE 分片" : unverifiedStream ? "文本链路通过，但只确认了 SSE 格式" : "文本链路通过，但上游没有真正流式返回"}
                description={
                    <div className="space-y-2 text-xs">
                        <div>耗时 {formatProbeDuration(probe.result.durationMs)} · {probeTransportLabel(probe.result.transport)}</div>
                        {streamed ? <div>这只证明当前文本协议和 SSE 链路可用；在线 Agent 还需要上游支持 Function Calling 与 tool_choice。若创作台提示“没有返回工具调用”，本次请求已停止且不会执行画布操作。</div> : null}
                        {probe.result.firstByteMs !== undefined ? <div>供应商首字节 {formatProbeDuration(probe.result.firstByteMs)}{probe.result.streamReadCount ? ` · 正文读取 ${probe.result.streamReadCount} 批` : ""}{probe.result.deliverySpanMs !== undefined ? ` · 首尾数据间隔 ${formatProbeDuration(probe.result.deliverySpanMs)}` : ""}{probe.result.totalChunkWaitMs !== undefined ? ` · 后续分片累计等待 ${formatProbeDuration(probe.result.totalChunkWaitMs)}` : ""}{probe.result.longestChunkWaitMs !== undefined ? ` · 最长单次等待 ${formatProbeDuration(probe.result.longestChunkWaitMs)}` : ""}</div> : null}
                        {unverifiedStream ? <div>事件格式和结束标记都完整，但本次短响应没有观察到分片跨时间到达；可能是响应太快被网络合并，也可能是网关缓冲到最后一次性返回。它不能解锁长分镜，请检查代理缓冲后重新测活。</div> : null}
                        {!streamed && !unverifiedStream ? <div>短响应可以使用；慢推理时仍容易被供应商或 CDN 的网关时限截断，不建议直接用于长分镜或多轮 Agent。请优先改用真正支持 SSE 的接口或渠道。</div> : null}
                        {probe.result.responsePreview ? <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded bg-black/[0.04] p-2 text-[11px] dark:bg-white/[0.06]">{probe.result.responsePreview}</pre> : null}
                    </div>
                }
            />
        );
    }
    return <Alert type="error" showIcon message={probe.status === "cancelled" ? "测活已取消" : "测活未通过"} description={probe.error || "上游没有返回可校验结果"} />;
}

export function channelProbeModels(channel: ModelChannel): ChannelProbeModelOption[] {
    const seen = new Set<string>();
    return channel.models.flatMap((rawModel) => {
        const model = modelOptionName(rawModel);
        if (!model || seen.has(model)) return [];
        const configured = channel.modelCosts?.find((item) => item.model === model || item.model === rawModel);
        const protocol = configured?.protocol || channel.interfaceType;
        const capability = configured?.capability || modelProtocolCapability(protocol);
        if (capability ? capability !== "text" : !modelMatchesCapability(model, "text")) return [];
        // 未显式配置文本协议时，测活必须和创作台的自动协议一致；Chat Completions
        // 是常见 OpenAI 兼容网关与 Kimi 工具调用的共同路径，Responses 仍可显式选择。
        const textProtocol: ModelProtocol = protocol && modelProtocolCapability(protocol) === "text" ? protocol : defaultTextProtocolForChannel(channel, model, configured?.capability) || "chat-completion";
        seen.add(model);
        return [{ model, label: configured?.displayName || model, protocol: textProtocol }];
    });
}

function channelProbeDisabledReason(channel: ModelChannel, models: ChannelProbeModelOption[], userRole?: "admin" | "user") {
    if (!models.length) return "当前渠道没有配置文本模型及对应协议";
    // 系统渠道的密钥由 Backend 托管，测活写入共享模型状态并受管理员权限保护；
    // 普通用户如果仍看到可点击按钮，只会把 403 误认为模型或 Agent 故障。
    if (channel.scope === "system") return userRole === "admin" ? "" : "系统渠道测活由管理员统一维护；请使用已发布的模型状态，或切换到自己的渠道后测活";
    if (!channel.baseUrl.trim()) return "请先填写 Base URL";
    if (!channel.apiKey.trim()) return "请先填写 API Key";
    return "";
}

function formatProbeDuration(durationMs: number) {
    if (durationMs < 1_000) return `${Math.max(0, durationMs)}ms`;
    const seconds = Math.round(durationMs / 1_000);
    if (seconds < 60) return `${seconds} 秒`;
    return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}

function probeTransportLabel(value: string) {
    if (value === "stream") return "渐进流式接收";
    if (value === "stream-unverified") return "SSE 完整，但未观察到渐进分片";
    if (value === "non-stream-compatible") return "上游返回完整 JSON";
    if (value === "non-stream-fallback") return "上游明确不支持流式，已回退非流式";
    return value || "非流式接收";
}
