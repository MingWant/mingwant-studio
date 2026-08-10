import { useMemo } from "react";
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { nanoid } from "nanoid";

import { aiConfigStorage } from "@/lib/ai-config-storage";
import type { ModelCapabilityConfig } from "@/lib/model-capabilities";
import { isGeminiModelProtocol, modelProtocolCapability, normalizeModelProtocol, type ModelProtocol } from "@/lib/model-protocols";
import { normalizeVideoDuration, normalizeVideoResolution } from "@/lib/video-generation-options";

export type ApiCallFormat = "openai" | "gemini";
export type ChannelInterfaceType = ModelProtocol;

export type ModelChannel = {
    id: string;
    name: string;
    baseUrl: string;
    apiKey: string;
    rememberApiKey?: boolean;
    probeCredentialVersion?: string;
    apiFormat: ApiCallFormat;
    interfaceType?: ChannelInterfaceType;
    models: string[];
    scope?: "system" | "user";
    enabled?: boolean;
    hasApiKey?: boolean;
    concurrencyLimit?: number;
    modelCosts?: Array<{
        model: string;
        displayName?: string;
        capability: ModelCapability;
        protocol?: ModelProtocol;
        capabilityVersion?: number;
        capabilityConfig?: ModelCapabilityConfig;
        billingMode: "fixed_request" | "per_second";
        unitPriceMicrocredits: number;
        probeStatus?: "succeeded" | "failed" | string;
        probeTransport?: string;
        probeDurationMs?: number;
        probeCheckedAt?: string;
        toolProbeStatus?: "succeeded" | "failed" | string;
        toolProbeCheckedAt?: string;
        toolProbeVerifierVersion?: string;
    }>;
};

export type AiConfig = {
    channelMode: "remote" | "local";
    baseUrl: string;
    apiKey: string;
    apiFormat: ApiCallFormat;
    channels: ModelChannel[];
    model: string;
    imageModel: string;
    videoModel: string;
    textModel: string;
    audioModel: string;
    audioVoice: string;
    audioFormat: string;
    audioSpeed: string;
    audioInstructions: string;
    videoSeconds: string;
    vquality: string;
    videoGenerateAudio: string;
    videoWatermark: string;
    systemPrompt: string;
    models: string[];
    imageModels: string[];
    videoModels: string[];
    textModels: string[];
    audioModels: string[];
    quality: string;
    size: string;
    transparentBackground: string;
    count: string;
    canvasImageCount: string;
};

export const CONFIG_STORE_KEY = "open_ai_canvas:ai_config_store";
export type ModelCapability = "image" | "video" | "text" | "audio";
const CHANNEL_MODEL_SEPARATOR = "::";
const OPENAI_BASE_URL = "https://api.openai.com";
const GEMINI_BASE_URL = "https://generativelanguage.googleapis.com";

export const defaultConfig: AiConfig = {
    channelMode: "local",
    baseUrl: OPENAI_BASE_URL,
    apiKey: "",
    apiFormat: "openai",
    channels: [
        {
            id: "default",
            name: "默认渠道",
            baseUrl: OPENAI_BASE_URL,
            apiKey: "",
            rememberApiKey: false,
            apiFormat: "openai",
            models: ["gpt-image-2", "grok-imagine-video", "gpt-5.5", "gpt-4o-mini-tts"],
        },
    ],
    model: "default::gpt-image-2",
    imageModel: "default::gpt-image-2",
    videoModel: "default::grok-imagine-video",
    textModel: "default::gpt-5.5",
    audioModel: "default::gpt-4o-mini-tts",
    audioVoice: "alloy",
    audioFormat: "mp3",
    audioSpeed: "1",
    audioInstructions: "",
    videoSeconds: "6",
    vquality: "720",
    videoGenerateAudio: "true",
    videoWatermark: "false",
    systemPrompt: "",
    models: ["default::gpt-image-2", "default::grok-imagine-video", "default::gpt-5.5", "default::gpt-4o-mini-tts"],
    imageModels: ["default::gpt-image-2"],
    videoModels: ["default::grok-imagine-video"],
    textModels: ["default::gpt-5.5"],
    audioModels: ["default::gpt-4o-mini-tts"],
    quality: "auto",
    size: "1:1",
    transparentBackground: "false",
    count: "1",
    canvasImageCount: "1",
};

