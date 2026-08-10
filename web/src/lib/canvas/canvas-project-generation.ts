import { createGenerationTask, waitForGenerationTask, type GenerationTask } from "@/services/api/task-center";
import { defaultConfig, normalizeModelOptionValue, resolveModelRequestConfig, resolveModelSelectionForCapability, type AiConfig } from "@/stores/use-config-store";
import { getImageBlob, resolveImageUrl, uploadImage } from "@/services/image-storage";
import { getMediaBlob, resolveMediaUrl } from "@/services/file-storage";
import { resourceIdFromStorageKey, resourceStorageKey, uploadResourceFile } from "@/services/api/resources";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { normalizeVideoDuration, normalizeVideoResolution } from "@/lib/video-generation-options";
import { isSeedanceVideoConfig } from "@/lib/seedance-video";
import { defaultModelCapabilityConfig, normalizeCapabilityDuration, normalizeCapabilityRatio, normalizeCapabilityResolution, ratioFromSize, resolveVideoOperation, sizeForCapabilityRatio, videoCapabilityFromConfig } from "@/lib/model-capabilities";
import { isXAIVideoRequest } from "@/lib/model-protocols";
import { imageMetadata, parseBackendGenerationResult } from "@/lib/canvas/canvas-generation-task-sync";
import type { CanvasNodeGenerationMode } from "@/components/canvas/canvas-node-prompt-panel";
import { CanvasNodeType, type CanvasAssistantSession, type CanvasConnection, type CanvasImageGenerationType, type CanvasNodeData, type CanvasNodeMetadata, type CanvasVideoEditOperation } from "@/types/canvas";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio, ReferenceVideo } from "@/types/media";

export async function runBackendCanvasGenerationTask({
    projectId,
    nodeId,
    mode,
    prompt,
    config,
    referenceImages = [],
    referenceVideos = [],
    referenceAudios = [],
    mask,
    signal,
    sourceTaskId,
    confirmNewProviderRequest,
    metadata,
    onTaskCreated,
}: {
    projectId: string;
    nodeId: string;
    mode: CanvasNodeGenerationMode;
    prompt: string;
    config: AiConfig;
    referenceImages?: ReferenceImage[];
    referenceVideos?: ReferenceVideo[];
    referenceAudios?: ReferenceAudio[];
    mask?: ReferenceImage;
    signal?: AbortSignal;
    sourceTaskId?: string;
    confirmNewProviderRequest?: boolean;
    metadata?: Record<string, unknown>;
    onTaskCreated?: (task: GenerationTask) => void;
}) {
    // 画布旧缓存和外部导入数据可能显式保存 null；任务创建边界统一收敛为空数组，
    // 避免在供应商尚未调用时抛出没有业务含义的 null.length / null.map。
    const safeReferenceImages = Array.isArray(referenceImages) ? referenceImages : [];
    const safeReferenceVideos = Array.isArray(referenceVideos) ? referenceVideos : [];
    const safeReferenceAudios = Array.isArray(referenceAudios) ? referenceAudios : [];
    const taskReferenceImages = await Promise.all(safeReferenceImages.map(prepareBackendImageReference));
    const taskReferenceVideos = await Promise.all(safeReferenceVideos.map((video) => mediaToBackendReference(video)));
    const taskReferenceAudios = await Promise.all(safeReferenceAudios.map((audio) => mediaToBackendReference(audio)));
    const taskMask = mask ? await prepareBackendImageReference(mask) : undefined;
    const task = await createGenerationTask({
        projectId,
        type: `canvas_${mode}`,
        operation: mode === "video" ? String(metadata?.videoEditOperation || "text_to_video") : mode,
        prompt,
        model: config.model,
        sourceTaskId,
        confirmNewProviderRequest,
        input: {
            mode,
            prompt,
            config: backendProviderConfig(config),
            referenceImages: taskReferenceImages,
            referenceVideos: taskReferenceVideos,
            referenceAudios: taskReferenceAudios,
            mask: taskMask,
            metadata: { nodeId, ...metadata },
        },
    });
    onTaskCreated?.(task);
    const completed = await waitForGenerationTask(task.id, { signal, initialTask: task, onTaskUpdate: onTaskCreated });
    return parseBackendGenerationResult(completed);
}

