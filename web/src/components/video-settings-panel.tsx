import { type ReactNode } from "react";
import { Switch } from "antd";

import { ImageSettingsTheme } from "@/components/image-settings-panel";
import { boolConfig, isSeedanceFastModel, isSeedanceVideoConfig, normalizeSeedanceDuration, normalizeSeedanceRatio, normalizeSeedanceResolution, seedanceRatioOptions, seedanceResolutionOptions } from "@/lib/seedance-video";
import { type CanvasTheme } from "@/lib/canvas-theme";
import { alignVideoSizeToMultiple, durationValues, formatCapabilityRatio, normalizeCapabilityRatio, normalizeCapabilityResolution, ratioFromSize, sizeForCapabilityRatio, videoCapabilityFromConfig, type RunningHubParameterMapping } from "@/lib/model-capabilities";
import { normalizeVideoDuration, normalizeVideoResolution } from "@/lib/video-generation-options";
import { isXAIVideoRequest } from "@/lib/model-protocols";
import { modelOptionName, resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import type { CanvasVideoEditOperation } from "@/types/canvas";

const sizeOptions = [
    { value: "1280x720", label: "横屏", width: 1280, height: 720 },
    { value: "720x1280", label: "竖屏", width: 720, height: 1280 },
    { value: "1024x1024", label: "方形", width: 1024, height: 1024 },
    { value: "1792x1024", label: "宽屏", width: 1792, height: 1024 },
    { value: "1024x1792", label: "长图", width: 1024, height: 1792 },
    { value: "auto", label: "auto", width: 0, height: 0 },
];

type VideoSettingsPanelProps = {
    config: AiConfig;
    operation?: CanvasVideoEditOperation;
    onConfigChange: (key: "vquality" | "size" | "videoSeconds" | "videoGenerateAudio" | "videoWatermark", value: string) => void;
    workflowParameters?: Record<string, string>;
    onWorkflowParameterChange?: (key: string, value: string) => void;
    theme: CanvasTheme;
    showTitle?: boolean;
    className?: string;
};

export function VideoSettingsPanel({ config, operation, onConfigChange, workflowParameters, onWorkflowParameterChange, theme, showTitle = true, className = "w-[292px] space-y-3" }: VideoSettingsPanelProps) {
    const capability = videoCapabilityForConfig(config);
    if (isSeedanceVideoConfig(config)) {
        return <SeedanceVideoSettingsPanel config={config} capability={capability} onConfigChange={onConfigChange} theme={theme} showTitle={showTitle} className={className} />;
    }

    const seconds = Number(config.videoSeconds) || capability.duration.default;
    const size = normalizeVideoSizeValue(config.size);
    const dimensions = readSizeDimensions(size);
    const resolution = normalizeCapabilityResolution(config.vquality);
    const request = resolveModelRequestConfig(config, config.model || config.videoModel);
    const runningHubParameters = request.interfaceType === "runninghub-workflow" ? capability.runningHub?.parameters.filter((item) => item.userEditable) || [] : [];
    const runningHubDimensionMultiple = request.interfaceType === "runninghub-workflow" ? capability.runningHub?.dimensionMultiple || 1 : 1;
    const alignedRunningHubSize = alignVideoSizeToMultiple(size, runningHubDimensionMultiple);
    const xai = isXAIVideoRequest(request.interfaceType, request.model);
    const xaiReferenceMode = operation === "reference_to_video" && xai;
    const xaiEditMode = operation === "edit_video" && xai;
    const xaiExtensionMode = operation === "extend" && xai;
    const xaiSourceVideoMode = xaiEditMode || xaiExtensionMode;
    const durationOptions = durationValues(capability).filter((value) => !xaiExtensionMode || (value >= 2 && value <= 10));
    const resolutionOptions = uniqueResolutionOptions(capability.resolutions).filter((item) => !xaiReferenceMode || normalizeCapabilityResolution(item.value) !== "1080p");
    const ratio = ratioFromSize(size, capability.ratios);
    const updateDimension = (key: "width" | "height", value: number | null) => {
        const next = Math.max(1, Math.floor(value || dimensions[key] || 720));
        onConfigChange("size", `${key === "width" ? next : dimensions.width}x${key === "height" ? next : dimensions.height}`);
    };
    const commitRunningHubDimensions = () => {
        if (runningHubDimensionMultiple > 1 && alignedRunningHubSize !== size) onConfigChange("size", alignedRunningHubSize);
    };

    return (
        <ImageSettingsTheme theme={theme}>
            <div data-canvas-no-zoom className={className} style={{ color: theme.node.text }} onPointerDown={(event) => event.stopPropagation()} onMouseDown={(event) => event.stopPropagation()}>
                {showTitle ? <div className="text-sm font-semibold">视频设置</div> : null}
                {!xaiSourceVideoMode ? <SettingGroup title="清晰度" color={theme.node.muted}>
                    <div className="grid grid-cols-3 gap-1.5">
                        {resolutionOptions.map((item) => (
                            <OptionPill key={item.value} selected={resolution === normalizeCapabilityResolution(item.value)} theme={theme} onClick={() => onConfigChange("vquality", item.value)}>
                                {item.label}
                            </OptionPill>
                        ))}
                    </div>
                    {xaiReferenceMode ? <div className="text-[10px] leading-4 opacity-55">多参考图实验模式最高支持 720P</div> : null}
                </SettingGroup> : null}
                {!xaiSourceVideoMode ? <SettingGroup title="尺寸" color={theme.node.muted}>
                    <div className="grid grid-cols-[1fr_auto_1fr] items-center gap-1.5">
                        <DimensionInput prefix="W" value={dimensions.width} disabled={size === "auto"} step={runningHubDimensionMultiple} theme={theme} onChange={(value) => updateDimension("width", value)} onCommit={commitRunningHubDimensions} />
                        <span className="text-xs opacity-45">×</span>
                        <DimensionInput prefix="H" value={dimensions.height} disabled={size === "auto"} step={runningHubDimensionMultiple} theme={theme} onChange={(value) => updateDimension("height", value)} onCommit={commitRunningHubDimensions} />
                    </div>
                    {runningHubDimensionMultiple > 1 ? <div className="text-[10px] leading-4 opacity-60">宽高需按 {runningHubDimensionMultiple} 对齐{alignedRunningHubSize !== size ? `，当前值将在提交时调整为 ${alignedRunningHubSize.replace("x", "×")}` : ""}</div> : null}
                    <div className="grid grid-cols-3 gap-1.5">
                        {capability.ratios.map((value) => {
                            const item = { value, label: formatCapabilityRatio(value), ...ratioPreview(normalizeCapabilityRatio(value)) };
                            const selected = ratio === normalizeCapabilityRatio(value);
                            return (
                            <button
                                key={item.value}
                                type="button"
                                className="flex h-8 cursor-pointer items-center justify-center gap-1.5 rounded-md border px-1 text-[11px] font-medium transition hover:opacity-80"
                                style={{ background: selected ? theme.accent.primarySoft : "transparent", borderColor: selected ? theme.accent.primary : theme.node.stroke, color: selected ? theme.accent.primary : theme.node.text }}
                                onMouseDown={(event) => event.stopPropagation()}
                                onClick={() => onConfigChange("size", alignVideoSizeToMultiple(sizeForCapabilityRatio(item.value), runningHubDimensionMultiple))}
                            >
                                <SizePreview width={item.width} height={item.height} color={selected ? theme.accent.primary : theme.node.text} />
                                <span>{item.label}</span>
                            </button>
                            );
                        })}
                    </div>
                </SettingGroup> : (
                    <div className="rounded-md border px-2.5 py-2 text-[10px] leading-4 opacity-70" style={{ borderColor: theme.node.stroke }}>
                        {xaiEditMode
                            ? "连接 1 段不超过 8.7 秒的 MP4 原片；编辑结果沿用原片时长和画幅，分辨率最高为 720P。"
                            : "连接 1 段 2–15 秒的 MP4 原片；结果会包含原片，下面设置的秒数只表示新增部分，画幅沿用原片且最高为 720P。"}
                    </div>
                )}
                {!xaiEditMode ? <SettingGroup title={xaiExtensionMode ? "新增时长" : "秒数"} color={theme.node.muted}>
                    {durationOptions.length ? <div className="grid grid-cols-4 gap-1.5">
                        {durationOptions.map((value) => (
                            <OptionPill key={value} selected={seconds === value} theme={theme} onClick={() => onConfigChange("videoSeconds", String(value))}>
                                {value}s
                            </OptionPill>
                        ))}
                    </div> : <DurationInput value={seconds} min={xaiExtensionMode ? 2 : capability.duration.min || 1} max={xaiExtensionMode ? 10 : capability.duration.max || 3600} step={capability.duration.step || 1} theme={theme} onChange={(value) => onConfigChange("videoSeconds", String(value))} />}
                </SettingGroup> : null}
                {capability.generateAudio.supported || capability.watermark.supported ? <SettingGroup title="输出" color={theme.node.muted}>
                    <div className="grid grid-cols-2 gap-3 rounded-md border px-2" style={{ borderColor: theme.node.stroke }}>
                        {capability.generateAudio.supported ? <SwitchRow label="生成声音" checked={boolConfig(config.videoGenerateAudio, capability.generateAudio.default)} theme={theme} onChange={(checked) => onConfigChange("videoGenerateAudio", String(checked))} /> : null}
                        {capability.watermark.supported ? <SwitchRow label="添加水印" checked={boolConfig(config.videoWatermark, capability.watermark.default)} theme={theme} onChange={(checked) => onConfigChange("videoWatermark", String(checked))} /> : null}
                    </div>
                </SettingGroup> : null}
                {runningHubParameters.length ? <SettingGroup title="工作流参数" color={theme.node.muted}>
                    <div className="space-y-2 rounded-md border p-2" style={{ borderColor: theme.node.stroke }}>
                        {runningHubParameters.map((mapping) => <RunningHubParameterField key={`${mapping.nodeId}:${mapping.fieldName}`} mapping={mapping} value={workflowParameters?.[`${mapping.nodeId}:${mapping.fieldName}`] ?? mapping.defaultValue ?? ""} theme={theme} onChange={(value) => onWorkflowParameterChange?.(`${mapping.nodeId}:${mapping.fieldName}`, value)} />)}
                    </div>
                </SettingGroup> : null}
            </div>
        </ImageSettingsTheme>
    );
}

function RunningHubParameterField({ mapping, value, theme, onChange }: { mapping: RunningHubParameterMapping; value: string; theme: CanvasTheme; onChange: (value: string) => void }) {
    if (mapping.valueType === "boolean") {
        return <div className="flex min-h-8 items-center justify-between gap-3 text-[11px]"><span>{mapping.label}</span><Switch size="small" checked={value === "true"} onChange={(checked) => onChange(String(checked))} /></div>;
    }
    return <label className="block text-[11px]">
        <span className="mb-1 block" style={{ color: theme.node.muted }}>{mapping.label}</span>
        <input
            type={mapping.valueType === "string" ? "text" : "number"}
            step={mapping.valueType === "integer" ? 1 : mapping.valueType === "number" ? "any" : undefined}
            className="h-8 w-full rounded-md border bg-transparent px-2 outline-none"
            style={{ borderColor: theme.node.stroke, color: theme.node.text }}
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onMouseDown={(event) => event.stopPropagation()}
        />
    </label>;
}

function SeedanceVideoSettingsPanel({ config, capability, onConfigChange, theme, showTitle, className }: VideoSettingsPanelProps & { capability: ReturnType<typeof videoCapabilityForConfig> }) {
    const model = modelOptionName(config.model || config.videoModel);
    const resolution = normalizeSeedanceResolution(config.vquality, model);
    const ratio = normalizeSeedanceRatio(config.size);
    const duration = normalizeSeedanceDuration(config.videoSeconds);
    const generateAudio = capability.generateAudio.supported && boolConfig(config.videoGenerateAudio, capability.generateAudio.default);
    const watermark = capability.watermark.supported && boolConfig(config.videoWatermark, capability.watermark.default);
    const dynamicDurations = durationValues(capability);
    const dynamicRatios = capability.ratios.length ? capability.ratios : seedanceRatioOptions.map((item) => item.value);
    const dynamicResolutions = capability.resolutions.length ? uniqueResolutionOptions(capability.resolutions).map((item) => item.value) : seedanceResolutionOptions.map((item) => item.value);

    return (
        <ImageSettingsTheme theme={theme}>
            <div data-canvas-no-zoom className={className} style={{ color: theme.node.text }} onPointerDown={(event) => event.stopPropagation()} onMouseDown={(event) => event.stopPropagation()}>
                {showTitle ? <div className="text-sm font-semibold">视频设置</div> : null}
                <SettingGroup title="分辨率" color={theme.node.muted}>
                    <div className="grid grid-cols-3 gap-1.5">
                        {dynamicResolutions.map((value) => {
                            const item = { value: normalizeCapabilityResolution(value), label: `${normalizeCapabilityResolution(value).replace(/p$/i, "")}P` };
                            const disabled = item.value === "1080p" && isSeedanceFastModel(model);
                            return (
                                <OptionPill key={item.value} selected={resolution === item.value} disabled={disabled} theme={theme} onClick={() => onConfigChange("vquality", item.value)}>
                                    {item.label}
                                </OptionPill>
                            );
                        })}
                    </div>
                    {isSeedanceFastModel(model) ? <div className="text-[10px] leading-4 opacity-55">fast 模型自动使用 720P</div> : null}
                </SettingGroup>
                <SettingGroup title="比例" color={theme.node.muted}>
                    <div className="grid grid-cols-4 gap-1.5">
                        {dynamicRatios.map((value) => {
                            const normalized = normalizeCapabilityRatioForSeedance(value);
                            const item = { value: normalized, label: formatCapabilityRatio(normalized) };
                            return (
                            <button
                                key={item.value}
                                type="button"
                                className="flex h-11 min-w-0 cursor-pointer flex-col items-center justify-center gap-0.5 rounded-md border px-1 text-[10px] font-medium leading-none transition hover:opacity-80"
                                style={{ background: ratio === item.value ? theme.accent.primarySoft : "transparent", borderColor: ratio === item.value ? theme.accent.primary : theme.node.stroke, color: ratio === item.value ? theme.accent.primary : theme.node.text }}
                                onMouseDown={(event) => event.stopPropagation()}
                                onClick={() => onConfigChange("size", item.value)}
                            >
                                <span className="grid h-4 place-items-center">
                                    <SizePreview width={ratioPreview(item.value).width} height={ratioPreview(item.value).height} color={ratio === item.value ? theme.accent.primary : theme.node.text} />
                                </span>
                                <span className="whitespace-nowrap">{item.label}</span>
                            </button>
                            );
                        })}
                    </div>
                </SettingGroup>
                <SettingGroup title="时长" color={theme.node.muted}>
                    {dynamicDurations.length ? <div className="grid grid-cols-4 gap-1.5">
                        {dynamicDurations.map((value) => (
                            <OptionPill key={value} selected={duration === value} theme={theme} onClick={() => onConfigChange("videoSeconds", String(value))}>
                                {value}s
                            </OptionPill>
                        ))}
                    </div> : <DurationInput value={duration} min={capability.duration.min || 1} max={capability.duration.max || 3600} step={capability.duration.step || 1} theme={theme} onChange={(value) => onConfigChange("videoSeconds", String(value))} />}
                </SettingGroup>
                {capability.generateAudio.supported || capability.watermark.supported ? <SettingGroup title="输出" color={theme.node.muted}>
                    <div className="grid grid-cols-2 gap-3 rounded-md border px-2" style={{ borderColor: theme.node.stroke }}>
                        {capability.generateAudio.supported ? <SwitchRow label="生成声音" checked={generateAudio} theme={theme} onChange={(checked) => onConfigChange("videoGenerateAudio", String(checked))} /> : null}
                        {capability.watermark.supported ? <SwitchRow label="添加水印" checked={watermark} theme={theme} onChange={(checked) => onConfigChange("videoWatermark", String(checked))} /> : null}
                    </div>
                </SettingGroup> : null}
            </div>
        </ImageSettingsTheme>
    );
}

export function videoResolutionLabel(value: string) {
    return `${normalizeVideoResolutionValue(value)}P`;
}

export function videoSizeLabel(value: string) {
    const ratio = normalizeSeedanceRatio(value);
    if (value === "adaptive" || value === "auto") return "自适应";
    if (ratio === value) return seedanceRatioOptions.find((item) => item.value === ratio)?.label || ratio;
    const size = normalizeVideoSizeValue(value);
    return sizeOptions.find((item) => item.value === size)?.label || size;
}

export function videoSecondsLabel(value: string) {
    const seconds = Math.max(1, Math.floor(Number(value) || Number(normalizeVideoDuration(value))));
    return `${seconds}s`;
}

export function normalizeVideoSizeValue(value: string) {
    if (value === "auto") return "auto";
    if (/^\d+x\d+$/.test(value || "")) return value;
    return ["9:16", "2:3", "3:4"].includes(value) ? "720x1280" : "1280x720";
}

export function normalizeVideoResolutionValue(value: string) {
    return normalizeVideoResolution(value);
}

function videoCapabilityForConfig(config: AiConfig) {
    const request = resolveModelRequestConfig(config, config.model || config.videoModel);
    const channel = config.channels.find((item) => item.id === request.resolvedChannelId);
    const modelCost = channel?.modelCosts?.find((item) => item.model === request.model);
    return videoCapabilityFromConfig(modelCost?.capabilityConfig, request.interfaceType);
}

function normalizeCapabilityRatioForSeedance(value: string) {
    const normalized = value.toLowerCase().trim();
    return normalized === "auto" ? "adaptive" : normalized;
}

function OptionPill({ selected, disabled = false, theme, onClick, children }: { selected: boolean; disabled?: boolean; theme: CanvasTheme; onClick: () => void; children: ReactNode }) {
    return (
        <button type="button" disabled={disabled} className="h-8 cursor-pointer whitespace-nowrap rounded-md border px-1 text-[11px] font-medium leading-none transition hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-35" style={{ background: selected ? theme.accent.primarySoft : "transparent", borderColor: selected ? theme.accent.primary : theme.node.stroke, color: selected ? theme.accent.primary : theme.node.text }} onMouseDown={(event) => event.stopPropagation()} onClick={onClick}>
            {children}
        </button>
    );
}

function SettingGroup({ title, color, children }: { title: string; color: string; children: ReactNode }) {
    return (
        <div className="space-y-1.5">
            <div className="text-[10px] font-semibold" style={{ color }}>
                {title}
            </div>
            {children}
        </div>
    );
}

function DimensionInput({ prefix, value, disabled, step = 1, theme, onChange, onCommit }: { prefix: string; value: number; disabled: boolean; step?: number; theme: CanvasTheme; onChange: (value: number | null) => void; onCommit?: () => void }) {
    return (
        <label className="flex h-8 overflow-hidden rounded-md border text-[11px]" style={{ background: theme.node.fill, borderColor: theme.node.stroke, color: theme.node.text, opacity: disabled ? 0.55 : 1 }}>
            <span className="grid w-7 place-items-center" style={{ color: theme.node.muted }}>
                {prefix}
            </span>
            <input type="number" min={1} step={step} disabled={disabled} className="min-w-0 flex-1 bg-transparent px-2 outline-none [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none" value={value || ""} onChange={(event) => onChange(Number(event.target.value) || null)} onBlur={onCommit} onMouseDown={(event) => event.stopPropagation()} />
        </label>
    );
}

function DurationInput({ value, min, max, step, theme, onChange }: { value: number; min: number; max: number; step: number; theme: CanvasTheme; onChange: (value: number) => void }) {
    return <label className="flex h-8 overflow-hidden rounded-md border text-[11px]" style={{ background: theme.node.fill, borderColor: theme.node.stroke, color: theme.node.text }}>
        <span className="grid w-10 place-items-center" style={{ color: theme.node.muted }}>秒数</span>
        <input type="number" min={min} max={max} step={step} className="min-w-0 flex-1 bg-transparent px-2 outline-none [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none" value={value || ""} onChange={(event) => onChange(Number(event.target.value) || min)} onMouseDown={(event) => event.stopPropagation()} />
    </label>;
}

function SizePreview({ width, height, color }: { width: number; height: number; color: string }) {
    if (!width || !height) return null;
    const longSide = Math.max(width, height);
    const previewWidth = Math.max(7, Math.round((width / longSide) * 16));
    const previewHeight = Math.max(7, Math.round((height / longSide) * 16));
    return <span className="shrink-0 rounded-[2px] border" style={{ width: previewWidth, height: previewHeight, borderColor: color }} />;
}

function ratioPreview(ratio: string) {
    if (ratio === "9:16") return { width: 9, height: 16 };
    if (ratio === "1:1") return { width: 1, height: 1 };
    if (ratio === "4:3") return { width: 4, height: 3 };
    if (ratio === "3:4") return { width: 3, height: 4 };
    if (ratio === "21:9") return { width: 21, height: 9 };
    if (ratio === "adaptive") return { width: 0, height: 0 };
    const match = ratio.match(/^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$/);
    if (match) return { width: Number(match[1]), height: Number(match[2]) };
    return { width: 16, height: 9 };
}

function uniqueResolutionOptions(values: string[]) {
    const seen = new Set<string>();
    return values.reduce<Array<{ value: string; label: string }>>((result, value) => {
        const normalized = normalizeCapabilityResolution(value);
        if (seen.has(normalized)) return result;
        seen.add(normalized);
        result.push({ value: normalized.replace(/p$/i, ""), label: `${normalized.replace(/p$/i, "")}P` });
        return result;
    }, []);
}

function SwitchRow({ label, checked, theme, onChange }: { label: string; checked: boolean; theme: CanvasTheme; onChange: (checked: boolean) => void }) {
    return (
        <div className="flex h-8 items-center justify-between gap-2">
            <span className="min-w-0 whitespace-nowrap text-[11px]" style={{ color: theme.node.text }}>
                {label}
            </span>
            <span className="shrink-0" onMouseDown={(event) => event.stopPropagation()}>
                <Switch size="small" checked={checked} onChange={onChange} />
            </span>
        </div>
    );
}

function readSizeDimensions(size: string) {
    if (size === "auto") return { width: 0, height: 0 };
    const match = size.match(/^(\d+)x(\d+)$/);
    return { width: Number(match?.[1]) || 1280, height: Number(match?.[2]) || 720 };
}