type ConfigStore = {
    config: AiConfig;
    updateConfig: <K extends keyof AiConfig>(key: K, value: AiConfig[K]) => void;
    replaceConfig: (config: AiConfig) => void;
    mergeSystemChannels: (channels: ModelChannel[]) => void;
    isAiConfigReady: (config: AiConfig, model: string) => boolean;
};

export type ConfigStoreSnapshot = {
    config?: Partial<AiConfig>;
};

function isVideoModelName(model: string) {
    const value = modelOptionName(model).toLowerCase();
    return value.includes("seedance") || value.includes("video") || value.includes("sora") || value.includes("veo") || value.includes("kling") || value.includes("wan") || value.includes("hailuo");
}

function isImageModelName(model: string) {
    const value = modelOptionName(model).toLowerCase();
    return (
        !isVideoModelName(model) &&
        !isAudioModelName(model) &&
        (value.includes("seedream") ||
            value.includes("gpt-image") ||
            value.includes("image") ||
            value.includes("dall-e") ||
            value.includes("dalle") ||
            value.includes("imagen") ||
            value.includes("flux") ||
            value.includes("sdxl") ||
            value.includes("stable-diffusion") ||
            value.includes("midjourney"))
    );
}

function isAudioModelName(model: string) {
    const value = modelOptionName(model).toLowerCase();
    return value.includes("audio") || value.includes("tts") || value.includes("speech") || value.includes("voice") || value.includes("music") || value.includes("sound");
}

function isTextModelName(model: string) {
    return !isImageModelName(model) && !isVideoModelName(model) && !isAudioModelName(model);
}

export function modelMatchesCapability(model: string, capability?: ModelCapability) {
    if (!capability) return true;
    if (capability === "image") return isImageModelName(model);
    if (capability === "video") return isVideoModelName(model);
    if (capability === "audio") return isAudioModelName(model);
    return isTextModelName(model);
}

export function filterModelsByCapability(models: string[], capability?: ModelCapability, channels?: ModelChannel[]) {
    if (!capability) return models;
    return models.filter((model) => {
        const decoded = decodeChannelModel(model);
        const channel = decoded ? channels?.find((item) => item.id === decoded.channelId) : undefined;
        const configuredCapability = channel?.modelCosts?.find((item) => item.model === decoded?.model)?.capability;
        if (configuredCapability) return configuredCapability === capability;
        const channelCapability = capabilityForChannelInterface(channel?.interfaceType);
        return channelCapability ? channelCapability === capability : modelMatchesCapability(model, capability);
    });
}

export function selectableModelsByCapability(config: AiConfig, capability?: ModelCapability) {
    if (!capability) return config.models;
    return filterModelsByCapability(config.models, capability, config.channels);
}

export function configuredModelMatchesCapability(config: AiConfig, model: string, capability?: ModelCapability) {
    const normalized = normalizeModelOptionValue(model, config.channels);
    if (!normalized || !config.models.includes(normalized)) return false;
    return capability ? selectableModelsByCapability(config, capability).includes(normalized) : true;
}

function isAiConfigReady(config: AiConfig, model: string) {
    // 不能只检查渠道有密钥；旧画布或已删模型的裸名称会被 resolveModelChannel 误投到第一条渠道。
    const normalizedModel = normalizeModelOptionValue(model, config.channels);
    if (!normalizedModel || !config.models.includes(normalizedModel)) return false;
    const channel = resolveModelChannel(config, normalizedModel);
    return Boolean(channel.baseUrl.trim() && channel.apiKey.trim());
}

