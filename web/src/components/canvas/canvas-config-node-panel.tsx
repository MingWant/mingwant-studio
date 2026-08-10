import type { CSSProperties } from "react";
import { Image as ImageIcon, LoaderCircle, MessageSquare, Music2, Play, Settings2, Square, Video } from "lucide-react";
import { Button, Segmented } from "antd";

import { ModelPicker } from "@/components/model-picker";
import { defaultConfig, modelOptionName, resolveModelChannel, resolveModelRequestConfig, resolveModelSelectionForCapability, useEffectiveConfig, type AiConfig } from "@/stores/use-config-store";
import { CreditSymbol, requestCreditCost } from "@/constant/credits";
import { canvasThemes } from "@/lib/canvas-theme";
import { normalizeCapabilityDuration, resolveVideoOperation, videoCapabilityFromConfig } from "@/lib/model-capabilities";
import { isXAIVideoRequest } from "@/lib/model-protocols";
import { normalizeVideoDuration, normalizeVideoResolution } from "@/lib/video-generation-options";
import { navigateToSettings } from "@/lib/settings-navigation";
import { hasPendingCanvasGenerationTask } from "@/lib/canvas/canvas-generation-task-state";
import { useThemeStore } from "@/stores/use-theme-store";
import { CanvasImageSettingsPopover } from "./canvas-image-settings-popover";
import { CanvasAudioSettingsPopover, type CanvasAudioSettingKey } from "./canvas-audio-settings-popover";
import { CanvasVideoOperationSelect } from "./canvas-video-operation-select";
import { CanvasVideoSettingsPopover } from "./canvas-video-settings-popover";
import type { CanvasGenerationMode, CanvasNodeData, CanvasNodeMetadata, CanvasWorkspaceMode } from "@/types/canvas";

type CanvasConfigNodePanelProps = {
    node: CanvasNodeData;
    isRunning: boolean;
    inputSummary: { textCount: number; imageCount: number; videoCount: number; videoDurationMs?: number; audioCount: number };
    onConfigChange: (nodeId: string, patch: Partial<CanvasNodeMetadata>) => void;
    onGenerate: (nodeId: string) => void;
    onStop: (nodeId: string) => void;
    onComposerToggle: () => void;
    workspaceMode?: CanvasWorkspaceMode;
};

