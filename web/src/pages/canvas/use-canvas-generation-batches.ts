import { useCallback, useEffect, useRef, type Dispatch, type SetStateAction } from "react";
import { App } from "antd";
import { nanoid } from "nanoid";

import { generationBatchStatus, isGenerationCostUncertainError } from "@/lib/canvas/canvas-generation-batch";
import { buildGenerationConfig, generationTaskMetadata, resetGenerationTaskMetadata } from "@/lib/canvas/canvas-project-generation";
import { unchangedModeratedPrompt } from "@/lib/generation-error";
import { inspectGenerationRetry } from "@/lib/generation-retry-safety";
import { abortGenerationTaskTracking, cancelGenerationTask, listGenerationTasks } from "@/services/api/task-center";
import { useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";
import type { CanvasGenerationBatch, CanvasGenerationBatchItem, CanvasGenerationBatchMode, CanvasNodeData } from "@/types/canvas";

import type { CanvasNodeGenerationOptions } from "./use-canvas-generation-executor";
import type { CanvasNodeGenerationMode } from "@/components/canvas/canvas-node-prompt-panel";

const SCHEDULER_INTERVAL_MS = 2_000;
const MAX_BATCH_HISTORY = 20;

type BatchTarget = Pick<CanvasGenerationBatchItem, "rowId" | "nodeId">;

type UseCanvasGenerationBatchesOptions = {
    projectId: string;
    projectLoaded: boolean;
    nodes: CanvasNodeData[];
    nodesRef: { current: CanvasNodeData[] };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    handleGenerateNode: (nodeId: string, mode: CanvasNodeGenerationMode, prompt: string, options?: CanvasNodeGenerationOptions) => Promise<void>;
};

export function useCanvasGenerationBatches({ projectId, projectLoaded, nodes, nodesRef, setNodes, handleGenerateNode }: UseCanvasGenerationBatchesOptions) {
    const { message, modal } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);
    const activeTaskLimit = useUserStore((state) => state.runtimeLimits.activeTaskLimit);
    const schedulingRef = useRef(false);
    const controllersRef = useRef(new Map<string, AbortController>());

    const updateBatch = useCallback((sourceNodeId: string, batchId: string, updater: (batch: CanvasGenerationBatch) => CanvasGenerationBatch) => {
        setNodes((current) => {
            let changed = false;
            const next = current.map((node) => {
                if (node.id !== sourceNodeId || !node.metadata?.generationBatches?.length) return node;
                const batches = node.metadata.generationBatches.map((batch) => {
                    if (batch.id !== batchId) return batch;
                    const updated = updater(batch);
                    if (updated !== batch) changed = true;
                    return updated;
                });
                return changed ? { ...node, metadata: { ...node.metadata, generationBatches: batches } } : node;
            });
            return changed ? next : current;
        });
    }, [setNodes]);

    const enqueueGenerationBatch = useCallback((sourceNodeId: string, mode: CanvasGenerationBatchMode, targets: BatchTarget[]) => {
        const sourceNode = nodesRef.current.find((node) => node.id === sourceNodeId);
        if (!sourceNode || !targets.length) return;
        const activeNodeIds = new Set((sourceNode.metadata?.generationBatches || []).flatMap((batch) =>
            batch.items.filter((item) => ["waiting", "submitting", "queued", "running"].includes(item.status)).map((item) => item.nodeId),
        ));
        const availableTargets = targets.filter((target) => !activeNodeIds.has(target.nodeId));
        if (!availableTargets.length) {
            message.info("所选镜头已在生成批次中");
            return;
        }
        const now = new Date().toISOString();
        const batch: CanvasGenerationBatch = {
            id: nanoid(),
            projectId,
            sourceNodeId,
            mode,
            status: "queued",
            items: availableTargets.map((target) => ({ id: nanoid(), ...target, status: "waiting", retryCount: 0 })),
            createdAt: now,
            updatedAt: now,
        };
        setNodes((current) => current.map((node) => node.id === sourceNodeId ? {
            ...node,
            metadata: {
                ...node.metadata,
                generationBatches: [...(node.metadata?.generationBatches || []), batch].slice(-MAX_BATCH_HISTORY),
            },
        } : node));
        return batch.id;
    }, [message, nodesRef, projectId, setNodes]);

    const reconcileBatches = useCallback(() => {
        setNodes((current) => {
            const nodeById = new Map(current.map((node) => [node.id, node]));
            const uncertainNodeIds = new Set<string>();
            let changed = false;
            const nextNodes = current.map((sourceNode) => {
                const batches = sourceNode.metadata?.generationBatches;
                if (!batches?.length) return sourceNode;
                let sourceChanged = false;
                const nextBatches = batches.map((batch) => {
                    if (batch.projectId !== projectId) return batch;
                    let batchChanged = false;
                    const nextItems = batch.items.map((item) => {
                        if (item.status === "succeeded" || item.status === "failed" || item.status === "cancelled") return item;
                        const node = nodeById.get(item.nodeId);
                        let patch: Partial<CanvasGenerationBatchItem> | null = null;
                        if (!node) {
                            patch = { status: "failed", submissionRecoveryUncertain: undefined, errorDetails: "目标节点已不存在" };
                        } else if (node.metadata?.status === "success" && node.metadata.content) {
                            patch = { status: "succeeded", taskId: node.metadata.taskId, retrySourceTaskId: undefined, errorDetails: undefined, costUncertain: false, submissionRecoveryUncertain: undefined };
                        } else if (node.metadata?.status === "error") {
                            const errorDetails = node.metadata.errorDetails || "生成失败";
                            patch = {
                                status: node.metadata.taskStatus === "cancelled" ? "cancelled" : "failed",
                                taskId: node.metadata.taskId,
                                ...(node.metadata.taskId ? { retrySourceTaskId: undefined } : {}),
                                errorDetails,
                                costUncertain: isGenerationCostUncertainError(new Error(errorDetails)),
                                submissionRecoveryUncertain: undefined,
                            };
                        } else if (node.metadata?.taskId) {
                            const taskStatus = node.metadata.taskStatus;
                            patch = {
                                taskId: node.metadata.taskId,
                                retrySourceTaskId: undefined,
                                // 后端成功后还要下载并写入媒体，节点真正拿到内容才算批次成功。
                                status: taskStatus === "queued" ? "queued" : taskStatus === "failed" ? "failed" : taskStatus === "cancelled" ? "cancelled" : "running",
                                errorDetails: undefined,
                                submissionRecoveryUncertain: undefined,
                            };
                        } else if (item.status === "submitting" && !controllersRef.current.has(batchItemKey(batch.id, item.id))) {
                            // 创建请求可能已经到达 Backend 但响应尚未回到浏览器；刷新后不能把它降级为 waiting，
                            // 否则调度器会创建第二个任务。画布恢复器会按节点 ID继续找回原任务。
                            uncertainNodeIds.add(item.nodeId);
                            patch = {
                                status: "submitting",
                                submissionRecoveryUncertain: true,
                                errorDetails: "页面刷新后暂时无法确认批次提交状态；系统会继续从任务中心恢复，请勿重复生成。",
                            };
                        }
                        if (!patch || !itemChanged(item, patch)) return item;
                        batchChanged = true;
                        return { ...item, ...patch };
                    });
                    const nextBatch = batchChanged ? { ...batch, items: nextItems } : batch;
                    const status = generationBatchStatus(nextBatch);
                    if (!batchChanged && status === batch.status) return batch;
                    sourceChanged = true;
                    return { ...nextBatch, status, updatedAt: new Date().toISOString() };
                });
                if (!sourceChanged) return sourceNode;
                changed = true;
                return { ...sourceNode, metadata: { ...sourceNode.metadata, generationBatches: nextBatches } };
            });
            const recoveredNodes = nextNodes.map((node) => {
                if (!uncertainNodeIds.has(node.id)) return node;
                const metadata = node.metadata || {};
                const nextMetadata = {
                    ...metadata,
                    status: "loading" as const,
                    taskRecoveryUncertain: true,
                    taskStage: "正在恢复批次提交状态",
                    errorDetails: "页面刷新后暂时无法确认批次提交状态；系统会继续从任务中心恢复，请勿重复生成。",
                };
                const metadataChanged = metadata.status !== nextMetadata.status || metadata.taskRecoveryUncertain !== true || metadata.taskStage !== nextMetadata.taskStage || metadata.errorDetails !== nextMetadata.errorDetails;
                if (!metadataChanged) return node;
                changed = true;
                return { ...node, metadata: nextMetadata };
            });
            return changed ? recoveredNodes : current;
        });
    }, [projectId, setNodes]);

    // 所有媒体任务统一由后端队列调度，批次预约也必须占用用户并发额度。
    const scheduleWaitingItems = useCallback(async () => {
        if (!projectLoaded || schedulingRef.current) return;
        schedulingRef.current = true;
        try {
            const currentNodes = nodesRef.current;
            const nodeById = new Map(currentNodes.map((node) => [node.id, node]));
            const tasks = await listGenerationTasks(100).catch(() => null);
            const activeTaskCount = Array.isArray(tasks) ? tasks.filter((task) => task.status === "queued" || task.status === "running").length : activeTaskLimit;
            const pendingReservations = [...controllersRef.current.keys()].filter((key) => {
                const [, itemId] = key.split(":");
                const reservation = currentNodes.flatMap((node) => node.metadata?.generationBatches || []).flatMap((batch) => batch.items.map((item) => ({ batch, item }))).find((candidate) => candidate.item.id === itemId);
                if (!reservation) return false;
                const reservedNode = nodeById.get(reservation.item.nodeId);
                return !reservedNode?.metadata?.taskId;
            }).length;
            let availableBackendSlots = Math.max(0, activeTaskLimit - activeTaskCount - pendingReservations);

            const candidates: Array<{ batch: CanvasGenerationBatch; item: CanvasGenerationBatchItem; node: CanvasNodeData; generationMode: CanvasNodeGenerationMode }> = [];
            for (const sourceNode of currentNodes) {
                for (const batch of sourceNode.metadata?.generationBatches || []) {
                    if (batch.projectId !== projectId || batch.status === "completed" || batch.status === "cancelled") continue;
                    for (const item of batch.items) {
                        if (item.status !== "waiting") continue;
                        const node = nodeById.get(item.nodeId);
                        if (!node) continue;
                        // 已绑定任务或已有成品的节点交给恢复/对账链路处理，绝不重复提交。
                        if (node.metadata?.taskId || (node.metadata?.status === "success" && node.metadata.content)) continue;
                        const generationMode: CanvasNodeGenerationMode = batch.mode === "storyboard_video" ? "video" : "image";
                        if (availableBackendSlots <= 0) continue;
                        availableBackendSlots -= 1;
                        candidates.push({ batch, item, node, generationMode });
                    }
                }
            }

            let reportedConfigError = false;
            for (const { batch, item, node, generationMode } of candidates) {
                const key = batchItemKey(batch.id, item.id);
                if (controllersRef.current.has(key)) continue;
                let generationConfig: ReturnType<typeof buildGenerationConfig>;
                try {
                    generationConfig = buildGenerationConfig(effectiveConfig, node, generationMode);
                } catch {
                    const errorDetails = "生成模型参数异常，请重新选择模型后再试";
                    updateBatch(batch.sourceNodeId, batch.id, (current) => withUpdatedItem(current, item.id, { status: "failed", errorDetails, costUncertain: false }));
                    if (!reportedConfigError) {
                        reportedConfigError = true;
                        message.error(errorDetails);
                    }
                    continue;
                }
                if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                    updateBatch(batch.sourceNodeId, batch.id, (current) => withUpdatedItem(current, item.id, { status: "failed", errorDetails: "生成模型未配置，请完成配置后重试" }));
                    continue;
                }
                const prompt = (node.metadata?.composerContent || node.metadata?.prompt || "").trim();
                if (!prompt) {
                    updateBatch(batch.sourceNodeId, batch.id, (current) => withUpdatedItem(current, item.id, { status: "failed", errorDetails: "生成提示词为空" }));
                    continue;
                }
                const controller = new AbortController();
                controllersRef.current.set(key, controller);
                updateBatch(batch.sourceNodeId, batch.id, (current) => withUpdatedItem(current, item.id, { status: "submitting", errorDetails: undefined }));
                void handleGenerateNode(node.id, generationMode, prompt, {
                    controller,
                    waitForTaskCapacity: true,
                    sourceTaskId: item.retrySourceTaskId,
                    confirmNewProviderRequest: Boolean(item.retrySourceTaskId),
                    onPreflightFailure: (errorDetails) => {
                        // 执行器在真正建连前就结束时，节点不会产生 taskId；批次必须落到明确失败，不能被 reconcile 反复放回 waiting。
                        updateBatch(batch.sourceNodeId, batch.id, (current) => withUpdatedItem(current, item.id, {
                            status: "failed",
                            errorDetails,
                            costUncertain: false,
                        }));
                    },
                }).finally(() => {
                    controllersRef.current.delete(key);
                    reconcileBatches();
                });
            }
        } finally {
            schedulingRef.current = false;
        }
    }, [activeTaskLimit, effectiveConfig, handleGenerateNode, isAiConfigReady, message, nodesRef, projectId, projectLoaded, reconcileBatches, updateBatch]);

    const retryFailedBatchItems = useCallback(async (sourceNodeId: string, batchId: string, itemId?: string) => {
        const batch = findBatch(nodesRef.current, sourceNodeId, batchId);
        if (!batch) return;
        const failedItems = batch.items.filter((item) => item.status === "failed" && (!itemId || item.id === itemId));
        if (!failedItems.length) return message.info("没有需要重试的失败项");
        const nodeById = new Map(nodesRef.current.map((node) => [node.id, node]));
        const blockedItems = failedItems.filter((item) => {
            const node = nodeById.get(item.nodeId);
            return unchangedModeratedPrompt(node?.metadata, node?.metadata?.composerContent || node?.metadata?.prompt || "");
        });
        const promptRetryableItems = failedItems.filter((item) => !blockedItems.includes(item));
        if (blockedItems.length) message.warning(`${blockedItems.length} 个镜头未通过内容审核，请先修改提示词`);
        if (!promptRetryableItems.length) return;

        const inspected = await Promise.all(promptRetryableItems.map(async (item) => {
            const node = nodeById.get(item.nodeId);
            const sourceTaskId = item.taskId || item.retrySourceTaskId || node?.metadata?.taskId;
            try {
                const inspection = await inspectGenerationRetry(sourceTaskId);
                return { item, sourceTaskId: inspection.sourceTask?.id, inspection, blockedReason: undefined };
            } catch (error) {
                return { item, sourceTaskId, inspection: undefined, blockedReason: error instanceof Error ? error.message : "无法核对原任务状态" };
            }
        }));
        const retryable = inspected.filter((entry) => !entry.blockedReason && !entry.inspection?.blockedReason);
        const stateBlocked = inspected.filter((entry) => entry.blockedReason || entry.inspection?.blockedReason);
        if (stateBlocked.length) {
            const firstReason = stateBlocked[0].blockedReason || stateBlocked[0].inspection?.blockedReason || "原任务暂不允许重试";
            message.warning(`${stateBlocked.length} 个镜头未重新提交：${firstReason}`);
        }
        if (!retryable.length) return;

        const hasCostUncertainItem = retryable.some((entry) => entry.inspection?.costUncertain || entry.item.costUncertain);
        const confirmed = await new Promise<boolean>((resolve) => modal.confirm({
            title: hasCostUncertainItem ? "确认供应商已停止原请求？" : `确认重新生成 ${retryable.length} 个镜头？`,
            content: hasCostUncertainItem
                ? `部分旧任务存在 524、连接中断或等待超时。它们可能仍在供应商执行并产生费用；继续会新建 ${retryable.length} 个外部模型请求，请先核对供应商后台或账单。`
                : `继续会新建 ${retryable.length} 个外部模型请求并可能产生新费用。系统已确认旧任务不在运行且平台计费不处于待核对状态。`,
            okText: "确认新建请求",
            cancelText: "暂不重试",
            centered: true,
            onOk: () => resolve(true),
            onCancel: () => resolve(false),
        }));
        if (!confirmed) return;

        const retryByItemId = new Map<string, string | undefined>(retryable.map((entry): [string, string | undefined] => [entry.item.id, entry.sourceTaskId]));
        const retryItemIds = new Set(retryByItemId.keys());
        const retryNodeIds = new Set(retryable.map((entry) => entry.item.nodeId));
        setNodes((current) => current.map((node) => {
            if (node.id === sourceNodeId) {
                const batches = (node.metadata?.generationBatches || []).map((currentBatch) => {
                    if (currentBatch.id !== batchId) return currentBatch;
                    const items = currentBatch.items.map((item) => retryItemIds.has(item.id) ? {
                        ...item,
                        status: "waiting" as const,
                        retrySourceTaskId: retryByItemId.get(item.id),
                        taskId: undefined,
                        errorDetails: undefined,
                        costUncertain: false,
                        submissionRecoveryUncertain: undefined,
                        retryCount: item.retryCount + 1,
                    } : item);
                    const nextBatch = { ...currentBatch, items, updatedAt: new Date().toISOString() };
                    return { ...nextBatch, status: generationBatchStatus(nextBatch) };
                });
                return { ...node, metadata: { ...node.metadata, generationBatches: batches } };
            }
            if (!retryNodeIds.has(node.id)) return node;
            return { ...node, metadata: resetGenerationTaskMetadata(node.metadata) };
        }));
        message.success(`已将 ${retryable.length} 个失败项重新加入等待队列`);
    }, [message, modal, nodesRef, setNodes]);

    const stopRemainingBatchItems = useCallback((sourceNodeId: string, batchId: string) => {
        const batch = findBatch(nodesRef.current, sourceNodeId, batchId);
        if (!batch) return;
        const nodeById = new Map(nodesRef.current.map((node) => [node.id, node]));
        const stoppableItems = batch.items.filter((item) => (item.status === "waiting" || item.status === "submitting") && !nodeById.get(item.nodeId)?.metadata?.taskId);
        if (!stoppableItems.length) return message.info("没有尚未提交或待核对的任务");
        const uncertainItems = stoppableItems.filter((item) => item.status === "submitting");
        modal.confirm({
            title: "停止剩余任务？",
            content: uncertainItems.length
                ? `将停止 ${stoppableItems.length - uncertainItems.length} 个尚未提交的任务；另有 ${uncertainItems.length} 个提交状态无法确认，系统只会停止本页等待并继续恢复原任务，不会把它们解锁为可重试。`
                : `将停止 ${stoppableItems.length} 个尚未提交的任务；已经排队或运行的任务会继续。`,
            okText: "停止剩余任务",
            cancelText: "继续生成",
            okButtonProps: { danger: true },
            onOk: () => {
                const latestNodeById = new Map(nodesRef.current.map((node) => [node.id, node]));
                const latestBatch = latestNodeById.get(sourceNodeId)?.metadata?.generationBatches?.find((candidate) => candidate.id === batchId);
                const latestStoppableItems = stoppableItems.filter((item) => {
                    const currentItem = latestBatch?.items.find((candidate) => candidate.id === item.id);
                    return Boolean(currentItem && (currentItem.status === "waiting" || currentItem.status === "submitting") && !latestNodeById.get(item.nodeId)?.metadata?.taskId);
                });
                const stoppableIds = new Set(latestStoppableItems.map((item) => item.id));
                const uncertainNodeIds = new Set(latestStoppableItems.filter((item) => item.status === "submitting").map((item) => item.nodeId));
                latestStoppableItems.forEach((item) => controllersRef.current.get(batchItemKey(batchId, item.id))?.abort());
                setNodes((current) => {
                    const next = current.map((node) => {
                        let nextNode = node;
                        if (node.id === sourceNodeId) {
                            const batches = (node.metadata?.generationBatches || []).map((currentBatch) => {
                                if (currentBatch.id !== batchId) return currentBatch;
                                const items = currentBatch.items.map((item) => {
                                    if (!stoppableIds.has(item.id)) return item;
                                    if (item.status === "submitting") return { ...item, status: "submitting" as const, submissionRecoveryUncertain: true, errorDetails: "本页已停止等待，但原批次提交状态仍待核对；系统会继续恢复，请勿重新生成。" };
                                    return { ...item, status: "cancelled" as const, submissionRecoveryUncertain: undefined, errorDetails: undefined };
                                });
                                const nextBatch = { ...currentBatch, items, updatedAt: new Date().toISOString() };
                                return { ...nextBatch, status: generationBatchStatus(nextBatch) };
                            });
                            nextNode = { ...node, metadata: { ...node.metadata, generationBatches: batches } };
                        }
                        if (!uncertainNodeIds.has(node.id)) return nextNode;
                        return {
                            ...nextNode,
                            metadata: {
                                ...nextNode.metadata,
                                status: "loading" as const,
                                taskRecoveryUncertain: true,
                                taskStage: "正在恢复批次提交状态",
                                errorDetails: "本页已停止等待，但原批次提交状态仍待核对；系统会继续恢复，请勿重新生成。",
                            },
                        };
                    });
                    return next;
                });
            },
        });
    }, [message, modal, nodesRef, setNodes]);

    const cancelSubmittedBatchItem = useCallback((sourceNodeId: string, batchId: string, itemId: string) => {
        const batch = findBatch(nodesRef.current, sourceNodeId, batchId);
        const item = batch?.items.find((candidate) => candidate.id === itemId);
        const node = item ? nodesRef.current.find((candidate) => candidate.id === item.nodeId) : undefined;
        const taskId = item?.taskId || node?.metadata?.taskId;
        if (!item || !taskId) return;
        modal.confirm({
            title: "取消这个后台任务？",
            content: item.status === "running"
                ? "系统会停止本地跟踪并请求后端取消，但不能保证供应商立即停止。原请求可能仍在执行并产生费用，取消后费用将进入待核对状态，请勿立即重试。"
                : "排队任务通常会在调用供应商前取消并退回冻结积分；若任务恰好已经开始，供应商仍可能执行并产生费用，届时请先核对费用再重试。",
            okText: "取消任务",
            cancelText: "继续生成",
            okButtonProps: { danger: true },
            onOk: async () => {
                try {
                    const task = await cancelGenerationTask(taskId);
                    abortGenerationTaskTracking(taskId);
                    const cancelDetails = task.error || "任务已取消";
                    setNodes((current) => current.map((currentNode) => {
                        if (currentNode.id === item.nodeId) return { ...currentNode, metadata: { ...currentNode.metadata, ...generationTaskMetadata(task), status: "error", errorDetails: cancelDetails } };
                        if (currentNode.id !== sourceNodeId) return currentNode;
                        const batches = (currentNode.metadata?.generationBatches || []).map((currentBatch) => currentBatch.id === batchId ? withUpdatedItem(currentBatch, item.id, { status: task.status === "cancelled" ? "cancelled" : "failed", taskId, errorDetails: cancelDetails, costUncertain: isGenerationCostUncertainError(cancelDetails) }) : currentBatch);
                        return { ...currentNode, metadata: { ...currentNode.metadata, generationBatches: batches } };
                    }));
                    if (task.status === "failed") message.error(cancelDetails);
                    else if (isGenerationCostUncertainError(cancelDetails)) message.warning(cancelDetails);
                    else message.success(cancelDetails);
                } catch (error) {
                    message.error(error instanceof Error ? error.message : "任务取消失败");
                }
            },
        });
    }, [message, modal, nodesRef, setNodes]);

    useEffect(() => {
        if (!projectLoaded) return;
        reconcileBatches();
    }, [nodes, projectLoaded, reconcileBatches]);

    useEffect(() => {
        if (!projectLoaded) return;
        void scheduleWaitingItems();
        const timer = window.setInterval(() => {
            reconcileBatches();
            void scheduleWaitingItems();
        }, SCHEDULER_INTERVAL_MS);
        return () => window.clearInterval(timer);
    }, [projectLoaded, reconcileBatches, scheduleWaitingItems]);

    return {
        cancelSubmittedBatchItem,
        enqueueGenerationBatch,
        retryFailedBatchItems,
        stopRemainingBatchItems,
    };
}

function batchItemKey(batchId: string, itemId: string) {
    return `${batchId}:${itemId}`;
}

function itemChanged(item: CanvasGenerationBatchItem, patch: Partial<CanvasGenerationBatchItem>) {
    return Object.entries(patch).some(([key, value]) => item[key as keyof CanvasGenerationBatchItem] !== value);
}

function withUpdatedItem(batch: CanvasGenerationBatch, itemId: string, patch: Partial<CanvasGenerationBatchItem>) {
    const items = batch.items.map((item) => item.id === itemId ? { ...item, ...patch } : item);
    const nextBatch = { ...batch, items, updatedAt: new Date().toISOString() };
    return { ...nextBatch, status: generationBatchStatus(nextBatch) };
}

function findBatch(nodes: CanvasNodeData[], sourceNodeId: string, batchId: string) {
    return nodes.find((node) => node.id === sourceNodeId)?.metadata?.generationBatches?.find((batch) => batch.id === batchId);
}