export const useConfigStore = create<ConfigStore>()(
    persist(
        (set) => ({
            config: defaultConfig,
            updateConfig: (key, value) =>
                set((state) => ({
                    config: {
                        ...state.config,
                        [key]: value,
                    },
                })),
            replaceConfig: (config) => set({ config }),
            mergeSystemChannels: (channels) =>
                set((state) => {
                    const systemChannels = channels.map((channel, index) =>
                        createModelChannel({
                            ...channel,
                            id: channel.id || `system-${index + 1}`,
                            name: channel.name || `系统渠道 ${index + 1}`,
                            scope: "system",
                            apiKey: channel.apiKey || "system",
                        }),
                    );
                    const userChannels = state.config.channels.filter((channel) => channel.scope !== "system");
                    return normalizeConfigSnapshot({ config: { ...state.config, channels: [...systemChannels, ...userChannels] } });
                }),
            isAiConfigReady: (config, model) => isAiConfigReady(config, model),
        }),
        {
            name: CONFIG_STORE_KEY,
            storage: createJSONStorage(() => aiConfigStorage),
            // 必须先由服务端确认当前账号，再按账号 scope 恢复配置和密钥，避免启动阶段短暂加载上一账号凭据。
            skipHydration: true,
            partialize: (state) => ({ config: state.config }),
            merge: (persisted, current) => {
                const persistedState = (persisted || {}) as Partial<ConfigStore>;
                return {
                    ...current,
                    ...normalizeConfigSnapshot({ config: persistedState.config }),
                };
            },
        },
    ),
);

export function normalizeConfigSnapshot(snapshot: ConfigStoreSnapshot) {
    const persistedConfig = (snapshot.config || {}) as Partial<AiConfig>;
    const config = { ...defaultConfig, ...persistedConfig };
    const hasPersistedChannels = Array.isArray(persistedConfig.channels);
    if (!hasPersistedChannels) config.channels = [];
    const channels = normalizeChannels(config, !hasPersistedChannels);
    const models = modelOptionsFromChannels(channels);
    const imageModels = filterModelsByCapability(models, "image", channels);
    const videoModels = filterModelsByCapability(models, "video", channels);
    const textModels = filterModelsByCapability(models, "text", channels);
    const audioModels = filterModelsByCapability(models, "audio", channels);
    const model = normalizeSelectedModel(config.model || config.imageModel || config.textModel, channels, models);
    return {
        config: {
            ...config,
            channelMode: "local" as const,
            apiFormat: normalizeApiFormat(config.apiFormat),
            channels,
            models,
            model,
            imageModel: normalizeSelectedModel(config.imageModel || model, channels, imageModels),
            videoModel: normalizeSelectedModel(config.videoModel || "grok-imagine-video", channels, videoModels),
            textModel: normalizeSelectedModel(config.textModel || model, channels, textModels),
            audioModel: normalizeSelectedModel(config.audioModel || defaultConfig.audioModel, channels, audioModels),
            audioVoice: config.audioVoice || defaultConfig.audioVoice,
            audioFormat: config.audioFormat || defaultConfig.audioFormat,
            audioSpeed: config.audioSpeed || defaultConfig.audioSpeed,
            audioInstructions: config.audioInstructions || "",
            videoSeconds: normalizeVideoDuration(config.videoSeconds),
            vquality: normalizeVideoResolution(config.vquality),
            videoGenerateAudio: config.videoGenerateAudio || "true",
            videoWatermark: config.videoWatermark || "false",
            transparentBackground: config.transparentBackground === "true" ? "true" : "false",
            canvasImageCount: config.canvasImageCount || defaultConfig.canvasImageCount,
            imageModels,
            videoModels,
            textModels,
            audioModels,
        },
    };
}

