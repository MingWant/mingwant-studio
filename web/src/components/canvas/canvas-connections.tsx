import React, { useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";

import { canvasThemes } from "@/lib/canvas-theme";
import { isFrameNode } from "@/lib/canvas/canvas-frame";
import { useThemeStore } from "@/stores/use-theme-store";
import { STORYBOARD_HEADER_HEIGHT, STORYBOARD_ROW_HEIGHT, storyboardTableHeight } from "@/components/canvas/canvas-script-node";
import type { CanvasConnection, CanvasNodeData, ConnectionHandle, Position } from "@/types/canvas";

const CONNECTION_MARKER_ID = "canvas-arrow-default";
const CONNECTION_ACTIVE_MARKER_ID = "canvas-arrow-active";
const CONNECTION_DRAFT_MARKER_ID = "canvas-arrow-draft";
const CONNECTION_INVALID_MARKER_ID = "canvas-arrow-invalid";

export function CanvasConnectionMarkers() {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    return (
        <defs>
            <ConnectionMarker id={CONNECTION_MARKER_ID} color={theme.node.muted} opacity={0.58} />
            <ConnectionMarker id={CONNECTION_ACTIVE_MARKER_ID} color={theme.accent.primary} opacity={0.92} />
            <ConnectionMarker id={CONNECTION_DRAFT_MARKER_ID} color={theme.accent.primary} opacity={0.88} />
            <ConnectionMarker id={CONNECTION_INVALID_MARKER_ID} color={theme.accent.danger} opacity={0.9} />
        </defs>
    );
}

function ConnectionMarker({ id, color, opacity }: { id: string; color: string; opacity: number }) {
    return <marker id={id} viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 1 L 9 5 L 0 9 z" fill={color} opacity={opacity} /></marker>;
}

export const ConnectionPath = React.memo(function ConnectionPath({
    connection,
    from,
    to,
    obstacles = [],
    fromScrollTop = 0,
    toScrollTop = 0,
    active,
    onSelect,
    onContextMenu,
}: {
    connection: CanvasConnection;
    from: CanvasNodeData;
    to: CanvasNodeData;
    obstacles?: CanvasNodeData[];
    fromScrollTop?: number;
    toScrollTop?: number;
    active: boolean;
    onSelect: () => void;
    onContextMenu?: (event: ReactMouseEvent<SVGPathElement>) => void;
}) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const [hovered, setHovered] = useState(false);
    const startX = from.position.x + from.width;
    const startY = connectionHandleY(from, connection.fromHandleId, fromScrollTop);
    const endX = to.position.x;
    const endY = connectionHandleY(to, connection.toHandleId, toScrollTop);
    const pathD = connectionPath(startX, startY, endX, endY, from, to, obstacles);
    const emphasized = active || hovered;

    return (
        <g>
            <path
                data-connection-id={connection.id}
                role="button"
                tabIndex={0}
                aria-label={`连接：${from.title || "起点"} 到 ${to.title || "终点"}`}
                d={pathD}
                stroke="transparent"
                strokeWidth="20"
                vectorEffect="non-scaling-stroke"
                fill="none"
                style={{ cursor: "pointer", pointerEvents: "stroke" }}
                onMouseEnter={() => setHovered(true)}
                onMouseLeave={() => setHovered(false)}
                onFocus={() => setHovered(true)}
                onBlur={() => setHovered(false)}
                onKeyDown={(event) => {
                    if (event.key !== "Enter" && event.key !== " ") return;
                    event.preventDefault();
                    event.stopPropagation();
                    onSelect();
                }}
                onClick={(event) => {
                    event.stopPropagation();
                    onSelect();
                }}
                onContextMenu={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    onContextMenu?.(event);
                }}
            />
            <path
                d={pathD}
                stroke={emphasized ? theme.accent.primary : theme.node.muted}
                strokeWidth={emphasized ? 2.2 : 1.5}
                vectorEffect="non-scaling-stroke"
                strokeOpacity={emphasized ? 0.9 : 0.48}
                fill="none"
                strokeLinecap="round"
                markerEnd={`url(#${emphasized ? CONNECTION_ACTIVE_MARKER_ID : CONNECTION_MARKER_ID})`}
                style={{ pointerEvents: "none" }}
            />
        </g>
    );
}, (previous, next) => previous.connection === next.connection && previous.from === next.from && previous.to === next.to && previous.obstacles === next.obstacles && previous.active === next.active && previous.fromScrollTop === next.fromScrollTop && previous.toScrollTop === next.toScrollTop);

