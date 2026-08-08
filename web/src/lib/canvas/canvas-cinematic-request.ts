import { resolveModelChannel, resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import { generationPromptFingerprint } from "@/lib/generation-error";

type ResolvedModelRequestConfig = ReturnType<typeof resolveModelRequestConfig>;

export function backendAgentProviderConfig(config: ResolvedModelRequestConfig) {
    return {
        channelId: config.channelId,
        apiFormat: config.apiFormat,
        interfaceType: config.interfaceType,
        baseUrl: config.baseUrl,
        apiKey: config.apiKey,
        model: config.model,
        size: config.size,
        quality: config.quality,
        transparentBackground: config.transparentBackground,
        count: config.count,
        videoSeconds: config.videoSeconds,
        vquality: config.vquality,
        videoGenerateAudio: config.videoGenerateAudio,
        videoWatermark: config.videoWatermark,
        audioVoice: config.audioVoice,
        audioFormat: config.audioFormat,
        audioSpeed: config.audioSpeed,
        audioInstructions: config.audioInstructions,
        systemPrompt: config.systemPrompt,
    };
}

export async function cinematicRequestConfigIdentity(config: AiConfig, requestConfig: ResolvedModelRequestConfig) {
    let subtle: SubtleCrypto | undefined;
    try {
        // 某些局域网 HTTP 浏览器会让 subtle 的读取或 digest 直接抛 SecurityError，
        // 不能让这种浏览器能力差异在真正创建任务前阻断影视入口。
        subtle = globalThis.crypto?.subtle;
    } catch {
        subtle = undefined;
    }
    // 优先复用请求开始时冻结的渠道；裸模型名在多渠道重名时不能重新猜测第一条。
    const channel = (requestConfig.resolvedChannelId && config.channels.find((item) => item.id === requestConfig.resolvedChannelId)) || resolveModelChannel(config, requestConfig.model);
    const request = backendAgentProviderConfig(requestConfig);
    const { apiKey: _apiKey, ...requestWithoutSecret } = request;
    const identityPayload = {
        version: 1,
        channel: {
            id: channel.id,
            scope: channel.scope || "user",
            credentialVersion: channel.probeCredentialVersion || "initial",
        },
        request: requestWithoutSecret,
    };
    if (!subtle) {
        // crypto.subtle 在局域网 HTTP 页面可能不可用。这里的降级值只用于本地恢复时比较，
        // 不承担鉴权或计费证明；API Key 不参与持久化，真正创建任务仍由 Backend 校验配置、价格和费用安全边界。
        return `fallback:${generationPromptFingerprint(JSON.stringify(identityPayload))}`;
    }
    try {
        const encoded = new TextEncoder().encode(JSON.stringify({
            ...identityPayload,
            // 原始 API Key 只参与浏览器内 SHA-256，不会写入画布会话或同步数据。
            request: backendAgentProviderConfig(requestConfig),
        }));
        const digest = await subtle.digest("SHA-256", encoded);
        return `sha256:${Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("")}`;
    } catch {
        // digest 在部分非安全上下文中会异步拒绝；降级值仅用于本地恢复比较，
        // 服务端仍会用真实配置、价格和费用安全边界决定是否允许调用供应商；测活只作为诊断记录。
        return `fallback:${generationPromptFingerprint(JSON.stringify(identityPayload))}`;
    }
}

export function cinematicCreationMessageId(requestKey: string) {
    return `cinematic-creation:${requestKey}`;
}

export function cinematicSessionMessageId(backendSessionId: string) {
    return `cinematic-session:${backendSessionId}`;
}