function normalizeSelectedModel(value: string, channels: ModelChannel[], options: string[]) {
    const model = normalizeModelOptionValue(value, channels);
    if (model && options.includes(model)) return model;
    const raw = typeof value === "string" ? value.trim() : "";
    // 带前缀的旧值曾经明确绑定过某个渠道；渠道被删除后不能把它当成裸模型名，
    // 否则刷新系统渠道会把用户静默切到 options[0]，测活和实际请求就会漂移。
    if (raw && isChannelModelValue(raw)) return "";
    // 旧配置只保存裸模型名且该名称跨渠道重复时，宁可要求重新选择，也不静默绑定第一条渠道。
    if (raw && !isChannelModelValue(raw) && channels.filter((channel) => channel.models.includes(raw)).length > 1) return "";
    return options[0] || "";
}

export function useEffectiveConfig() {
    const config = useConfigStore((state) => state.config);
    return useMemo(() => ({ ...config, channelMode: "local" as const }), [config]);
}

export function createModelChannel(channel?: Partial<ModelChannel>): ModelChannel {
    const apiFormat = normalizeApiFormat(channel?.apiFormat);
    const interfaceType = normalizeChannelInterfaceType(channel?.interfaceType);
    const providedBaseUrl = channel?.baseUrl?.trim();
    return {
        id: channel?.id?.trim() || nanoid(),
        name: channel?.name?.trim() || "新渠道",
        baseUrl: providedBaseUrl || (interfaceType ? defaultBaseUrlForChannelInterface(interfaceType) : defaultBaseUrlForApiFormat(apiFormat)),
        apiKey: channel?.apiKey || "",
        rememberApiKey: channel?.rememberApiKey === true,
        probeCredentialVersion: channel?.probeCredentialVersion,
        apiFormat,
        interfaceType,
        models: uniqueRawModels(channel?.models || []),
        scope: channel?.scope === "system" ? "system" : "user",
        enabled: channel?.enabled !== false,
        hasApiKey: channel?.hasApiKey,
        modelCosts: channel?.modelCosts?.map((item) => ({ ...item, protocol: normalizeModelProtocol(item.protocol) })),
    };
}

export function encodeChannelModel(channelId: string, model: string) {
    return `${channelId}${CHANNEL_MODEL_SEPARATOR}${model.trim()}`;
}

export function isChannelModelValue(value: string) {
    return value.includes(CHANNEL_MODEL_SEPARATOR);
}

export function decodeChannelModel(value: string) {
    const index = value.indexOf(CHANNEL_MODEL_SEPARATOR);
    if (index < 0) return null;
    return { channelId: value.slice(0, index), model: value.slice(index + CHANNEL_MODEL_SEPARATOR.length) };
}

export function modelOptionName(value: string) {
    return decodeChannelModel(value)?.model || value;
}

export function modelDisplayName(config: AiConfig, value: string) {
    const model = modelOptionName(value);
    const channel = resolveModelChannel(config, value);
    return channel.modelCosts?.find((item) => item.model === model)?.displayName?.trim() || model;
}

export function modelOptionLabel(config: AiConfig, value: string) {
    const decoded = decodeChannelModel(value);
    const channel = resolveModelChannel(config, value);
    const displayName = modelDisplayName(config, value);
    if (channel.id === "__unresolved_model__") return `需重新选择：${displayName}`;
    if (!decoded) return displayName;
    return `${displayName}（${channel.name}）`;
}

export function modelOptionsFromChannels(channels: ModelChannel[]) {
    return uniqueModelOptions(
        channels.flatMap((channel) =>
            channel.models
                .map(normalizeRawModelName)
                .filter(Boolean)
                .filter((model) => channel.scope !== "system" || hasSystemModelPrice(channel, model))
                .map((model) => encodeChannelModel(channel.id, model)),
        ),
    );
}

export function hasSystemModelPrice(channel: ModelChannel, model: string) {
    if (channel.scope !== "system") return true;
    return channel.modelCosts?.some((item) => item.model === model && Number.isFinite(item.unitPriceMicrocredits) && item.unitPriceMicrocredits >= 0) === true;
}

