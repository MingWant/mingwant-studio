import { isGeminiModelProtocol } from "@/lib/model-protocols";
import { scopedLocalStorage } from "@/lib/user-scope";
import { defaultTextProtocolForChannel, modelOptionName, type ModelChannel } from "@/stores/use-config-store";

const CHANNEL_PROBE_READINESS_KEY = "mingwant:channel_probe_readiness";
const CHANNEL_PROBE_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1_000;
const CHANNEL_PROBE_MAX_RECORDS = 120;
export const CHANNEL_PROBE_VERIFIER_VERSION = "sse-progress-v3";

type StoredChannelProbe = {
    status: string;
    transport: string;
    durationMs: number;
    checkedAt: string;
    model?: string;
    protocol?: string;
    probeTaskId?: string;
    verifierVersion?: string;
    toolCalling?: string;
    toolProbeTaskId?: string;
    toolProbeCheckedAt?: string;
    toolProbeVerifierVersion?: string;
};

type StoredChannelProbeMap = Record<string, StoredChannelProbe>;

export type ChannelProbeReadiness = {
    state: "stream" | "non_stream" | "failed" | "stale" | "unverified";
    transport?: string;
    durationMs?: number;
    checkedAt?: string;
    probeTaskId?: string;
    verifierVersion?: string;
    /** 最近一次无副作用工具调用诊断的结论；未诊断时不作为强制门禁。 */
    toolCalling?: "supported" | "failed" | "stale" | "unverified";
    toolProbeTaskId?: string;
    toolProbeCheckedAt?: string;
    /** 当前模型/协议没有精确测活时，提供最近一次同渠道但不同绑定的测活对象，便于用户纠正选择。 */
    nearbyProbe?: {
        model: string;
        protocol: string;
        checkedAt: string;
    };
};

/** 返回长分镜的测活风险提示；它只用于费用确认文案，不是普通用户调用门禁。 */
export function longTextStreamingAdvisory(readiness: ChannelProbeReadiness, model: string, requireProbeTaskId = false, requireToolCalling = false) {
    const name = model.trim() || "当前文本模型";
    const toolCallingBlockReason = () => {
        if (!requireToolCalling || readiness.toolCalling === "supported") return "";
        if (readiness.toolCalling === "failed") return `${name} 最近一次 Function Calling 诊断未通过，管理员可重新测活；本次工具能力以真实模型响应为准`;
        if (readiness.toolCalling === "stale") return `${name} 的 Function Calling 诊断已过期，管理员可重新测活；本次工具能力以真实模型响应为准`;
        return `${name} 尚未完成 Function Calling 诊断；文本测活成功不等于创作台工具调用可用，本次仍可在明确预算下尝试`;
    };
    switch (readiness.state) {
        case "stream":
            if (requireProbeTaskId && (!readiness.probeTaskId || readiness.verifierVersion !== CHANNEL_PROBE_VERIFIER_VERSION)) return `${name} 的测活结论缺少当前版本可验证的完整性记录，管理员可重新测活`;
            return toolCallingBlockReason();
        case "non_stream":
            if (readiness.transport === "stream-unverified") return `${name} 最近一次只确认了完整 SSE 格式，但没有观察到响应分片跨时间到达，渠道可能仍在缓冲`;
            return `${name} 最近一次测活确认不是 SSE 流式响应，长分镜很容易被供应商或 CDN 以 524 截断`;
        case "failed":
            return `${name} 最近一次 LLM 测活未通过`;
        case "stale":
            return `${name} 的流式测活已超过 7 天或检查时间异常，供应商行为无法确认`;
        default:
            if (readiness.nearbyProbe) {
                return `${name} 尚未完成当前协议的渐进式 SSE 测活；最近一次测活的是 ${readiness.nearbyProbe.model}（${readiness.nearbyProbe.protocol}），管理员可为当前模型重新测活`;
            }
            return `${name} 尚未完成渐进式 SSE 测活`;
    }
}

/**
 * 部分 Kimi 兼容别名虽然能完成短文本或单函数探针，但长 JSON/工具请求的首包
 * 行为并不稳定。画布入口会把它们降级为一次有界的手动分镜整理；任务中心也必须
 * 使用同一条策略，避免用户从另一个入口再次撞上上游网关 524。
 */