export function CanvasConfigNodePanel({ node, isRunning, inputSummary, onConfigChange, onGenerate, onStop, onComposerToggle, workspaceMode = "professional" }: CanvasConfigNodePanelProps) {
    const globalConfig = useEffectiveConfig();
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const mode = node.metadata?.generationMode || "image";
    const simpleMode = workspaceMode === "simple";
    const config = buildNodeConfig(globalConfig, node, mode);
    const videoCapability = mode === "video" ? videoCapabilityForConfig(config) : undefined;
    const videoReferenceCounts = { images: inputSummary.imageCount, videos: inputSummary.videoCount, audios: inputSummary.audioCount };
    const selectedOperation = mode === "video" ? resolveVideoOperation(node.metadata?.videoEditOperation, videoReferenceCounts, videoCapability) : undefined;
    const videoRequest = mode === "video" ? resolveModelRequestConfig(config, config.model) : undefined;
    const xaiVideo = Boolean(videoRequest && isXAIVideoRequest(videoRequest.interfaceType, videoRequest.model));
    const xaiReferenceMode = xaiVideo && selectedOperation === "reference_to_video";
    const xaiSourceVideoMode = xaiVideo && (selectedOperation === "edit_video" || selectedOperation === "extend");
    const count = Math.max(1, Math.min(15, Math.floor(Math.abs(Number(config.count)) || 1)));
    const priceChannel = resolveModelChannel(config, config.model);
    const editBillingSeconds = xaiVideo && selectedOperation === "edit_video" && inputSummary.videoCount === 1 && (inputSummary.videoDurationMs || 0) > 0
        ? Math.ceil((inputSummary.videoDurationMs || 0) / 1_000)
        : config.videoSeconds;
    const credits = requestCreditCost({ channelMode: priceChannel.scope === "system" ? "remote" : "local", modelCosts: priceChannel.modelCosts, model: modelOptionName(config.model), count: mode === "image" ? count : 1, seconds: mode === "video" ? editBillingSeconds : 1 });
    const hasPrice = credits !== null;
    const chipStyle = { background: theme.node.fill, borderColor: theme.node.stroke, color: theme.node.text };
    const hasAnyInput = Boolean(inputSummary.textCount || inputSummary.imageCount || inputSummary.videoCount || inputSummary.audioCount);
    const hasComposerContent = Boolean((node.metadata?.composerContent ?? node.metadata?.prompt ?? "").trim());
    const canGenerate = hasComposerContent || (mode === "audio" ? inputSummary.textCount > 0 : hasAnyInput);
    const backendTaskPending = !isRunning && hasPendingCanvasGenerationTask(node);
    return (
        <div className="flex h-full w-full cursor-move flex-col px-3 pb-3 pt-7 text-sm" style={{ color: theme.node.text }} onWheel={(event) => event.stopPropagation()}>
            <div className="mb-2 flex items-center justify-between gap-3">
                <div className="shrink-0 text-sm font-semibold">{simpleMode ? "快速生成" : "生成配置"}</div>
                {simpleMode ? <span className="rounded-md px-2 py-1 text-[10px]" style={{ background: theme.node.fill, color: theme.node.muted }}>自动配置</span> : <div data-canvas-no-zoom className="cursor-default" onMouseDown={(event) => event.stopPropagation()} onPointerDown={(event) => event.stopPropagation()}>
                    <Segmented
                        size="small"
                        className="canvas-config-mode !rounded-md !p-0.5"
                        value={mode}
                        onChange={(value) => onConfigChange(node.id, { generationMode: value as CanvasGenerationMode })}
                        options={[
                            {
                                value: "image",
                                label: (
                                    <span className="inline-flex items-center gap-1">
                                        <ImageIcon className="size-3.5" />
                                        生图
                                    </span>
                                ),
                            },
                            {
                                value: "text",
                                label: (
                                    <span className="inline-flex items-center gap-1">
                                        <MessageSquare className="size-3.5" />
                                        文本
                                    </span>
                                ),
                            },
                            {
                                value: "video",
                                label: (
                                    <span className="inline-flex items-center gap-1">
                                        <Video className="size-3.5" />
                                        视频
                                    </span>
                                ),
                            },
                            {
                                value: "audio",
                                label: (
                                    <span className="inline-flex items-center gap-1">
                                        <Music2 className="size-3.5" />
                                        音频
                                    </span>
                                ),
                            },
                        ]}
                    />
                </div>}
            </div>

            <div className="mb-2 flex flex-wrap gap-1.5">
                <InputChip label="提示词" value={`${inputSummary.textCount} 个`} style={chipStyle} />
                <InputChip label="参考图" value={`${inputSummary.imageCount} 张`} style={chipStyle} />
                <InputChip label="参考视频" value={`${inputSummary.videoCount} 个`} style={chipStyle} />
                <InputChip label="参考音频" value={`${inputSummary.audioCount} 个`} style={chipStyle} />
                <button type="button" className="inline-flex h-7 cursor-pointer items-center gap-1 rounded-md border px-2 text-[11px]" style={chipStyle} onMouseDown={(event) => event.stopPropagation()} onClick={onComposerToggle}>
                    {simpleMode ? <MessageSquare className="size-3.5" /> : <Settings2 className="size-3.5" />}
                    {simpleMode ? "编辑生成内容" : "组装提示词"}
                </button>
            </div>

            {mode === "video" && !simpleMode ? (
                <div className="mb-2 min-w-0 cursor-default" data-canvas-no-zoom onMouseDown={(event) => event.stopPropagation()} onPointerDown={(event) => event.stopPropagation()}>
                    <CanvasVideoOperationSelect
                        value={selectedOperation}
                        operations={videoCapability?.operations}
                        onChange={(value) => onConfigChange(node.id, videoOperationPatch(config, node.metadata, value))}
                    />
                    {xaiReferenceMode ? <div className="mt-1 px-1 text-[9px] leading-4" style={{ color: theme.node.muted }}>提示词可用 &lt;IMAGE_1&gt;… 指向参考图；开场和结尾选择仅作软引导，最高 720P。</div> : null}
                    {xaiSourceVideoMode ? <div className="mt-1 px-1 text-[9px] leading-4" style={{ color: theme.node.muted }}>{selectedOperation === "edit_video" ? "连接 1 段不超过 8.7 秒的 MP4 原片；输出参数沿用原片。" : "连接 1 段 2–15 秒的 MP4 原片；时长设置表示新增 2–10 秒。"}</div> : null}
                </div>
            ) : null}

            {simpleMode ? (
                <div className="mb-2 rounded-lg px-2 py-2 text-[11px]" style={{ background: theme.node.fill, color: theme.node.muted }}>将使用当前默认模型与生成参数</div>
            ) : (
                <div className={`mb-2 grid min-w-0 cursor-default items-center gap-2 ${mode === "image" || mode === "video" || mode === "audio" ? "grid-cols-[minmax(0,1fr)_148px]" : "grid-cols-1"}`} onMouseDown={(event) => event.stopPropagation()}>
                    <ModelPicker
                        className="canvas-compact-control h-10"
                        config={config}
                        value={config.model}
                        onChange={(model) => {
                            if (mode !== "video") {
                                onConfigChange(node.id, { model });
                                return;
                            }
                            const nextCapability = videoCapabilityForConfig({ ...config, model });
                            const nextRequest = resolveModelRequestConfig({ ...config, model }, model);
                            const nextOperation = resolveVideoOperation(node.metadata?.videoEditOperation, videoReferenceCounts, nextCapability);
                            const nextIsXAI = isXAIVideoRequest(nextRequest.interfaceType, nextRequest.model);
                            const nextUsesImageFrame = nextOperation === "image_to_video" || nextOperation === "reference_to_video";
                            onConfigChange(node.id, {
                                model,
                                videoEditOperation: nextOperation,
                                videoStartFrameNodeId: nextIsXAI && !nextUsesImageFrame ? undefined : node.metadata?.videoStartFrameNodeId,
                                videoEndFrameNodeId: nextIsXAI && nextOperation !== "reference_to_video" ? undefined : node.metadata?.videoEndFrameNodeId,
                                vquality: nextIsXAI && nextOperation === "reference_to_video" && normalizeVideoResolution(config.vquality) === "1080" ? "720" : node.metadata?.vquality,
                                seconds: nextIsXAI && nextOperation === "extend" ? String(Math.max(2, Math.min(10, Number(node.metadata?.seconds || config.videoSeconds) || 6))) : node.metadata?.seconds,
                            });
                        }}
                        capability={mode}
                        onMissingConfig={() => navigateToSettings({ section: "models", continueCreation: true })}
                        fullWidth
                    />
                    {mode === "video" ? (
                        <CanvasVideoSettingsPopover config={config} operation={selectedOperation} placement="topRight" buttonClassName="canvas-compact-control !h-10 !w-full !justify-start !rounded-lg !px-2" onConfigChange={(key, value) => onConfigChange(node.id, videoConfigPatch(key, value))} />
                    ) : mode === "image" ? (
                        <CanvasImageSettingsPopover config={config} placement="topRight" autoAdjustOverflow={false} buttonClassName="canvas-compact-control !h-10 !w-full !justify-start !rounded-lg !px-2" onConfigChange={(key, value) => onConfigChange(node.id, key === "count" ? { count: Number(value) || 1 } : { [key]: value })} />
                    ) : mode === "audio" ? (
                        <CanvasAudioSettingsPopover config={config} placement="topRight" buttonClassName="canvas-compact-control !h-10 !w-full !justify-start !rounded-lg !px-2" onConfigChange={(key, value) => onConfigChange(node.id, audioConfigPatch(key, value))} />
                    ) : null}
                </div>
            )}

            <Button
                type="primary"
                className="mt-auto !h-9 !w-full !cursor-pointer !rounded-lg"
                danger={isRunning}
                disabled={backendTaskPending || (!isRunning && !canGenerate)}
                onMouseDown={(event) => event.stopPropagation()}
                onClick={() => (isRunning ? onStop(node.id) : onGenerate(node.id))}
            >
                <span className="inline-flex items-center gap-1.5">
                    {isRunning ? (
                        <>
                            <LoaderCircle className="size-4 animate-spin" />
                            <Square className="size-3.5 fill-current" />
                            <span>停止</span>
                        </>
                    ) : backendTaskPending ? (
                        <>
                            <LoaderCircle className="size-4 animate-spin" />
                            <span>后台任务进行中</span>
                        </>
                    ) : (
                        <>
                            {hasPrice ? (
                                <span className="inline-flex items-center gap-1">
                                    <CreditSymbol />
                                    {credits.toLocaleString()}
                                </span>
                            ) : (
                                <span className="text-xs" title="当前渠道没有模型价格数据">
                                    无价格
                                </span>
                            )}
                            <Play className="size-4" />
                            <span>开始生成</span>
                        </>
                    )}
                </span>
            </Button>
        </div>
    );
}

