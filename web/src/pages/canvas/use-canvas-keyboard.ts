import { useEffect, type Dispatch, type SetStateAction } from "react";

import { expandCanvasFramesToFit, getFrameChildIds, isFrameNode } from "@/lib/canvas/canvas-frame";
import type { CanvasNodeData, ContextMenuState } from "@/types/canvas";

type UseCanvasKeyboardOptions = {
    nodesRef: { current: CanvasNodeData[] };
    selectedNodeIdsRef: { current: Set<string> };
    selectedConnectionId: string | null;
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setContextMenu: Dispatch<SetStateAction<ContextMenuState | null>>;
    setShortcutRequestNonce: Dispatch<SetStateAction<number>>;
    setInfoNodeId: Dispatch<SetStateAction<string | null>>;
    setCropNodeId: Dispatch<SetStateAction<string | null>>;
    setMaskEditNodeId: Dispatch<SetStateAction<string | null>>;
    setAnnotationNodeId: Dispatch<SetStateAction<string | null>>;
    saveCanvasProject: () => unknown;
    zoomToActualSize: () => void;
    fitCanvasContent: () => void;
    fitCanvasSelection: () => void;
    undoCanvas: () => void;
    redoCanvas: () => void;
    cancelSelectionBox: () => void;
    copySelectedNodes: () => void;
    pasteCopiedNodes: () => boolean;
    shouldPreferCopiedNodes: () => boolean;
    pasteSystemClipboard: (position?: undefined, clipboardEvent?: ClipboardEvent | null) => Promise<boolean> | boolean;
    deleteNodes: (ids: Set<string>) => void;
    deleteConnection: (connectionId: string) => void;
    deselectCanvas: () => void;
};

