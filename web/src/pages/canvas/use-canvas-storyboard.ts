import { useCallback, type Dispatch, type SetStateAction } from "react";
import { App } from "antd";
import { nanoid } from "nanoid";

import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { expandCanvasFramesToFit } from "@/lib/canvas/canvas-frame";
import {
    backendProviderConfig,
    buildGenerationConfig,
    generationTaskMetadata,
    resetGenerationTaskMetadata,
} from "@/lib/canvas/canvas-project-generation";
import {
    cinematicStoryboardColumns,
    buildStoryboardPromptWithContext,
    createCanvasNode,
    createStoryboardRow,
    storyboardRowsFromTask,
} from "@/lib/canvas/canvas-project-domain";
import { projectShotGenerationBlockReason } from "@/lib/canvas/project-shot-contract";
import { buildNodeMentionReferences } from "@/lib/canvas/canvas-resource-references";
import { placeCanvasNodeGroup } from "@/lib/canvas/canvas-layout";
import { inspectGenerationRetry } from "@/lib/generation-retry-safety";
import { resolveChannelProbeReadiness, type ChannelProbeReadiness } from "@/lib/channel-probe-readiness";
import { navigateToSettings } from "@/lib/settings-navigation";
import { createGenerationTask, waitForGenerationTask } from "@/services/api/task-center";
import { configuredModelMatchesCapability, modelOptionName, resolveModelChannel, resolveModelRequestConfig, useConfigStore, useEffectiveConfig, type AiConfig } from "@/stores/use-config-store";
import {
    CanvasNodeType,
    type CanvasConnection,
    type CanvasGenerationBatchMode,
    type CanvasNodeData,
    type StoryboardRow,
} from "@/types/canvas";

type UseCanvasStoryboardOptions = {
    projectId: string;
    manualDelivery?: boolean;
    nodesRef: { current: CanvasNodeData[] };
    connectionsRef: { current: CanvasConnection[] };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    focusGeneratedNodes: (nodeIds: string[]) => void;
    enqueueGenerationBatch: (sourceNodeId: string, mode: CanvasGenerationBatchMode, targets: Array<{ rowId: string; nodeId: string }>) => string | undefined;
};

const NODE_STATUS_IDLE = "idle" as const;
const NODE_STATUS_LOADING = "loading" as const;
const NODE_STATUS_SUCCESS = "success" as const;
const NODE_STATUS_ERROR = "error" as const;
// 同一镜头的衍生产物保持同一行，并按图片、视频、动作板分列，避免流水线运行后节点互相覆盖。
const STORYBOARD_LAYOUT_GAP = 160;
const STORYBOARD_ROW_GAP = 36;

type StoryboardLayoutColumn = "image" | "video" | "action";

function storyboardColumnLeft(scriptNode: CanvasNodeData, column: StoryboardLayoutColumn, nodes: CanvasNodeData[]) {
    const imageLeft = scriptNode.position.x + scriptNode.width + 120;
    if (column === "image") return imageLeft;
    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    const rows = scriptNode.metadata?.storyboard?.rows || [];
    const imageWidth = Math.max(NODE_DEFAULT_SIZE[CanvasNodeType.Image].width, ...rows.flatMap((row) => {
        const node = row.imageNodeId ? nodeById.get(row.imageNodeId) : undefined;
        return node?.type === CanvasNodeType.Image ? [node.width] : [];
    }));
    const videoWidth = Math.max(NODE_DEFAULT_SIZE[CanvasNodeType.Video].width, ...rows.flatMap((row) => {
        const node = row.videoNodeId ? nodeById.get(row.videoNodeId) : undefined;
        return node?.type === CanvasNodeType.Video ? [node.width] : [];
    }));
    const videoLeft = imageLeft + imageWidth + STORYBOARD_LAYOUT_GAP;
    if (column === "video") return videoLeft;
    return videoLeft + videoWidth + STORYBOARD_LAYOUT_GAP;
}

function storyboardRowCenterY(scriptNode: CanvasNodeData, rows: StoryboardRow[], rowId: string, nodeHeight: number) {
    return storyboardRowTop(scriptNode, rows, rowId) + nodeHeight / 2;
}

function storyboardRowTop(scriptNode: CanvasNodeData, rows: StoryboardRow[], rowId: string) {
    const rowIndex = Math.max(0, rows.findIndex((row) => row.id === rowId));
    const rowPitch = Math.max(NODE_DEFAULT_SIZE[CanvasNodeType.Image].height, NODE_DEFAULT_SIZE[CanvasNodeType.Video].height) + STORYBOARD_ROW_GAP;
    return scriptNode.position.y + rowIndex * rowPitch;
}

function shouldRepairLegacyStoryboardPosition(node: CanvasNodeData, scriptNode: CanvasNodeData) {
    const legacyLeft = scriptNode.position.x + scriptNode.width + 120;
    return !node.metadata?.locked && Math.abs(node.position.x - legacyLeft) < 4;
}