function videoCapabilityForConfig(config: AiConfig) {
    const request = resolveModelRequestConfig(config, config.model);
    const channel = config.channels.find((item) => item.id === request.resolvedChannelId);
    const modelCost = channel?.modelCosts?.find((item) => item.model === request.model);
    return videoCapabilityFromConfig(modelCost?.capabilityConfig, request.interfaceType);
}

function InputChip({ label, value, style }: { label: string; value: string; style: CSSProperties }) {
    return (
        <div className="inline-flex h-7 items-center gap-1 rounded-md border px-2 text-[11px]" style={style}>
            <span>{label}</span>
            <span className="font-medium">{value}</span>
        </div>
    );
}

function buildNodeConfig(globalConfig: AiConfig, node: CanvasNodeData, mode: CanvasGenerationMode): AiConfig {
    const defaultModel = mode === "image" ? globalConfig.imageModel : mode === "video" ? globalConfig.videoModel : mode === "audio" ? globalConfig.audioModel : globalConfig.textModel;
    const storedModel = node.metadata?.model;
    const model = resolveModelSelectionForCapability(globalConfig, mode, storedModel, defaultModel);
    const rawVideoSeconds = node.metadata?.seconds || globalConfig.videoSeconds || defaultConfig.videoSeconds;
    const videoCapability = mode === "video" ? videoCapabilityForConfig({ ...globalConfig, model }) : undefined;
    return {
        ...globalConfig,
        model,
        quality: node.metadata?.quality || globalConfig.quality || defaultConfig.quality,
        size: node.metadata?.size || globalConfig.size || defaultConfig.size,
        transparentBackground: (node.metadata?.transparentBackground || globalConfig.transparentBackground) === "true" ? "true" : "false",
        videoSeconds: videoCapability ? String(normalizeCapabilityDuration(rawVideoSeconds, videoCapability)) : normalizeVideoDuration(rawVideoSeconds),
        vquality: normalizeVideoResolution(node.metadata?.vquality || globalConfig.vquality || defaultConfig.vquality),
        videoGenerateAudio: node.metadata?.generateAudio || globalConfig.videoGenerateAudio || defaultConfig.videoGenerateAudio,
        videoWatermark: node.metadata?.watermark || globalConfig.videoWatermark || defaultConfig.videoWatermark,
        audioVoice: node.metadata?.audioVoice || globalConfig.audioVoice || defaultConfig.audioVoice,
        audioFormat: node.metadata?.audioFormat || globalConfig.audioFormat || defaultConfig.audioFormat,
        audioSpeed: node.metadata?.audioSpeed || globalConfig.audioSpeed || defaultConfig.audioSpeed,
        audioInstructions: node.metadata?.audioInstructions || globalConfig.audioInstructions || defaultConfig.audioInstructions,
        count: String(node.metadata?.count || (mode === "image" ? globalConfig.canvasImageCount || globalConfig.count : globalConfig.count) || defaultConfig.count),
    };
}

