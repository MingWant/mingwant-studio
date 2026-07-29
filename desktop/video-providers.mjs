export const DEFAULT_VIDEO_PROVIDER = "lumina";

export const VIDEO_PROVIDERS = Object.freeze([
    Object.freeze({ id: "lumina", name: "Lumina", homeUrl: "https://ai.byteplus.com/lumina" }),
    Object.freeze({ id: "dola", name: "Dola", homeUrl: "https://www.dola.com/" }),
    Object.freeze({ id: "dreamina", name: "Dreamina", homeUrl: "https://dreamina.capcut.com/" }),
]);

const VIDEO_PROVIDER_BY_ID = new Map(VIDEO_PROVIDERS.map((provider) => [provider.id, provider]));

export function normalizeVideoProvider(value, fallback = DEFAULT_VIDEO_PROVIDER) {
    const id = typeof value === "string" ? value.trim().toLowerCase() : "";
    const normalized = id || fallback;
    if (!VIDEO_PROVIDER_BY_ID.has(normalized)) throw new Error("不支持的网页视频平台");
    return normalized;
}

export function videoProvider(value) {
    return VIDEO_PROVIDER_BY_ID.get(normalizeVideoProvider(value));
}