export function normalizeModelOptionValue(value: unknown, channels: ModelChannel[]) {
    const model = typeof value === "string" ? value.trim() : "";
    if (!normalizeRawModelName(model)) return "";
    const decoded = decodeChannelModel(model);
    if (decoded) {
        const channel = channels.find((item) => item.id === decoded.channelId);
        return channel && channel.models.includes(decoded.model) ? model : "";
    }
    const matches = channels.filter((item) => item.models.includes(model));
    // 裸模型名在多渠道中不具备归属信息；不能猜第一条渠道，否则测活通过的渠道
    // 可能与创作台实际请求的渠道不同。调用方会回退到当前已绑定的默认模型或提示重新选择。
    return matches.length === 1 ? encodeChannelModel(matches[0].id, model) : "";
}

export function resolveModelChannel(config: AiConfig, value: string) {
    const decoded = decodeChannelModel(value);
    if (decoded) {
        // 带渠道前缀的模型已经是一次明确绑定；渠道被删除或模型被下架时，
        // 不能回退到第一条渠道，否则测活通过的配置会被静默换成另一条 Base URL。
        return config.channels.find((channel) => channel.id === decoded.channelId) || unresolvedModelChannel(config);
    }
    const model = value.trim();
    const matches = config.channels.filter((channel) => channel.models.includes(model));
    if (matches.length === 1) return matches[0];
    if (matches.length > 1) {
        // 旧数据里的裸模型名在多渠道中没有归属信息，必须要求用户重新选择。
        return unresolvedModelChannel(config);
    }
    if (model && config.channels.length) {
        // 模型已从所有当前渠道移除时也不能借用第一条渠道的凭据。
        return unresolvedModelChannel(config);
    }
    return config.channels[0] || createModelChannel({ id: "default", name: "默认渠道", baseUrl: config.baseUrl, apiKey: config.apiKey, apiFormat: config.apiFormat, models: config.models.map(modelOptionName) });
}

function unresolvedModelChannel(config: AiConfig): ModelChannel {
    return {
        id: "__unresolved_model__",
        name: "模型需重新选择",
        baseUrl: "",
        apiKey: "",
        apiFormat: config.apiFormat,
        models: [],
        scope: "user",
        enabled: false,
    };
}

export function resolveModelRequestConfig(config: AiConfig, value: string) {
    const requestedValue = value || config.model;
    const normalizedValue = normalizeModelOptionValue(requestedValue, config.channels);
    const selectedValue = normalizedValue || requestedValue;
    const channel = resolveModelChannel(config, selectedValue);
    const model = modelOptionName(selectedValue);
    const modelCost = channel.modelCosts?.find((item) => item.model === model);
    const modelProtocol = modelCost?.protocol;
    // 渠道默认协议只对同能力模型有效；混合渠道里文本模型不能继承视频协议，
    // 否则测活会按 Chat 通过，创作台却把同一模型发到错误的请求路径。
    const channelProtocol = channel.interfaceType;
    const modelProtocolMatchesCapability = !modelCost?.capability || !modelProtocol || modelProtocolCapability(modelProtocol) === modelCost.capability;
    const channelProtocolMatchesCapability = !modelCost?.capability || !channelProtocol || modelProtocolCapability(channelProtocol) === modelCost.capability;
    const interfaceType = (modelProtocolMatchesCapability ? modelProtocol : undefined) || (channelProtocolMatchesCapability ? channelProtocol : undefined) || defaultTextProtocolForChannel(channel, model, modelCost?.capability);
    return {
        ...config,
        model,
        baseUrl: channel.baseUrl,
        apiKey: channel.apiKey,
        apiFormat: interfaceType ? (isGeminiModelProtocol(interfaceType) ? "gemini" as const : "openai" as const) : channel.apiFormat,
        interfaceType,
        // 仅供前端在一轮 Agent/影视会话中复用已解析渠道；后端请求体不会透传该内部字段，
        // 自定义渠道不能把它当作系统 channelId 使用。
        resolvedChannelId: channel.id,
        channelId: channel.scope === "system" ? channel.id : "",
    };
}

