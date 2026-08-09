import type { CanvasConnection, CanvasNodeData, Position } from "@/types/canvas";
import { canFrameContain, FRAME_HEADER_HEIGHT, FRAME_PADDING, isFrameNode } from "@/lib/canvas/canvas-frame";

export type CanvasLayoutMode = "row" | "column" | "grid";
export type CanvasAlignmentMode = "left" | "centerX" | "right" | "top" | "centerY" | "bottom" | "distributeX" | "distributeY";

type CanvasPlacementContext = {
    parentId?: string;
    ignoreNodeIds?: ReadonlySet<string>;
};

export function findAvailableCanvasNodePosition(
    nodes: CanvasNodeData[],
    preferred: Position,
    size: { width: number; height: number },
    gap = 44,
    context: CanvasPlacementContext = {},
) {
    const parentFrame = context.parentId
        ? nodes.find((node) => node.id === context.parentId && isFrameNode(node))
        : undefined;
    // 父背板是当前节点的容器而不是障碍；其他背板与折叠内容仍保留占位。
    const obstacles = nodes.filter((node) => node.width > 0 && node.height > 0 && node.id !== parentFrame?.id && !context.ignoreNodeIds?.has(node.id));
    const parentWidth = parentFrame?.metadata?.frame?.collapsed ? parentFrame.metadata.frame.expandedWidth || parentFrame.width : parentFrame?.width;
    const parentHeight = parentFrame?.metadata?.frame?.collapsed ? parentFrame.metadata.frame.expandedHeight || parentFrame.height : parentFrame?.height;
    const contentBounds = parentFrame && parentWidth && parentHeight ? {
        left: parentFrame.position.x + FRAME_PADDING,
        top: parentFrame.position.y + FRAME_HEADER_HEIGHT + FRAME_PADDING,
        right: parentFrame.position.x + parentWidth - FRAME_PADDING,
        bottom: parentFrame.position.y + parentHeight - FRAME_PADDING,
    } : null;
    const isFree = (position: Position) => obstacles.every((node) => (
        position.x + size.width + gap <= node.position.x
        || position.x >= node.position.x + node.width + gap
        || position.y + size.height + gap <= node.position.y
        || position.y >= node.position.y + node.height + gap
    ));
    if (isFree(preferred)) return preferred;

    const edgeCandidates = obstacles.flatMap((node) => [
        { position: { x: node.position.x + node.width + gap, y: preferred.y }, bias: 0 },
        { position: { x: preferred.x, y: node.position.y + node.height + gap }, bias: 0.04 },
        { position: { x: node.position.x - size.width - gap, y: preferred.y }, bias: 0.08 },
        { position: { x: preferred.x, y: node.position.y - size.height - gap }, bias: 0.12 },
    ]).sort((left, right) => candidateScore(left, preferred, size, gap, contentBounds) - candidateScore(right, preferred, size, gap, contentBounds));
    for (const candidate of edgeCandidates) {
        if (isFree(candidate.position)) return candidate.position;
    }

    const stepX = size.width + gap;
    const stepY = size.height + gap;
    for (let ring = 1; ring <= 12; ring += 1) {
        const offsets: Array<{ x: number; y: number }> = [];
        for (let y = -ring; y <= ring; y += 1) {
            for (let x = -ring; x <= ring; x += 1) {
                if (Math.max(Math.abs(x), Math.abs(y)) === ring) offsets.push({ x, y });
            }
        }
        offsets.sort((left, right) => {
            const leftPosition = { x: preferred.x + left.x * stepX, y: preferred.y + left.y * stepY };
            const rightPosition = { x: preferred.x + right.x * stepX, y: preferred.y + right.y * stepY };
            return placementScore(left) + parentOverflowPenalty(leftPosition, size, contentBounds) - placementScore(right) - parentOverflowPenalty(rightPosition, size, contentBounds);
        });
        for (const offset of offsets) {
            const candidate = { x: preferred.x + offset.x * stepX, y: preferred.y + offset.y * stepY };
            if (isFree(candidate)) return candidate;
        }
    }

    // 极密画布下保持确定性：落到最右侧，不把新节点悄悄叠在已有内容上。
    const rightEdge = obstacles.reduce((value, node) => Math.max(value, node.position.x + node.width), preferred.x);
    return { x: rightEdge + gap, y: preferred.y };
}

function candidateScore(candidate: { position: Position; bias: number }, preferred: Position, size: { width: number; height: number }, gap: number, contentBounds: PlacementBounds | null) {
    const dx = (candidate.position.x - preferred.x) / Math.max(1, size.width + gap);
    const dy = (candidate.position.y - preferred.y) / Math.max(1, size.height + gap);
    return Math.hypot(dx, dy) + candidate.bias + parentOverflowPenalty(candidate.position, size, contentBounds);
}