export function useCanvasKeyboard({
    nodesRef,
    selectedNodeIdsRef,
    selectedConnectionId,
    setNodes,
    setSelectedNodeIds,
    setSelectedConnectionId,
    setContextMenu,
    setShortcutRequestNonce,
    setInfoNodeId,
    setCropNodeId,
    setMaskEditNodeId,
    setAnnotationNodeId,
    saveCanvasProject,
    zoomToActualSize,
    fitCanvasContent,
    fitCanvasSelection,
    undoCanvas,
    redoCanvas,
    cancelSelectionBox,
    copySelectedNodes,
    pasteCopiedNodes,
    shouldPreferCopiedNodes,
    pasteSystemClipboard,
    deleteNodes,
    deleteConnection,
    deselectCanvas,
}: UseCanvasKeyboardOptions) {
    useEffect(() => {
        const handleKeyDown = (event: KeyboardEvent) => {
            const target = event.target instanceof Element ? event.target : null;
            const key = event.key.toLowerCase();
            const isModifierShortcut = event.metaKey || event.ctrlKey;

            if (isModifierShortcut && !event.altKey && key === "s") {
                event.preventDefault();
                event.stopPropagation();
                if (!event.repeat) void saveCanvasProject();
                return;
            }
            if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement || event.target instanceof HTMLButtonElement || target?.closest("[contenteditable='true'],[data-canvas-no-zoom]")) return;
            if (event.key === "?" && !isModifierShortcut && !event.altKey) {
                event.preventDefault();
                setShortcutRequestNonce((value) => value + 1);
                return;
            }
            if (isModifierShortcut && !event.altKey && (key === "1" || key === "2" || key === "3")) {
                event.preventDefault();
                if (key === "1") zoomToActualSize();
                else if (key === "2") fitCanvasContent();
                else fitCanvasSelection();
                return;
            }
            if (isModifierShortcut && !event.altKey && key === "z") {
                event.preventDefault();
                if (event.shiftKey) redoCanvas();
                else undoCanvas();
                return;
            }
            if (isModifierShortcut && !event.altKey && key === "y") {
                event.preventDefault();
                redoCanvas();
                return;
            }
            if (isModifierShortcut && !event.altKey && key === "a") {
                event.preventDefault();
                setSelectedNodeIds(new Set(nodesRef.current.map((node) => node.id)));
                setSelectedConnectionId(null);
                setContextMenu(null);
                cancelSelectionBox();
                return;
            }
            if (isModifierShortcut && !event.altKey && key === "c") {
                event.preventDefault();
                copySelectedNodes();
                return;
            }
            if (isModifierShortcut && !event.altKey && key === "v") {
                // 不在 keydown 直接粘贴节点：交给 paste 事件，优先系统剪贴板图片。
                // 这里只做标记，真正逻辑在 paste 监听器里。
                return;
            }
            if (!isModifierShortcut && !event.altKey && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key) && selectedNodeIdsRef.current.size) {
                event.preventDefault();
                const distance = event.shiftKey ? 10 : 1;
                const dx = event.key === "ArrowLeft" ? -distance : event.key === "ArrowRight" ? distance : 0;
                const dy = event.key === "ArrowUp" ? -distance : event.key === "ArrowDown" ? distance : 0;
                setNodes((currentNodes) => {
                    const moveIds = new Set(selectedNodeIdsRef.current);
                    const inheritedMoveIds = new Set<string>();
                    currentNodes.forEach((node) => {
                        if (!moveIds.has(node.id) || node.metadata?.locked) return;
                        node.metadata?.batchChildIds?.forEach((childId) => {
                            moveIds.add(childId);
                            inheritedMoveIds.add(childId);
                        });
                        if (isFrameNode(node)) getFrameChildIds(node.id, currentNodes).forEach((childId) => {
                            moveIds.add(childId);
                            inheritedMoveIds.add(childId);
                        });
                    });
                    currentNodes.forEach((node) => {
                        if (!moveIds.has(node.id) || (node.metadata?.locked && !inheritedMoveIds.has(node.id))) return;
                        node.metadata?.batchChildIds?.forEach((childId) => {
                            moveIds.add(childId);
                            inheritedMoveIds.add(childId);
                        });
                    });
                    const next = currentNodes.map((node) => moveIds.has(node.id) && (!node.metadata?.locked || inheritedMoveIds.has(node.id))
                        ? { ...node, position: { x: node.position.x + dx, y: node.position.y + dy } }
                        : node);
                    const parentFrameIds = new Set(next.filter((node) => moveIds.has(node.id) && node.parentId).map((node) => node.parentId as string));
                    const expanded = expandCanvasFramesToFit(next, parentFrameIds);
                    nodesRef.current = expanded;
                    return expanded;
                });
                return;
            }
            if (event.key === "Delete" || event.key === "Backspace") {
                if (selectedNodeIdsRef.current.size) deleteNodes(new Set(selectedNodeIdsRef.current));
                else if (selectedConnectionId) deleteConnection(selectedConnectionId);
            }
            if (event.key === "Escape") {
                deselectCanvas();
                setInfoNodeId(null);
                setCropNodeId(null);
                setMaskEditNodeId(null);
                setAnnotationNodeId(null);
            }
        };

        const handlePaste = (event: ClipboardEvent) => {
            const target = event.target instanceof Element ? event.target : null;
            if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement || event.target instanceof HTMLButtonElement || target?.closest("[contenteditable='true'],[data-canvas-no-zoom]")) return;
            // 节点标记写入失败或仍在写入时避开旧系统图片，其余情况保持系统内容优先。
            event.preventDefault();
            if (shouldPreferCopiedNodes() && pasteCopiedNodes()) return;
            void (async () => {
                const handled = await pasteSystemClipboard(undefined, event);
                if (!handled) pasteCopiedNodes();
            })();
        };

        window.addEventListener("keydown", handleKeyDown, true);
        window.addEventListener("paste", handlePaste, true);
        return () => {
            window.removeEventListener("keydown", handleKeyDown, true);
            window.removeEventListener("paste", handlePaste, true);
        };
    }, [cancelSelectionBox, copySelectedNodes, deleteConnection, deleteNodes, deselectCanvas, fitCanvasContent, fitCanvasSelection, nodesRef, pasteCopiedNodes, pasteSystemClipboard, redoCanvas, saveCanvasProject, selectedConnectionId, selectedNodeIdsRef, setAnnotationNodeId, setContextMenu, setCropNodeId, setInfoNodeId, setMaskEditNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds, setShortcutRequestNonce, shouldPreferCopiedNodes, undoCanvas, zoomToActualSize]);
}