export function prefersShortCinematicDelivery(model: string, interfaceType?: string) {
    if (interfaceType !== "chat-completion") return false;
    const normalized = model.trim().toLowerCase().replace(/[/:_]/g, "-");
    return [
        "kimi-k3-ls",
        "kimi-k2.7-code",
        "kimi-k2-7-code",
        "kimi-k27-code",
        "kimi-k2.6",
        "kimi-k2-6",
        "kimi-k26",
    ].some((alias) => normalized.includes(alias));
}

type ChannelProbeRecordInput = {
    model: string;
    protocol: string;
    status: string;
    transport?: string;
    durationMs?: number;
    checkedAt?: string;
    completedAt?: string;
    updatedAt?: string;
    probeTaskId?: string;
    verifierVersion?: string;
};

/** 保存的只是小型能力结论，不包含 API Key、提示词或供应商响应。 */
export function recordChannelProbeReadiness(channel: ModelChannel, probe: ChannelProbeRecordInput) {
    const checkedAt = probe.checkedAt || probe.completedAt || probe.updatedAt || new Date().toISOString();
    const records = readStoredChannelProbes();
    const key = channelProbeKey(channel, probe.model, probe.protocol);
    records[key] = {
        ...records[key],
        status: probe.status,
        transport: probe.transport || "",
        durationMs: Math.max(0, Number(probe.durationMs) || 0),
        checkedAt,
        model: modelOptionName(probe.model),
        protocol: normalizeProbeProtocol(channel, probe.model, probe.protocol),
        probeTaskId: probe.probeTaskId?.trim() || undefined,
        verifierVersion: probe.verifierVersion?.trim() || undefined,
    };
    persistStoredChannelProbes(records);
}

type ChannelToolProbeRecordInput = {
    model: string;
    protocol: string;
    status: string;
    toolCalling?: string;
    checkedAt?: string;
    completedAt?: string;
    updatedAt?: string;
    probeTaskId?: string;
    verifierVersion?: string;
};

/** 工具诊断只记录能力结论，不能把工具响应或供应商错误正文写入浏览器存储。 */
export function recordChannelToolProbeReadiness(channel: ModelChannel, probe: ChannelToolProbeRecordInput) {
    const checkedAt = probe.checkedAt || probe.completedAt || probe.updatedAt || new Date().toISOString();
    const records = readStoredChannelProbes();
    const key = channelProbeKey(channel, probe.model, probe.protocol);
    const toolCalling = probe.status === "succeeded" && probe.toolCalling === "supported" ? "supported" : "failed";
    records[key] = {
        ...records[key],
        status: records[key]?.status || "",
        transport: records[key]?.transport || "",
        durationMs: records[key]?.durationMs || 0,
        checkedAt: records[key]?.checkedAt || checkedAt,
        model: modelOptionName(probe.model),
        protocol: normalizeProbeProtocol(channel, probe.model, probe.protocol),
        probeTaskId: records[key]?.probeTaskId,
        verifierVersion: records[key]?.verifierVersion,
        toolCalling,
        toolProbeTaskId: probe.probeTaskId?.trim() || undefined,
        toolProbeCheckedAt: checkedAt,
        toolProbeVerifierVersion: probe.verifierVersion?.trim() || undefined,
    };
    persistStoredChannelProbes(records);
}

function persistStoredChannelProbes(records: StoredChannelProbeMap) {
    const trimmed = Object.fromEntries(
        Object.entries(records)
            .sort((left, right) => Date.parse(right[1].checkedAt) - Date.parse(left[1].checkedAt))
            .slice(0, CHANNEL_PROBE_MAX_RECORDS),
    );
    scopedLocalStorage.setItem(CHANNEL_PROBE_READINESS_KEY, JSON.stringify(trimmed));
}