type PlacementBounds = { left: number; top: number; right: number; bottom: number };

function parentOverflowPenalty(position: Position, size: { width: number; height: number }, bounds: PlacementBounds | null) {
    if (!bounds) return 0;
    const overflow = Math.max(0, bounds.left - position.x)
        + Math.max(0, bounds.top - position.y)
        + Math.max(0, position.x + size.width - bounds.right)
        + Math.max(0, position.y + size.height - bounds.bottom);
    return overflow ? 4 + overflow / Math.max(1, size.width + size.height) : 0;
}

export function placeCanvasNodeInContext(nodes: CanvasNodeData[], node: CanvasNodeData, preferred = node.position, sourceNode?: CanvasNodeData, gap = 44) {
    const contextualNode = assignCanvasNodePlacementParent(nodes, node, preferred, sourceNode);
    return {
        ...contextualNode,
        position: findAvailableCanvasNodePosition(nodes, preferred, contextualNode, gap, { parentId: contextualNode.parentId }),
    };
}

export function assignCanvasNodePlacementParent(nodes: CanvasNodeData[], node: CanvasNodeData, preferred = node.position, sourceNode?: CanvasNodeData) {
    const parentId = resolveCanvasPlacementParent(nodes, node, preferred, sourceNode);
    return { ...node, ...(parentId ? { parentId } : { parentId: undefined }) };
}

function resolveCanvasPlacementParent(nodes: CanvasNodeData[], node: CanvasNodeData, preferred: Position, sourceNode?: CanvasNodeData) {
    if (!canFrameContain(node)) return undefined;
    const inheritedParentId = node.parentId || sourceNode?.parentId;
    // 派生任务完成时背板可能已被折叠；仍应保留原分组，展开时再按子节点扩容。
    const inheritedParent = inheritedParentId ? nodes.find((item) => item.id === inheritedParentId && isFrameNode(item)) : undefined;
    if (inheritedParent) return inheritedParent.id;

    const centerX = preferred.x + node.width / 2;
    const centerY = preferred.y + node.height / 2;
    return [...nodes].reverse().find((item) => (
        isFrameNode(item)
        && !item.metadata?.frame?.collapsed
        && centerX >= item.position.x
        && centerX <= item.position.x + item.width
        && centerY >= item.position.y + FRAME_HEADER_HEIGHT
        && centerY <= item.position.y + item.height
    ))?.id;
}

export function placeCanvasNodeGroup(nodes: CanvasNodeData[], group: CanvasNodeData[], gap = 44, sourceNode?: CanvasNodeData) {
    if (!group.length) return group;
    const groupIds = new Set(group.map((node) => node.id));
    const left = Math.min(...group.map((node) => node.position.x));
    const top = Math.min(...group.map((node) => node.position.y));
    const right = Math.max(...group.map((node) => node.position.x + node.width));
    const bottom = Math.max(...group.map((node) => node.position.y + node.height));
    const parentId = group.every(canFrameContain) ? resolveCanvasPlacementParent(nodes, group[0], { x: left, y: top }, sourceNode) : undefined;
    const available = findAvailableCanvasNodePosition(nodes, { x: left, y: top }, { width: right - left, height: bottom - top }, gap, { parentId, ignoreNodeIds: new Set(group.map((node) => node.id)) });
    const dx = available.x - left;
    const dy = available.y - top;
    return group.map((node) => ({
        ...node,
        ...(canFrameContain(node)
            ? parentId
                ? { parentId }
                : node.parentId && groupIds.has(node.parentId)
                  ? {}
                  : { parentId: undefined }
            : {}),
        position: dx || dy ? { x: node.position.x + dx, y: node.position.y + dy } : node.position,
    }));
}

function placementScore(offset: { x: number; y: number }) {
    const distance = Math.abs(offset.x) + Math.abs(offset.y);
    const diagonalPenalty = offset.x && offset.y ? 0.25 : 0;
    // 派生工作流默认从左到右展开；只有右侧被占用时才向下换行。
    const directionBias = offset.x > 0 ? 0 : offset.y > 0 ? 0.08 : offset.x < 0 ? 0.16 : 0.24;
    return distance + diagonalPenalty + directionBias;
}

