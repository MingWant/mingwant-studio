import type { CanvasNodeData, ViewportTransform } from "@/types/canvas";

export type CanvasBounds = {
    left: number;
    top: number;
    right: number;
    bottom: number;
};

export type CanvasViewportSize = {
    width: number;
    height: number;
};

export const CANVAS_MIN_SCALE = 0.05;
export const CANVAS_MAX_SCALE = 2;

export function clampCanvasScale(scale: number) {
    return Math.min(CANVAS_MAX_SCALE, Math.max(CANVAS_MIN_SCALE, scale));
}

export function getCanvasNodesBounds(nodes: CanvasNodeData[]): CanvasBounds | null {
    if (!nodes.length) return null;
    let left = Number.POSITIVE_INFINITY;
    let top = Number.POSITIVE_INFINITY;
    let right = Number.NEGATIVE_INFINITY;
    let bottom = Number.NEGATIVE_INFINITY;

    nodes.forEach((node) => {
        left = Math.min(left, node.position.x);
        top = Math.min(top, node.position.y);
        right = Math.max(right, node.position.x + node.width);
        bottom = Math.max(bottom, node.position.y + node.height);
    });

    return { left, top, right, bottom };
}

export function viewportForBounds(bounds: CanvasBounds, viewportSize: CanvasViewportSize, options: { padding?: number; minScale?: number; maxScale?: number } = {}): ViewportTransform {
    const padding = options.padding ?? 96;
    const minScale = options.minScale ?? CANVAS_MIN_SCALE;
    const maxScale = options.maxScale ?? 1;
    const boundsWidth = Math.max(1, bounds.right - bounds.left);
    const boundsHeight = Math.max(1, bounds.bottom - bounds.top);
    const availableWidth = Math.max(1, viewportSize.width - padding * 2);
    const availableHeight = Math.max(1, viewportSize.height - padding * 2);
    const k = Math.min(maxScale, Math.max(minScale, Math.min(availableWidth / boundsWidth, availableHeight / boundsHeight)));
    const centerX = (bounds.left + bounds.right) / 2;
    const centerY = (bounds.top + bounds.bottom) / 2;

    return {
        x: viewportSize.width / 2 - centerX * k,
        y: viewportSize.height / 2 - centerY * k,
        k,
    };
}

export function viewportAtScale(viewport: ViewportTransform, viewportSize: CanvasViewportSize, scale: number): ViewportTransform {
    const k = clampCanvasScale(scale);
    const centerWorldX = (viewportSize.width / 2 - viewport.x) / viewport.k;
    const centerWorldY = (viewportSize.height / 2 - viewport.y) / viewport.k;
    return {
        x: viewportSize.width / 2 - centerWorldX * k,
        y: viewportSize.height / 2 - centerWorldY * k,
        k,
    };
}

export function viewportToRevealBounds(
    bounds: CanvasBounds,
    viewportSize: CanvasViewportSize,
    viewport: ViewportTransform,
    padding = 72,
): ViewportTransform | null {
    const visibleLeft = padding;
    const visibleTop = padding;
    const visibleRight = Math.max(visibleLeft, viewportSize.width - padding);
    const visibleBottom = Math.max(visibleTop, viewportSize.height - padding);
    const screenLeft = viewport.x + bounds.left * viewport.k;
    const screenTop = viewport.y + bounds.top * viewport.k;
    const screenRight = viewport.x + bounds.right * viewport.k;
    const screenBottom = viewport.y + bounds.bottom * viewport.k;
    const intersects = screenRight >= visibleLeft && screenLeft <= visibleRight && screenBottom >= visibleTop && screenTop <= visibleBottom;
    if (intersects) return null;

    // 自动生成只在结果完全离屏时平移到最近边缘，保持用户正在使用的缩放尺度。
    const offsetX = screenRight < visibleLeft ? visibleLeft - screenRight : screenLeft > visibleRight ? visibleRight - screenLeft : 0;
    const offsetY = screenBottom < visibleTop ? visibleTop - screenBottom : screenTop > visibleBottom ? visibleBottom - screenTop : 0;
    return { x: viewport.x + offsetX, y: viewport.y + offsetY, k: viewport.k };
}

export function interpolateViewport(from: ViewportTransform, to: ViewportTransform, progress: number): ViewportTransform {
    const t = 1 - Math.pow(1 - Math.min(1, Math.max(0, progress)), 3);
    return {
        x: from.x + (to.x - from.x) * t,
        y: from.y + (to.y - from.y) * t,
        k: from.k + (to.k - from.k) * t,
    };
}