export function resolveChannelProbeReadiness(channel: ModelChannel, model: string, protocol?: string): ChannelProbeReadiness {
    const modelName = modelOptionName(model);
    const configured = channel.modelCosts?.find((item) => item.model === modelName || item.model === model);
    const records = readStoredChannelProbes();
    const exactKey = channelProbeKey(channel, modelName, protocol);
    const stored = records[exactKey];
    const configuredReadiness = configured?.probeCheckedAt
        ? normalizeProbeReadiness({
            status: configured.probeStatus || "",
            transport: configured.probeTransport || "",
            durationMs: configured.probeDurationMs || 0,
            checkedAt: configured.probeCheckedAt,
            toolCalling: configured.toolProbeStatus === "succeeded" ? "supported" : configured.toolProbeStatus === "failed" ? "failed" : undefined,
            toolProbeCheckedAt: configured.toolProbeCheckedAt,
            toolProbeVerifierVersion: configured.toolProbeVerifierVersion,
        }, false)
        : undefined;
    if (stored && channel.scope === "system" && configuredReadiness) {
        // 系统模型状态由 Backend 共享维护；本地旧绿灯不能覆盖后来真实请求观察到的降级。
        const storedAt = Date.parse(stored.checkedAt);
        const configuredAt = Date.parse(configured?.probeCheckedAt || "");
        if (!Number.isFinite(storedAt) || !Number.isFinite(configuredAt) || configuredAt >= storedAt) return withToolProbe(configuredReadiness, stored);
    }
    if (channel.scope === "system") {
        // 系统渠道的 Base URL/API Key 不会下发到浏览器，配置变化后前端无法靠本地
        // 指纹自证旧结论仍属于当前连接；没有 Backend 当前模型状态时必须回到未验证，
        // 否则旧标签页的绿色测活会误导 Agent 继续出站。
        return { state: "unverified", nearbyProbe: findNearbyProbe(channel, modelName, protocol, records) };
    }
    // 自定义渠道的模型列表只保存简化状态，不能覆盖当前标签页刚保存的完整诊断记录；
    // 任务 ID 仍可随请求携带供后台审计，但不作为普通用户调用授权。
    if (stored) return normalizeProbeReadiness(stored, true);
    if (configuredReadiness) return configuredReadiness;
    const nearbyProbe = findNearbyProbe(channel, modelName, protocol, records);
    return nearbyProbe ? { state: "unverified", nearbyProbe } : { state: "unverified" };
}

function normalizeProbeReadiness(probe: StoredChannelProbe, requireVerifierVersion: boolean): ChannelProbeReadiness {
    const checkedTime = Date.parse(probe.checkedAt);
    if (!Number.isFinite(checkedTime)) return { state: "unverified" };
    const detail = { transport: probe.transport, durationMs: probe.durationMs, checkedAt: probe.checkedAt, probeTaskId: probe.probeTaskId, verifierVersion: probe.verifierVersion, ...toolProbeDetail(probe) };
    const age = Date.now() - checkedTime;
    if (age > CHANNEL_PROBE_MAX_AGE_MS || age < -5 * 60 * 1_000) return { state: "stale", ...detail };
    if (probe.status !== "succeeded") return { state: "failed", ...detail };
    if (requireVerifierVersion && probe.verifierVersion !== CHANNEL_PROBE_VERIFIER_VERSION) return { state: "stale", ...detail };
    return { state: probe.transport === "stream" ? "stream" : "non_stream", ...detail };
}

function withToolProbe(readiness: ChannelProbeReadiness, probe: StoredChannelProbe): ChannelProbeReadiness {
    // 系统渠道的共享模型目录是管理员发布的事实源；本地旧记录不能覆盖已经
    // 持久化的当前工具结论。只有服务端尚未保存工具状态时，才使用本地标签页记录。
    if (readiness.toolCalling && readiness.toolCalling !== "unverified") return readiness;
    return { ...readiness, ...toolProbeDetail(probe) };
}

