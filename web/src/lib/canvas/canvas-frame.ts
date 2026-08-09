import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";

export const FRAME_HEADER_HEIGHT = 36;
export const FRAME_PADDING = 24;
export const FRAME_COLLAPSED_WIDTH = 240;
export const FRAME_COLLAPSED_HEIGHT = 144;

export function isFrameNode(node?: CanvasNodeData | null): node is CanvasNodeData & { type: CanvasNodeType.Frame } {
    return node?.type === CanvasNodeType.Frame;
}

export function canFrameContain(node: CanvasNodeData) {
    // 背板承担工作流分组，不应因节点媒介类型不同而把配置、音频或技能节点留在组外。
    return node.type !== CanvasNodeType.Frame;
}

export function getFrameChildren(frameId: string, nodes: CanvasNodeData[]) {
    return nodes.filter((node) => node.parentId === frameId);
}

export function getFrameChildIds(frameId: string, nodes: CanvasNodeData[]) {
    return new Set(getFrameChildren(frameId, nodes).map((node) => node.id));
}

export function getCollapsedParentFrame(node: CanvasNodeData, nodes: CanvasNodeData[]) {
    if (!node.parentId) return null;
    const frame = nodes.find((item) => item.id === node.parentId && isFrameNode(item));
    return frame?.metadata?.frame?.collapsed ? frame : null;
}

export function isNodeHiddenByCollapsedFrame(node: CanvasNodeData, nodes: CanvasNodeData[]) {
    return Boolean(getCollapsedParentFrame(node, nodes));
}

export function findFrameDropTarget(nodes: CanvasNodeData[], draggedNodeIds: Set<string>) {
    const dragged = nodes.filter((node) => draggedNodeIds.has(node.id) && canFrameContain(node));
    if (!dragged.length) return null;

    return (
        [...nodes]
            .reverse()
            .find((frame) => {
                if (!isFrameNode(frame) || frame.metadata?.frame?.collapsed || draggedNodeIds.has(frame.id)) return false;
                const left = frame.position.x;
                const top = frame.position.y + FRAME_HEADER_HEIGHT;
                const right = frame.position.x + frame.width;
                const bottom = frame.position.y + frame.height;
                return dragged.every((node) => {
                    const centerX = node.position.x + node.width / 2;
                    const centerY = node.position.y + node.height / 2;
                    return centerX >= left && centerX <= right && centerY >= top && centerY <= bottom;
                });
            })?.id || null
    );
}

export function applyFrameDrop(nodes: CanvasNodeData[], draggedNodeIds: Set<string>, frameId: string | null) {
    const next = nodes.map((node) => (draggedNodeIds.has(node.id) && canFrameContain(node) ? { ...node, parentId: frameId || undefined } : node));
    if (!frameId) return next;

    return expandCanvasFramesToFit(next, new Set([frameId]));
}

export function appendCanvasNodesWithFrameExpansion(nodes: CanvasNodeData[], addedNodes: CanvasNodeData[]) {
    if (!addedNodes.length) return nodes;
    const parentIds = new Set(addedNodes.map((node) => node.parentId).filter((id): id is string => Boolean(id)));
    return expandCanvasFramesToFit([...nodes, ...addedNodes], parentIds);
}

export function expandCanvasFramesToFit(nodes: CanvasNodeData[], frameIds: ReadonlySet<string>) {
    if (!frameIds.size) return nodes;
    let next = nodes;
    frameIds.forEach((frameId) => {
        next = expandCanvasFrameToFit(next, frameId);
    });
    return next;
}

function expandCanvasFrameToFit(nodes: CanvasNodeData[], frameId: string) {
    const children = getFrameChildren(frameId, nodes);
    if (!children.length) return nodes;
    const frame = nodes.find((node) => node.id === frameId);
    if (!frame || !isFrameNode(frame) || frame.metadata?.frame?.collapsed) return nodes;

    const left = Math.min(frame.position.x, ...children.map((node) => node.position.x - FRAME_PADDING));
    const top = Math.min(frame.position.y, ...children.map((node) => node.position.y - FRAME_HEADER_HEIGHT - FRAME_PADDING));
    const right = Math.max(frame.position.x + frame.width, ...children.map((node) => node.position.x + node.width + FRAME_PADDING));
    const bottom = Math.max(frame.position.y + frame.height, ...children.map((node) => node.position.y + node.height + FRAME_PADDING));

    return nodes.map((node) =>
        node.id === frameId
            ? {
                  ...node,
                  position: { x: left, y: top },
                  width: right - left,
                  height: bottom - top,
                  metadata: {
                      ...node.metadata,
                      frame: {
                          collapsed: false,
                          expandedWidth: right - left,
                          expandedHeight: bottom - top,
                      },
                  },
              }
            : node,
    );
}

export function resolveFrameConnection(connection: CanvasConnection, nodes: CanvasNodeData[]) {
    const from = nodes.find((node) => node.id === connection.fromNodeId);
    const to = nodes.find((node) => node.id === connection.toNodeId);
    if (!from || !to) return null;

    const displayFrom = getCollapsedParentFrame(from, nodes) || from;
    const displayTo = getCollapsedParentFrame(to, nodes) || to;
    if (displayFrom.id === displayTo.id) return null;
    return { from: displayFrom, to: displayTo };
}
