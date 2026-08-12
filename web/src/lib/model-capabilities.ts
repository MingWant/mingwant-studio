import type { CanvasVideoEditOperation } from "@/types/canvas";

export type ModelCapabilityConfig = {
    version: number;
    video?: VideoCapabilityConfig;
};

export type VideoCapabilityConfig = {
    references: {
        promptMaxChars: number;
        maxImages: number;
        maxImageBytes: number;
        maxVideos: number;
        maxVideoBytes: number;
        maxVideoDurationSeconds: number;
        maxAudios: number;
        maxAudioBytes: number;
        maxAudioDurationSeconds: number;
    };
    duration: {
        selection: "range" | "enum";
        min?: number;
        max?: number;
        step?: number;
        values?: number[];
        default: number;
    };
    ratios: string[];
    defaultRatio: string;
    resolutions: string[];
    defaultResolution: string;
    generateAudio: VideoBooleanConfig;
    watermark: VideoBooleanConfig;
    operations: string[];
    defaultOperation: string;
    runningHub?: RunningHubWorkflowConfig;
};

export type RunningHubWorkflowConfig = {
    resultNodeId: string;
    stopOnNodeIds: string[];
    terminationMode: "breakpoint" | "cancel";
    failureNodeId: string;
    failureNodeField: string;
    failureNodeValue: string;
    wssWaitSeconds: number;
    monitorSilenceSeconds: number;
    dimensionMultiple: number;
    parameters: RunningHubParameterMapping[];
    references: RunningHubReferenceMapping[];
};

export type RunningHubParameterMapping = {
    label: string;
    source: "prompt" | "duration" | "width" | "height" | "resolution" | "generate_audio" | "watermark" | "seed" | "fps" | "custom";
    nodeId: string;
    fieldName: string;
    valueType: "string" | "integer" | "number" | "boolean";
    defaultValue?: string;
    userEditable?: boolean;
};

export type RunningHubReferenceMapping = {
    kind: "image" | "video" | "audio";
    index: number;
    nodeId: string;
    fieldName: string;
    required?: boolean;
};

export type VideoBooleanConfig = {
    supported: boolean;
    default: boolean;
};

export const MODEL_CAPABILITY_CONFIG_VERSION = 5;
const XAI_VIDEO_OPERATIONS_MIGRATION_VERSION = 2;
const RUNNING_HUB_BREAKPOINT_MIGRATION_VERSION = 4;
const RUNNING_HUB_REQUIRED_IMAGES_MIGRATION_VERSION = 5;

export function defaultModelCapabilityConfig(protocol?: string): ModelCapabilityConfig {
    const video: VideoCapabilityConfig = {
        references: { promptMaxChars: 10_000, maxImages: 9, maxImageBytes: 30 * 1024 * 1024, maxVideos: 0, maxVideoBytes: 0, maxVideoDurationSeconds: 0, maxAudios: 0, maxAudioBytes: 0, maxAudioDurationSeconds: 0 },
        duration: { selection: "range", min: 1, max: 15, step: 1, default: 6 },
        ratios: ["16:9", "9:16", "1:1", "4:3", "3:4", "21:9", "adaptive"],
        defaultRatio: "16:9",
        resolutions: ["480p", "720p", "1080p", "2160p"],
        defaultResolution: "720p",
        generateAudio: { supported: false, default: false },
        watermark: { supported: false, default: false },
        operations: ["text_to_video", "image_to_video"],
        defaultOperation: "text_to_video",
    };
    switch (protocol) {
        case "gemini-veo":
            video.references.maxImages = 1;
            video.duration = { selection: "enum", values: [4, 6, 8], default: 6 };
            video.ratios = ["16:9", "9:16", "1:1"];
            video.resolutions = ["720p", "1080p"];
            break;
        case "newapi-channel-1":
        case "newapi-channel-2":
            video.references.maxVideos = 3;
            video.references.maxAudios = 3;
            video.references.maxVideoBytes = 200 * 1024 * 1024;
            video.references.maxAudioBytes = 15 * 1024 * 1024;
            video.references.maxVideoDurationSeconds = 15;
            video.references.maxAudioDurationSeconds = 15;
            video.generateAudio = { supported: true, default: true };
            video.operations = ["text_to_video", "image_to_video", "audio_to_video", "extend"];
            break;
        case "xai-video":
            video.references.maxImages = 7;
            video.references.maxVideos = 1;
            video.references.maxVideoDurationSeconds = 15;
            video.ratios = ["16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"];
            video.resolutions = ["480p", "720p", "1080p"];
            video.defaultResolution = "480p";
            video.operations = ["text_to_video", "image_to_video", "reference_to_video", "edit_video", "extend"];
            break;
        case "grok2api-video":
            video.references.maxImages = 1;
            video.ratios = ["16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"];
            video.resolutions = ["480p", "720p", "1080p"];
            video.operations = ["text_to_video", "image_to_video"];
            break;
        case "newapi":
            video.references.maxImages = 1;
            video.ratios = ["16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"];
            break;
        case "runninghub-workflow":
            video.references = { promptMaxChars: 100_000, maxImages: 3, maxImageBytes: 30 * 1024 * 1024, maxVideos: 1, maxVideoBytes: 30 * 1024 * 1024, maxVideoDurationSeconds: 3600, maxAudios: 1, maxAudioBytes: 30 * 1024 * 1024, maxAudioDurationSeconds: 3600 };
            video.duration = { selection: "range", min: 1, max: 60, step: 1, default: 5 };
            video.operations = ["workflow"];
            video.defaultOperation = "workflow";
            video.runningHub = defaultRunningHubWorkflowConfig();
            break;
    }
    return { version: MODEL_CAPABILITY_CONFIG_VERSION, video };
}