function toolProbeDetail(probe: StoredChannelProbe): Pick<ChannelProbeReadiness, "toolCalling" | "toolProbeTaskId" | "toolProbeCheckedAt"> {
    const checkedAt = probe.toolProbeCheckedAt || "";
    if (!checkedAt || !Number.isFinite(Date.parse(checkedAt))) return { toolCalling: "unverified" };
    const age = Date.now() - Date.parse(checkedAt);
    // 工具 Schema、参数合并和响应校验变更后，缺少版本号的旧绿灯不能继续代表当前 Agent 能力。
    if (age > CHANNEL_PROBE_MAX_AGE_MS || age < -5 * 60 * 1_000 || probe.toolProbeVerifierVersion !== CHANNEL_PROBE_VERIFIER_VERSION) return { toolCalling: "stale", toolProbeCheckedAt: checkedAt, toolProbeTaskId: probe.toolProbeTaskId };
    return {
        toolCalling: probe.toolCalling === "supported" ? "supported" : "failed",
        toolProbeCheckedAt: checkedAt,
        toolProbeTaskId: probe.toolProbeTaskId,
    };
}

function channelProbeKey(channel: ModelChannel, model: string, protocol?: string) {
    return JSON.stringify(channelProbeKeyParts(channel, model, protocol));
}

function channelProbeKeyParts(channel: ModelChannel, model: string, protocol?: string) {
    const modelName = modelOptionName(model);
    const configured = channel.modelCosts?.find((item) => item.model === modelName || item.model === model);
    const resolvedProtocol = (protocol || defaultTextProtocolForChannel(channel, modelName, configured?.capability) || "chat-completion").trim().toLowerCase();
    // 测活请求会按模型最终协议选择 API 格式；不能继续使用渠道默认格式作为
    // 本地索引，否则模型级协议覆盖（例如 OpenAI 渠道上的 Gemini 文本协议）
    // 会出现“测活成功但创作台查不到绿灯”，从而在真正调用前被误拦截。
    const resolvedApiFormat = isGeminiModelProtocol(resolvedProtocol) ? "gemini" : "openai";
    return [
        channel.scope || "user",
        channel.id,
        // 不保存 API Key 本身；凭据变化时由渠道编辑器轮换此随机版本，使旧结论立即失效。
        channel.probeCredentialVersion || "initial",
        channel.baseUrl.trim().replace(/\/+$/, ""),
        resolvedApiFormat,
        resolvedProtocol,
        modelOptionName(model),
    ] as const;
}

function normalizeProbeProtocol(channel: ModelChannel, model: string, protocol?: string) {
    return channelProbeKeyParts(channel, model, protocol)[5];
}

function findNearbyProbe(channel: ModelChannel, model: string, protocol: string | undefined, records: StoredChannelProbeMap) {
    const target = channelProbeKeyParts(channel, model, protocol);
    const candidates = Object.entries(records).flatMap(([key, probe]) => {
        let parts: unknown;
        try {
            parts = JSON.parse(key);
        } catch {
            return [];
        }
        if (!Array.isArray(parts) || parts.length < 7) return [];
        const sameChannel = parts.slice(0, 5).every((value, index) => value === target[index]);
        const differentBinding = parts[5] !== target[5] || parts[6] !== target[6];
        const parsedModel = typeof parts[6] === "string" ? parts[6].trim() : "";
        const parsedProtocol = typeof parts[5] === "string" ? parts[5].trim() : "";
        const checkedAt = typeof probe.checkedAt === "string" ? probe.checkedAt : "";
        if (!sameChannel || !differentBinding || !parsedModel || !parsedProtocol || !checkedAt || !Number.isFinite(Date.parse(checkedAt))) return [];
        return [{ model: parsedModel, protocol: parsedProtocol, checkedAt }];
    });
    candidates.sort((left, right) => Date.parse(right.checkedAt) - Date.parse(left.checkedAt));
    return candidates[0];
}

function readStoredChannelProbes(): StoredChannelProbeMap {
    let raw: string | null;
    try {
        raw = scopedLocalStorage.getItem(CHANNEL_PROBE_READINESS_KEY);
    } catch {
        // 隐私模式或扩展拦截存储时，测活结论只能降级为未验证提示，不能让创作台点击直接抛出存储异常。
        return {};
    }
    if (!raw) return {};
    try {
        const parsed = JSON.parse(raw);
        return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as StoredChannelProbeMap : {};
    } catch {
        try {
            scopedLocalStorage.removeItem(CHANNEL_PROBE_READINESS_KEY);
        } catch {
            // 清理失败不影响本次按未验证处理。
        }
        return {};
    }
}
