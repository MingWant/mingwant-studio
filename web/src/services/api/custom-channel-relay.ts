import { isSystemProxyBaseUrl, resolveBackendApiUrl, type AiConfig } from "@/stores/use-config-store";

type RelayConfig = Pick<AiConfig, "baseUrl" | "apiKey" | "apiFormat">;

export type ChannelRequest = {
    url: string;
    headers: Record<string, string>;
    credentials: RequestCredentials;
};

/** 浏览器保留的交互式模型请求统一经登录态后端中转；媒体生成则只能走后端持久化任务。 */
export function channelRequest(config: RelayConfig, upstreamUrl: string, headers: HeadersInit = {}): ChannelRequest {
    const normalizedHeaders = new Headers(headers);
    if (isSystemProxyBaseUrl(config.baseUrl)) {
        return { url: upstreamUrl, headers: Object.fromEntries(normalizedHeaders.entries()), credentials: "include" };
    }

    const normalizedUpstreamUrl = new URL(upstreamUrl).toString();
    normalizedHeaders.delete("x-goog-api-key");
    normalizedHeaders.set("Authorization", `Bearer ${config.apiKey}`);
    normalizedHeaders.set("X-Canvas-Upstream-URL", normalizedUpstreamUrl);
    normalizedHeaders.set("X-Canvas-Upstream-Format", config.apiFormat === "gemini" ? "gemini" : "openai");
    return {
        url: resolveBackendApiUrl("/api/ai/custom"),
        headers: Object.fromEntries(normalizedHeaders.entries()),
        credentials: "include",
    };
}