/**
 * 自动协议只在没有模型级/渠道级声明时生效；Chat Completions 是 OpenAI 兼容
 * 网关和 Kimi 工具调用的共同交集，Responses 仍可由用户显式选择。
 */
export function defaultTextProtocolForChannel(channel: Pick<ModelChannel, "apiFormat">, model: string, capability?: ModelCapability): ChannelInterfaceType | undefined {
    const resolvedCapability = capability || (modelMatchesCapability(model, "text") ? "text" : undefined);
    if (resolvedCapability !== "text") return undefined;
    return channel.apiFormat === "gemini" ? "gemini-content" : "chat-completion";
}

function normalizeChannels(config: AiConfig, ensureDefault = true) {
    const persistedChannels = Array.isArray(config.channels) ? config.channels : [];
    const channels = persistedChannels
        .map((channel, index) =>
            createModelChannel({
                ...channel,
                id: channel.id || (index === 0 ? "default" : `channel-${index + 1}`),
                name: channel.name || (index === 0 ? "默认渠道" : `渠道 ${index + 1}`),
                models: uniqueRawModels(channel.models || []),
            }),
        )
        .filter((channel) => !isEmptyDefaultChannel(channel));
    if (!channels.length && ensureDefault && config.apiKey.trim()) {
        channels.push(
            createModelChannel({
                id: "default",
                name: "默认渠道",
                baseUrl: config.baseUrl || defaultConfig.baseUrl,
                apiKey: config.apiKey || "",
                apiFormat: config.apiFormat || defaultConfig.apiFormat,
                models: uniqueRawModels([...(config.models || []), config.model, config.imageModel, config.videoModel, config.textModel, config.audioModel]),
            }),
        );
    }
    return channels.map((channel) => ({ ...channel, models: uniqueRawModels(channel.models) }));
}

function isEmptyDefaultChannel(channel: ModelChannel) {
    if (channel.scope === "system") return false;
    if (channel.id !== "default" || channel.name.trim() !== "默认渠道" || channel.apiKey.trim()) return false;
    const baseUrl = channel.baseUrl.trim().replace(/\/+$/, "");
    const defaultBaseUrl = defaultConfig.baseUrl.trim().replace(/\/+$/, "");
    if (baseUrl && baseUrl !== defaultBaseUrl) return false;
    const defaultModels = new Set((defaultConfig.channels[0]?.models || []).map(modelOptionName));
    return !channel.models.length || channel.models.every((model) => defaultModels.has(modelOptionName(model)));
}

export function defaultBaseUrlForApiFormat(apiFormat: ApiCallFormat) {
    return apiFormat === "gemini" ? GEMINI_BASE_URL : OPENAI_BASE_URL;
}

export function defaultBaseUrlForChannelInterface(interfaceType?: ChannelInterfaceType) {
    if (isGeminiModelProtocol(interfaceType)) return GEMINI_BASE_URL;
    if (interfaceType === "newapi" || interfaceType === "newapi-channel-1" || interfaceType === "newapi-channel-2" || interfaceType === "xai-image" || interfaceType === "xai-video") return "";
    return OPENAI_BASE_URL;
}

function capabilityForChannelInterface(interfaceType?: ChannelInterfaceType): ModelCapability | undefined {
    return modelProtocolCapability(interfaceType);
}

function normalizeApiFormat(apiFormat: unknown): ApiCallFormat {
    return apiFormat === "gemini" ? "gemini" : "openai";
}

function normalizeChannelInterfaceType(value: unknown): ChannelInterfaceType | undefined {
    return normalizeModelProtocol(value);
}

