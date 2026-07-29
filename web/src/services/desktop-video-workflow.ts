import type { NodeGenerationContext } from "@/components/canvas/canvas-node-generation";
import { getMediaBlob } from "@/services/file-storage";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio, ReferenceVideo } from "@/types/media";
import type { DesktopVideoProvider } from "@/types/canvas";

const DESKTOP_VIDEO_PROVIDER_KEY = "mingwant:desktop-video-provider";
export const DEFAULT_DESKTOP_VIDEO_PROVIDER: DesktopVideoProvider = "lumina";

export const DESKTOP_VIDEO_PROVIDER_OPTIONS: Array<{ value: DesktopVideoProvider; label: string }> = [
    { value: "lumina", label: "Lumina" },
    { value: "dola", label: "Dola" },
    { value: "dreamina", label: "Dreamina" },
];

type DesktopReferenceFile = {
    name: string;
    mimeType: string;
    data?: Uint8Array;
    sourceUrl?: string;
};

type DesktopVideoTaskRequest = {
    taskId: string;
    projectId: string;
    nodeId: string;
    provider: DesktopVideoProvider;
    title: string;
    prompt: string;
    settings: {
        durationSeconds: string;
        aspectRatio: string;
        resolution: string;
        generateAudio: boolean;
        watermark: boolean;
    };
    references: DesktopReferenceFile[];
};

type DesktopVideoTaskResult = {
    resultId: string;
    fileName: string;
    mimeType: string;
    bytes: number;
    accountId: string;
    accountName: string;
    provider: DesktopVideoProvider;
};

type DesktopVideoProviderWorkflowState = {
    enabledAccountCount: number;
    busyAccountCount: number;
    queuedTaskCount: number;
};

export type DesktopVideoWorkflowState = {
    enabledAccountCount: number;
    busyAccountCount: number;
    queuedTaskCount: number;
    providers: Record<DesktopVideoProvider, DesktopVideoProviderWorkflowState>;
};

type MingwantDesktopBridge = {
    version: 1;
    openVideoWorkbench: () => Promise<void>;
    getVideoWorkflowState: () => Promise<DesktopVideoWorkflowState>;
    startVideoTask: (request: DesktopVideoTaskRequest) => Promise<DesktopVideoTaskResult>;
    cancelVideoTask: (taskId: string) => Promise<void>;
    readVideoResult: (resultId: string) => Promise<Uint8Array | ArrayBuffer>;
    releaseVideoResult: (resultId: string) => Promise<void>;
};

declare global {
    interface Window {
        mingwantDesktop?: MingwantDesktopBridge;
    }
}

export type DesktopVideoGenerationResult = {
    blob: Blob;
    taskId: string;
    accountId: string;
    accountName: string;
    fileName: string;
    provider: DesktopVideoProvider;
};

type RequestDesktopVideoGenerationInput = {
    taskId: string;
    projectId: string;
    nodeId: string;
    provider: DesktopVideoProvider;
    title: string;
    prompt: string;
    config: AiConfig;
    context: NodeGenerationContext;
    signal?: AbortSignal;
};

export function isDesktopVideoWorkflowAvailable() {
    return typeof window !== "undefined" && window.mingwantDesktop?.version === 1;
}

export function createDesktopVideoTaskId() {
    return crypto.randomUUID();
}

export function resolveDesktopVideoProvider(value?: string): DesktopVideoProvider {
    return value === "dola" || value === "dreamina" || value === "lumina" ? value : DEFAULT_DESKTOP_VIDEO_PROVIDER;
}

export function desktopVideoProviderLabel(value?: string) {
    const provider = resolveDesktopVideoProvider(value);
    return DESKTOP_VIDEO_PROVIDER_OPTIONS.find((option) => option.value === provider)?.label || "Lumina";
}

export function preferredDesktopVideoProvider(nodeProvider?: string): DesktopVideoProvider {
    if (nodeProvider === "lumina" || nodeProvider === "dola" || nodeProvider === "dreamina") return nodeProvider;
    try {
        return resolveDesktopVideoProvider(window.localStorage.getItem(DESKTOP_VIDEO_PROVIDER_KEY) || undefined);
    } catch {
        return DEFAULT_DESKTOP_VIDEO_PROVIDER;
    }
}

export function rememberDesktopVideoProvider(provider: DesktopVideoProvider) {
    try {
        window.localStorage.setItem(DESKTOP_VIDEO_PROVIDER_KEY, provider);
    } catch {
        // 平台偏好属于极小展示配置，存储不可用时仅退回 Lumina，不影响当前节点选择。
    }
}

export async function openDesktopVideoWorkbench() {
    const bridge = requireDesktopBridge();
    await bridge.openVideoWorkbench();
}

export async function getDesktopVideoWorkflowState() {
    return requireDesktopBridge().getVideoWorkflowState();
}

