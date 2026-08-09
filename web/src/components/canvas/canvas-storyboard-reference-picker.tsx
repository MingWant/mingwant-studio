import { Popover } from "antd";
import { Check, CircleCheck, ImagePlus, Images, Settings2, TriangleAlert, Upload, UserRound } from "lucide-react";
import type { CSSProperties } from "react";

import type { CanvasResourceReference } from "@/lib/canvas/canvas-resource-references";
import { canvasThemes } from "@/lib/canvas-theme";
import { CanvasNodeType } from "@/types/canvas";

export type StoryboardReferenceModelStatus = {
    state: "ready" | "missing" | "unsupported";
    label: string;
    detail: string;
};

type CanvasTheme = (typeof canvasThemes)[keyof typeof canvasThemes];

export function StoryboardReferenceControl({
    compact = false,
    disabled = false,
    scopeLabel,
    references,
    selectedIds,
    inheritedIds = [],
    modelStatus,
    theme,
    onToggle,
    onAddProjectAsset,
    onUploadImage,
    onOpenSettings,
}: {
    compact?: boolean;
    disabled?: boolean;
    scopeLabel: string;
    references: CanvasResourceReference[];
    selectedIds: string[];
    inheritedIds?: string[];
    modelStatus: StoryboardReferenceModelStatus;
    theme: CanvasTheme;
    onToggle: (referenceNodeId: string, enabled: boolean) => void;
    onAddProjectAsset?: () => void;
    onUploadImage: () => void;
    onOpenSettings: () => void;
}) {
    const selected = new Set([...selectedIds, ...inheritedIds]);
    const selectedReferences = references.filter((reference) => selected.has(reference.nodeId));
    const count = selectedReferences.length;
    const content = (
        <StoryboardReferencePicker
            scopeLabel={scopeLabel}
            references={references}
            selectedIds={selectedIds}
            inheritedIds={inheritedIds}
            modelStatus={modelStatus}
            onToggle={onToggle}
            onAddProjectAsset={onAddProjectAsset}
            onUploadImage={onUploadImage}
            onOpenSettings={onOpenSettings}
        />
    );

    return (
        <Popover trigger={disabled ? [] : ["click" as const]} placement={compact ? "bottomRight" : "bottom"} content={content}>
            <button
                type="button"
                disabled={disabled}
                data-canvas-no-zoom
                className={compact
                    ? "relative grid size-6 shrink-0 place-items-center rounded outline-none transition hover:bg-black/5 focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-35 dark:hover:bg-white/10"
                    : "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md border px-2 text-[10px] font-medium outline-none transition hover:brightness-105 focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-35"}
                style={compact
                    ? { color: count ? theme.accent.primary : theme.node.muted, "--tw-ring-color": theme.accent.primary } as CSSProperties
                    : { background: count ? theme.accent.primarySoft : theme.toolbar.itemHover, borderColor: count ? `${theme.accent.primary}55` : theme.node.stroke, color: count ? theme.accent.primary : theme.node.muted, "--tw-ring-color": theme.accent.primary } as CSSProperties}
                title={disabled ? "生成任务进行中，暂不能修改参考素材" : `${scopeLabel}：${count ? `已连接 ${count} 项` : "添加角色或参考图"}`}
                aria-label={`${scopeLabel}参考素材${count ? `，已连接 ${count} 项` : "，尚未添加"}`}
                onMouseDown={(event) => event.stopPropagation()}
                onPointerDown={(event) => event.stopPropagation()}
                onClick={(event) => event.stopPropagation()}
            >
                <Images className={compact ? "size-3.5" : "size-3"} />
                {compact ? null : <span>{count ? `参考 ${count}` : "添加参考"}</span>}
                {compact && count ? <span className="absolute -right-1 -top-1 grid min-w-3.5 place-items-center rounded-full px-0.5 text-[8px] font-bold leading-3.5 text-white" style={{ background: theme.accent.primary }}>{count}</span> : null}
            </button>
        </Popover>
    );
}