export function useCanvasStoryboard({
    projectId,
    manualDelivery = false,
    nodesRef,
    connectionsRef,
    setNodes,
    setConnections,
    focusGeneratedNodes,
    enqueueGenerationBatch,
}: UseCanvasStoryboardOptions) {
    const { message, modal } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);

    const confirmGenerationSubmission = useCallback((count: number, model: string, taskLabel: string) => new Promise<boolean>((resolve) => {
        if (!count) return resolve(false);
        modal.confirm({
            title: `确认提交 ${count} 个${taskLabel}任务`,
            content: `任务数：${count}；模型：${modelOptionName(model) || model}。确认后将提交 ${count} 个外部模型任务，请先核对供应商额度和费用。`,
            okText: "确认生成",
            cancelText: "取消",
            centered: true,
            onOk: () => resolve(true),
            onCancel: () => resolve(false),
        });
    }), [modal]);

    const confirmStoryboardSubmission = useCallback((generationConfig: AiConfig) => new Promise<ChannelProbeReadiness | null>((resolve) => {
        const model = generationConfig.model;
        const requestConfig = resolveModelRequestConfig(generationConfig, model);
        // requestConfig 已把选中的渠道冻结并把模型名还原为裸名称；再次按裸名查找
        // 会在多渠道重名时拿到第一条渠道，造成测活通过但分镜任务落到错误端点。
        const channel = (requestConfig.resolvedChannelId && generationConfig.channels.find((item) => item.id === requestConfig.resolvedChannelId)) || resolveModelChannel(generationConfig, model);
        const modelLabel = modelOptionName(requestConfig.model) || requestConfig.model;
        const readiness = resolveChannelProbeReadiness(channel, modelLabel, requestConfig.interfaceType);
        const repairNotice = manualDelivery
            ? "手动交付会按短文本兼容路径整理分镜；只有结果无法收敛为完整镜头时，才会在本次已确认范围内询问一次修复。"
            : "系统先发起 1 次文本请求；若返回结果无法通过 JSON 结构校验，将自动发起最多 1 次修复请求。";
        const streamNotice = "渠道若明确拒绝流式，系统会停止且不会补发非流式请求。";
        modal.confirm({
            title: "确认生成分镜",
            content: `模型：${modelLabel}。测活状态仅供管理员诊断；即使当前未测活或最近一次测活失败，本次仍会按当前配置发起请求，真实失败会按供应商响应和费用状态处理。${repairNotice}自定义 API Key 可能因此产生最多 2 次供应商调用费用，系统渠道仍按当前任务显示的积分价格计费。${streamNotice}`,
            okText: "确认并允许一次修复",
            cancelText: "取消",
            centered: true,
            onOk: () => resolve(readiness),
            onCancel: () => resolve(null),
        });
    }), [manualDelivery, modal]);

    const rejectInvalidProjectShots = useCallback((rows: StoryboardRow[]) => {
        const invalidRows = rows.filter((row) => projectShotGenerationBlockReason(row));
        if (!invalidRows.length) return false;
        // 降级导入只保证画布可读；补齐数据库契约前不能创建节点或进入计费生成队列。
        message.warning(`有 ${invalidRows.length} 个项目镜头的结构化定义无效，请先回到项目补齐定义并重新导入画布`);
        return true;
    }, [message]);

    const updateScriptRows = useCallback((nodeId: string, updater: (rows: StoryboardRow[]) => StoryboardRow[]) => {
        setNodes((current) => current.map((node) => node.id === nodeId ? {
            ...node,
            metadata: {
                ...node.metadata,
                storyboard: {
                    rows: updater(node.metadata?.storyboard?.rows || []),
                    visibleColumns: node.metadata?.storyboard?.visibleColumns || ["shotNumber", "durationSeconds", "plotDescription", "dialogue"],
                    referenceNodeIds: node.metadata?.storyboard?.referenceNodeIds || [],
                },
            },
        } : node));
    }, [setNodes]);

    const replaceScriptRows = useCallback((nodeId: string, rows: StoryboardRow[]) => {
        const rowIds = new Set(rows.map((row) => `row:${row.id}`));
        setConnections((current) => {
            const nextConnections = current.filter((connection) => {
                const outgoingRowConnection = connection.fromNodeId === nodeId
                    && connection.fromHandleId?.startsWith("row:");
                const incomingRowConnection = connection.toNodeId === nodeId
                    && connection.toHandleId?.startsWith("row:");

                // 分镜行连线随行替换；storyboard:context 等上下文连线必须保留，否则运行后故事和风格节点会被误断开。
                if (outgoingRowConnection) return rowIds.has(connection.fromHandleId || "");
                if (incomingRowConnection) return rowIds.has(connection.toHandleId || "");
                return true;
            });
            connectionsRef.current = nextConnections;
            return nextConnections;
        });
        updateScriptRows(nodeId, () => rows);
    }, [connectionsRef, setConnections, updateScriptRows]);

    const addScriptRow = useCallback((nodeId: string) => {
        updateScriptRows(nodeId, (rows) => [...rows, createStoryboardRow(rows.length + 1)]);
    }, [updateScriptRows]);

    const updateScriptRow = useCallback((nodeId: string, rowId: string, patch: Partial<StoryboardRow>) => {
        updateScriptRows(nodeId, (rows) => rows.map((row) => row.id === rowId ? { ...row, ...patch } : row));
    }, [updateScriptRows]);

    const removeScriptRow = useCallback((nodeId: string, rowId: string) => {
        const node = nodesRef.current.find((item) => item.id === nodeId);
        const rows = (node?.metadata?.storyboard?.rows || []).filter((row) => row.id !== rowId).map((row, index) => ({ ...row, shotNumber: index + 1 }));
        replaceScriptRows(nodeId, rows);
    }, [nodesRef, replaceScriptRows]);

    const generateScriptRows = useCallback(async (nodeId: string, prompt: string) => {
        const scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        if (!scriptNode || !prompt.trim()) return;
        const shotDuration = scriptNode.metadata?.storyboardShotDuration || "auto";
        const shotDurationSeconds = shotDuration === "auto" ? 0 : Number(shotDuration);
        const shotCount = scriptNode.metadata?.storyboardShotCount || "auto";
        const requestedShotCount = shotCount === "auto" ? 0 : Number(shotCount);
        const expandedPrompt = buildStoryboardPromptWithContext(prompt, buildNodeMentionReferences(scriptNode, nodesRef.current, connectionsRef.current));
        const generationConfig = buildGenerationConfig(effectiveConfig, scriptNode, "text");
        if (!isAiConfigReady(generationConfig, generationConfig.model)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        let sourceTaskId: string | undefined;
        let sourceRetryCostUncertain = false;
        if (scriptNode.metadata?.status === NODE_STATUS_ERROR && scriptNode.metadata.taskId) {
            try {
                const inspection = await inspectGenerationRetry(scriptNode.metadata.taskId);
                if (inspection.blockedReason) {
                    message.warning(inspection.blockedReason);
                    return false;
                }
                sourceTaskId = inspection.sourceTask?.id;
                sourceRetryCostUncertain = inspection.costUncertain;
            } catch (error) {
                message.warning(error instanceof Error ? error.message : "无法核对原任务状态，本次未重新生成");
                return false;
            }
        }
        if (sourceTaskId && sourceRetryCostUncertain) {
            const confirmed = await new Promise<boolean>((resolve) => modal.confirm({
                title: "确认已核对原任务费用？",
                content: "原分镜请求的费用状态仍可能待核对。继续会创建新的分镜请求并可能重复计费；旧记录会保留，不需要先去管理员审核。",
                okText: "确认继续",
                cancelText: "取消",
                centered: true,
                onOk: () => resolve(true),
                onCancel: () => resolve(false),
            }));
            if (!confirmed) return false;
        }
        const streamingReadiness = await confirmStoryboardSubmission(generationConfig);
        if (!streamingReadiness) return false;
        const taskProviderConfig = {
            ...backendProviderConfig(generationConfig),
            // 手动交付的分镜与 Agent 一样遵循测活结论；非流式渠道不能先被后台强行发起 SSE。
            ...(manualDelivery && streamingReadiness.state === "non_stream" ? { preferNonStreaming: true } : {}),
        };
        setNodes((current) => current.map((node) => node.id === nodeId ? { ...node, metadata: { ...node.metadata, composerContent: prompt, status: NODE_STATUS_LOADING, taskStage: "正在创建任务", taskProgress: 0, errorDetails: undefined } } : node));
        try {
            const task = await createGenerationTask({
                projectId,
                type: "agent_storyboard_rows",
                operation: "storyboard_rows",
                prompt: expandedPrompt,
                model: generationConfig.model,
                sourceTaskId,
                confirmNewProviderRequest: Boolean(sourceTaskId),
                input: {
                    canvasSnapshot: { nodes: nodesRef.current, connections: connectionsRef.current },
                    requirements: "输出可直接编辑并用于批量生成图片和视频的分镜表。",
                    shotDurationSeconds,
                    shotCount: requestedShotCount,
                    manualDelivery,
                    channelProbeTaskId: streamingReadiness.probeTaskId,
                    allowPaidStructureRepair: true,
                    config: taskProviderConfig,
                    metadata: { nodeId },
                },
            });
            setNodes((current) => current.map((node) => node.id === nodeId ? { ...node, metadata: { ...node.metadata, ...generationTaskMetadata(task), status: NODE_STATUS_LOADING } } : node));
            const completed = await waitForGenerationTask(task.id, {
                initialTask: task,
                onTaskUpdate: (next) => setNodes((current) => current.map((node) => node.id === nodeId ? { ...node, metadata: { ...node.metadata, ...generationTaskMetadata(next), status: NODE_STATUS_LOADING } } : node)),
            });
            const result = storyboardRowsFromTask(completed);
            setNodes((current) => current.map((node) => node.id === nodeId ? {
                ...node,
                title: result.title || node.title,
                metadata: {
                    ...node.metadata,
                    status: NODE_STATUS_SUCCESS,
                    errorDetails: undefined,
                    ...generationTaskMetadata(completed),
                    storyboard: {
                        rows: result.rows,
                        visibleColumns: cinematicStoryboardColumns(node.metadata?.storyboard?.visibleColumns),
                        referenceNodeIds: node.metadata?.storyboard?.referenceNodeIds || [],
                    },
                },
            } : node));
            message.success(result.structureRepairUsed ? `已生成 ${result.rows.length} 个镜头；首轮结构异常，已使用一次授权修复` : `已生成 ${result.rows.length} 个镜头`);
            return true;
        } catch (error) {
            const details = error instanceof Error ? error.message : "脚本生成失败";
            setNodes((current) => current.map((node) => node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_ERROR, errorDetails: details } } : node));
            message.error(details);
            return false;
        }
    }, [connectionsRef, confirmStoryboardSubmission, effectiveConfig, isAiConfigReady, manualDelivery, message, modal, nodesRef, projectId, setNodes]);

    // 批次真正提交前只保存无密钥的渠道模型标识，刷新后可继续沿用原渠道，密钥仍由当前可信配置提供。
    const ensureScriptImageNodes = useCallback((nodeId: string, rowIds: string[], model?: string) => {
        const scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const allRows = scriptNode?.metadata?.storyboard?.rows || [];
        const rows = allRows.filter((row) => rowIds.includes(row.id));
        if (!scriptNode || !rows.length) return [];
        const imageSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
        const startLeft = storyboardColumnLeft(scriptNode, "image", nodesRef.current);
        const layoutNodeIds = new Set(allRows.map((row) => row.imageNodeId).filter((id): id is string => Boolean(id)));
        const layoutOffset = storyboardGroupOffset(scriptNode, allRows, nodesRef.current, (row) => row.imageNodeId, startLeft);
        const nextNodes = [...nodesRef.current];
        const nextConnections = [...connectionsRef.current];
        const targets: Array<{ row: StoryboardRow; node: CanvasNodeData; prompt: string }> = [];
        const createdNodes: CanvasNodeData[] = [];
        rows.forEach((row) => {
            const prompt = (row.imageGenerationPrompt || row.plotDescription).trim();
            const existing = row.imageNodeId ? nextNodes.find((node) => node.id === row.imageNodeId && node.type === CanvasNodeType.Image) : undefined;
            const existingMetadata = existing?.metadata?.content ? existing.metadata : resetGenerationTaskMetadata(existing?.metadata);
            const position = { x: startLeft + layoutOffset.x, y: storyboardRowTop(scriptNode, allRows, row.id) + layoutOffset.y };
            const imageNode = existing
                ? { ...existing, metadata: { ...existingMetadata, prompt, workflowKind: "shot" as const, workflowTitle: `镜头 ${row.shotNumber} 分镜图`, shotIndex: row.shotNumber, ...(model ? { model } : {}) } }
                : createCanvasNode(CanvasNodeType.Image, { x: position.x + imageSpec.width / 2, y: position.y + imageSpec.height / 2 }, { prompt, workflowKind: "shot", workflowTitle: `镜头 ${row.shotNumber} 分镜图`, shotIndex: row.shotNumber, ...(model ? { model } : {}), status: NODE_STATUS_IDLE });
            if (!existing) {
                imageNode.title = `镜头 ${row.shotNumber} · 分镜图`;
                nextNodes.push(imageNode);
                createdNodes.push(imageNode);
                nextConnections.push({ id: nanoid(), fromNodeId: scriptNode.id, toNodeId: imageNode.id, fromHandleId: `row:${row.id}` });
            } else {
                const existingIndex = nextNodes.findIndex((node) => node.id === existing.id);
                nextNodes[existingIndex] = imageNode;
            }
            const referenceIds = new Set([
                ...(scriptNode.metadata?.storyboard?.referenceNodeIds || []),
                ...(row.referenceNodeIds || []),
                ...nextConnections.filter((connection) => connection.toNodeId === scriptNode.id && connection.toHandleId === `row:${row.id}`).map((connection) => connection.fromNodeId),
            ]);
            referenceIds.forEach((referenceId) => {
                if (referenceId !== imageNode.id && !nextConnections.some((connection) => connection.fromNodeId === referenceId && connection.toNodeId === imageNode.id)) nextConnections.push({ id: nanoid(), fromNodeId: referenceId, toNodeId: imageNode.id });
            });
            targets.push({ row, node: imageNode, prompt });
        });
        placeNewStoryboardNodes(nodesRef.current, createdNodes, layoutNodeIds, scriptNode);
        const imageNodeByRowId = new Map(targets.map((target) => [target.row.id, target.node.id]));
        const scriptIndex = nextNodes.findIndex((node) => node.id === scriptNode.id);
        nextNodes[scriptIndex] = {
            ...scriptNode,
            metadata: {
                ...scriptNode.metadata,
                storyboard: {
                    rows: (scriptNode.metadata?.storyboard?.rows || []).map((row) => ({ ...row, imageNodeId: imageNodeByRowId.get(row.id) || row.imageNodeId })),
                    visibleColumns: scriptNode.metadata?.storyboard?.visibleColumns || ["shotNumber", "durationSeconds", "plotDescription", "dialogue"],
                    referenceNodeIds: scriptNode.metadata?.storyboard?.referenceNodeIds || [],
                },
            },
        };
        const finalizedNodes = expandCanvasFramesToFit(nextNodes, new Set(targets.map((target) => target.node.parentId).filter((id): id is string => Boolean(id))));
        nodesRef.current = finalizedNodes;
        connectionsRef.current = nextConnections;
        setNodes(finalizedNodes);
        setConnections(nextConnections);
        return targets;
    }, [connectionsRef, nodesRef, setConnections, setNodes]);

    const createScriptImageNodes = useCallback((nodeId: string, rowIds?: string[]) => {
        const scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const rows = scriptNode?.metadata?.storyboard?.rows || [];
        const selectedRows = rowIds?.length ? rows.filter((row) => rowIds.includes(row.id)) : rows;
        if (!scriptNode || !selectedRows.length) return;
        if (rejectInvalidProjectShots(selectedRows)) return;
        const missing = selectedRows.filter((row) => !(row.imageGenerationPrompt || row.plotDescription).trim());
        if (missing.length) return message.warning(`有 ${missing.length} 个镜头缺少画面描述或图片提示词`);
        const createdCount = selectedRows.filter((row) => !row.imageNodeId || !nodesRef.current.some((node) => node.id === row.imageNodeId && node.type === CanvasNodeType.Image)).length;
        const targets = ensureScriptImageNodes(nodeId, selectedRows.map((row) => row.id));
        focusGeneratedNodes(targets.map((target) => target.node.id));
        message.success(createdCount ? `已创建 ${createdCount} 个图片节点` : "已同步现有图片节点的提示词");
    }, [ensureScriptImageNodes, focusGeneratedNodes, message, nodesRef, rejectInvalidProjectShots]);

    const generateScriptImages = useCallback(async (nodeId: string, rowIds: string[]) => {
        const scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const rows = (scriptNode?.metadata?.storyboard?.rows || []).filter((row) => rowIds.includes(row.id));
        if (!scriptNode || !rows.length) return;
        if (rejectInvalidProjectShots(rows)) return;
        const missing = rows.filter((row) => !(row.imageGenerationPrompt || row.plotDescription).trim());
        if (missing.length) return message.warning(`有 ${missing.length} 个镜头缺少画面描述或图片提示词`);
        const imageModel = effectiveConfig.imageModel;
        if (!imageModel || !configuredModelMatchesCapability(effectiveConfig, imageModel, "image")) {
            message.warning("当前没有配置可用的图片模型；文本模型可以生成分镜，但不能直接生成分镜图。请先在设置中选择图片模型，或复制提示词到网页工作台");
            return;
        }
        if (!isAiConfigReady(effectiveConfig, imageModel)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        const activeNodeIds = activeGenerationBatchNodeIds(scriptNode, "storyboard_image");
        const targetRows = rows.filter((row) => {
            const imageNode = row.imageNodeId ? nodesRef.current.find((node) => node.id === row.imageNodeId && node.type === CanvasNodeType.Image) : undefined;
            return !imageNode?.metadata?.content && (!imageNode || !activeNodeIds.has(imageNode.id));
        });
        if (!targetRows.length) return message.info("所选分镜图已生成或正在生成");
        if (!await confirmGenerationSubmission(targetRows.length, imageModel, "图片生成")) return;
        const targets = ensureScriptImageNodes(nodeId, targetRows.map((row) => row.id), imageModel);
        focusGeneratedNodes(targets.map((target) => target.node.id));
        if (enqueueGenerationBatch(nodeId, "storyboard_image", targets.map((target) => ({ rowId: target.row.id, nodeId: target.node.id })))) message.success("分镜图已加入生成队列");
    }, [effectiveConfig, enqueueGenerationBatch, ensureScriptImageNodes, confirmGenerationSubmission, focusGeneratedNodes, isAiConfigReady, message, nodesRef, rejectInvalidProjectShots]);

    const createScriptVideoNodes = useCallback((nodeId: string, silent = false, rowIds?: string[], model?: string) => {
        if (manualDelivery) {
            message.info("手动交付模式不会创建视频节点；请复制视频提示词后到网页工作台逐镜生成");
            return;
        }
        const scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const allRows = scriptNode?.metadata?.storyboard?.rows || [];
        const rows = rowIds?.length ? allRows.filter((row) => rowIds.includes(row.id)) : allRows;
        if (!scriptNode || !rows.length) return;
        if (rejectInvalidProjectShots(rows)) return;
        const videoSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Video];
        const startLeft = storyboardColumnLeft(scriptNode, "video", nodesRef.current);
        const layoutNodeIds = new Set(allRows.map((row) => row.videoNodeId).filter((id): id is string => Boolean(id)));
        const layoutOffset = storyboardGroupOffset(scriptNode, allRows, nodesRef.current, (row) => row.videoNodeId, startLeft, true);
        const nextNodes = [...nodesRef.current];
        const nextConnections = [...connectionsRef.current];
        const videoNodeByRowId = new Map<string, string>();
        const createdNodes: CanvasNodeData[] = [];
        let createdCount = 0;
        rows.forEach((row) => {
            const prompt = (row.videoMotionPrompt || row.plotDescription).trim();
            const existingIndex = row.videoNodeId ? nextNodes.findIndex((node) => node.id === row.videoNodeId && node.type === CanvasNodeType.Video) : -1;
            if (existingIndex >= 0) {
                const existing = nextNodes[existingIndex];
                const existingMetadata = existing.metadata?.content ? existing.metadata : resetGenerationTaskMetadata(existing.metadata);
                const position = { x: startLeft + layoutOffset.x, y: storyboardRowTop(scriptNode, allRows, row.id) + layoutOffset.y };
                nextNodes[existingIndex] = { ...existing, ...(shouldRepairLegacyStoryboardPosition(existing, scriptNode) ? { position } : {}), metadata: { ...existingMetadata, prompt, composerContent: prompt, seconds: String(row.durationSeconds), shotIndex: row.shotNumber, workflowKind: "shot", workflowTitle: `镜头 ${row.shotNumber} 视频`, generationMode: "video", videoEditOperation: existing.metadata?.videoEditOperation || "text_to_video", ...(model ? { model } : {}) } };
                videoNodeByRowId.set(row.id, existing.id);
                return;
            }
            const videoNode = createCanvasNode(CanvasNodeType.Video, { x: startLeft + layoutOffset.x + videoSpec.width / 2, y: storyboardRowCenterY(scriptNode, allRows, row.id, videoSpec.height) + layoutOffset.y }, { prompt, composerContent: prompt, workflowKind: "shot", workflowTitle: `镜头 ${row.shotNumber} 视频`, shotIndex: row.shotNumber, generationMode: "video", videoEditOperation: "text_to_video", ...(model ? { model } : {}), status: NODE_STATUS_IDLE, seconds: String(row.durationSeconds) });
            videoNode.title = `镜头 ${row.shotNumber} · 视频`;
            nextNodes.push(videoNode);
            createdNodes.push(videoNode);
            nextConnections.push({ id: nanoid(), fromNodeId: scriptNode.id, toNodeId: videoNode.id, fromHandleId: `row:${row.id}` });
            videoNodeByRowId.set(row.id, videoNode.id);
            createdCount += 1;
        });
        placeNewStoryboardNodes(nodesRef.current, createdNodes, layoutNodeIds, scriptNode);
        const scriptIndex = nextNodes.findIndex((node) => node.id === scriptNode.id);
        nextNodes[scriptIndex] = {
            ...scriptNode,
            metadata: {
                ...scriptNode.metadata,
                storyboard: {
                    rows: allRows.map((row) => ({ ...row, videoNodeId: videoNodeByRowId.get(row.id) || row.videoNodeId })),
                    visibleColumns: scriptNode.metadata?.storyboard?.visibleColumns || ["shotNumber", "durationSeconds", "plotDescription", "dialogue"],
                    referenceNodeIds: scriptNode.metadata?.storyboard?.referenceNodeIds || [],
                },
            },
        };
        const finalizedNodes = expandCanvasFramesToFit(nextNodes, new Set([...videoNodeByRowId.values()].map((id) => nextNodes.find((node) => node.id === id)?.parentId).filter((id): id is string => Boolean(id))));
        nodesRef.current = finalizedNodes;
        connectionsRef.current = nextConnections;
        setNodes(finalizedNodes);
        setConnections(nextConnections);
        focusGeneratedNodes(rows.map((row) => videoNodeByRowId.get(row.id)).filter((id): id is string => Boolean(id)));
        if (!silent) message.success(createdCount ? `已创建 ${createdCount} 个视频节点` : "已同步现有视频节点的提示词");
    }, [connectionsRef, focusGeneratedNodes, manualDelivery, message, nodesRef, rejectInvalidProjectShots, setConnections, setNodes]);

    const createAndGenerateScriptVideos = useCallback(async (nodeId: string) => {
        if (manualDelivery) {
            message.info("手动交付模式不会提交视频任务；请先复制视频提示词，再到网页工作台逐镜生成");
            return;
        }
        const videoModel = effectiveConfig.videoModel || effectiveConfig.model;
        let scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const rows = scriptNode?.metadata?.storyboard?.rows || [];
        const describedRows = rows.filter((row) => Boolean((row.videoMotionPrompt || row.plotDescription).trim()));
        const activeNodeIds = scriptNode ? activeGenerationBatchNodeIds(scriptNode, "storyboard_video") : new Set<string>();
        const targetRows = describedRows.filter((row) => {
            const videoNode = row.videoNodeId ? nodesRef.current.find((node) => node.id === row.videoNodeId && node.type === CanvasNodeType.Video) : undefined;
            return !videoNode?.metadata?.content && (!videoNode || !activeNodeIds.has(videoNode.id));
        });
        if (!targetRows.length) {
            if (describedRows.some((row) => row.videoNodeId && nodesRef.current.some((node) => node.id === row.videoNodeId && Boolean(node.metadata?.content)))) message.info("镜头视频已存在");
            else message.warning("请先补充镜头画面描述");
            return;
        }
        if (rejectInvalidProjectShots(targetRows)) return;
        if (!isAiConfigReady(effectiveConfig, videoModel)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        if (!await confirmGenerationSubmission(targetRows.length, videoModel, "视频生成")) return;
        createScriptVideoNodes(nodeId, true, targetRows.map((row) => row.id), videoModel);
        scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const targetRowIds = new Set(targetRows.map((row) => row.id));
        const targets = rows.flatMap((row) => {
            if (!targetRowIds.has(row.id)) return [];
            const currentRow = scriptNode?.metadata?.storyboard?.rows.find((item) => item.id === row.id) || row;
            const videoNode = currentRow.videoNodeId ? nodesRef.current.find((node) => node.id === currentRow.videoNodeId && node.type === CanvasNodeType.Video) : undefined;
            if (!videoNode || videoNode.metadata?.content) return [];
            const prompt = (currentRow.videoMotionPrompt || currentRow.plotDescription).trim();
            if (!prompt) return [];
            const imageNode = currentRow.imageNodeId ? nodesRef.current.find((node) => node.id === currentRow.imageNodeId && node.type === CanvasNodeType.Image && node.metadata?.content) : undefined;
            return [{ row: currentRow, videoNode, imageNode, prompt }];
        });
        const targetById = new Map(targets.map((target) => [target.videoNode.id, target]));
        const nextNodes = nodesRef.current.map((node) => {
            if (node.id === nodeId) return node;
            const target = targetById.get(node.id);
            return target ? { ...node, metadata: { ...node.metadata, prompt: target.prompt, composerContent: target.prompt, generationMode: "video" as const, videoEditOperation: target.imageNode ? "image_to_video" as const : "text_to_video" as const } } : node;
        });
        const nextConnections = [...connectionsRef.current];
        targets.forEach((target) => {
            const imageNode = target.imageNode;
            if (imageNode && !nextConnections.some((connection) => connection.fromNodeId === imageNode.id && connection.toNodeId === target.videoNode.id)) nextConnections.push({ id: nanoid(), fromNodeId: imageNode.id, toNodeId: target.videoNode.id });
        });
        nodesRef.current = nextNodes;
        connectionsRef.current = nextConnections;
        setNodes(nextNodes);
        setConnections(nextConnections);
        focusGeneratedNodes(targets.map((target) => target.videoNode.id));
        if (enqueueGenerationBatch(nodeId, "storyboard_video", targets.map((target) => ({ rowId: target.row.id, nodeId: target.videoNode.id })))) message.success("镜头视频已加入生成队列");
    }, [connectionsRef, confirmGenerationSubmission, createScriptVideoNodes, effectiveConfig, enqueueGenerationBatch, focusGeneratedNodes, isAiConfigReady, manualDelivery, message, nodesRef, rejectInvalidProjectShots, setConnections, setNodes]);

    const createScriptActionBoards = useCallback(async (nodeId: string) => {
        const scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const rows = scriptNode?.metadata?.storyboard?.rows || [];
        if (!scriptNode || !rows.length) return;
        if (rejectInvalidProjectShots(rows)) return;
        const imageModel = effectiveConfig.imageModel;
        if (!imageModel || !configuredModelMatchesCapability(effectiveConfig, imageModel, "image")) {
            message.warning("当前没有配置可用的图片模型；文本模型可以生成分镜，但不能直接生成动作板。请先在设置中选择图片模型，或复制提示词到网页工作台");
            return;
        }
        if (!isAiConfigReady(effectiveConfig, imageModel)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        const actionBoardRows = rows.filter((row) => !nodesRef.current.some((node) => node.type === CanvasNodeType.Image && node.metadata?.workflowKind === "action_board" && node.metadata.shotIndex === row.shotNumber && Boolean(node.metadata.content)));
        if (!actionBoardRows.length) {
            message.info("动作拆分板已存在");
            return;
        }
        if (!await confirmGenerationSubmission(actionBoardRows.length, imageModel, "动作板生成")) return;
        const imageSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
        const startLeft = storyboardColumnLeft(scriptNode, "action", nodesRef.current);
        const actionNodeByShot = new Map(nodesRef.current.filter((node) => node.type === CanvasNodeType.Image && node.metadata?.workflowKind === "action_board").map((node) => [node.metadata?.shotIndex, node]));
        const layoutNodeIds = new Set(rows.map((row) => actionNodeByShot.get(row.shotNumber)?.id).filter((id): id is string => Boolean(id)));
        const layoutOffset = storyboardGroupOffset(scriptNode, rows, nodesRef.current, (row) => actionNodeByShot.get(row.shotNumber)?.id, startLeft, true);
        const nextNodes = [...nodesRef.current];
        const nextConnections = [...connectionsRef.current];
        const targets: Array<{ row: StoryboardRow; node: CanvasNodeData; prompt: string }> = [];
        const createdNodes: CanvasNodeData[] = [];
        actionBoardRows.forEach((row) => {
            const prompt = [
                "生成一张电影动作拆分 12 宫格参考图，严格 3 列 4 行，12 个格子清晰分隔，保持同一角色、服装、场景和光线连续。",
                `镜头 ${row.shotNumber}：${row.plotDescription || row.videoMotionPrompt || "根据镜头剧情补全动作"}`,
                row.characters.length ? `角色：${row.characters.map((item) => item.characterName).join("、")}` : "",
                "按时间顺序展示动作起势、推进、转折、落点和结束姿态，不要添加文字、边框标题或额外画面。",
            ].filter(Boolean).join("\n");
            const existingIndex = nextNodes.findIndex((node) => node.type === CanvasNodeType.Image && node.metadata?.workflowKind === "action_board" && node.metadata.shotIndex === row.shotNumber);
            if (existingIndex >= 0 && nextNodes[existingIndex].metadata?.content) return;
            // 动作板也会进入延迟批次；把确认时的渠道模型前缀写入节点，
            // 防止用户在排队期间切换默认模型后，调度器按另一条同名渠道出站。
            const imageNode = existingIndex >= 0
                ? { ...nextNodes[existingIndex], ...(shouldRepairLegacyStoryboardPosition(nextNodes[existingIndex], scriptNode) ? { position: { x: startLeft + layoutOffset.x, y: storyboardRowTop(scriptNode, rows, row.id) + layoutOffset.y } } : {}), metadata: { ...resetGenerationTaskMetadata(nextNodes[existingIndex].metadata), prompt, model: imageModel } }
                : createCanvasNode(CanvasNodeType.Image, { x: startLeft + layoutOffset.x + imageSpec.width / 2, y: storyboardRowCenterY(scriptNode, rows, row.id, imageSpec.height) + layoutOffset.y }, { prompt, workflowKind: "action_board", workflowTitle: `镜头 ${row.shotNumber} 动作板`, shotIndex: row.shotNumber, actionBoardRows: 4, actionBoardColumns: 3, model: imageModel, status: NODE_STATUS_IDLE });
            imageNode.title = `镜头 ${row.shotNumber} · 动作板`;
            if (existingIndex >= 0) nextNodes[existingIndex] = imageNode;
            else {
                nextNodes.push(imageNode);
                createdNodes.push(imageNode);
                nextConnections.push({ id: nanoid(), fromNodeId: scriptNode.id, toNodeId: imageNode.id, fromHandleId: `row:${row.id}` });
            }
            targets.push({ row, node: imageNode, prompt });
        });
        placeNewStoryboardNodes(nodesRef.current, createdNodes, layoutNodeIds, scriptNode);
        const finalizedNodes = expandCanvasFramesToFit(nextNodes, new Set(targets.map((target) => target.node.parentId).filter((id): id is string => Boolean(id))));
        nodesRef.current = finalizedNodes;
        connectionsRef.current = nextConnections;
        setNodes(finalizedNodes);
        setConnections(nextConnections);
        focusGeneratedNodes(targets.map((target) => target.node.id));
        if (enqueueGenerationBatch(nodeId, "action_board", targets.map((target) => ({ rowId: target.row.id, nodeId: target.node.id })))) message.success("动作拆分板已加入生成队列");
    }, [connectionsRef, confirmGenerationSubmission, effectiveConfig, enqueueGenerationBatch, focusGeneratedNodes, isAiConfigReady, message, nodesRef, rejectInvalidProjectShots, setConnections, setNodes]);

    const generateScriptVideos = useCallback(async (nodeId: string, rowIds: string[]) => {
        if (manualDelivery) {
            message.info("手动交付模式不会提交视频任务；请先复制视频提示词，再到网页工作台逐镜生成");
            return;
        }
        let scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        const rows = (scriptNode?.metadata?.storyboard?.rows || []).filter((row) => rowIds.includes(row.id));
        if (!scriptNode || !rows.length) return;
        if (rejectInvalidProjectShots(rows)) return;
        const readyRows = rows.filter((row) => row.imageNodeId && nodesRef.current.some((node) => node.id === row.imageNodeId && node.type === CanvasNodeType.Image && node.metadata?.content));
        if (!readyRows.length) return message.warning("请先生成选中镜头的分镜图");
        if (readyRows.length !== rows.length) message.warning(`${rows.length - readyRows.length} 个镜头没有可用分镜图，已跳过`);
        const videoModel = effectiveConfig.videoModel || effectiveConfig.model;
        const activeNodeIds = activeGenerationBatchNodeIds(scriptNode, "storyboard_video");
        const targetRows = readyRows.filter((row) => {
            const videoNode = row.videoNodeId ? nodesRef.current.find((node) => node.id === row.videoNodeId && node.type === CanvasNodeType.Video) : undefined;
            return !videoNode?.metadata?.content && (!videoNode || !activeNodeIds.has(videoNode.id));
        });
        if (!targetRows.length) return message.info("所选镜头视频已生成或正在生成");
        if (!isAiConfigReady(effectiveConfig, videoModel)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        if (!await confirmGenerationSubmission(targetRows.length, videoModel, "视频生成")) return;
        createScriptVideoNodes(nodeId, true, targetRows.map((row) => row.id), videoModel);
        scriptNode = nodesRef.current.find((node) => node.id === nodeId && node.type === CanvasNodeType.Script);
        if (!scriptNode) return;
        const currentScriptNode = scriptNode;
        const videoSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Video];
        const allRows = currentScriptNode.metadata?.storyboard?.rows || [];
        const currentRows = targetRows.map((row) => allRows.find((item) => item.id === row.id) || row);
        const startLeft = storyboardColumnLeft(currentScriptNode, "video", nodesRef.current);
        const nextNodes = [...nodesRef.current];
        const nextConnections = [...connectionsRef.current];
        const targets: Array<{ row: StoryboardRow; node: CanvasNodeData; prompt: string }> = [];
        currentRows.forEach((row) => {
            const prompt = (row.videoMotionPrompt || row.plotDescription).trim();
            const existing = row.videoNodeId ? nextNodes.find((node) => node.id === row.videoNodeId && node.type === CanvasNodeType.Video) : undefined;
            const existingMetadata = existing?.metadata?.content ? existing.metadata : resetGenerationTaskMetadata(existing?.metadata);
            const position = { x: startLeft, y: storyboardRowTop(currentScriptNode, allRows, row.id) };
            const videoNode = existing
                ? { ...existing, ...(shouldRepairLegacyStoryboardPosition(existing, currentScriptNode) ? { position } : {}), metadata: { ...existingMetadata, prompt, composerContent: prompt, workflowKind: "shot" as const, workflowTitle: `镜头 ${row.shotNumber} 视频`, shotIndex: row.shotNumber, generationMode: "video" as const, videoEditOperation: "image_to_video" as const, seconds: String(row.durationSeconds) } }
                : createCanvasNode(CanvasNodeType.Video, { x: startLeft + videoSpec.width / 2, y: position.y + videoSpec.height / 2 }, { prompt, workflowKind: "shot", workflowTitle: `镜头 ${row.shotNumber} 视频`, shotIndex: row.shotNumber, generationMode: "video", videoEditOperation: "image_to_video", status: NODE_STATUS_IDLE, seconds: String(row.durationSeconds) });
            if (!existing) {
                videoNode.title = `镜头 ${row.shotNumber} · 视频`;
                nextNodes.push(videoNode);
                nextConnections.push({ id: nanoid(), fromNodeId: currentScriptNode.id, toNodeId: videoNode.id, fromHandleId: `row:${row.id}` });
            } else {
                const existingIndex = nextNodes.findIndex((node) => node.id === existing.id);
                nextNodes[existingIndex] = videoNode;
            }
            if (!nextConnections.some((connection) => connection.fromNodeId === row.imageNodeId && connection.toNodeId === videoNode.id)) nextConnections.push({ id: nanoid(), fromNodeId: row.imageNodeId!, toNodeId: videoNode.id });
            targets.push({ row, node: videoNode, prompt });
        });
        const finalizedNodes = expandCanvasFramesToFit(nextNodes, new Set(targets.map((target) => target.node.parentId).filter((id): id is string => Boolean(id))));
        nodesRef.current = finalizedNodes;
        connectionsRef.current = nextConnections;
        setNodes(finalizedNodes);
        setConnections(nextConnections);
        focusGeneratedNodes(targets.map((target) => target.node.id));
        if (enqueueGenerationBatch(nodeId, "storyboard_video", targets.map((target) => ({ rowId: target.row.id, nodeId: target.node.id })))) message.success("镜头视频已加入生成队列");
    }, [connectionsRef, confirmGenerationSubmission, createScriptVideoNodes, effectiveConfig, enqueueGenerationBatch, focusGeneratedNodes, isAiConfigReady, manualDelivery, message, nodesRef, rejectInvalidProjectShots, setConnections, setNodes]);

    return {
        addScriptRow,
        createAndGenerateScriptVideos,
        createScriptActionBoards,
        createScriptImageNodes,
        createScriptVideoNodes,
        generateScriptImages,
        generateScriptRows,
        generateScriptVideos,
        removeScriptRow,
        replaceScriptRows,
        updateScriptRow,
        updateScriptRows,
    };
}

function activeGenerationBatchNodeIds(node: CanvasNodeData, mode: CanvasGenerationBatchMode) {
    return new Set((node.metadata?.generationBatches || [])
        .filter((batch) => batch.mode === mode)
        .flatMap((batch) => batch.items
            .filter((item) => item.status === "waiting" || item.status === "submitting" || item.status === "queued" || item.status === "running")
            .map((item) => item.nodeId)));
}

function placeNewStoryboardNodes(existingNodes: CanvasNodeData[], createdNodes: CanvasNodeData[], layoutNodeIds: Set<string>, sourceNode: CanvasNodeData) {
    const obstacles = existingNodes.filter((node) => !layoutNodeIds.has(node.id));
    const placedById = new Map(placeCanvasNodeGroup(obstacles, createdNodes, 32, sourceNode).map((node) => [node.id, node]));
    createdNodes.forEach((node) => {
        const placed = placedById.get(node.id);
        if (placed) Object.assign(node, placed);
    });
}

function storyboardGroupOffset(
    scriptNode: CanvasNodeData,
    rows: StoryboardRow[],
    nodes: CanvasNodeData[],
    nodeIdForRow: (row: StoryboardRow) => string | undefined,
    expectedLeft: number,
    ignoreLegacy = false,
) {
    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    const offsets = rows.flatMap((row) => {
        const nodeId = nodeIdForRow(row);
        const node = nodeId ? nodeById.get(nodeId) : undefined;
        if (!node || (ignoreLegacy && shouldRepairLegacyStoryboardPosition(node, scriptNode))) return [];
        return [{ x: node.position.x - expectedLeft, y: node.position.y - storyboardRowTop(scriptNode, rows, row.id) }];
    });
    if (!offsets.length) return { x: 0, y: 0 };

    const clusters = new Map<string, { count: number; x: number; y: number }>();
    offsets.forEach((offset) => {
        const key = `${Math.round(offset.x / 8)}:${Math.round(offset.y / 8)}`;
        const cluster = clusters.get(key) || { count: 0, x: 0, y: 0 };
        cluster.count += 1;
        cluster.x += offset.x;
        cluster.y += offset.y;
        clusters.set(key, cluster);
    });
    const cluster = [...clusters.values()].sort((left, right) => right.count - left.count || Math.hypot(left.x / left.count, left.y / left.count) - Math.hypot(right.x / right.count, right.y / right.count))[0];
    return { x: cluster.x / cluster.count, y: cluster.y / cluster.count };
}