export function layoutCanvasNodes(nodes: CanvasNodeData[], mode: CanvasLayoutMode) {
    const sorted = [...nodes].sort((a, b) => a.position.y - b.position.y || a.position.x - b.position.x);
    const left = Math.min(...sorted.map((node) => node.position.x));
    const top = Math.min(...sorted.map((node) => node.position.y));
    const gap = 32;
    const result = new Map<string, Position>();

    if (mode === "row") {
        let x = left;
        sorted.forEach((node) => {
            result.set(node.id, { x, y: top });
            x += node.width + gap;
        });
        return result;
    }

    if (mode === "column") {
        let y = top;
        sorted.forEach((node) => {
            result.set(node.id, { x: left, y });
            y += node.height + gap;
        });
        return result;
    }

    const columns = Math.ceil(Math.sqrt(sorted.length));
    const cellWidth = Math.max(...sorted.map((node) => node.width)) + gap;
    const cellHeight = Math.max(...sorted.map((node) => node.height)) + gap;
    sorted.forEach((node, index) => result.set(node.id, { x: left + (index % columns) * cellWidth, y: top + Math.floor(index / columns) * cellHeight }));
    return result;
}

export function layoutCanvasFlow(nodes: CanvasNodeData[], connections: CanvasConnection[], options: { obstacles?: CanvasNodeData[]; parentId?: string } = {}) {
    const selectedIds = new Set(nodes.map((node) => node.id));
    const outbound = new Map(nodes.map((node) => [node.id, [] as string[]]));

    connections.forEach((connection) => {
        if (!selectedIds.has(connection.fromNodeId) || !selectedIds.has(connection.toNodeId)) return;
        outbound.get(connection.fromNodeId)?.push(connection.toNodeId);
    });

    // 先把环路收敛为强连通分量，再对分量 DAG 分层；循环工作流因此仍能保留前后依赖。
    const components = stronglyConnectedNodeGroups(nodes.map((node) => node.id), outbound);
    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    components.forEach((component) => component.sort((leftId, rightId) => {
        const leftNode = nodeById.get(leftId)!;
        const rightNode = nodeById.get(rightId)!;
        return leftNode.position.x - rightNode.position.x || leftNode.position.y - rightNode.position.y;
    }));
    const componentByNodeId = new Map<string, number>();
    const localLayerByNodeId = new Map<string, number>();
    const componentSpan = new Map<number, number>();
    components.forEach((component, componentIndex) => component.forEach((nodeId) => componentByNodeId.set(nodeId, componentIndex)));
    components.forEach((component, componentIndex) => {
        // 环内节点采用最多四列的紧凑网格；既显露回路，又避免长环把画布无限拉宽。
        const span = component.length > 1 ? Math.min(4, Math.ceil(Math.sqrt(component.length))) : 1;
        componentSpan.set(componentIndex, span);
        component.forEach((nodeId, index) => localLayerByNodeId.set(nodeId, index % span));
    });
    const componentOutbound = new Map(components.map((_, index) => [index, new Set<number>()]));
    const componentInbound = new Map(components.map((_, index) => [index, 0]));
    outbound.forEach((targets, sourceId) => {
        const sourceComponent = componentByNodeId.get(sourceId);
        if (sourceComponent === undefined) return;
        targets.forEach((targetId) => {
            const targetComponent = componentByNodeId.get(targetId);
            if (targetComponent === undefined || targetComponent === sourceComponent || componentOutbound.get(sourceComponent)?.has(targetComponent)) return;
            componentOutbound.get(sourceComponent)?.add(targetComponent);
            componentInbound.set(targetComponent, (componentInbound.get(targetComponent) || 0) + 1);
        });
    });
    const queue = components.map((_, index) => index).filter((index) => !componentInbound.get(index));
    const layerByComponent = new Map(queue.map((index) => [index, 0]));
    while (queue.length) {
        const componentIndex = queue.shift()!;
        const layer = layerByComponent.get(componentIndex) || 0;
        componentOutbound.get(componentIndex)?.forEach((targetComponent) => {
            layerByComponent.set(targetComponent, Math.max(layerByComponent.get(targetComponent) || 0, layer + (componentSpan.get(componentIndex) || 1)));
            componentInbound.set(targetComponent, (componentInbound.get(targetComponent) || 1) - 1);
            if (!componentInbound.get(targetComponent)) queue.push(targetComponent);
        });
    }

    const groups = new Map<number, CanvasNodeData[]>();
    nodes.forEach((node) => {
        const componentIndex = componentByNodeId.get(node.id) || 0;
        const layer = (layerByComponent.get(componentIndex) || 0) + (localLayerByNodeId.get(node.id) || 0);
        groups.set(layer, [...(groups.get(layer) || []), node]);
    });

    const left = Math.min(...nodes.map((node) => node.position.x));
    const top = Math.min(...nodes.map((node) => node.position.y));
    const result = new Map<string, Position>();
    let x = left;
    [...groups.keys()].sort((a, b) => a - b).forEach((layer) => {
        const column = groups.get(layer)!.sort((a, b) => a.position.y - b.position.y);
        let y = top;
        const width = Math.max(...column.map((node) => node.width));
        column.forEach((node) => {
            result.set(node.id, { x, y });
            y += node.height + 48;
        });
        x += width + 120;
    });
    if (!options.obstacles?.length) return result;

    const arranged = nodes.map((node) => ({ ...node, position: result.get(node.id) || node.position }));
    const arrangedRight = Math.max(...arranged.map((node) => node.position.x + node.width));
    const arrangedBottom = Math.max(...arranged.map((node) => node.position.y + node.height));
    const available = findAvailableCanvasNodePosition(options.obstacles, { x: left, y: top }, { width: arrangedRight - left, height: arrangedBottom - top }, 44, { parentId: options.parentId });
    const dx = available.x - left;
    const dy = available.y - top;
    if (dx || dy) result.forEach((position, nodeId) => result.set(nodeId, { x: position.x + dx, y: position.y + dy }));
    return result;
}

