import { useCallback, useEffect, useLayoutEffect, useRef, useState, type Dispatch, type PointerEvent as ReactPointerEvent, type SetStateAction } from "react";
import { App } from "antd";
import { nanoid } from "nanoid";

import type { PendingConnectionCreate } from "@/components/canvas/canvas-workspace-overlays";
import { attachNodeToStoryboardRow, createCanvasNode, getConnectionTargetAnchor, isHiddenBatchChild, normalizeConnection, storyboardHandleAtY, storyboardRowFromHandle } from "@/lib/canvas/canvas-project-domain";
import { createCanvasDrawingFromImage } from "@/lib/canvas/canvas-drawing-storage";
import { appendCanvasNodesWithFrameExpansion, isFrameNode, isNodeHiddenByCollapsedFrame } from "@/lib/canvas/canvas-frame";
import { placeCanvasNodeInContext } from "@/lib/canvas/canvas-layout";
import { getGenerationCount } from "@/lib/canvas/canvas-project-generation";
import { useEffectiveConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type ConnectionHandle, type ContextMenuState, type Position, type ViewportTransform } from "@/types/canvas";

type UseCanvasConnectionControllerOptions = {
    projectId: string;
    nodesRef: { current: CanvasNodeData[] };
    connectionsRef: { current: CanvasConnection[] };
    viewportRef: { current: ViewportTransform };
    scriptScrollTopById: Record<string, number>;
    screenToCanvas: (clientX: number, clientY: number) => Position;
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setContextMenu: Dispatch<SetStateAction<ContextMenuState | null>>;
    setDialogNodeId: Dispatch<SetStateAction<string | null>>;
    setDrawingNodeId: Dispatch<SetStateAction<string | null>>;
};

type ConnectionDropTarget = {
    nodeId: string | null;
    handleId?: string;
    isNearNode: boolean;
    blockedNodeId: string | null;
};

const CONNECTION_HANDLE_HIT_RADIUS = 40;
const CONNECTION_NODE_HIT_PADDING = 32;
const NODE_STATUS_IDLE = "idle" as const;