export function ActiveConnectionPath({ node, handle, mouseWorld, target, targetHandleId, nodeScrollTop = 0, targetScrollTop = 0, obstacles = [], invalid = false }: { node?: CanvasNodeData; handle: ConnectionHandle; mouseWorld: Position; target?: CanvasNodeData; targetHandleId?: string; nodeScrollTop?: number; targetScrollTop?: number; obstacles?: CanvasNodeData[]; invalid?: boolean }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    if (!node) return null;

    const startX = handle.handleType === "source" ? node.position.x + node.width : mouseWorld.x;
    const startY = handle.handleType === "source" ? connectionHandleY(node, handle.handleId, nodeScrollTop) : mouseWorld.y;
    const endX = handle.handleType === "source" ? mouseWorld.x : node.position.x;
    const endY = handle.handleType === "source" ? mouseWorld.y : connectionHandleY(node, handle.handleId, nodeScrollTop);
    const snappedStartX = handle.handleType === "target" && target ? target.position.x + target.width : startX;
    const snappedStartY = handle.handleType === "target" && target ? connectionHandleY(target, targetHandleId, targetScrollTop) : startY;
    const snappedEndX = handle.handleType === "source" && target ? target.position.x : endX;
    const snappedEndY = handle.handleType === "source" && target ? connectionHandleY(target, targetHandleId, targetScrollTop) : endY;
    const sourceNode = handle.handleType === "source" ? node : target;
    const targetNode = handle.handleType === "source" ? target : node;
    const pathD = connectionPath(snappedStartX, snappedStartY, snappedEndX, snappedEndY, sourceNode, targetNode, obstacles);
    const color = invalid ? theme.accent.danger : theme.accent.primary;

    return <path className="canvas-connection-draft" d={pathD} stroke={color} strokeWidth="1.8" strokeOpacity="0.86" vectorEffect="non-scaling-stroke" fill="none" strokeDasharray={invalid ? "4,5" : "7,7"} strokeLinecap="round" markerEnd={`url(#${invalid ? CONNECTION_INVALID_MARKER_ID : CONNECTION_DRAFT_MARKER_ID})`} />;
}

