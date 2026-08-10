import { describe, expect, test } from "bun:test";

import { isXAIVideoRequest } from "../src/lib/model-protocols";
import { buildApiUrl, defaultConfig, isSystemProxyBaseUrl, modelOptionLabel, modelSelectionNeedsReselection, normalizeConfigSnapshot, resolveModelRequestConfig, resolveModelSelectionForCapability, type AiConfig } from "../src/stores/use-config-store";

function configWithChannel(patch: Partial<AiConfig["channels"][number]>): AiConfig {
    const channel = { ...defaultConfig.channels[0], apiKey: "test-key", models: ["kimi-k3-ls"], ...patch };
    return {
        ...defaultConfig,
        channels: [channel],
        models: [`${channel.id}::kimi-k3-ls`],
        textModels: [`${channel.id}::kimi-k3-ls`],
        model: `${channel.id}::kimi-k3-ls`,
        textModel: `${channel.id}::kimi-k3-ls`,
    };
}

describe("model request protocol resolution", () => {
    test("explicit relay video protocol is not overwritten by a Grok model name", () => {
        expect(isXAIVideoRequest("xai-video", "grok-imagine-video-1.5")).toBe(true);
        expect(isXAIVideoRequest("newapi", "grok-imagine-video-1.5")).toBe(false);
        expect(isXAIVideoRequest("newapi-channel-2", "grok-imagine-video-1.5")).toBe(false);
        expect(isXAIVideoRequest(undefined, "grok-imagine-video-1.5")).toBe(true);
    });

    test("preserves explicit OpenAI-compatible version roots", () => {
        expect(buildApiUrl("https://provider.example/v1", "/chat/completions")).toBe("https://provider.example/v1/chat/completions");
        expect(buildApiUrl("https://provider.example/v1beta", "/responses")).toBe("https://provider.example/v1beta/responses");
        expect(buildApiUrl("https://provider.example", "/chat/completions")).toBe("https://provider.example/v1/chat/completions");
    });

    test("only the exact relative system proxy path bypasses custom relay", () => {
        expect(isSystemProxyBaseUrl("/api/ai/system/channel-1")).toBe(true);
        expect(isSystemProxyBaseUrl("/prefix/api/ai/system/channel-1")).toBe(false);
        expect(isSystemProxyBaseUrl("https://attacker.example/api/ai/system/channel-1")).toBe(false);
        expect(isSystemProxyBaseUrl("/api/ai/system/channel-1?redirect=https://attacker.example")).toBe(false);
    });

    test("automatic OpenAI-compatible text channels use Chat Completions for Agent tools", () => {
        const config = configWithChannel({ interfaceType: undefined, apiFormat: "openai" });
        expect(resolveModelRequestConfig(config, config.textModel).interfaceType).toBe("chat-completion");
    });

    test("explicit Responses protocol remains unchanged", () => {
        const config = configWithChannel({ interfaceType: "openai-response", apiFormat: "openai" });
        expect(resolveModelRequestConfig(config, config.textModel).interfaceType).toBe("openai-response");
    });

    test("text model capability does not inherit a video channel default", () => {
        const channel = {
            ...defaultConfig.channels[0],
            apiKey: "test-key",
            apiFormat: "openai" as const,
            interfaceType: "newapi" as const,
            models: ["kimi-k3-ls"],
            modelCosts: [{ model: "kimi-k3-ls", capability: "text" as const, billingMode: "fixed_request" as const, unitPriceMicrocredits: 0 }],
        };
        const config = { ...configWithChannel(channel), channels: [channel] };
        expect(resolveModelRequestConfig(config, config.textModel).interfaceType).toBe("chat-completion");
    });

    test("stale video protocol on a text model falls back to Chat Completions", () => {
        const channel = {
            ...defaultConfig.channels[0],
            apiKey: "test-key",
            apiFormat: "openai" as const,
            interfaceType: "chat-completion" as const,
            models: ["kimi-k3-ls"],
            modelCosts: [{ model: "kimi-k3-ls", capability: "text" as const, protocol: "newapi" as const, billingMode: "fixed_request" as const, unitPriceMicrocredits: 0 }],
        };
        const config = { ...configWithChannel(channel), channels: [channel] };
        expect(resolveModelRequestConfig(config, config.textModel).interfaceType).toBe("chat-completion");
    });

    test("ambiguous or stale channel bindings never fall back to the first channel", () => {
        const first = { ...defaultConfig.channels[0], id: "first", name: "第一渠道", apiKey: "first-key", models: ["shared-model"] };
        const second = { ...defaultConfig.channels[0], id: "second", name: "第二渠道", apiKey: "second-key", models: ["shared-model"] };
        const config = { ...defaultConfig, channels: [first, second], models: ["first::shared-model", "second::shared-model"], textModels: ["first::shared-model", "second::shared-model"] };
        const ambiguous = resolveModelRequestConfig(config, "shared-model");
        const stale = resolveModelRequestConfig(config, "removed::shared-model");
        const removed = resolveModelRequestConfig(config, "removed-model");
        expect(ambiguous.resolvedChannelId).toBe("__unresolved_model__");
        expect(ambiguous.baseUrl).toBe("");
        expect(stale.resolvedChannelId).toBe("__unresolved_model__");
        expect(stale.apiKey).toBe("");
        expect(removed.resolvedChannelId).toBe("__unresolved_model__");
        expect(modelOptionLabel(config, "removed::shared-model")).toContain("需重新选择");
        const normalized = normalizeConfigSnapshot({ config: { ...config, textModel: "removed::shared-model", model: "removed::shared-model" } }).config;
        expect(normalized.textModel).toBe("removed::shared-model");
        expect(normalized.model).toBe("removed::shared-model");
        expect(modelSelectionNeedsReselection(normalized, normalized.textModel)).toBe(true);
        expect(resolveModelSelectionForCapability(normalized, "text", normalized.textModel)).toBe("removed::shared-model");
    });

    test("built-in placeholders are not mistaken for a deleted channel on an unconfigured account", () => {
        const normalized = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [] } }).config;
        expect(normalized.imageModel).toBe("");
        expect(normalized.videoModel).toBe("");
        expect(modelSelectionNeedsReselection(normalized, defaultConfig.imageModel)).toBe(false);
        expect(resolveModelSelectionForCapability(normalized, "image", normalized.imageModel, defaultConfig.imageModel)).toBe("");
    });

    test("an existing model that changed capability is not retained as another domain default", () => {
        const channel = {
            ...defaultConfig.channels[0],
            id: "system-image",
            scope: "system" as const,
            apiKey: "system",
            models: ["visual-model"],
            modelCosts: [{ model: "visual-model", capability: "image" as const, billingMode: "fixed_request" as const, unitPriceMicrocredits: 1 }],
        };
        const normalized = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel], model: "system-image::visual-model", textModel: "system-image::visual-model" } }).config;
        expect(normalized.textModel).toBe("");
        expect(resolveModelSelectionForCapability(normalized, "text", "system-image::visual-model")).toBe("");
    });
});