function videoConfigPatch(key: keyof AiConfig, value: string) {
    if (key === "videoSeconds") return { seconds: value };
    if (key === "videoGenerateAudio") return { generateAudio: value };
    if (key === "videoWatermark") return { watermark: value };
    return { [key]: value };
}

function videoOperationPatch(config: AiConfig, metadata: CanvasNodeMetadata | undefined, value: NonNullable<CanvasNodeMetadata["videoEditOperation"]>) {
    const request = resolveModelRequestConfig(config, config.model);
    const xai = isXAIVideoRequest(request.interfaceType, request.model);
    const usesImageFrame = value === "image_to_video" || value === "reference_to_video";
    return {
        videoEditOperation: value,
        videoStartFrameNodeId: xai && !usesImageFrame ? undefined : metadata?.videoStartFrameNodeId,
        videoEndFrameNodeId: xai && value !== "reference_to_video" ? undefined : metadata?.videoEndFrameNodeId,
        vquality: xai && value === "reference_to_video" && normalizeVideoResolution(config.vquality) === "1080" ? "720" : metadata?.vquality,
        ...(xai && value === "extend" ? { seconds: String(Math.max(2, Math.min(10, Number(metadata?.seconds || config.videoSeconds) || 6))) } : {}),
    };
}

function audioConfigPatch(key: CanvasAudioSettingKey, value: string) {
    if (key === "audioVoice") return { audioVoice: value };
    if (key === "audioFormat") return { audioFormat: value };
    if (key === "audioSpeed") return { audioSpeed: value };
    return { audioInstructions: value };
}