function connectionPath(startX: number, startY: number, endX: number, endY: number, from?: CanvasNodeData, to?: CanvasNodeData, obstacles: CanvasNodeData[] = []) {
    const forwardGap = endX - startX;
    const routeLeft = Math.min(startX, endX);
    const routeRight = Math.max(startX, endX);
    const corridorTop = Math.min(startY, endY) - 28;
    const corridorBottom = Math.max(startY, endY) + 28;
    const blockers = obstacles.filter((node) => !isFrameNode(node) && node.id !== from?.id && node.id !== to?.id && node.position.x < routeRight && node.position.x + node.width > routeLeft && node.position.y < corridorBottom && node.position.y + node.height > corridorTop);
    if (forwardGap >= 72 && !blockers.length) {
        const curvature = Math.min(240, Math.max(56, forwardGap * 0.45));
        return `M ${startX} ${startY} C ${startX + curvature} ${startY}, ${endX - curvature} ${endY}, ${endX} ${endY}`;
    }

    // 有中间障碍或发生回连时改走节点群外侧通道；转角使用圆滑曲线，避免避障成功却退化成生硬折线。
    // 正向避障只需要绕开真正挡路的节点；把超高的起终点也算入会让路线被无谓拉到画布远端。
    const laneNodes = (forwardGap >= 0 && blockers.length ? blockers : [from, to, ...blockers]).filter((node): node is CanvasNodeData => Boolean(node));
    const upperLane = Math.min(startY, endY, ...laneNodes.map((node) => node.position.y)) - 56;
    const lowerLane = Math.max(startY, endY, ...laneNodes.map((node) => node.position.y + node.height)) + 56;
    const upperCost = Math.abs(startY - upperLane) + Math.abs(endY - upperLane);
    const lowerCost = Math.abs(startY - lowerLane) + Math.abs(endY - lowerLane);
    const detourY = upperCost < lowerCost ? upperLane : lowerLane;
    const lead = Math.min(120, Math.max(48, Math.abs(forwardGap) * 0.2 + 48));
    const closeCorridorInset = Math.max(0, forwardGap / 3);
    const exitX = forwardGap >= 0
        ? blockers.length
            ? Math.max(startX + 2, Math.min(startX + lead, Math.min(...blockers.map((node) => node.position.x)) - 24))
            : startX + closeCorridorInset
        : startX + lead;
    const entryX = forwardGap >= 0
        ? blockers.length
            ? Math.min(endX - 2, Math.max(endX - lead, Math.max(...blockers.map((node) => node.position.x + node.width)) + 24))
            : endX - closeCorridorInset
        : endX - lead;
    return roundedOrthogonalPath([
        { x: startX, y: startY },
        { x: exitX, y: startY },
        { x: exitX, y: detourY },
        { x: entryX, y: detourY },
        { x: entryX, y: endY },
        { x: endX, y: endY },
    ]);
}

function roundedOrthogonalPath(points: Position[], cornerRadius = 32) {
    const route = points.filter((point, index) => index === 0 || point.x !== points[index - 1].x || point.y !== points[index - 1].y);
    if (route.length < 2) return "";
    let path = `M ${route[0].x} ${route[0].y}`;
    for (let index = 1; index < route.length - 1; index++) {
        const previous = route[index - 1];
        const corner = route[index];
        const next = route[index + 1];
        const incomingLength = Math.hypot(corner.x - previous.x, corner.y - previous.y);
        const outgoingLength = Math.hypot(next.x - corner.x, next.y - corner.y);
        if (!incomingLength || !outgoingLength) continue;
        const radius = Math.min(cornerRadius, incomingLength / 2, outgoingLength / 2);
        const before = {
            x: corner.x - ((corner.x - previous.x) / incomingLength) * radius,
            y: corner.y - ((corner.y - previous.y) / incomingLength) * radius,
        };
        const after = {
            x: corner.x + ((next.x - corner.x) / outgoingLength) * radius,
            y: corner.y + ((next.y - corner.y) / outgoingLength) * radius,
        };
        path += ` L ${before.x} ${before.y} Q ${corner.x} ${corner.y} ${after.x} ${after.y}`;
    }
    const end = route[route.length - 1];
    return `${path} L ${end.x} ${end.y}`;
}

function connectionHandleY(node: CanvasNodeData, handleId?: string, scrollTop = 0) {
    if (handleId === "storyboard:context") return node.position.y + node.height - (node.metadata?.storyboardComposerHeight || 104) / 2;
    if (!handleId?.startsWith("row:")) return node.position.y + node.height / 2;
    const rowId = handleId.slice(4);
    const index = (node.metadata?.storyboard?.rows || []).findIndex((row) => row.id === rowId);
    if (index < 0) return node.position.y + node.height / 2;
    const tableHeight = storyboardTableHeight(node.height, node.metadata?.storyboardComposerHeight);
    const localY = Math.min(Math.max(index * STORYBOARD_ROW_HEIGHT + STORYBOARD_ROW_HEIGHT / 2 - scrollTop, 4), tableHeight - 4);
    return node.position.y + STORYBOARD_HEADER_HEIGHT + localY;
}
