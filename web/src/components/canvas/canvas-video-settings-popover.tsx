import { useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Settings2 } from "lucide-react";
import { Button } from "antd";

import { VideoSettingsPanel, videoResolutionLabel, videoSecondsLabel, videoSizeLabel } from "@/components/video-settings-panel";
import { canvasThemes } from "@/lib/canvas-theme";
import { isXAIVideoRequest } from "@/lib/model-protocols";
import { useThemeStore } from "@/stores/use-theme-store";
import { resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import type { CanvasVideoEditOperation } from "@/types/canvas";

type CanvasVideoSettingsPopoverProps = {
    config: AiConfig;
    operation?: CanvasVideoEditOperation;
    onConfigChange: (key: keyof AiConfig, value: string) => void;
    workflowParameters?: Record<string, string>;
    onWorkflowParameterChange?: (key: string, value: string) => void;
    buttonClassName?: string;
    placement?: "topLeft" | "top" | "topRight" | "bottomLeft" | "bottom" | "bottomRight";
};

export function CanvasVideoSettingsPopover({ config, operation, onConfigChange, workflowParameters, onWorkflowParameterChange, buttonClassName, placement = "topLeft" }: CanvasVideoSettingsPopoverProps) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const buttonRef = useRef<HTMLSpanElement>(null);
    const panelRef = useRef<HTMLDivElement>(null);
    const [open, setOpen] = useState(false);
    const [buttonRect, setButtonRect] = useState<DOMRect | null>(null);
    const request = resolveModelRequestConfig(config, config.model || config.videoModel);
    const xai = isXAIVideoRequest(request.interfaceType, request.model);
    const summary = xai && operation === "edit_video"
        ? "沿用原片参数 · 最高 720P"
        : xai && operation === "extend"
          ? `新增 ${videoSecondsLabel(config.videoSeconds)} · 沿用原片画面`
          : `${videoResolutionLabel(config.vquality)} · ${videoSizeLabel(config.size)} · ${videoSecondsLabel(config.videoSeconds)}`;

    useEffect(() => {
        if (!open) return;
        const syncPosition = () => setButtonRect(buttonRef.current?.getBoundingClientRect() || null);
        const closeOnOutsidePointer = (event: PointerEvent) => {
            const target = event.target;
            if (!(target instanceof Node)) return;
            if (buttonRef.current?.contains(target) || panelRef.current?.contains(target)) return;
            setOpen(false);
        };

        syncPosition();
        window.addEventListener("resize", syncPosition);
        window.addEventListener("scroll", syncPosition, true);
        window.addEventListener("pointerdown", closeOnOutsidePointer, true);
        return () => {
            window.removeEventListener("resize", syncPosition);
            window.removeEventListener("scroll", syncPosition, true);
            window.removeEventListener("pointerdown", closeOnOutsidePointer, true);
        };
    }, [open]);

    const panel = open && buttonRect ? <VideoSettingsPortal buttonRect={buttonRect} panelRef={panelRef} placement={placement} theme={theme} config={config} operation={operation} onConfigChange={onConfigChange} workflowParameters={workflowParameters} onWorkflowParameterChange={onWorkflowParameterChange} /> : null;

    return (
        <>
            <span ref={buttonRef} className="inline-flex min-w-0">
                <Button size="small" type="text" className={buttonClassName || "!h-8 !max-w-[170px] !justify-start !rounded-full !px-2.5"} style={{ background: theme.node.fill, color: theme.node.text }} icon={<Settings2 className="size-3.5" />} onClick={() => setOpen((current) => !current)}>
                    <span className="truncate">
                        {summary}
                    </span>
                </Button>
            </span>
            {panel}
        </>
    );
}

function VideoSettingsPortal({
    buttonRect,
    panelRef,
    placement,
    theme,
    config,
    operation,
    onConfigChange,
    workflowParameters,
    onWorkflowParameterChange,
}: {
    buttonRect: DOMRect;
    panelRef: RefObject<HTMLDivElement | null>;
    placement: CanvasVideoSettingsPopoverProps["placement"];
    theme: (typeof canvasThemes)[keyof typeof canvasThemes];
    config: AiConfig;
    operation?: CanvasVideoEditOperation;
    onConfigChange: (key: keyof AiConfig, value: string) => void;
    workflowParameters?: Record<string, string>;
    onWorkflowParameterChange?: (key: string, value: string) => void;
}) {
    const gap = 8;
    const margin = 12;
    const width = Math.min(356, window.innerWidth - margin * 2);
    const alignRight = placement?.endsWith("Right");
    const alignCenter = placement === "top" || placement === "bottom";
    const left = alignCenter ? buttonRect.left + buttonRect.width / 2 - width / 2 : alignRight ? buttonRect.right - width : buttonRect.left;
    const topPlacement = placement?.startsWith("top");
    const estimatedHeight = 480;
    const topSpace = buttonRect.top - gap - margin;
    const bottomSpace = window.innerHeight - buttonRect.bottom - gap - margin;
    const placeAbove = topPlacement ? topSpace >= estimatedHeight || topSpace >= bottomSpace : bottomSpace < estimatedHeight && topSpace > bottomSpace;
    const style = {
        position: "fixed",
        zIndex: 1200,
        width,
        left: Math.max(margin, Math.min(window.innerWidth - width - margin, left)),
        ...(placeAbove ? { bottom: window.innerHeight - buttonRect.top + gap, maxHeight: Math.max(260, topSpace) } : { top: buttonRect.bottom + gap, maxHeight: Math.max(260, bottomSpace) }),
        background: theme.spatial.elevated,
        border: `1px solid ${theme.toolbar.border}`,
        borderRadius: 10,
        boxShadow: `0 24px 72px ${theme.spatial.shadow}, inset 0 1px 0 rgba(255,255,255,.08)`,
        padding: 12,
        overflowY: "auto",
        color: theme.node.text,
    } as const;

    return createPortal(
        <div
            ref={panelRef}
            data-canvas-no-zoom
            className="canvas-image-settings-popover aceternity-floating-panel backdrop-blur-2xl"
            style={style}
            onPointerDown={(event) => event.stopPropagation()}
            onMouseDown={(event) => event.stopPropagation()}
            onClick={(event) => event.stopPropagation()}
        >
            <VideoSettingsPanel config={config} operation={operation} onConfigChange={(key, value) => onConfigChange(key, value)} workflowParameters={workflowParameters} onWorkflowParameterChange={onWorkflowParameterChange} theme={theme} className="space-y-3" />
        </div>,
        document.body,
    );
}