export function videoCapabilityFromConfig(config: ModelCapabilityConfig | undefined, protocol?: string) {
    const video = config?.video || defaultModelCapabilityConfig(protocol).video!;
    if (protocol === "runninghub-workflow") {
        const fallback = defaultModelCapabilityConfig(protocol).video!;
        const runningHubFallback = fallback.runningHub || defaultRunningHubWorkflowConfig();
        const runningHub = (config?.version || 0) < RUNNING_HUB_BREAKPOINT_MIGRATION_VERSION
            ? {
                ...(video.runningHub || runningHubFallback),
                stopOnNodeIds: [...runningHubFallback.stopOnNodeIds],
                terminationMode: runningHubFallback.terminationMode,
                failureNodeId: runningHubFallback.failureNodeId,
                failureNodeField: runningHubFallback.failureNodeField,
                failureNodeValue: runningHubFallback.failureNodeValue,
            }
            : video.runningHub || runningHubFallback;
        const migratedRunningHub = (config?.version || 0) < RUNNING_HUB_REQUIRED_IMAGES_MIGRATION_VERSION
            ? {
                ...runningHub,
                references: (Array.isArray(runningHub.references) ? runningHub.references : []).map((item) => isDefaultRequiredRunningHubImage(item) ? { ...item, required: true } : item),
            }
            : runningHub;
        return {
            ...fallback,
            ...video,
            references: { ...fallback.references, ...video.references },
            duration: { ...fallback.duration, ...video.duration },
            runningHub: normalizeRunningHubWorkflowConfig(migratedRunningHub),
        };
    }
    // 与后端 v1→v2 的窄迁移保持一致，让尚未重新保存的本地自定义 xAI 配置也能立即看到官方编辑/续写入口。
    if (protocol === "xai-video" && (config?.version || 0) < XAI_VIDEO_OPERATIONS_MIGRATION_VERSION && isLegacyXAIVideoOperations(video.operations)) {
        return {
            ...video,
            references: { ...video.references, maxVideos: 1, maxVideoDurationSeconds: 15 },
            operations: ["text_to_video", "image_to_video", "reference_to_video", "edit_video", "extend"],
        };
    }
    return video;
}

function isDefaultRequiredRunningHubImage(item: RunningHubReferenceMapping) {
    const defaults: Array<[number, string]> = [[0, "16"], [1, "19"], [2, "22"]];
    return item.kind === "image"
        && item.fieldName === "image"
        && defaults.some(([index, nodeId]) => item.index === index && item.nodeId === nodeId);
}

