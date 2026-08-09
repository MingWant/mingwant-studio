import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import { canvasThemes, type CanvasBackgroundMode } from "@/lib/canvas-theme";
import { applyCanvasLiveViewport, subscribeCanvasViewportPreview } from "@/lib/canvas/canvas-live-viewport";
import { clampCanvasScale } from "@/lib/canvas/canvas-viewport";
import { useThemeStore } from "@/stores/use-theme-store";
import type { ViewportTransform } from "@/types/canvas";

type InfiniteCanvasProps = {
    containerRef: React.RefObject<HTMLDivElement | null>;
    viewport: ViewportTransform;
    backgroundMode?: CanvasBackgroundMode;
    onViewportChange: (viewport: ViewportTransform) => void;
    onViewportPreviewChange?: (viewport: ViewportTransform) => void;
    onCanvasMouseDown?: (event: React.PointerEvent<HTMLDivElement>) => void;
    onCanvasDoubleClick?: (event: React.MouseEvent<HTMLDivElement>) => void;
    onCanvasDeselect?: () => void;
    onContextMenu?: (event: React.MouseEvent) => void;
    onDrop?: (event: React.DragEvent<HTMLDivElement>) => void;
    onFileDragEnter?: (event: React.DragEvent<HTMLDivElement>) => void;
    onFileDragLeave?: (event: React.DragEvent<HTMLDivElement>) => void;
    onFileDragOver?: (event: React.DragEvent<HTMLDivElement>) => void;
    children: React.ReactNode;
};

const CANVAS_WHEEL_BLOCK_SELECTOR = "[data-canvas-no-zoom],.ant-modal,.ant-popover,.ant-dropdown,.ant-select-dropdown,.ant-picker-dropdown";
const CANVAS_WHEEL_SCROLL_SELECTOR = "[data-canvas-wheel-scroll]";
const WHEEL_ZOOM_DELTA = 100;
const TRACKPAD_PINCH_ZOOM_DELTA = 36;

type TouchPoint = { x: number; y: number };

type PinchState = {
    active: boolean;
    pointerIds: [number, number];
    initialDistance: number;
    worldX: number;
    worldY: number;
    initialScale: number;
};

