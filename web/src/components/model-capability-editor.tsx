import { Divider, Input, InputNumber, Select, Space, Switch } from "antd";

import { defaultModelCapabilityConfig, MODEL_CAPABILITY_CONFIG_VERSION, type ModelCapabilityConfig, type VideoCapabilityConfig } from "@/lib/model-capabilities";

type Props = {
    protocol?: string;
    value?: ModelCapabilityConfig;
    onChange?: (value: ModelCapabilityConfig) => void;
};

export function ModelCapabilityEditor({ protocol, value, onChange }: Props) {
    const video = value?.video || defaultModelCapabilityConfig(protocol).video!;
    const update = (patch: Partial<VideoCapabilityConfig>) => onChange?.({ version: MODEL_CAPABILITY_CONFIG_VERSION, video: { ...video, ...patch } });
    const updateReferences = (patch: Partial<VideoCapabilityConfig["references"]>) => update({ references: { ...video.references, ...patch } });
    const updateDuration = (patch: Partial<VideoCapabilityConfig["duration"]>) => update({ duration: { ...video.duration, ...patch } });
    const updateBoolean = (key: "generateAudio" | "watermark", patch: Partial<VideoCapabilityConfig["generateAudio"]>) => update({ [key]: { ...video[key], ...patch } } as Pick<VideoCapabilityConfig, "generateAudio" | "watermark">);

    return (
        <div className="space-y-3 rounded-lg border border-border/70 bg-foreground/[.025] p-3">
            <div>
                <div className="text-sm font-semibold">视频能力约束</div>
                <div className="mt-1 text-xs text-foreground/50">这些参数会在任务计费和供应商请求前由后端再次校验。</div>
            </div>
            <div className="grid grid-cols-2 gap-3">
                <NumberField label="提示词上限（字符）" value={video.references.promptMaxChars} min={1} max={1_000_000} onChange={(next) => updateReferences({ promptMaxChars: next })} />
                <NumberField label="图片引用数" value={video.references.maxImages} min={0} max={100} onChange={(next) => updateReferences({ maxImages: next })} />
                <NumberField label="图片上限（MB）" value={bytesToMB(video.references.maxImageBytes)} min={0} max={4096} step={1} onChange={(next) => updateReferences({ maxImageBytes: mbToBytes(next) })} />
                <NumberField label="视频引用数" value={video.references.maxVideos} min={0} max={100} onChange={(next) => updateReferences({ maxVideos: next })} />
                <NumberField label="视频上限（MB）" value={bytesToMB(video.references.maxVideoBytes)} min={0} max={4096} step={1} onChange={(next) => updateReferences({ maxVideoBytes: mbToBytes(next) })} />
                <NumberField label="单个视频时长（秒）" value={video.references.maxVideoDurationSeconds} min={0} max={3600} onChange={(next) => updateReferences({ maxVideoDurationSeconds: next })} />
                <NumberField label="音频引用数" value={video.references.maxAudios} min={0} max={100} onChange={(next) => updateReferences({ maxAudios: next })} />
                <NumberField label="音频上限（MB）" value={bytesToMB(video.references.maxAudioBytes)} min={0} max={4096} step={1} onChange={(next) => updateReferences({ maxAudioBytes: mbToBytes(next) })} />
                <NumberField label="单个音频时长（秒）" value={video.references.maxAudioDurationSeconds} min={0} max={3600} onChange={(next) => updateReferences({ maxAudioDurationSeconds: next })} />
            </div>
            <Divider className="!my-2" />
            <div className="grid grid-cols-2 gap-3">
                <label className="text-xs"><span className="mb-1 block text-foreground/60">时长选择</span><Select className="w-full" value={video.duration.selection} options={[{ label: "范围", value: "range" }, { label: "固定选项", value: "enum" }]} onChange={(selection) => updateDuration({ selection: selection as "range" | "enum" })} /></label>
                {video.duration.selection === "range" ? <>
                    <NumberField label="最短时长（秒）" value={video.duration.min || 1} min={1} max={3600} onChange={(next) => updateDuration({ min: next })} />
                    <NumberField label="最长时长（秒）" value={video.duration.max || 15} min={1} max={3600} onChange={(next) => updateDuration({ max: next })} />
                    <NumberField label="步长（秒）" value={video.duration.step || 1} min={1} max={3600} onChange={(next) => updateDuration({ step: next })} />
                </> : <TextField label="固定时长（逗号分隔）" value={(video.duration.values || []).join(", ")} onChange={(next) => updateDuration({ values: parseNumberList(next) })} />}
                <NumberField label="默认时长（秒）" value={video.duration.default} min={1} max={3600} onChange={(next) => updateDuration({ default: next })} />
            </div>
            <TextField label="支持比例（逗号分隔，如 16:9, 9:16, adaptive）" value={video.ratios.join(", ")} onChange={(next) => update({ ratios: parseStringList(next) })} />
            <TextField label="默认比例" value={video.defaultRatio} onChange={(next) => update({ defaultRatio: next.trim() })} />
            <TextField label="支持分辨率（逗号分隔，如 480p, 720p）" value={video.resolutions.join(", ")} onChange={(next) => update({ resolutions: parseStringList(next) })} />
            <TextField label="默认分辨率" value={video.defaultResolution} onChange={(next) => update({ defaultResolution: next.trim() })} />
            <TextField label="生成模式（逗号分隔）" value={video.operations.join(", ")} onChange={(next) => update({ operations: parseStringList(next) })} />
            <TextField label="默认生成模式" value={video.defaultOperation} onChange={(next) => update({ defaultOperation: next.trim() })} />
            <div className="grid grid-cols-2 gap-3">
                <BooleanField label="支持生成声音" value={video.generateAudio} onChange={(patch) => updateBoolean("generateAudio", patch)} />
                <BooleanField label="支持水印" value={video.watermark} onChange={(patch) => updateBoolean("watermark", patch)} />
            </div>
        </div>
    );
}

function NumberField({ label, value, min, max, step = 1, onChange }: { label: string; value: number; min: number; max: number; step?: number; onChange: (value: number) => void }) {
    return <label className="text-xs"><span className="mb-1 block text-foreground/60">{label}</span><InputNumber className="!w-full" value={value} min={min} max={max} step={step} onChange={(next) => onChange(Math.max(min, Math.min(max, Number(next) || min)))} /></label>;
}

function TextField({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
    return <label className="block text-xs"><span className="mb-1 block text-foreground/60">{label}</span><Input value={value} onChange={(event) => onChange(event.target.value)} /></label>;
}

function BooleanField({ label, value, onChange }: { label: string; value: { supported: boolean; default: boolean }; onChange: (patch: Partial<{ supported: boolean; default: boolean }>) => void }) {
    return <div className="flex items-center justify-between rounded-md border border-border/70 px-3 py-2 text-xs"><Space size={8}><span>{label}</span><span className="text-foreground/45">默认</span><Switch size="small" checked={value.default} disabled={!value.supported} onChange={(checked) => onChange({ default: checked })} /></Space><Switch size="small" checked={value.supported} onChange={(supported) => onChange({ supported, default: supported ? value.default : false })} /></div>;
}

function parseStringList(value: string) {
    return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function parseNumberList(value: string) {
    return value.split(",").map((item) => Number(item.trim())).filter((item) => Number.isFinite(item) && item > 0).map(Math.floor);
}

function bytesToMB(value: number) {
    return value > 0 ? Math.round(value / (1024 * 1024)) : 0;
}

function mbToBytes(value: number) {
    return Math.round(value * 1024 * 1024);
}