export async function cancelDesktopVideoTask(taskId: string) {
    await requireDesktopBridge().cancelVideoTask(taskId);
}

export async function requestDesktopVideoGeneration({ taskId, projectId, nodeId, provider, title, prompt, config, context, signal }: RequestDesktopVideoGenerationInput): Promise<DesktopVideoGenerationResult> {
    const bridge = requireDesktopBridge();
    throwIfAborted(signal);
    let references = await serializeReferences(context, signal);
    throwIfAborted(signal);

    let resultId = "";
    const cancel = () => {
        void bridge.cancelVideoTask(taskId).catch(() => undefined);
    };
    signal?.addEventListener("abort", cancel, { once: true });
    try {
        const pendingResult = bridge.startVideoTask({
            taskId,
            projectId,
            nodeId,
            provider,
            title,
            prompt,
            settings: {
                durationSeconds: config.videoSeconds,
                aspectRatio: config.size,
                resolution: config.vquality,
                generateAudio: config.videoGenerateAudio !== "false",
                watermark: config.videoWatermark === "true",
            },
            references,
        });
        references = [];
        const result = await pendingResult;
        resultId = result.resultId;
        if (result.provider !== provider) throw new Error("桌面工作台返回的平台与任务不一致");
        throwIfAborted(signal);
        const payload = await bridge.readVideoResult(result.resultId);
        throwIfAborted(signal);
        const bytes = payload instanceof Uint8Array ? payload : new Uint8Array(payload);
        if (!bytes.byteLength) throw new Error("桌面工作台返回了空视频文件");
        const copy = new Uint8Array(bytes.byteLength);
        copy.set(bytes);
        return {
            blob: new Blob([copy.buffer], { type: result.mimeType || "video/mp4" }),
            taskId,
            accountId: result.accountId,
            accountName: result.accountName,
            fileName: result.fileName,
            provider,
        };
    } catch (error) {
        if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
        throw error;
    } finally {
        signal?.removeEventListener("abort", cancel);
        if (resultId) await bridge.releaseVideoResult(resultId).catch(() => undefined);
    }
}

async function serializeReferences(context: NodeGenerationContext, signal?: AbortSignal) {
    const files: DesktopReferenceFile[] = [];
    for (const image of context.referenceImages) {
        throwIfAborted(signal);
        files.push(await serializeImage(image));
    }
    for (const video of context.referenceVideos) {
        throwIfAborted(signal);
        files.push(await serializeMedia(video));
    }
    for (const audio of context.referenceAudios) {
        throwIfAborted(signal);
        files.push(await serializeMedia(audio));
    }
    return files;
}

async function serializeImage(image: ReferenceImage): Promise<DesktopReferenceFile> {
    if (!image.dataUrl) throw new Error(`参考图片“${image.name}”无法读取`);
    try {
        const response = await fetch(image.dataUrl);
        if (response.ok) {
            const blob = await response.blob();
            return binaryReference(image.name, image.type || blob.type || "image/png", blob);
        }
    } catch {
        // 跨域图片无法导出二进制时保留原链接，让用户仍能在网页工作台中取用。
    }
    const sourceUrl = absoluteHttpUrl(image.dataUrl);
    if (sourceUrl) return { name: image.name, mimeType: image.type || "image/png", sourceUrl };
    throw new Error(`参考图片“${image.name}”无法导出到桌面工作台`);
}

async function serializeMedia(media: ReferenceVideo | ReferenceAudio): Promise<DesktopReferenceFile> {
    let blob: Blob | null = null;
    if (media.storageKey) blob = await getMediaBlob(media.storageKey).catch(() => null);
    if (!blob && media.url) {
        try {
            const response = await fetch(media.url);
            if (response.ok) blob = await response.blob();
        } catch {
            blob = null;
        }
    }
    if (blob) return binaryReference(media.name, media.type || blob.type || "application/octet-stream", blob);
    const sourceUrl = absoluteHttpUrl(media.url);
    if (sourceUrl) return { name: media.name, mimeType: media.type || "application/octet-stream", sourceUrl };
    throw new Error(`参考素材“${media.name}”无法导出到桌面工作台`);
}

async function binaryReference(name: string, mimeType: string, blob: Blob): Promise<DesktopReferenceFile> {
    return { name, mimeType, data: new Uint8Array(await blob.arrayBuffer()) };
}

function absoluteHttpUrl(value: string) {
    if (!value) return "";
    try {
        const url = new URL(value, window.location.href);
        return url.protocol === "http:" || url.protocol === "https:" ? url.toString() : "";
    } catch {
        return "";
    }
}

function requireDesktopBridge() {
    const bridge = window.mingwantDesktop;
    if (!bridge || bridge.version !== 1) throw new Error("当前不是 MingWant Electron 桌面环境");
    return bridge;
}

function throwIfAborted(signal?: AbortSignal) {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
}