async function mediaToBackendReference(media: ReferenceVideo | ReferenceAudio) {
    if (resourceIdFromStorageKey(media.storageKey)) return { ...media, dataUrl: "" };
    const url = media.url || "";
    if (/^https?:\/\//i.test(url)) return media;
    let blob: Blob | null = null;
    if (media.storageKey) blob = await getMediaBlob(media.storageKey);
    if (!blob && (url.startsWith("blob:") || url.startsWith("data:"))) blob = await (await fetch(url)).blob();
    if (!blob) throw new Error("参考媒体尚未保存，请重新上传后再生成");
    try {
        const kind: "video" | "audio" | "file" = blob.type.startsWith("video/") ? "video" : blob.type.startsWith("audio/") ? "audio" : "file";
        const resource = await uploadResourceFile(blob, kind, { fileName: media.name, width: "width" in media ? media.width : undefined, height: "height" in media ? media.height : undefined, durationMs: media.durationMs });
        return { ...media, url: resource.publicUrl || `/api/resources/${resource.id}/file`, storageKey: resourceStorageKey(resource.id), dataUrl: "", type: resource.mimeType || media.type || blob.type, bytes: resource.size || blob.size, durationMs: resource.durationMs || media.durationMs };
    } catch (error) {
        throw new Error(error instanceof Error ? `参考媒体上传失败：${error.message}` : "参考媒体上传失败");
    }
}

async function prepareBackendImageReference(image: ReferenceImage) {
    if (resourceIdFromStorageKey(image.storageKey)) return { ...image, dataUrl: "" };
    if (/^https?:\/\//i.test(image.dataUrl)) return { ...image, url: image.url || image.dataUrl, dataUrl: "" };
    const blob = image.storageKey ? await getImageBlob(image.storageKey) : image.dataUrl ? await (await fetch(image.dataUrl)).blob() : null;
    if (blob) {
        try {
            const resource = await uploadResourceFile(blob, "image", { fileName: image.name });
            return { ...image, dataUrl: "", url: resource.publicUrl || `/api/resources/${resource.id}/file`, storageKey: resourceStorageKey(resource.id), type: resource.mimeType || image.type || blob.type, bytes: resource.size || blob.size };
        } catch (error) {
            throw new Error(error instanceof Error ? `参考图片上传失败：${error.message}` : "参考图片上传失败");
        }
    }
    throw new Error("参考图片尚未保存，请重新上传后再生成");
}

type ResolvedProviderConfig = ReturnType<typeof resolveModelRequestConfig>;

export function backendProviderConfig(config: AiConfig | ResolvedProviderConfig) {
    const resolved = config as ResolvedProviderConfig;
    const frozenChannel = resolved.resolvedChannelId
        ? config.channels.find((channel) => channel.id === resolved.resolvedChannelId)
        : undefined;
    // Agent/任务页已经在发送前冻结了渠道；再次用裸模型名解析会在同名模型跨渠道时落到第一条渠道，
    // 造成测活通过但实际任务走错 Base URL、协议或 API Key。只有找不到冻结渠道时才重新解析。
    const requestConfig = frozenChannel ? resolved : resolveModelRequestConfig(config, config.model);
    const requestChannel = requestConfig.channels.find((channel) => channel.id === requestConfig.resolvedChannelId);
    const modelCost = requestChannel?.modelCosts?.find((item) => item.model === requestConfig.model);
    return {
        channelId: requestConfig.channelId,
        apiFormat: requestConfig.apiFormat,
        interfaceType: requestConfig.interfaceType,
        baseUrl: requestConfig.baseUrl,
        apiKey: requestConfig.apiKey,
        model: requestConfig.model,
        capabilityConfig: modelCost?.capabilityConfig || defaultModelCapabilityConfig(requestConfig.interfaceType),
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

export function generationTaskMetadata(task: GenerationTask): CanvasNodeMetadata {
    const progress = normalizeTaskProgress(task.progress, task.status);
    return {
        taskId: task.id,
        taskStatus: task.status,
        taskProgress: progress,
        taskStage: task.stage,
        taskCreatedAt: task.createdAt || task.created_at,
        taskUpdatedAt: task.updatedAt || task.updated_at,
        taskRecoveryUncertain: undefined,
    };
}

// 失败节点再次提交前必须移除旧任务绑定，否则批次调度会把它误判为仍在处理。
export function resetGenerationTaskMetadata(metadata: CanvasNodeMetadata | undefined, status: CanvasNodeMetadata["status"] = "idle"): CanvasNodeMetadata {
    const next = {
        ...(metadata || {}),
        status,
        errorDetails: undefined,
        generationErrorCode: undefined,
        failedPromptFingerprint: undefined,
    };
    delete next.taskId;
    delete next.taskStatus;
    delete next.taskProgress;
    delete next.taskStage;
    delete next.taskCreatedAt;
    delete next.taskUpdatedAt;
    delete next.taskRecoveryUncertain;
    return next;
}

function normalizeTaskProgress(progress: number | undefined, status: GenerationTask["status"]) {
    if (typeof progress === "number" && Number.isFinite(progress)) return Math.max(0, Math.min(100, Math.round(progress)));
    if (status === "queued") return 0;
    if (status === "succeeded") return 100;
    return undefined;
}


export function imageExtension(dataUrl: string) {
    return dataUrl.match(/^data:image[/]([^;]+)/)?.[1] || dataUrl.match(/image[/]([^;]+)/)?.[1] || "png";
}

export function audioExtension(mimeType?: string) {
    if (mimeType?.includes("wav")) return "wav";
    if (mimeType?.includes("opus")) return "opus";
    if (mimeType?.includes("aac")) return "aac";
    if (mimeType?.includes("flac")) return "flac";
    if (mimeType?.includes("pcm")) return "pcm";
    return "mp3";
}

export function buildImageGenerationMetadata(type: CanvasImageGenerationType, config: AiConfig, count: number, references: ReferenceImage[]): CanvasNodeMetadata {
    return {
        generationType: type,
        model: config.model,
        size: config.size,
        quality: config.quality,
        transparentBackground: config.transparentBackground,
        count,
        references: references.map(referenceUrl).filter((url): url is string => Boolean(url)),
        referenceCount: references.length,
    };
}

export function nodeReferenceImage(node: CanvasNodeData): ReferenceImage | null {
    if (node.type !== CanvasNodeType.Image || !node.metadata?.content) return null;
    return {
        id: node.id,
        name: `reference-${node.id}.png`,
        type: node.metadata.mimeType || "image/png",
        dataUrl: node.metadata.content,
        storageKey: node.metadata.storageKey,
        bytes: node.metadata.bytes,
    };
}

export function buildAudioGenerationMetadata(config: AiConfig): CanvasNodeMetadata {
    return {
        model: config.model,
        audioVoice: config.audioVoice,
        audioFormat: config.audioFormat,
        audioSpeed: config.audioSpeed,
        audioInstructions: config.audioInstructions,
    };
}

function referenceUrl(image: ReferenceImage) {
    return image.storageKey || image.url || (!image.dataUrl.startsWith("data:") ? image.dataUrl : undefined);
}

export async function resolveStoredReferenceImages(references?: string[]) {
    if (!references?.length) return [];
    const imageReferences = references.filter(isStoredImageReference);
    const images = await Promise.all(
        imageReferences.map(async (url, index) => {
            const storageKey = url.startsWith("image:") || resourceIdFromStorageKey(url) ? url : undefined;
            const dataUrl = storageKey ? await resolveImageUrl(storageKey, "") : url;
            if (!dataUrl) return null;
            return {
                id: `${index}`,
                name: `reference-${index + 1}.png`,
                type: imageMimeType(dataUrl),
                dataUrl,
                url: /^https?:\/\//i.test(dataUrl) ? dataUrl : undefined,
                storageKey,
            };
        }),
    );
    return images.every(Boolean) ? (images as ReferenceImage[]) : null;
}

function isStoredImageReference(url: string) {
    return resourceIdFromStorageKey(url) || url.startsWith("image:") || url.startsWith("data:image/") || /\.(png|jpe?g|webp|gif|avif)(?:[?#]|$)/i.test(url);
}

function imageMimeType(url: string) {
    return url.match(/^data:(image\/[^;,]+)/)?.[1] || "image/png";
}

export function generationReferenceUrls(context: { referenceImages: ReferenceImage[]; referenceVideos: Array<{ storageKey?: string; url?: string }>; referenceAudios?: Array<{ storageKey?: string; url?: string }> }) {
    return [
        ...context.referenceImages.map(referenceUrl).filter((url): url is string => Boolean(url)),
        ...context.referenceVideos.map((video) => video.storageKey || video.url).filter((url): url is string => Boolean(url)),
        ...(context.referenceAudios || []).map((audio) => audio.storageKey || audio.url).filter((url): url is string => Boolean(url)),
    ];
}

function resolveVideoEditOperation(
    node: CanvasNodeData | undefined,
    context?: {
        referenceImages: ReferenceImage[];
        referenceVideos: ReferenceVideo[];
        referenceAudios: ReferenceAudio[];
    },
    config?: AiConfig,
): CanvasVideoEditOperation {
    const request = config ? resolveModelRequestConfig(config, config.model) : undefined;
    const channel = request ? config?.channels.find((item) => item.id === request.resolvedChannelId) : undefined;
    const modelCost = request ? channel?.modelCosts?.find((item) => item.model === request.model) : undefined;
    const capability = request ? videoCapabilityFromConfig(modelCost?.capabilityConfig, request.interfaceType) : undefined;
    return resolveVideoOperation(node?.metadata?.videoEditOperation, { images: context?.referenceImages.length, videos: context?.referenceVideos.length, audios: context?.referenceAudios.length }, capability);
}

export function buildVideoGenerationMetadata(
    node: CanvasNodeData | undefined,
    context?: {
        referenceImages: ReferenceImage[];
        referenceVideos: ReferenceVideo[];
        referenceAudios: ReferenceAudio[];
    },
    config?: AiConfig,
): CanvasNodeMetadata {
    const metadata = node?.metadata;
    const request = config ? resolveModelRequestConfig(config, config.model) : undefined;
    const operation = resolveVideoEditOperation(node, context, config);
    const referenceMode = operation === "reference_to_video";
    const xai = Boolean(request && isXAIVideoRequest(request.interfaceType, request.model));
    const supportsEndFrame = !xai;
    const usesImageFrame = !xai || operation === "image_to_video" || referenceMode;
    const startFrame = usesImageFrame && metadata?.videoStartFrameNodeId && context?.referenceImages.some((image) => image.id === metadata.videoStartFrameNodeId) ? metadata.videoStartFrameNodeId : undefined;
    const endFrame = (supportsEndFrame || referenceMode) && metadata?.videoEndFrameNodeId && context?.referenceImages.some((image) => image.id === metadata.videoEndFrameNodeId) ? metadata.videoEndFrameNodeId : undefined;
    return {
        videoEditOperation: operation,
        videoCameraMoveId: metadata?.videoCameraMoveId,
        videoCameraMovePrompt: metadata?.videoCameraMovePrompt,
        videoStartFrameNodeId: startFrame,
        videoEndFrameNodeId: endFrame,
    };
}

// xAI 编辑按原片时长计费，续写的 duration 只表示新增部分；首次生成和付费重试必须共用同一规则。
export function normalizeXAISourceVideoConfig(config: AiConfig, operation: CanvasVideoEditOperation | undefined, referenceVideos: ReferenceVideo[]) {
    const request = resolveModelRequestConfig(config, config.model);
    if (!isXAIVideoRequest(request.interfaceType, request.model)) return config;
    if (operation === "extend") {
        return { ...config, videoSeconds: String(Math.max(2, Math.min(10, Number(config.videoSeconds) || 6))) };
    }
    if (operation !== "edit_video" || referenceVideos.length !== 1) return config;
    const sourceDuration = referenceVideos[0].durationMs || 0;
    return sourceDuration > 0 ? { ...config, videoSeconds: String(Math.max(1, Math.ceil(sourceDuration / 1_000))) } : config;
}

export function normalizeVideoReferenceImages(config: AiConfig, metadata: CanvasNodeMetadata | undefined, referenceImages: ReferenceImage[]) {
    const request = resolveModelRequestConfig(config, config.model);
    if (!isXAIVideoRequest(request.interfaceType, request.model)) return referenceImages;

    if (metadata?.videoEditOperation === "edit_video" || metadata?.videoEditOperation === "extend") {
        if (referenceImages.length) {
            throw new Error(`xAI ${metadata.videoEditOperation === "edit_video" ? "视频编辑" : "视频续写"}只接受 1 段 MP4 原片，不能混用图片。本次未调用供应商`);
        }
        return [];
    }

    if (metadata?.videoEditOperation === "reference_to_video") {
        const channel = config.channels.find((item) => item.id === request.resolvedChannelId);
        const modelCost = channel?.modelCosts?.find((item) => item.model === request.model);
        const capability = videoCapabilityFromConfig(modelCost?.capabilityConfig, request.interfaceType);
        const maxImages = Math.min(7, Math.max(1, capability.references.maxImages || 7));
        if (referenceImages.length < 1 || referenceImages.length > maxImages) {
            throw new Error(`xAI 多参考图实验模式需要 1-${maxImages} 张图片，当前连接了 ${referenceImages.length} 张。本次未调用供应商`);
        }
        for (const [id, label] of [
            [metadata.videoStartFrameNodeId, "开场参考"],
            [metadata.videoEndFrameNodeId, "结尾参考"],
        ] as const) {
            if (id && !referenceImages.some((image) => image.id === id)) {
                throw new Error(`已选择的 xAI ${label}未包含在当前连接图片中，本次未调用供应商`);
            }
        }
        return referenceImages;
    }

    const startFrameId = metadata?.videoStartFrameNodeId;
    const endFrameId = metadata?.videoEndFrameNodeId;
    if (!startFrameId && endFrameId) {
        throw new Error("xAI 图生视频不支持尾帧；请把这张图片设为首帧，或切换到支持首尾帧的模型。本次未调用供应商");
    }
    if (startFrameId) {
        const startFrame = referenceImages.find((image) => image.id === startFrameId);
        if (!startFrame) throw new Error("已选择的 xAI 首帧未包含在当前连接图片中，本次未调用供应商");
        // xAI 的 image 字段只接受一张起始图；明确首帧后，其余连接图片不进入供应商请求。
        return [startFrame];
    }
    if (referenceImages.length > 1) {
        throw new Error(`xAI 图生视频只支持 1 张首帧，当前连接了 ${referenceImages.length} 张；请在视频提示词下方选择其中一张作为首帧。本次未调用供应商`);
    }
    return referenceImages;
}

export async function resolveMetadataReferences(metadata: CanvasNodeMetadata) {
    if (metadata.generationType !== "edit") return [];
    if (!metadata.references?.length) return null;
    return resolveStoredReferenceImages(metadata.references);
}

export async function hydrateCanvasImages(nodes: CanvasNodeData[]) {
    return Promise.all(
        nodes.map(async (node) => {
            const content = node.metadata?.content;
            if ((node.type === CanvasNodeType.Video || node.type === CanvasNodeType.Audio) && node.metadata?.storageKey) return { ...node, metadata: { ...node.metadata, content: await resolveMediaUrl(node.metadata.storageKey, content) } };
            if (node.type !== CanvasNodeType.Image || !content) return node;
            if (node.metadata?.storageKey) return { ...node, metadata: { ...node.metadata, content: await resolveImageUrl(node.metadata.storageKey, content, { cacheMiss: true }) } };
            if (!content.startsWith("data:image/")) return node;
            return { ...node, metadata: { ...node.metadata, ...imageMetadata(await uploadImage(content)) } };
        }),
    );
}

export async function hydrateAssistantImages(sessions: CanvasAssistantSession[]) {
    const hydrateItem = async <T extends { dataUrl?: string; storageKey?: string }>(item: T) => {
        if (item.storageKey) return { ...item, dataUrl: await resolveImageUrl(item.storageKey, item.dataUrl) };
        if (item.dataUrl?.startsWith("data:image/")) {
            const image = await uploadImage(item.dataUrl);
            return { ...item, dataUrl: image.url, storageKey: image.storageKey };
        }
        return item;
    };
    return Promise.all(
        sessions.map(async (session) => ({
            ...session,
            messages: await Promise.all(
                session.messages.map(async (message) => ({
                    ...message,
                    references: await Promise.all((message.references || []).map(hydrateItem)),
                })),
            ),
        })),
    );
}

export function getGenerationCount(count: string) {
    return Math.max(1, Math.min(15, Math.floor(Math.abs(Number(count)) || 1)));
}


export function buildGenerationConfig(config: AiConfig, node: CanvasNodeData | undefined, mode: CanvasNodeGenerationMode): AiConfig {
    const defaultModel = mode === "image" ? config.imageModel : mode === "video" ? config.videoModel : mode === "audio" ? config.audioModel : config.textModel;
    const storedModel = node?.metadata?.model;
    // 节点或默认项已经绑定过渠道模型时，渠道被删除或能力被撤销必须保留原值并明确失败，
    // 不能静默落到另一条同名渠道，也不能用内置占位模型遮住真正失效的系统选择。
    const selectedModel = resolveModelSelectionForCapability(config, mode, storedModel, defaultModel);
    // 旧画布节点可能只保存了裸模型名；生成前必须补回渠道前缀，否则同名模型会被解析到第一条渠道，测活结论与实际请求就会错配。
    const model = normalizeModelOptionValue(selectedModel, config.channels) || selectedModel;
    const modelConfig = { ...config, model };
    const requestConfig = resolveModelRequestConfig(modelConfig, model);
    const rawSize = node?.metadata?.size || config.size || defaultConfig.size;
    let size = rawSize;
    let videoSeconds = normalizeVideoDuration(node?.metadata?.seconds || config.videoSeconds || defaultConfig.videoSeconds);
    let videoResolution = normalizeVideoResolution(node?.metadata?.vquality || config.vquality || defaultConfig.vquality);
    let videoGenerateAudio = node?.metadata?.generateAudio || config.videoGenerateAudio || defaultConfig.videoGenerateAudio;
    let videoWatermark = node?.metadata?.watermark || config.videoWatermark || defaultConfig.videoWatermark;

    // 图片、文本和音频任务不依赖视频能力 JSON。旧渠道若把视频列表保存为 null，
    // 不应阻断图片任务，更不能在任务创建前泄漏原始 JavaScript 异常。
    if (mode === "video") {
        const requestChannel = modelConfig.channels.find((channel) => channel.id === requestConfig.resolvedChannelId);
        const modelCost = requestChannel?.modelCosts?.find((item) => item.model === requestConfig.model);
        const capability = videoCapabilityFromConfig(modelCost?.capabilityConfig, requestConfig.interfaceType);
        const requestedRatio = ratioFromSize(rawSize, capability.ratios) || normalizeCapabilityRatio(capability.defaultRatio);
        size = isSeedanceVideoConfig(modelConfig) ? requestedRatio : sizeForCapabilityRatio(requestedRatio);
        const normalizedResolution = normalizeCapabilityResolution(node?.metadata?.vquality || config.vquality || defaultConfig.vquality);
        const supportedResolutions = capability.resolutions.map(normalizeCapabilityResolution);
        videoResolution = supportedResolutions.includes(normalizedResolution) ? normalizedResolution.replace(/p$/, "") : normalizeCapabilityResolution(capability.defaultResolution).replace(/p$/i, "");
        if (isXAIVideoRequest(requestConfig.interfaceType, requestConfig.model) && node?.metadata?.videoEditOperation === "reference_to_video" && normalizeCapabilityResolution(videoResolution) === "1080p") {
            videoResolution = "720";
        }
        videoSeconds = String(normalizeCapabilityDuration(node?.metadata?.seconds || config.videoSeconds || defaultConfig.videoSeconds, capability));
        if (isXAIVideoRequest(requestConfig.interfaceType, requestConfig.model) && node?.metadata?.videoEditOperation === "extend") {
            videoSeconds = String(Math.max(2, Math.min(10, Number(videoSeconds) || 6)));
        }
        if (!capability.generateAudio.supported) videoGenerateAudio = "false";
        if (!capability.watermark.supported) videoWatermark = "false";
    }
    return {
        ...modelConfig,
        model,
        quality: node?.metadata?.quality || config.quality || defaultConfig.quality,
        size,
        transparentBackground: (node?.metadata?.transparentBackground || config.transparentBackground) === "true" ? "true" : "false",
        videoSeconds,
        vquality: videoResolution,
        videoGenerateAudio,
        videoWatermark,
        audioVoice: node?.metadata?.audioVoice || config.audioVoice || defaultConfig.audioVoice,
        audioFormat: node?.metadata?.audioFormat || config.audioFormat || defaultConfig.audioFormat,
        audioSpeed: node?.metadata?.audioSpeed || config.audioSpeed || defaultConfig.audioSpeed,
        audioInstructions: node?.metadata?.audioInstructions || config.audioInstructions || defaultConfig.audioInstructions,
        count: String(node?.metadata?.count || (mode === "image" ? config.canvasImageCount || config.count : config.count) || defaultConfig.count),
    };
}

export function supportsVideoReferenceAudio(config: AiConfig) {
    const request = resolveModelRequestConfig(config, config.model);
    const channel = config.channels.find((item) => item.id === request.resolvedChannelId);
    const modelCost = channel?.modelCosts?.find((item) => item.model === request.model);
    const capability = videoCapabilityFromConfig(modelCost?.capabilityConfig, request.interfaceType);
    return capability.references.maxVideos > 0 || capability.references.maxAudios > 0 || isSeedanceVideoConfig(config);
}

export function resetInterruptedGeneration(nodes: CanvasNodeData[]) {
    const configHeight = NODE_DEFAULT_SIZE[CanvasNodeType.Config].height;
    return nodes.map((node) => {
        const resizedNode = node.type === CanvasNodeType.Config && node.height < configHeight ? { ...node, height: configHeight } : node.type === CanvasNodeType.Script && node.height < NODE_DEFAULT_SIZE[CanvasNodeType.Script].height ? { ...node, height: NODE_DEFAULT_SIZE[CanvasNodeType.Script].height } : node;
        return resizedNode.metadata?.status === "loading" ? { ...resizedNode, metadata: { ...resizedNode.metadata, errorDetails: "正在从任务中心恢复生成状态..." } } : resizedNode;
    });
}

export function isGenerationCanceled(error: unknown) {
    return error instanceof Error && (error.message === "请求已取消" || error.name === "AbortError");
}

export function findRetrySourceNode(nodeId: string, nodes: CanvasNodeData[], connections: CanvasConnection[]) {
    const queue = connections.filter((connection) => connection.toNodeId === nodeId).map((connection) => connection.fromNodeId);
    const visited = new Set<string>();
    while (queue.length) {
        const id = queue.shift()!;
        if (visited.has(id)) continue;
        visited.add(id);
        const node = nodes.find((item) => item.id === id);
        if (node?.type === CanvasNodeType.Config) return node;
        connections.filter((connection) => connection.toNodeId === id).forEach((connection) => queue.push(connection.fromNodeId));
    }
    return null;
}

export function sourceNodeReferenceImages(node: CanvasNodeData | null) {
    const reference = node ? nodeReferenceImage(node) : null;
    return reference ? [reference] : [];
}

export function isAudioFile(file: File) {
    return file.type.startsWith("audio/") || /\.(mp3|wav)$/i.test(file.name);
}
