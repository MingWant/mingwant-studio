import { Button, Divider, Input, InputNumber, Select, Space, Switch } from "antd";
import { Plus, Trash2 } from "lucide-react";

import { defaultModelCapabilityConfig, MODEL_CAPABILITY_CONFIG_VERSION, normalizeRunningHubWorkflowConfig, type ModelCapabilityConfig, type RunningHubParameterMapping, type RunningHubReferenceMapping, type RunningHubWorkflowConfig, type VideoCapabilityConfig } from "@/lib/model-capabilities";

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
    const runningHub = normalizeRunningHubWorkflowConfig(video.runningHub);

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
            {protocol === "runninghub-workflow" ? <RunningHubWorkflowEditor value={runningHub} onChange={(next) => update({ runningHub: next })} /> : null}
        </div>
    );
}

const runningHubParameterSources: Array<{ label: string; value: RunningHubParameterMapping["source"] }> = [
    { label: "提示词", value: "prompt" },
    { label: "视频时长", value: "duration" },
    { label: "画面宽度", value: "width" },
    { label: "画面高度", value: "height" },
    { label: "分辨率", value: "resolution" },
    { label: "生成声音", value: "generate_audio" },
    { label: "水印", value: "watermark" },
    { label: "随机种子", value: "seed" },
    { label: "帧率", value: "fps" },
    { label: "自定义值", value: "custom" },
];

const runningHubValueTypes: Array<{ label: string; value: RunningHubParameterMapping["valueType"] }> = [
    { label: "文本", value: "string" },
    { label: "整数", value: "integer" },
    { label: "小数", value: "number" },
    { label: "开关", value: "boolean" },
];

const runningHubReferenceKinds: Array<{ label: string; value: RunningHubReferenceMapping["kind"] }> = [
    { label: "图片", value: "image" },
    { label: "视频", value: "video" },
    { label: "音频", value: "audio" },
];