function uniqueRawModels(models: string[]) {
    return Array.from(new Set((models || []).map(normalizeRawModelName).filter(Boolean)));
}

function uniqueModelOptions(models: string[]) {
    return Array.from(new Set((models || []).filter((model): model is string => typeof model === "string").map((model) => model.trim()).filter(Boolean)));
}

function normalizeRawModelName(value: unknown) {
    if (typeof value !== "string") return "";
    const model = modelOptionName(value).trim();
    return model && model !== "undefined" && model !== "null" ? model : "";
}

export function buildApiUrl(baseUrl: string, path: string) {
    // 先判断原始相对系统代理路径。开发环境把 VITE_CANVAS_BACKEND_URL 指向
    // 另一端口时，解析后的绝对地址会变成跨源 URL；若此时再按“第三方地址”判断，
    // 会错误追加 /v1，把 /api/ai/system/<id>/chat/completions 发成不存在的路径。
    const systemProxy = isSystemProxyBaseUrl(baseUrl);
    let normalizedBaseUrl = resolveBackendApiUrl(baseUrl).replace(/\/+$/, "");
    normalizedBaseUrl = normalizeArkPlanBaseUrl(normalizedBaseUrl);
    const lowerBaseUrl = normalizedBaseUrl.toLowerCase();
    // 与 Backend 的 apiURL 保持同一套版本根识别；/v1beta 常见于兼容网关，
    // 若遗漏会被错误拼成 /v1beta/v1/chat/completions。
    const apiBaseUrl = systemProxy || isSystemProxyBaseUrl(normalizedBaseUrl) || lowerBaseUrl.endsWith("/v1") || lowerBaseUrl.endsWith("/v1beta") || lowerBaseUrl.endsWith("/api/v3") || lowerBaseUrl.endsWith("/api/plan/v3") ? normalizedBaseUrl : `${normalizedBaseUrl}/v1`;
    return `${apiBaseUrl}${path}`;
}

export function resolveBackendApiUrl(value: string) {
    const url = value.trim();
    if (!url.startsWith("/api/")) return url;
    const backendBaseUrl = String(import.meta.env.VITE_CANVAS_BACKEND_URL || "/api").trim().replace(/\/+$/, "");
    return backendBaseUrl === "/api" ? url : `${backendBaseUrl}${url.slice("/api".length)}`;
}

export function isSystemProxyBaseUrl(baseUrl: string) {
    const value = baseUrl.trim();
    if (!value) return false;
    let pathname = value;
    if (/^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(value)) {
        // 系统渠道通常保存为同源相对路径；兼容同源绝对地址时也必须核对
        // origin，不能因为自定义 Base URL 恰好包含同一段路径就绕过后端中转，
        // 否则浏览器会把用户 API Key 直接发给第三方主机。
        if (typeof window === "undefined") return false;
        try {
            const url = new URL(value, window.location.origin);
            if (url.origin !== window.location.origin || url.username || url.password || url.search || url.hash) return false;
            pathname = url.pathname;
        } catch {
            return false;
        }
    } else if (!value.startsWith("/")) {
        return false;
    }
    const match = pathname.match(/^\/api\/ai\/system\/([^/]+)$/i);
    return Boolean(match?.[1]);
}

function normalizeArkPlanBaseUrl(baseUrl: string) {
    try {
        const url = new URL(baseUrl);
        const path = url.pathname.replace(/\/+$/, "");
        const lowerPath = path.toLowerCase();
        const arkPlanIndex = lowerPath.indexOf("/api/plan/v3");
        if (arkPlanIndex < 0) return baseUrl;
        const end = arkPlanIndex + "/api/plan/v3".length;
        if (lowerPath.length !== end && lowerPath[end] !== "/") return baseUrl;
        url.pathname = path.slice(0, end);
        url.search = "";
        url.hash = "";
        return url.toString().replace(/\/+$/, "");
    } catch {
        return baseUrl;
    }
}