export function InfiniteCanvas({ containerRef, viewport, backgroundMode = "lines", onViewportChange, onViewportPreviewChange, onCanvasMouseDown, onCanvasDoubleClick, onCanvasDeselect, onContextMenu, onDrop, onFileDragEnter, onFileDragLeave, onFileDragOver, children }: InfiniteCanvasProps) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const panState = useRef({
        isPanning: false,
        pointerId: -1,
        startX: 0,
        startY: 0,
        initialX: 0,
        initialY: 0,
        hasMoved: false,
    });
    const viewportRef = useRef(viewport);
    const scaleRef = useRef(viewport.k);
    const containerRectRef = useRef<DOMRect | null>(null);
    const frameRef = useRef<number | null>(null);
    const nextViewportRef = useRef<ViewportTransform | null>(null);
    const syncTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const lastPreviewNotifyRef = useRef(0);
    const interactingRef = useRef(false);
    const touchPointsRef = useRef(new Map<number, TouchPoint>());
    const pinchStateRef = useRef<PinchState>({ active: false, pointerIds: [-1, -1], initialDistance: 1, worldX: 0, worldY: 0, initialScale: viewport.k });
    const documentPanStyleRef = useRef<{ cursor: string; userSelect: string } | null>(null);
    const [isSpacePressed, setIsSpacePressed] = useState(false);
    const [isPanning, setIsPanning] = useState(false);

    const setDocumentPanState = useCallback((active: boolean) => {
        if (active) {
            if (!documentPanStyleRef.current) {
                documentPanStyleRef.current = {
                    cursor: document.body.style.cursor,
                    userSelect: document.body.style.userSelect,
                };
            }
            document.body.style.cursor = "grabbing";
            document.body.style.userSelect = "none";
            return;
        }
        const previous = documentPanStyleRef.current;
        if (!previous) return;
        document.body.style.cursor = previous.cursor;
        document.body.style.userSelect = previous.userSelect;
        documentPanStyleRef.current = null;
    }, []);

    useLayoutEffect(() => {
        if (interactingRef.current) return;
        viewportRef.current = viewport;
        scaleRef.current = viewport.k;
        applyCanvasLiveViewport(containerRef.current, viewport);
    }, [containerRef, viewport]);

    useEffect(() => {
        const container = containerRef.current;
        if (!container) return;
        return subscribeCanvasViewportPreview(container, (next) => {
            viewportRef.current = next;
            scaleRef.current = next.k;
        });
    }, [containerRef]);

    useEffect(
        () => () => {
            if (frameRef.current) cancelAnimationFrame(frameRef.current);
            if (syncTimerRef.current) clearTimeout(syncTimerRef.current);
            delete containerRef.current?.dataset.canvasViewportInteracting;
            setDocumentPanState(false);
        },
        [containerRef, setDocumentPanState],
    );

    const syncViewport = useCallback(() => onViewportChange(viewportRef.current), [onViewportChange]);

    const finishViewportInteraction = useCallback(() => {
        const wasInteracting = interactingRef.current || panState.current.isPanning || pinchStateRef.current.active;
        const pointerIds = new Set([panState.current.pointerId, ...pinchStateRef.current.pointerIds].filter((pointerId) => pointerId >= 0));
        panState.current.isPanning = false;
        panState.current.pointerId = -1;
        pinchStateRef.current.active = false;
        pinchStateRef.current.pointerIds = [-1, -1];
        touchPointsRef.current.clear();
        interactingRef.current = false;
        if (syncTimerRef.current) {
            clearTimeout(syncTimerRef.current);
            syncTimerRef.current = null;
        }
        if (frameRef.current) {
            cancelAnimationFrame(frameRef.current);
            frameRef.current = null;
        }
        const pending = nextViewportRef.current;
        nextViewportRef.current = null;
        if (pending) applyCanvasLiveViewport(containerRef.current, pending);
        delete containerRef.current?.dataset.canvasViewportInteracting;
        const container = containerRef.current;
        pointerIds.forEach((pointerId) => {
            if (container?.hasPointerCapture(pointerId)) {
                try {
                    container.releasePointerCapture(pointerId);
                } catch {
                    // 指针可能已由浏览器自动释放；收尾状态仍必须继续清理。
                }
            }
        });
        if (wasInteracting) syncViewport();
        setIsPanning(false);
        setDocumentPanState(false);
    }, [containerRef, setDocumentPanState, syncViewport]);

    const scheduleViewportChange = useCallback(
        (next: ViewportTransform, commitAfterIdle = false) => {
            viewportRef.current = next;
            scaleRef.current = next.k;
            onViewportPreviewChange?.(next);
            const container = containerRef.current;
            if (container) container.dataset.canvasViewportInteracting = "true";
            nextViewportRef.current = next;
            if (frameRef.current) return;
            frameRef.current = requestAnimationFrame((now) => {
                frameRef.current = null;
                const pending = nextViewportRef.current;
                if (!pending) return;
                const notify = now - lastPreviewNotifyRef.current >= 32;
                applyCanvasLiveViewport(containerRef.current, pending, notify);
                if (notify) lastPreviewNotifyRef.current = now;
            });
            if (!commitAfterIdle) return;
            if (syncTimerRef.current) clearTimeout(syncTimerRef.current);
            syncTimerRef.current = setTimeout(() => {
                interactingRef.current = false;
                delete containerRef.current?.dataset.canvasViewportInteracting;
                syncViewport();
                syncTimerRef.current = null;
            }, 120);
        },
        [containerRef, onViewportPreviewChange, syncViewport],
    );

    useEffect(() => {
        const handleKeyDown = (event: KeyboardEvent) => {
            if (event.code !== "Space") return;
            const target = event.target instanceof Element ? event.target : null;
            if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement || event.target instanceof HTMLButtonElement || target?.closest("[contenteditable='true'],[data-canvas-no-zoom]")) return;
            setIsSpacePressed(true);
        };

        const handleKeyUp = (event: KeyboardEvent) => {
            if (event.code === "Space") setIsSpacePressed(false);
        };

        const handleWindowBlur = () => {
            setIsSpacePressed(false);
            finishViewportInteraction();
        };

        window.addEventListener("keydown", handleKeyDown);
        window.addEventListener("keyup", handleKeyUp);
        window.addEventListener("blur", handleWindowBlur);
        return () => {
            window.removeEventListener("keydown", handleKeyDown);
            window.removeEventListener("keyup", handleKeyUp);
            window.removeEventListener("blur", handleWindowBlur);
        };
    }, [finishViewportInteraction]);

    const handleWheel = useCallback(
        (event: WheelEvent) => {
            const target = event.target instanceof Element ? event.target : null;
            const deltaX = wheelDeltaToPixels(event.deltaX, event.deltaMode);
            const deltaY = wheelDeltaToPixels(event.deltaY, event.deltaMode);
            const absX = Math.abs(deltaX);
            const absY = Math.abs(deltaY);
            const isPinchZoom = event.ctrlKey || event.metaKey;
            if (target?.closest(CANVAS_WHEEL_BLOCK_SELECTOR)) {
                // 内部区域保留普通纵向滚动，但不能泄漏为页面级缩放或浏览器前进/后退。
                if (isPinchZoom || event.shiftKey || absX > absY) event.preventDefault();
                return;
            }
            const scrollRegion = target?.closest<HTMLElement>(CANVAS_WHEEL_SCROLL_SELECTOR);
            // 分镜表没有溢出时不能吞掉整张卡片上的滚轮；Ctrl/Cmd 缩放也始终交还画布。
            if (!isPinchZoom && scrollRegion && scrollRegionCanConsumeWheel(scrollRegion, deltaX, deltaY, event.shiftKey)) return;

            event.preventDefault();
            interactingRef.current = true;
            const current = viewportRef.current;
            const rawAbsY = Math.abs(event.deltaY);
            const looksLikeMouseWheel = event.deltaMode !== 0 || (rawAbsY >= 80 && Math.abs(rawAbsY - Math.round(rawAbsY / 100) * 100) < 1);
            const looksLikeTrackpadPan = !isPinchZoom && (event.shiftKey || absX > 0 || (!looksLikeMouseWheel && absY > 0));

            if (looksLikeTrackpadPan) {
                const panX = event.shiftKey && absX < 1 ? deltaY : deltaX;
                scheduleViewportChange({
                    x: current.x - panX,
                    y: current.y - (event.shiftKey && absX < 1 ? 0 : deltaY),
                    k: current.k,
                }, true);
                return;
            }

            const rect = containerRectRef.current || containerRef.current?.getBoundingClientRect();
            if (!rect) return;
            const mouseX = event.clientX - rect.left;
            const mouseY = event.clientY - rect.top;
            const zoomDelta = isPinchZoom && !looksLikeMouseWheel ? TRACKPAD_PINCH_ZOOM_DELTA : WHEEL_ZOOM_DELTA;
            const factor = Math.pow(1.1, -deltaY / zoomDelta);
            const newScale = clampCanvasScale(current.k * factor);
            const worldX = (mouseX - current.x) / current.k;
            const worldY = (mouseY - current.y) / current.k;

            scheduleViewportChange({
                x: mouseX - worldX * newScale,
                y: mouseY - worldY * newScale,
                k: newScale,
            }, true);
        },
        [containerRef, scheduleViewportChange],
    );

    const handlePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
        const target = event.target instanceof Element ? event.target : null;
        if (target?.closest("[data-canvas-no-zoom]")) return;
        if (target?.closest("[data-connection-create-menu]")) return;
        const isBackgroundClick = !target?.closest("[data-node-id],[data-connection-id]");
        const isTouch = event.pointerType === "touch";

        if (event.button === 0 && !isSpacePressed && !isTouch && isBackgroundClick) {
            event.preventDefault();
            event.currentTarget.setPointerCapture(event.pointerId);
            onCanvasMouseDown?.(event);
            return;
        }

        if (isTouch) {
            touchPointsRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
            if (touchPointsRef.current.size >= 2) {
                const [[firstId, first], [secondId, second]] = Array.from(touchPointsRef.current.entries());
                event.preventDefault();
                event.currentTarget.setPointerCapture(firstId);
                event.currentTarget.setPointerCapture(secondId);
                const rect = containerRectRef.current || event.currentTarget.getBoundingClientRect();
                const current = viewportRef.current;
                const centerX = (first.x + second.x) / 2 - rect.left;
                const centerY = (first.y + second.y) / 2 - rect.top;
                pinchStateRef.current = {
                    active: true,
                    pointerIds: [firstId, secondId],
                    initialDistance: Math.max(Math.hypot(second.x - first.x, second.y - first.y), 1),
                    worldX: (centerX - current.x) / current.k,
                    worldY: (centerY - current.y) / current.k,
                    initialScale: current.k,
                };
                panState.current.isPanning = false;
                interactingRef.current = true;
                setIsPanning(false);
                setDocumentPanState(false);
                return;
            }

            if (!isBackgroundClick) return;
            event.preventDefault();
            event.currentTarget.setPointerCapture(event.pointerId);
            if (!event.isPrimary) return;
            const current = viewportRef.current;
            interactingRef.current = true;
            panState.current = {
                isPanning: true,
                pointerId: event.pointerId,
                startX: event.clientX,
                startY: event.clientY,
                initialX: current.x,
                initialY: current.y,
                hasMoved: false,
            };
            setIsPanning(true);
            setDocumentPanState(true);
            return;
        }

    };

    const handleNavigationPointerDownCapture = (event: React.PointerEvent<HTMLDivElement>) => {
        if (event.pointerType === "touch" || (event.button !== 1 && !(event.button === 0 && isSpacePressed))) return;
        const target = event.target instanceof Element ? event.target : null;
        if (target?.closest("[data-canvas-no-zoom],[data-connection-create-menu]")) return;
        // Space + 左键和中键都应在节点上也能平移，捕获阶段先于节点拖拽接管指针。
        const current = viewportRef.current;
        event.preventDefault();
        event.stopPropagation();
        event.currentTarget.setPointerCapture(event.pointerId);
        interactingRef.current = true;
        panState.current = {
            isPanning: true,
            pointerId: event.pointerId,
            startX: event.clientX,
            startY: event.clientY,
            initialX: current.x,
            initialY: current.y,
            hasMoved: false,
        };
        setIsPanning(true);
        setDocumentPanState(true);
    };

    useEffect(() => {
        const handlePointerMove = (event: PointerEvent) => {
            if (event.pointerType === "touch" && touchPointsRef.current.has(event.pointerId)) {
                touchPointsRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
                const pinch = pinchStateRef.current;
                if (pinch.active) {
                    const first = touchPointsRef.current.get(pinch.pointerIds[0]);
                    const second = touchPointsRef.current.get(pinch.pointerIds[1]);
                    const rect = containerRectRef.current || containerRef.current?.getBoundingClientRect();
                    if (!first || !second || !rect) return;
                    event.preventDefault();
                    const centerX = (first.x + second.x) / 2 - rect.left;
                    const centerY = (first.y + second.y) / 2 - rect.top;
                    const distance = Math.max(Math.hypot(second.x - first.x, second.y - first.y), 1);
                    const scale = clampCanvasScale(pinch.initialScale * (distance / pinch.initialDistance));
                    scheduleViewportChange({
                        x: centerX - pinch.worldX * scale,
                        y: centerY - pinch.worldY * scale,
                        k: scale,
                    });
                    return;
                }
            }

            if (!panState.current.isPanning || panState.current.pointerId !== event.pointerId) return;

            event.preventDefault();
            const dx = event.clientX - panState.current.startX;
            const dy = event.clientY - panState.current.startY;
            if (Math.abs(dx) > 3 || Math.abs(dy) > 3) {
                panState.current.hasMoved = true;
            }

            scheduleViewportChange({
                x: panState.current.initialX + dx,
                y: panState.current.initialY + dy,
                k: scaleRef.current,
            });
        };

        const handlePointerEnd = (event: PointerEvent) => {
            if (event.pointerType === "touch" && pinchStateRef.current.active && pinchStateRef.current.pointerIds.includes(event.pointerId)) {
                finishViewportInteraction();
                return;
            }

            if (event.pointerType === "touch") touchPointsRef.current.delete(event.pointerId);
            if (!panState.current.isPanning || panState.current.pointerId !== event.pointerId) return;

            if (event.type === "pointerup" && !panState.current.hasMoved) {
                onCanvasDeselect?.();
            }
            finishViewportInteraction();
        };

        const handleLostPointerCapture = (event: PointerEvent) => {
            if (panState.current.pointerId === event.pointerId || (pinchStateRef.current.active && pinchStateRef.current.pointerIds.includes(event.pointerId))) finishViewportInteraction();
        };

        window.addEventListener("pointermove", handlePointerMove);
        window.addEventListener("pointerup", handlePointerEnd);
        window.addEventListener("pointercancel", handlePointerEnd);
        containerRef.current?.addEventListener("lostpointercapture", handleLostPointerCapture);
        return () => {
            window.removeEventListener("pointermove", handlePointerMove);
            window.removeEventListener("pointerup", handlePointerEnd);
            window.removeEventListener("pointercancel", handlePointerEnd);
            containerRef.current?.removeEventListener("lostpointercapture", handleLostPointerCapture);
        };
    }, [containerRef, finishViewportInteraction, onCanvasDeselect, scheduleViewportChange]);

    useEffect(() => {
        const container = containerRef.current;
        if (!container) return;
        const updateRect = () => {
            containerRectRef.current = container.getBoundingClientRect();
        };
        updateRect();
        const observer = new ResizeObserver(updateRect);
        observer.observe(container);
        window.addEventListener("resize", updateRect);
        container.addEventListener("wheel", handleWheel, { passive: false, capture: true });
        return () => {
            observer.disconnect();
            window.removeEventListener("resize", updateRect);
            container.removeEventListener("wheel", handleWheel, { capture: true });
        };
    }, [containerRef, handleWheel]);

    const renderedViewport = viewportRef.current;

    return (
        <div
            ref={containerRef}
            className={`relative h-full w-full select-none overflow-hidden touch-none ${isPanning ? "cursor-grabbing" : isSpacePressed ? "cursor-grab" : "cursor-default"}`}
            style={{
                background: theme.canvas.background,
                overscrollBehavior: "none",
                "--canvas-live-x": `${renderedViewport.x}px`,
                "--canvas-live-y": `${renderedViewport.y}px`,
                "--canvas-live-scale": renderedViewport.k,
                "--canvas-grid-size": `${48 * renderedViewport.k}px`,
                "--canvas-grid-x": `${renderedViewport.x % (48 * renderedViewport.k)}px`,
                "--canvas-grid-y": `${renderedViewport.y % (48 * renderedViewport.k)}px`,
                "--canvas-dot-size": renderedViewport.k < 0.12 ? "0.8px" : "1.15px",
            } as React.CSSProperties}
            onPointerDownCapture={handleNavigationPointerDownCapture}
            onPointerDown={handlePointerDown}
            onAuxClick={(event) => {
                if (event.button === 1) event.preventDefault();
            }}
            onDoubleClick={(event) => {
                const target = event.target instanceof Element ? event.target : null;
                if (!target?.closest("[data-node-id],[data-connection-id],[data-canvas-no-zoom]")) onCanvasDoubleClick?.(event);
            }}
            onContextMenu={onContextMenu}
            onDragEnter={onFileDragEnter}
            onDragLeave={onFileDragLeave}
            onDragOver={(event) => {
                event.preventDefault();
                onFileDragOver?.(event);
            }}
            onDrop={onDrop}
        >
            <CanvasGrid mode={backgroundMode} />
            <div
                data-canvas-world-layer
                className="absolute origin-top-left"
                style={{
                    transform: "translate3d(var(--canvas-live-x), var(--canvas-live-y), 0) scale(var(--canvas-live-scale))",
                    willChange: "transform",
                }}
            >
                {children}
            </div>
        </div>
    );
}