function RunningHubWorkflowEditor({ value, onChange }: { value: RunningHubWorkflowConfig; onChange: (value: RunningHubWorkflowConfig) => void }) {
    const patch = (next: Partial<RunningHubWorkflowConfig>) => onChange({ ...value, ...next });
    const updateParameter = (index: number, next: Partial<RunningHubParameterMapping>) => patch({ parameters: value.parameters.map((item, itemIndex) => itemIndex === index ? { ...item, ...next } : item) });
    const updateReference = (index: number, next: Partial<RunningHubReferenceMapping>) => patch({ references: value.references.map((item, itemIndex) => itemIndex === index ? { ...item, ...next } : item) });
    return <>
        <Divider className="!my-2" />
        <div>
            <div className="text-sm font-semibold">RHWorkspace 节点映射</div>
            <div className="mt-1 text-xs leading-5 text-foreground/50">模型标识填写 workflowId。推荐让结果节点后的轻量断点确定性失败，避免依赖消费级 Key 的取消权限；取消 API 仅作为异常兜底。</div>
        </div>
        <div className="grid grid-cols-2 gap-3">
            <TextField label="视频结果节点 ID" value={value.resultNodeId} onChange={(resultNodeId) => patch({ resultNodeId })} />
            <label className="text-xs"><span className="mb-1 block text-foreground/60">主终止方式</span><Select className="w-full" value={value.terminationMode} options={[{ label: "工作流断点失败（推荐）", value: "breakpoint" }, { label: "调用取消 API", value: "cancel" }]} onChange={(terminationMode: RunningHubWorkflowConfig["terminationMode"]) => patch({ terminationMode })} /></label>
            <TextField label="取消兜底节点（逗号分隔）" value={value.stopOnNodeIds.join(", ")} onChange={(next) => patch({ stopOnNodeIds: parseStringList(next) })} />
            {value.terminationMode === "breakpoint" ? <>
                <TextField label="预期失败节点 ID" value={value.failureNodeId} onChange={(failureNodeId) => patch({ failureNodeId })} />
                <TextField label="触发失败的字段名" value={value.failureNodeField} onChange={(failureNodeField) => patch({ failureNodeField })} />
                <div className="col-span-2"><TextField label="触发失败的字段值" value={value.failureNodeValue} onChange={(failureNodeValue) => patch({ failureNodeValue })} /></div>
            </> : null}
            <NumberField label="等待 WSS 地址（秒）" value={value.wssWaitSeconds} min={5} max={600} onChange={(wssWaitSeconds) => patch({ wssWaitSeconds })} />
            <NumberField label="WSS 静默保护（秒）" value={value.monitorSilenceSeconds} min={15} max={600} onChange={(monitorSilenceSeconds) => patch({ monitorSilenceSeconds })} />
            <NumberField label="宽高对齐倍数" value={value.dimensionMultiple} min={1} max={1024} onChange={(dimensionMultiple) => patch({ dimensionMultiple })} />
        </div>
        <div className="rounded-md border border-amber-500/25 bg-amber-500/[.06] px-3 py-2 text-[11px] leading-5 text-foreground/65">All-in-One 默认在节点 12 保存首段视频后，让节点 73 先拆出视频/音频，再把二采条件节点 69 的 width 设为非 32 对齐的 481，使其在第二次采样前触发尺寸契约错误；若节点 69 未失败，则在采样器 48 开始时调用取消兜底。节点映射只能替换字段值，不能启用被禁用的工作流节点；视频/音频参考分支 23–26 仍需先在 RHWorkspace 中启用并保持连线。</div>
        <div className="space-y-2">
            <div><div className="text-xs font-semibold">画布参数 → 工作流字段</div><div className="mt-0.5 text-[11px] text-foreground/45">勾选“画布可调”后，用户会在视频设置中看到该参数；否则使用通用参数或这里的默认值。</div></div>
            {value.parameters.map((item, index) => <div key={`parameter-${index}`} className="rounded-md border border-border/70 bg-background/35 p-2.5">
                <div className="mb-2 flex items-center justify-between"><span className="text-xs font-medium">参数 {index + 1}</span><Button type="text" danger size="small" aria-label={`删除参数 ${index + 1}`} icon={<Trash2 className="size-3.5" />} onClick={() => patch({ parameters: value.parameters.filter((_, itemIndex) => itemIndex !== index) })} /></div>
                <div className="grid grid-cols-2 gap-2">
                    <TextField label="显示名称" value={item.label} onChange={(label) => updateParameter(index, { label })} />
                    <label className="text-xs"><span className="mb-1 block text-foreground/60">取值来源</span><Select className="w-full" value={item.source} options={runningHubParameterSources} onChange={(source) => updateParameter(index, { source })} /></label>
                    <TextField label="节点 ID" value={item.nodeId} onChange={(nodeId) => updateParameter(index, { nodeId })} />
                    <TextField label="字段名" value={item.fieldName} onChange={(fieldName) => updateParameter(index, { fieldName })} />
                    <label className="text-xs"><span className="mb-1 block text-foreground/60">字段类型</span><Select className="w-full" value={item.valueType} options={runningHubValueTypes} onChange={(valueType) => updateParameter(index, { valueType })} /></label>
                    <TextField label="默认值" value={item.defaultValue || ""} onChange={(defaultValue) => updateParameter(index, { defaultValue })} />
                </div>
                <div className="mt-2 flex items-center justify-end gap-2 text-xs"><span className="text-foreground/55">画布可调</span><Switch size="small" checked={Boolean(item.userEditable)} onChange={(userEditable) => updateParameter(index, { userEditable })} /></div>
            </div>)}
            <Button type="dashed" block icon={<Plus className="size-3.5" />} onClick={() => patch({ parameters: [...value.parameters, { label: "自定义参数", source: "custom", nodeId: "", fieldName: "", valueType: "string", userEditable: true }] })}>添加参数映射</Button>
        </div>
        <div className="space-y-2">
            <div><div className="text-xs font-semibold">画布参考素材 → Load 节点</div><div className="mt-0.5 text-[11px] text-foreground/45">图片、视频、音频可以同时连接并分别编号；“第 1 个”就是该类型在画布中的第一个有效连接。</div></div>
            {value.references.map((item, index) => <div key={`reference-${index}`} className="rounded-md border border-border/70 bg-background/35 p-2.5">
                <div className="mb-2 flex items-center justify-between"><span className="text-xs font-medium">素材 {index + 1}</span><Button type="text" danger size="small" aria-label={`删除素材映射 ${index + 1}`} icon={<Trash2 className="size-3.5" />} onClick={() => patch({ references: value.references.filter((_, itemIndex) => itemIndex !== index) })} /></div>
                <div className="grid grid-cols-2 gap-2">
                    <label className="text-xs"><span className="mb-1 block text-foreground/60">素材类型</span><Select className="w-full" value={item.kind} options={runningHubReferenceKinds} onChange={(kind) => updateReference(index, { kind })} /></label>
                    <NumberField label="该类型第几个" value={item.index + 1} min={1} max={100} onChange={(next) => updateReference(index, { index: next - 1 })} />
                    <TextField label="Load 节点 ID" value={item.nodeId} onChange={(nodeId) => updateReference(index, { nodeId })} />
                    <TextField label="上传字段名" value={item.fieldName} onChange={(fieldName) => updateReference(index, { fieldName })} />
                </div>
                <div className="mt-2 flex items-center justify-end gap-2 text-xs"><span className="text-foreground/55">必需素材</span><Switch size="small" checked={Boolean(item.required)} onChange={(required) => updateReference(index, { required })} /></div>
            </div>)}
            <Button type="dashed" block icon={<Plus className="size-3.5" />} onClick={() => patch({ references: [...value.references, { kind: "image", index: nextRunningHubReferenceIndex(value.references, "image"), nodeId: "", fieldName: "image" }] })}>添加素材映射</Button>
        </div>
    </>;
}

function nextRunningHubReferenceIndex(items: RunningHubReferenceMapping[], kind: RunningHubReferenceMapping["kind"]) {
    return Math.max(-1, ...items.filter((item) => item.kind === kind).map((item) => item.index)) + 1;
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