export function useCanvasConnectionController({
    projectId,
    nodesRef,
    connectionsRef,
    viewportRef,
    scriptScrollTopById,
    screenToCanvas,
    setNodes,
    setConnections,
    setSelectedNodeIds,
    setSelectedConnectionId,
    setContextMenu,
    setDialogNodeId,
    setDrawingNodeId,
}: UseCanvasConnectionControllerOptions) {
    const { message } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const [connectingParams, setConnectingParams] = useState<ConnectionHandle | null>(null);
    const [connectionTargetNodeId, setConnectionTargetNodeId] = useState<string | null>(null);
    const [connectionBlockedNodeId, setConnectionBlockedNodeId] = useState<string | null>(null);
    const [connectionTargetHandleId, setConnectionTargetHandleId] = useState<string | undefined>(undefined);
    const [pendingConnectionCreate, setPendingConnectionCreate] = useState<PendingConnectionCreate | null>(null);
    const [mouseWorld, setMouseWorld] = useState<Position>({ x: 0, y: 0 });
    const connectingParamsRef = useRef(connectingParams);
    const connectingPointerIdRef = useRef<number | null>(null);
    const pendingConnectionCreateRef = useRef(pendingConnectionCreate);

    useLayoutEffect(() => {
        connectingParamsRef.current = connectingParams;
        pendingConnectionCreateRef.current = pendingConnectionCreate;
    }, [connectingParams, pendingConnectionCreate]);

    const setConnecting = useCallback((next: ConnectionHandle | null) => {
        connectingParamsRef.current = next;
        setConnectingParams(next);
        if (!next) {
            connectingPointerIdRef.current = null;
            setConnectionTargetNodeId(null);
            setConnectionBlockedNodeId(null);
            setConnectionTargetHandleId(undefined);
        }
    }, []);

    const closeConnectionCreateMenu = useCallback(() => {
        pendingConnectionCreateRef.current = null;
        setPendingConnectionCreate(null);
    }, []);

    const cancelPendingConnectionCreate = useCallback(() => {
        closeConnectionCreateMenu();
        setConnecting(null);
    }, [closeConnectionCreateMenu, setConnecting]);

    const connectNodes = useCallback((current: ConnectionHandle, targetNodeId: string, targetHandleId?: string) => {
        if (current.nodeId === targetNodeId) return;
        const connection = normalizeConnection(current.nodeId, targetNodeId, nodesRef.current, current.handleType);
        if (!connection) {
            message.warning("配置节点之间不能连接");
            return;
        }
        const { fromNodeId, toNodeId } = connection;
        const fromHandleId = fromNodeId === current.nodeId ? current.handleId : targetHandleId;
        const toHandleId = toNodeId === current.nodeId ? current.handleId : targetHandleId;
        const exists = connectionsRef.current.some((item) => item.fromNodeId === fromNodeId && item.toNodeId === toNodeId && item.fromHandleId === fromHandleId && item.toHandleId === toHandleId);
        if (!exists) {
            setConnections((currentConnections) => [...currentConnections, { id: `conn-${Date.now()}`, fromNodeId, toNodeId, fromHandleId, toHandleId }]);
            setNodes((currentNodes) => attachNodeToStoryboardRow(currentNodes, { fromNodeId, toNodeId, fromHandleId, toHandleId }));
        } else {
            message.info("这两个节点已经连接");
        }
        setContextMenu(null);
    }, [connectionsRef, message, nodesRef, setConnections, setContextMenu, setNodes]);

    const createConnectedNode = useCallback(async (type: CanvasNodeType.Image | CanvasNodeType.Text | CanvasNodeType.Script | CanvasNodeType.Config | CanvasNodeType.Video | CanvasNodeType.Audio | CanvasNodeType.Drawing, pending: PendingConnectionCreate) => {
        if (type === CanvasNodeType.Script && pending.connection.handleType === "target") {
            message.info("空白分镜脚本还没有可用的输出镜头，请先创建脚本再从具体镜头行连接");
            return;
        }
        const storyboardRow = type === CanvasNodeType.Video ? storyboardRowFromHandle(nodesRef.current, pending.connection.nodeId, pending.connection.handleId) : undefined;
        const videoPrompt = storyboardRow ? (storyboardRow.videoMotionPrompt || storyboardRow.plotDescription).trim() : "";
        const originNode = nodesRef.current.find((node) => node.id === pending.connection.nodeId);
        const sourceNode = pending.connection.handleType === "source" ? originNode : undefined;
        const scriptPrompt = type === CanvasNodeType.Script && sourceNode?.type === CanvasNodeType.Text ? (sourceNode.metadata?.content || sourceNode.metadata?.prompt || "").trim() : "";
        const metadata = type === CanvasNodeType.Config
            ? { model: effectiveConfig.imageModel || effectiveConfig.model, size: effectiveConfig.size, count: getGenerationCount(effectiveConfig.canvasImageCount || effectiveConfig.count) }
            : type === CanvasNodeType.Script && scriptPrompt
              ? { prompt: scriptPrompt, composerContent: scriptPrompt }
            : type === CanvasNodeType.Video && storyboardRow
              ? { prompt: videoPrompt, composerContent: videoPrompt, generationMode: "video" as const, videoEditOperation: "text_to_video" as const, workflowKind: "shot" as const, workflowTitle: `镜头 ${storyboardRow.shotNumber} 视频`, shotIndex: storyboardRow.shotNumber, seconds: String(storyboardRow.durationSeconds), status: NODE_STATUS_IDLE }
              : undefined;
        let newNode = placeCanvasNodeInContext(nodesRef.current, createCanvasNode(type, pending.position, metadata), undefined, originNode);
        if (storyboardRow) newNode.title = `镜头 ${storyboardRow.shotNumber} · 视频`;
        const connection = normalizeConnection(pending.connection.nodeId, newNode.id, [...nodesRef.current, newNode], pending.connection.handleType);
        if (!connection) {
            message.warning("配置节点之间不能连接");
            return;
        }
        if (type === CanvasNodeType.Drawing) {
            const drawingSourceNode = nodesRef.current.find((node) => node.id === pending.connection.nodeId);
            const sourceUrl = drawingSourceNode?.type === CanvasNodeType.Image ? drawingSourceNode.metadata?.content : "";
            if (pending.connection.handleType !== "source" || !drawingSourceNode || !sourceUrl || !newNode.metadata?.drawingId) {
                message.error("只有已有图片内容的输出连线可以创建绘图");
                return;
            }
            closeConnectionCreateMenu();
            setConnecting(null);
            try {
                const saved = await createCanvasDrawingFromImage(projectId, newNode.metadata.drawingId, {
                    url: sourceUrl,
                    storageKey: drawingSourceNode.metadata?.storageKey,
                    name: drawingSourceNode.title || "来源图片",
                    mimeType: drawingSourceNode.metadata?.mimeType,
                });
                newNode.title = `${drawingSourceNode.title || "图片"} · 绘图`;
                newNode.metadata = {
                    ...newNode.metadata,
                    drawingRevision: saved.revision,
                    drawingUpdatedAt: saved.updatedAt,
                    drawingShapeCount: saved.shapeCount,
                    drawingPageCount: saved.pageCount,
                };
            } catch (error) {
                message.error(error instanceof Error ? `创建绘图失败：${error.message}` : "创建绘图失败");
                return;
            }
        }
        const fromHandleId = connection.fromNodeId === pending.connection.nodeId ? pending.connection.handleId : undefined;
        // 从已有节点向下游创建分镜脚本时，关系属于项目上下文输入；不能落成一条指向卡片中心的幽灵连线。
        const toHandleId = connection.toNodeId === pending.connection.nodeId
            ? pending.connection.handleId
            : type === CanvasNodeType.Script
              ? "storyboard:context"
              : undefined;
        const connected = { ...connection, fromHandleId, toHandleId };
        setNodes((currentNodes) => attachNodeToStoryboardRow(appendCanvasNodesWithFrameExpansion(currentNodes, [newNode]), connected));
        setConnections((currentConnections) => [...currentConnections, { id: nanoid(), ...connected }]);
        setSelectedNodeIds(new Set([newNode.id]));
        setSelectedConnectionId(null);
        if (type === CanvasNodeType.Drawing) setDrawingNodeId(newNode.id);
        else if (type !== CanvasNodeType.Text && type !== CanvasNodeType.Script && type !== CanvasNodeType.Audio) setDialogNodeId(newNode.id);
        closeConnectionCreateMenu();
        setConnecting(null);
    }, [closeConnectionCreateMenu, effectiveConfig.canvasImageCount, effectiveConfig.count, effectiveConfig.imageModel, effectiveConfig.model, effectiveConfig.size, message, nodesRef, projectId, setConnecting, setConnections, setDialogNodeId, setDrawingNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds]);

    const getConnectionDropTarget = useCallback((clientX: number, clientY: number, current: ConnectionHandle): ConnectionDropTarget => {
        const world = screenToCanvas(clientX, clientY);
        const scale = Math.max(viewportRef.current.k, 0.05);
        const padding = CONNECTION_NODE_HIT_PADDING / scale;
        const handleRadius = CONNECTION_HANDLE_HIT_RADIUS / scale;
        let isNearNode = false;
        let bestNodeId: string | null = null;
        let bestHandleId: string | undefined;
        let bestPriority = Number.POSITIVE_INFINITY;
        let blockedNodeId: string | null = null;

        [...nodesRef.current]
            .filter((node) => !isHiddenBatchChild(node, nodesRef.current) && !isNodeHiddenByCollapsedFrame(node, nodesRef.current) && !isFrameNode(node))
            .reverse()
            .forEach((node) => {
                const hitsInside = world.x >= node.position.x && world.x <= node.position.x + node.width && world.y >= node.position.y && world.y <= node.position.y + node.height;
                const hitsExpanded = world.x >= node.position.x - padding && world.x <= node.position.x + node.width + padding && world.y >= node.position.y - padding && world.y <= node.position.y + node.height + padding;
                const scrollTop = scriptScrollTopById[node.id] || 0;
                const targetHandleId = node.type === CanvasNodeType.Script ? storyboardHandleAtY(node, world.y, scrollTop) : undefined;
                if (node.type === CanvasNodeType.Script && !targetHandleId) {
                    if (hitsInside || hitsExpanded) {
                        isNearNode = true;
                        blockedNodeId = node.id;
                    }
                    return;
                }
                const anchor = getConnectionTargetAnchor(node, current, targetHandleId, scrollTop);
                const dx = world.x - anchor.x;
                const dy = world.y - anchor.y;
                const hitsHandle = dx * dx + dy * dy <= handleRadius * handleRadius;
                if (!hitsHandle && !hitsInside && !hitsExpanded) return;
                isNearNode = true;
                if (node.id === current.nodeId) return;
                if (current.handleType === "target" && (node.type === CanvasNodeType.Config || targetHandleId === "storyboard:context")) {
                    blockedNodeId = node.id;
                    return;
                }
                if (!normalizeConnection(current.nodeId, node.id, nodesRef.current, current.handleType)) {
                    blockedNodeId = node.id;
                    return;
                }
                const priority = hitsInside ? 0 : hitsHandle ? 1 : 2;
                if (priority < bestPriority) {
                    bestNodeId = node.id;
                    bestHandleId = targetHandleId;
                    bestPriority = priority;
                }
            });
        return { nodeId: bestNodeId, handleId: bestHandleId, isNearNode, blockedNodeId };
    }, [nodesRef, screenToCanvas, scriptScrollTopById, viewportRef]);

    const finishConnection = useCallback((clientX: number, clientY: number) => {
        if (pendingConnectionCreateRef.current) return;
        const currentConnection = connectingParamsRef.current;
        if (!currentConnection) return;
        const dropTarget = getConnectionDropTarget(clientX, clientY, currentConnection);
        if (dropTarget.nodeId) {
            connectNodes(currentConnection, dropTarget.nodeId, dropTarget.handleId);
            setConnecting(null);
        } else if (dropTarget.isNearNode) {
            if (dropTarget.blockedNodeId) message.info("该节点不能作为当前连线的目标");
            setConnecting(null);
        } else {
            const position = screenToCanvas(clientX, clientY);
            setMouseWorld(position);
            const pending = { connection: currentConnection, position };
            pendingConnectionCreateRef.current = pending;
            setPendingConnectionCreate(pending);
        }
    }, [connectNodes, getConnectionDropTarget, message, screenToCanvas, setConnecting]);

    const handleConnectStart = useCallback((event: ReactPointerEvent, nodeId: string, handleType: "source" | "target", handleId?: string) => {
        event.preventDefault();
        event.stopPropagation();
        event.currentTarget.setPointerCapture(event.pointerId);
        connectingPointerIdRef.current = event.pointerId;
        setMouseWorld(screenToCanvas(event.clientX, event.clientY));
        setConnecting({ nodeId, handleType, handleId });
        setConnectionTargetNodeId(null);
        setConnectionBlockedNodeId(null);
        setConnectionTargetHandleId(undefined);
        setSelectedConnectionId(null);
    }, [screenToCanvas, setConnecting, setSelectedConnectionId]);

    useEffect(() => {
        const handlePointerMove = (event: PointerEvent) => {
            const current = connectingParamsRef.current;
            if (!current || connectingPointerIdRef.current !== event.pointerId || pendingConnectionCreateRef.current) return;
            const dropTarget = getConnectionDropTarget(event.clientX, event.clientY, current);
            setConnectionTargetNodeId(dropTarget.nodeId);
            setConnectionBlockedNodeId(dropTarget.nodeId ? null : dropTarget.blockedNodeId);
            setConnectionTargetHandleId(dropTarget.handleId);
            setMouseWorld(screenToCanvas(event.clientX, event.clientY));
        };
        const handlePointerUp = (event: PointerEvent) => {
            if (connectingPointerIdRef.current === event.pointerId) finishConnection(event.clientX, event.clientY);
        };
        const handlePointerCancel = (event: PointerEvent) => {
            if (connectingPointerIdRef.current === event.pointerId) setConnecting(null);
        };
        const cancel = () => {
            if (connectingParamsRef.current) setConnecting(null);
        };
        window.addEventListener("pointermove", handlePointerMove);
        window.addEventListener("pointerup", handlePointerUp);
        window.addEventListener("pointercancel", handlePointerCancel);
        window.addEventListener("blur", cancel);
        return () => {
            window.removeEventListener("pointermove", handlePointerMove);
            window.removeEventListener("pointerup", handlePointerUp);
            window.removeEventListener("pointercancel", handlePointerCancel);
            window.removeEventListener("blur", cancel);
        };
    }, [finishConnection, getConnectionDropTarget, screenToCanvas, setConnecting]);

    return {
        cancelPendingConnectionCreate,
        closeConnectionCreateMenu,
        connectionBlockedNodeId,
        connectionTargetHandleId,
        connectionTargetNodeId,
        connectingParams,
        createConnectedNode,
        handleConnectStart,
        mouseWorld,
        pendingConnectionCreate,
        setConnecting,
    };
}