function StoryboardReferencePicker({ scopeLabel, references, selectedIds, inheritedIds, modelStatus, onToggle, onAddProjectAsset, onUploadImage, onOpenSettings }: {
    scopeLabel: string;
    references: CanvasResourceReference[];
    selectedIds: string[];
    inheritedIds: string[];
    modelStatus: StoryboardReferenceModelStatus;
    onToggle: (referenceNodeId: string, enabled: boolean) => void;
    onAddProjectAsset?: () => void;
    onUploadImage: () => void;
    onOpenSettings: () => void;
}) {
    const selected = new Set(selectedIds);
    const inherited = new Set(inheritedIds);
    const availableIds = new Set(references.map((reference) => reference.nodeId));
    const effectiveCount = new Set([...selectedIds, ...inheritedIds].filter((id) => availableIds.has(id))).size;
    const capacityFull = effectiveCount >= 9;
    const statusReady = modelStatus.state === "ready";
    const StatusIcon = statusReady ? CircleCheck : TriangleAlert;
    return (
        <div
            className="w-[320px] max-w-[calc(100vw-32px)] overflow-hidden"
            onMouseDown={(event) => event.stopPropagation()}
            onPointerDown={(event) => event.stopPropagation()}
            onWheel={(event) => event.stopPropagation()}
        >
            <div className="border-b border-border/70 px-1 pb-2">
                <div className="text-xs font-semibold">{scopeLabel} · 角色与参考图</div>
                <div className="mt-1 text-[10px] leading-4 text-foreground/45">所选素材会随生成任务提交；角色卡会自动解析当前三视图与角色设定。</div>
            </div>
            <button type="button" className={`mt-2 flex w-full items-start gap-2 rounded-md px-2 py-2 text-left ${statusReady ? "bg-emerald-500/[.08] text-emerald-700 dark:text-emerald-300" : "bg-amber-500/[.10] text-amber-700 dark:text-amber-300"}`} onClick={statusReady ? undefined : onOpenSettings}>
                <StatusIcon className="mt-0.5 size-3.5 shrink-0" />
                <span className="min-w-0 flex-1"><span className="block truncate text-[10px] font-semibold">{modelStatus.label}</span><span className="mt-0.5 block text-[9px] leading-3.5 opacity-75">{modelStatus.detail}</span></span>
                {statusReady ? null : <Settings2 className="mt-0.5 size-3.5 shrink-0" />}
            </button>
            <div className="thin-scrollbar mt-2 max-h-64 space-y-1 overflow-y-auto pr-1">
                {references.length ? references.map((reference) => {
                    const inheritedFromGlobal = inherited.has(reference.nodeId);
                    const checked = selected.has(reference.nodeId) || inheritedFromGlobal;
                    const capacityBlocked = capacityFull && !checked;
                    const character = reference.kind === "character";
                    return (
                        <button
                            key={reference.nodeId}
                            type="button"
                            role="checkbox"
                            aria-checked={checked}
                            disabled={inheritedFromGlobal || capacityBlocked}
                            className={`flex w-full items-center gap-2 rounded-md border px-2 py-1.5 text-left outline-none transition focus-visible:ring-2 disabled:cursor-default ${checked ? "border-[var(--workspace-accent)] bg-[var(--workspace-accent-soft)]" : "border-border/70 hover:border-foreground/25 hover:bg-foreground/[.035]"}`}
                            title={inheritedFromGlobal ? "此素材已应用到全部镜头；请从标题栏的全局参考中移除" : capacityBlocked ? "当前范围已达到 9 项参考素材上限" : undefined}
                            onClick={() => onToggle(reference.nodeId, !checked)}
                        >
                            <span className="relative grid size-9 shrink-0 place-items-center overflow-hidden rounded bg-foreground/[.05] text-foreground/30">
                                {reference.previewUrl ? <img src={reference.previewUrl} alt="" className={`size-full ${character ? "object-contain p-0.5" : "object-cover"}`} /> : character ? <UserRound className="size-4" /> : <ImagePlus className="size-4" />}
                            </span>
                            <span className="min-w-0 flex-1"><span className="block truncate text-[11px] font-medium">{reference.title}</span><span className="mt-0.5 block truncate text-[9px] text-foreground/42">{character ? `角色卡 · ${reference.characterVersionPolicy === "pinned" ? "固定版本" : "跟随当前版本"}` : reference.sourceType === CanvasNodeType.Drawing ? "画布绘图" : "参考图片"}{inheritedFromGlobal ? " · 全局继承" : ""}</span></span>
                            <span className={`grid size-5 shrink-0 place-items-center rounded-full border ${checked ? "border-[var(--workspace-accent)] bg-[var(--workspace-accent)] text-white" : "border-border text-transparent"}`}><Check className="size-3" /></span>
                        </button>
                    );
                }) : <div className="rounded-md border border-dashed border-border px-3 py-5 text-center text-[10px] leading-4 text-foreground/42">画布里还没有可用的角色卡或图片。可直接从项目添加，或上传一张参考图。</div>}
            </div>
            <div className="mt-2 flex gap-2 border-t border-border/70 pt-2">
                {onAddProjectAsset ? <button type="button" disabled={capacityFull} className="inline-flex h-8 flex-1 items-center justify-center gap-1.5 rounded-md border border-border text-[10px] font-medium hover:bg-foreground/[.04] disabled:cursor-not-allowed disabled:opacity-35" title={capacityFull ? "当前范围已达到 9 项参考素材上限" : undefined} onClick={onAddProjectAsset}><UserRound className="size-3.5" />从项目添加</button> : null}
                <button type="button" disabled={capacityFull} className="inline-flex h-8 flex-1 items-center justify-center gap-1.5 rounded-md border border-border text-[10px] font-medium hover:bg-foreground/[.04] disabled:cursor-not-allowed disabled:opacity-35" title={capacityFull ? "当前范围已达到 9 项参考素材上限" : undefined} onClick={onUploadImage}><Upload className="size-3.5" />上传参考图</button>
            </div>
            <div className="mt-2 text-[9px] leading-3.5 text-foreground/38">当前范围 {effectiveCount}/9 项；角色卡会在模型容量内自动补充多视角图。</div>
        </div>
    );
}