function CanvasGrid({ mode }: { mode: CanvasBackgroundMode }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const backgroundImage = mode === "dots" ? `radial-gradient(circle, ${theme.canvas.dot} var(--canvas-dot-size), transparent calc(var(--canvas-dot-size) + 0.2px))` : `linear-gradient(${theme.canvas.line} 1px, transparent 1px), linear-gradient(90deg, ${theme.canvas.line} 1px, transparent 1px)`;
    if (mode === "blank") return null;

    return (
        <div
            data-canvas-grid-layer
            className="pointer-events-none absolute opacity-40"
            style={{
                inset: "calc(-1 * var(--canvas-grid-size))",
                backgroundImage,
                backgroundSize: "var(--canvas-grid-size) var(--canvas-grid-size)",
                transform: "translate3d(var(--canvas-grid-x), var(--canvas-grid-y), 0)",
                willChange: "transform",
            }}
        />
    );
}

function wheelDeltaToPixels(delta: number, deltaMode: number) {
    if (deltaMode === 1) return delta * 16;
    if (deltaMode === 2) return delta * 720;
    return delta;
}

function scrollRegionCanConsumeWheel(region: HTMLElement, deltaX: number, deltaY: number, shiftKey: boolean) {
    const style = window.getComputedStyle(region);
    const horizontalIntent = shiftKey || Math.abs(deltaX) > Math.abs(deltaY);
    if (horizontalIntent) {
        const delta = shiftKey && Math.abs(deltaX) < 1 ? deltaY : deltaX;
        if (!/^(auto|scroll)$/.test(style.overflowX) || region.scrollWidth <= region.clientWidth + 1) return false;
        if (delta < 0) return region.scrollLeft > 0;
        if (delta > 0) return region.scrollLeft + region.clientWidth < region.scrollWidth - 1;
        return false;
    }
    if (!/^(auto|scroll)$/.test(style.overflowY) || region.scrollHeight <= region.clientHeight + 1) return false;
    if (deltaY < 0) return region.scrollTop > 0;
    if (deltaY > 0) return region.scrollTop + region.clientHeight < region.scrollHeight - 1;
    return false;
}