export function defaultRunningHubWorkflowConfig(): RunningHubWorkflowConfig {
    return {
        resultNodeId: "12",
        stopOnNodeIds: ["48"],
        terminationMode: "breakpoint",
        failureNodeId: "69",
        failureNodeField: "width",
        failureNodeValue: "481",
        wssWaitSeconds: 180,
        monitorSilenceSeconds: 120,
        dimensionMultiple: 32,
        parameters: [
            { label: "提示词", source: "prompt", nodeId: "7", fieldName: "prompt", valueType: "string" },
            { label: "视频时长", source: "duration", nodeId: "6", fieldName: "duration_seconds", valueType: "integer" },
            { label: "画面宽度", source: "width", nodeId: "6", fieldName: "width", valueType: "integer" },
            { label: "画面高度", source: "height", nodeId: "6", fieldName: "height", valueType: "integer" },
            { label: "随机种子", source: "seed", nodeId: "9", fieldName: "seed", valueType: "integer", defaultValue: "0", userEditable: true },
            { label: "帧率", source: "fps", nodeId: "11", fieldName: "fps", valueType: "integer", defaultValue: "24", userEditable: true },
        ],
        references: [
            { kind: "image", index: 0, nodeId: "16", fieldName: "image", required: true },
            { kind: "image", index: 1, nodeId: "19", fieldName: "image", required: true },
            { kind: "image", index: 2, nodeId: "22", fieldName: "image", required: true },
            { kind: "video", index: 0, nodeId: "24", fieldName: "file" },
            { kind: "audio", index: 0, nodeId: "26", fieldName: "audio" },
        ],
    };
}

export function normalizeRunningHubWorkflowConfig(value?: RunningHubWorkflowConfig): RunningHubWorkflowConfig {
    const fallback = defaultRunningHubWorkflowConfig();
    if (!value) return fallback;
    const dimensionMultiple = Math.floor(Number(value.dimensionMultiple));
    const terminationMode = value.terminationMode || fallback.terminationMode;
    const failureNodeId = value.failureNodeId || fallback.failureNodeId;
    const failureNodeField = value.failureNodeField || fallback.failureNodeField;
    const failureNodeValue = value.failureNodeValue || fallback.failureNodeValue;
    const stopOnNodeIds = (Array.isArray(value.stopOnNodeIds) ? value.stopOnNodeIds : [])
        .filter((nodeId) => terminationMode !== "breakpoint" || nodeId !== failureNodeId);
    return {
        ...fallback,
        ...value,
        terminationMode,
        failureNodeId,
        failureNodeField,
        failureNodeValue,
        dimensionMultiple: Number.isFinite(dimensionMultiple) && dimensionMultiple > 0 ? dimensionMultiple : fallback.dimensionMultiple,
        stopOnNodeIds,
        parameters: Array.isArray(value.parameters) ? value.parameters : [],
        references: Array.isArray(value.references) ? value.references : [],
    };
}

function isLegacyXAIVideoOperations(values: string[]) {
    if (values.length !== 3) return false;
    const normalized = values.map((value) => value.trim().toLowerCase());
    return new Set(normalized).size === normalized.length
        && normalized.includes("text_to_video")
        && normalized.includes("image_to_video")
        && normalized.includes("reference_to_video")
        && normalized.every((value) => ["text_to_video", "image_to_video", "reference_to_video"].includes(value));
}

export function resolveVideoOperation(
    current: string | undefined,
    references: { images?: number; videos?: number; audios?: number } = {},
    capability?: Pick<VideoCapabilityConfig, "operations" | "defaultOperation">,
): CanvasVideoEditOperation {
    const configured = (capability?.operations?.length ? capability.operations : defaultModelCapabilityConfig().video!.operations).map((value) => value.trim().toLowerCase()).filter(Boolean);
    const isSupported = (value: string) => configured.includes(value.trim().toLowerCase());
    const stored = current?.trim().toLowerCase();

    // concat 是画布本地合并操作，不属于供应商模型能力；保留它交给上层的本地流程拦截。
    if (stored === "concat") return "concat";

    const hasImages = (references.images || 0) > 0;
    const hasVideos = (references.videos || 0) > 0;
    const hasAudios = (references.audios || 0) > 0;
    const preferred = hasAudios && !hasImages && !hasVideos ? "audio_to_video" : hasVideos ? "extend" : hasImages ? "image_to_video" : "text_to_video";

    // 连接参考图后，残留的 text_to_video 会让真实输入和任务模式不一致；优先切到可用的图生视频。
    if (stored === "text_to_video" && hasImages && isSupported("image_to_video")) return "image_to_video";
    if (stored === "text_to_video" && hasVideos && isSupported("extend")) return "extend";
    if (stored === "text_to_video" && hasAudios && isSupported("audio_to_video")) return "audio_to_video";
    if (stored && isSupported(stored)) return stored as CanvasVideoEditOperation;

    const candidates = [preferred, capability?.defaultOperation || "", "text_to_video", "image_to_video"];
    const fallback = candidates.find(isSupported) || configured[0] || "text_to_video";
    return fallback as CanvasVideoEditOperation;
}