function stronglyConnectedNodeGroups(nodeIds: string[], outbound: Map<string, string[]>) {
    let nextIndex = 0;
    const indexById = new Map<string, number>();
    const lowLinkById = new Map<string, number>();
    const stack: string[] = [];
    const onStack = new Set<string>();
    const components: string[][] = [];

    const visit = (nodeId: string) => {
        indexById.set(nodeId, nextIndex);
        lowLinkById.set(nodeId, nextIndex);
        nextIndex += 1;
        stack.push(nodeId);
        onStack.add(nodeId);
        (outbound.get(nodeId) || []).forEach((targetId) => {
            if (!indexById.has(targetId)) {
                visit(targetId);
                lowLinkById.set(nodeId, Math.min(lowLinkById.get(nodeId)!, lowLinkById.get(targetId)!));
            } else if (onStack.has(targetId)) {
                lowLinkById.set(nodeId, Math.min(lowLinkById.get(nodeId)!, indexById.get(targetId)!));
            }
        });
        if (lowLinkById.get(nodeId) !== indexById.get(nodeId)) return;
        const component: string[] = [];
        while (stack.length) {
            const current = stack.pop()!;
            onStack.delete(current);
            component.push(current);
            if (current === nodeId) break;
        }
        components.push(component);
    };

    nodeIds.forEach((nodeId) => {
        if (!indexById.has(nodeId)) visit(nodeId);
    });
    return components;
}

export function alignCanvasNodes(nodes: CanvasNodeData[], mode: CanvasAlignmentMode) {
    const result = new Map<string, Position>();
    if (nodes.length < 2) return result;
    const left = Math.min(...nodes.map((node) => node.position.x));
    const top = Math.min(...nodes.map((node) => node.position.y));
    const right = Math.max(...nodes.map((node) => node.position.x + node.width));
    const bottom = Math.max(...nodes.map((node) => node.position.y + node.height));
    const centerX = (left + right) / 2;
    const centerY = (top + bottom) / 2;

    if (mode === "distributeX") {
        const sorted = [...nodes].sort((a, b) => a.position.x - b.position.x);
        const totalWidth = sorted.reduce((sum, node) => sum + node.width, 0);
        const gap = (right - left - totalWidth) / Math.max(1, sorted.length - 1);
        let x = left;
        sorted.forEach((node) => {
            result.set(node.id, { x, y: node.position.y });
            x += node.width + gap;
        });
        return result;
    }

    if (mode === "distributeY") {
        const sorted = [...nodes].sort((a, b) => a.position.y - b.position.y);
        const totalHeight = sorted.reduce((sum, node) => sum + node.height, 0);
        const gap = (bottom - top - totalHeight) / Math.max(1, sorted.length - 1);
        let y = top;
        sorted.forEach((node) => {
            result.set(node.id, { x: node.position.x, y });
            y += node.height + gap;
        });
        return result;
    }

    nodes.forEach((node) => {
        const x = mode === "left" ? left : mode === "centerX" ? centerX - node.width / 2 : mode === "right" ? right - node.width : node.position.x;
        const y = mode === "top" ? top : mode === "centerY" ? centerY - node.height / 2 : mode === "bottom" ? bottom - node.height : node.position.y;
        result.set(node.id, { x, y });
    });
    return result;
}

export function nextCanvasVersionLabel(rootId: string, nodes: CanvasNodeData[]) {
    const labels = nodes
        .filter((node) => (node.metadata?.versionOfNodeId || node.id) === rootId)
        .map((node) => node.metadata?.versionLabel)
        .filter((label): label is string => Boolean(label));
    if (!labels.length) return "B";
    const highest = Math.max(...labels.map((label) => label.charCodeAt(0)), "A".charCodeAt(0));
    return String.fromCharCode(Math.min("Z".charCodeAt(0), highest + 1));
}