export function durationValues(capability: VideoCapabilityConfig) {
    if (capability.duration.selection === "enum") return [...(capability.duration.values || [])].sort((a, b) => a - b);
    const min = capability.duration.min || 1;
    const max = capability.duration.max || min;
    const step = capability.duration.step || 1;
    const count = Math.floor((max - min) / step) + 1;
    return count > 0 && count <= 32 ? Array.from({ length: count }, (_, index) => min + index * step) : [];
}

export function normalizeCapabilityDuration(value: string | number | undefined, capability: VideoCapabilityConfig) {
    const requested = Math.floor(Number(value) || capability.duration.default);
    if (capability.duration.selection === "enum") {
        const values = durationValues(capability);
        return values.includes(requested) ? requested : capability.duration.default;
    }
    const min = capability.duration.min || 1;
    const max = capability.duration.max || min;
    const step = capability.duration.step || 1;
    const clamped = Math.max(min, Math.min(max, requested));
    const slotCount = Math.max(0, Math.floor((max - min) / step));
    const slot = Math.min(slotCount, Math.max(0, Math.round((clamped - min) / step)));
    return min + slot * step;
}

export function normalizeCapabilityResolution(value: string | number | undefined) {
    const token = String(value || "").trim().toLowerCase();
    if (token === "4k") return "2160p";
    const normalized = token.replace(/p$/i, "");
    return normalized && /^\d+$/.test(normalized) ? `${normalized}p` : "720p";
}

export function capabilityResolutionValue(value: string) {
    return normalizeCapabilityResolution(value).replace(/p$/, "");
}

export function normalizeCapabilityRatio(value: string | undefined) {
    const token = String(value || "").trim().toLowerCase().replace("×", "x");
    if (token === "auto" || token === "adaptive") return "adaptive";
    return token;
}

export function ratioFromSize(value: string | undefined, options: string[]) {
    const normalized = normalizeCapabilityRatio(value);
    if (options.some((item) => normalizeCapabilityRatio(item) === normalized)) return normalized;
    const match = normalized.match(/^(\d+(?:\.\d+)?)x(\d+(?:\.\d+)?)$/);
    if (!match) return "";
    const actual = Number(match[1]) / Number(match[2]);
    let best = "";
    let difference = Number.POSITIVE_INFINITY;
    for (const option of options) {
        const ratio = normalizeCapabilityRatio(option).split(":");
        if (ratio.length !== 2) continue;
        const candidate = Number(ratio[0]) / Number(ratio[1]);
        const next = Math.abs(candidate - actual);
        if (Number.isFinite(candidate) && next < difference) {
            best = option;
            difference = next;
        }
    }
    return best;
}

export function sizeForCapabilityRatio(ratio: string) {
    const normalized = normalizeCapabilityRatio(ratio);
    switch (normalized) {
        case "9:16": return "720x1280";
        case "1:1": return "1024x1024";
        case "4:3": return "1280x960";
        case "3:4": return "960x1280";
        case "21:9": return "1470x630";
        case "3:2": return "1080x720";
        case "2:3": return "720x1080";
        case "adaptive": return "adaptive";
    }
    const match = normalized.match(/^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$/);
    if (!match) return "1280x720";
    const widthRatio = Number(match[1]);
    const heightRatio = Number(match[2]);
    if (!Number.isFinite(widthRatio) || !Number.isFinite(heightRatio) || widthRatio <= 0 || heightRatio <= 0) return "1280x720";
    const scale = 1280 / Math.max(widthRatio, heightRatio);
    return `${Math.max(1, Math.round(widthRatio * scale))}x${Math.max(1, Math.round(heightRatio * scale))}`;
}

export function alignVideoSizeToMultiple(value: string, multiple: number) {
    const match = String(value || "").trim().toLowerCase().match(/^(\d+)x(\d+)$/);
    const normalizedMultiple = Math.max(1, Math.floor(Number(multiple) || 1));
    if (!match || normalizedMultiple === 1) return value;
    const align = (dimension: number) => Math.max(normalizedMultiple, Math.round(dimension / normalizedMultiple) * normalizedMultiple);
    return `${align(Number(match[1]))}x${align(Number(match[2]))}`;
}

export function formatCapabilityRatio(value: string) {
    const normalized = normalizeCapabilityRatio(value);
    if (normalized === "adaptive") return "自适应";
    const labels: Record<string, string> = { "16:9": "横屏", "9:16": "竖屏", "1:1": "方形", "4:3": "标准横屏", "3:4": "标准竖屏", "21:9": "宽银幕", "3:2": "横向", "2:3": "纵向" };
    return labels[normalized] || value;
}
