import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { App } from "antd";

import { applyGenerationTaskResultToNodes, generationTaskNodeId } from "@/lib/canvas/canvas-generation-task-sync";
import { ensureCanvasNodeAsset } from "@/services/project-asset-sync";
import { abortGenerationTaskTracking, cancelGenerationTask, listGenerationTasks, listTaskLogs, queryGenerationTask, waitForGenerationTask, type GenerationTask, type TaskLog } from "@/services/api/task-center";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import { cinematicStoryboardColumns, storyboardRowsFromTask } from "@/lib/canvas/canvas-project-domain";
import { isGenerationCostUncertainError } from "@/lib/canvas/canvas-generation-batch";
import { generationTaskMetadata } from "@/lib/canvas/canvas-project-generation";
import { generationFailureMetadata } from "@/lib/generation-error";

type CanvasGenerationRequest = {
    targetNodeId: string;
    originNodeId: string;
    runningNodeId: string;
    controller: AbortController;
};

type UseCanvasGenerationOptions = {
    projectId: string;
    domainProjectId?: string;
    projectLoaded: boolean;
    nodes: CanvasNodeData[];
    nodesRef: { current: CanvasNodeData[] };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
};

const NODE_STATUS_IDLE = "idle" as const;
const NODE_STATUS_LOADING = "loading" as const;
const NODE_STATUS_SUCCESS = "success" as const;
const NODE_STATUS_ERROR = "error" as const;

export function useCanvasGeneration({ projectId, domainProjectId, projectLoaded, nodes, nodesRef, setNodes }: UseCanvasGenerationOptions) {
    const { message, modal } = App.useApp();
    const queryClient = useQueryClient();
    const generationRequestsRef = useRef(new Map<string, CanvasGenerationRequest>());
    const recoveringTaskIdsRef = useRef(new Set<string>());
    const autoSavedTaskIdsRef = useRef(new Set<string>());
    const recoveryRetryTimerRef = useRef<number | null>(null);
    const recoveryRunnerRef = useRef<(() => void) | null>(null);
    const recoveryInFlightRef = useRef(false);
    const [runningNodeIds, setRunningNodeIds] = useState<Set<string>>(new Set());
    const [taskDetail, setTaskDetail] = useState<GenerationTask | null>(null);
    const [taskDetailLogs, setTaskDetailLogs] = useState<TaskLog[]>([]);
    const [taskDetailLoading, setTaskDetailLoading] = useState(false);

    const startGenerationRequest = useCallback((targetNodeId: string, originNodeId: string, runningId = originNodeId, controller = new AbortController()) => {
        const previous = generationRequestsRef.current.get(targetNodeId);
        if (previous?.controller !== controller) previous?.controller.abort();
        generationRequestsRef.current.set(targetNodeId, { targetNodeId, originNodeId, runningNodeId: runningId, controller });
        return controller;
    }, []);

    const finishGenerationRequest = useCallback((targetNodeId: string, controller: AbortController) => {
        const request = generationRequestsRef.current.get(targetNodeId);
        if (request?.controller === controller) generationRequestsRef.current.delete(targetNodeId);
    }, []);

    const startRunningNode = useCallback((nodeId: string) => {
        setRunningNodeIds((current) => current.has(nodeId) ? current : new Set(current).add(nodeId));
    }, []);

    const finishRunningNode = useCallback((nodeId: string) => {
        setRunningNodeIds((current) => {
            if (!current.has(nodeId)) return current;
            const next = new Set(current);
            next.delete(nodeId);
            return next;
        });
    }, []);

    const clearRunningNodes = useCallback(() => setRunningNodeIds(new Set()), []);

    const stopGenerationByRunningId = useCallback((runningId: string) => {
        const affectedNodeIds = new Set<string>();
        generationRequestsRef.current.forEach((request) => {
            if (request.runningNodeId !== runningId) return;
            request.controller.abort();
            affectedNodeIds.add(request.targetNodeId);
            affectedNodeIds.add(request.originNodeId);
        });
        if (!affectedNodeIds.size) return;
        // 取消结果返回前保持生成锁定，防止原供应商请求尚未停止时再次提交并重复计费。
        setNodes((current) => current.map((node) => affectedNodeIds.has(node.id) && node.metadata?.status === NODE_STATUS_LOADING ? { ...node, metadata: { ...node.metadata, errorDetails: "正在确认后台取消结果，请勿重新生成或关闭页面。" } } : node));
    }, [setNodes]);

    const confirmStopGeneration = useCallback((nodeId: string) => {
        modal.confirm({
            title: "停止生成？",
            content: "系统会中断当前等待并尝试取消后台任务，已完成内容会保留；若供应商请求已经开始，仍可能继续执行并产生费用，请先到任务中心核对状态和账单，不要立即重试。",
            okText: "停止",
            cancelText: "继续生成",
            okButtonProps: { danger: true },
            onOk: () => stopGenerationByRunningId(nodeId),
        });
    }, [modal, stopGenerationByRunningId]);

    const cancelNodeTask = useCallback((node: CanvasNodeData) => {
        const taskId = node.metadata?.taskId;
        if (!taskId) {
            confirmStopGeneration(node.id);
            return;
        }
        modal.confirm({
            title: "取消后台任务？",
            content: node.metadata?.taskStatus === "running"
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
                    setNodes((current) => current.map((item) => item.id === node.id ? { ...item, metadata: { ...item.metadata, ...generationTaskMetadata(task), status: NODE_STATUS_ERROR, errorDetails: cancelDetails } } : item));
                    if (task.status === "failed") message.error(cancelDetails);
                    else if (isGenerationCostUncertainError(cancelDetails)) message.warning(cancelDetails);
                    else message.success(cancelDetails);
                } catch (error) {
                    message.error(error instanceof Error ? error.message : "任务取消失败");
                }
            },
        });
    }, [confirmStopGeneration, message, modal, setNodes]);

    const openNodeTaskDetails = useCallback(async (node: CanvasNodeData) => {
        const taskId = node.metadata?.taskId;
        if (!taskId) return;
        setTaskDetailLoading(true);
        setTaskDetailLogs([]);
        setTaskDetail({
            id: taskId,
            type: "",
            status: (node.metadata?.taskStatus as GenerationTask["status"]) || "running",
            stage: node.metadata?.taskStage,
            progress: node.metadata?.taskProgress,
            prompt: node.metadata?.prompt || "",
            attempts: 1,
            createdAt: node.metadata?.taskCreatedAt || new Date().toISOString(),
            updatedAt: node.metadata?.taskUpdatedAt || new Date().toISOString(),
        });
        try {
            const [task, logs] = await Promise.all([queryGenerationTask(taskId), listTaskLogs(taskId)]);
            setTaskDetail(task);
            setTaskDetailLogs(logs);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "任务详情加载失败");
        } finally {
            setTaskDetailLoading(false);
        }
    }, [message]);

    const bindGenerationTask = useCallback((targetNodeId: string, task: GenerationTask) => {
        setNodes((current) => current.map((node) => {
            if (node.id !== targetNodeId) return node;
            const failed = task.status === "failed" || task.status === "cancelled";
            const hasCompletedContent = task.status === "succeeded" && Boolean(node.metadata?.content);
            const failure = failed
                ? generationFailureMetadata(task.error || (task.status === "cancelled" ? "任务已取消" : "任务失败"), node.metadata?.composerContent || node.metadata?.prompt || task.prompt || "")
                : undefined;
            return {
                ...node,
                metadata: {
                    ...node.metadata,
                    ...generationTaskMetadata(task),
                    status: failed ? NODE_STATUS_ERROR : hasCompletedContent ? NODE_STATUS_SUCCESS : NODE_STATUS_LOADING,
                    ...(failure || { errorDetails: undefined, generationErrorCode: undefined, failedPromptFingerprint: undefined }),
                },
            };
        }));
    }, [setNodes]);

    const saveGeneratedAsset = useCallback(async (node: CanvasNodeData, taskId: string) => {
        const result = await ensureCanvasNodeAsset({ canvasId: projectId, domainProjectId, node, source: "canvas-generation", taskId });
        setNodes((current) => current.map((item) => item.id === node.id ? { ...item, metadata: { ...item.metadata, assetId: result.assetId } } : item));
        if (domainProjectId) await queryClient.invalidateQueries({ queryKey: ["project", domainProjectId] });
    }, [domainProjectId, projectId, queryClient, setNodes]);

    const applyGenerationTaskResult = useCallback(async (nodeId: string, task: GenerationTask) => {
        const applied = await applyGenerationTaskResultToNodes(nodesRef.current, task, nodeId);
        if (!applied.updated || !applied.node) throw new Error("画布中找不到对应任务节点");
        setNodes((current) => current.map((node) => node.id === applied.nodeId ? applied.node! : node));
    }, [nodesRef, setNodes]);

    const scheduleRecoveryRetry = useCallback(() => {
        if (typeof window === "undefined" || recoveryRetryTimerRef.current !== null) return;
        recoveryRetryTimerRef.current = window.setTimeout(() => {
            recoveryRetryTimerRef.current = null;
            recoveryRunnerRef.current?.();
        }, 10_000);
    }, []);

    const recoverInterruptedGenerationTasks = useCallback(async () => {
        if (recoveryInFlightRef.current) return;
        recoveryInFlightRef.current = true;
        try {
            const recoveryNodes = nodesRef.current.filter((node) => node.metadata?.status === NODE_STATUS_LOADING || node.metadata?.errorDetails === "页面刷新后生成已中断，请重新生成。" || Boolean(node.metadata?.taskId && node.metadata.status !== NODE_STATUS_SUCCESS));
            if (!recoveryNodes.length) return;
            const taskIds = Array.from(new Set(recoveryNodes.map((node) => node.metadata?.taskId).filter((id): id is string => Boolean(id))));
            const tasks = (await Promise.all(taskIds.map((id) => queryGenerationTask(id).catch(() => undefined)))).filter((task): task is GenerationTask => Boolean(task));
            if (recoveryNodes.some((node) => !node.metadata?.taskId)) {
                const recentTasks = await listGenerationTasks(30).catch(() => []);
                tasks.push(...recentTasks.filter((task) => !tasks.some((item) => item.id === task.id)));
            }
            const projectTasks = tasks.filter((task) => task.projectId === projectId && (task.type.startsWith("canvas_") || task.type === "agent_storyboard_rows"));
            let recoveryPending = false;
            await Promise.all(recoveryNodes.map(async (node) => {
                let task = projectTasks.find((item) => item.id === node.metadata?.taskId) || projectTasks.find((item) => generationTaskNodeId(item) === node.id);
                if (!task && node.metadata?.taskId) task = await queryGenerationTask(node.metadata.taskId).catch(() => undefined);
                if (!task) {
                    // 有任务 ID 或查询列表本身失败时，不能把“暂时查不到”当成未调用；
                    // 保留安全锁并稍后重查，避免用户刷新后立即重试造成重复供应商调用。
                    recoveryPending = true;
                    setNodes((current) => current.map((item) => item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_LOADING, taskRecoveryUncertain: true, taskStage: "正在恢复后台任务状态", errorDetails: "页面刷新后暂时无法确认原后台任务状态；系统会继续恢复，请到任务中心核对，勿重新生成。" } } : item));
                    return;
                }
                if (recoveringTaskIdsRef.current.has(task.id)) return;
                recoveringTaskIdsRef.current.add(task.id);
                bindGenerationTask(node.id, task);
                try {
                    const completed = task.status === "succeeded" ? task : await waitForGenerationTask(task.id, { initialTask: task });
                    if (node.type === CanvasNodeType.Script && completed.type === "agent_storyboard_rows") {
                        const result = storyboardRowsFromTask(completed);
                        setNodes((current) => current.map((item) => item.id === node.id ? { ...item, title: result.title || item.title, metadata: { ...item.metadata, ...generationTaskMetadata(completed), status: NODE_STATUS_SUCCESS, errorDetails: undefined, generationErrorCode: undefined, failedPromptFingerprint: undefined, storyboard: { rows: result.rows, visibleColumns: cinematicStoryboardColumns(item.metadata?.storyboard?.visibleColumns), referenceNodeIds: item.metadata?.storyboard?.referenceNodeIds || [] } } } : item));
                    } else {
                        await applyGenerationTaskResult(node.id, completed);
                    }
                } catch (error) {
                    const failure = generationFailureMetadata(error, node.metadata?.composerContent || node.metadata?.prompt || task.prompt || "");
                    setNodes((current) => current.map((item) => item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, ...failure } } : item));
                } finally {
                    recoveringTaskIdsRef.current.delete(task.id);
                }
            }));
            if (recoveryPending) scheduleRecoveryRetry();
        } finally {
            recoveryInFlightRef.current = false;
        }
    }, [applyGenerationTaskResult, bindGenerationTask, nodesRef, projectId, scheduleRecoveryRetry, setNodes]);

    recoveryRunnerRef.current = () => { void recoverInterruptedGenerationTasks(); };

    useEffect(() => {
        if (!projectLoaded) return;
        void recoverInterruptedGenerationTasks();
    }, [projectLoaded, recoverInterruptedGenerationTasks]);

    useEffect(() => {
        if (!projectLoaded || !nodes.some((node) => node.metadata?.taskRecoveryUncertain)) return;
        // 批次提交响应丢失时，批次状态会先写入安全锁；触发与页面初始恢复相同的任务查询，
        // 让当前页面也能找回原任务，而不是等用户刷新后才开始恢复。
        scheduleRecoveryRetry();
    }, [nodes, projectLoaded, scheduleRecoveryRetry]);

    useEffect(() => () => {
        if (recoveryRetryTimerRef.current !== null) window.clearTimeout(recoveryRetryTimerRef.current);
        recoveryRetryTimerRef.current = null;
        recoveryRunnerRef.current = null;
    }, []);

    useEffect(() => {
        if (!projectLoaded) return;
        nodes.forEach((node) => {
            const taskId = node.metadata?.taskId;
            if (!taskId || !node.metadata?.content || node.metadata.status !== NODE_STATUS_SUCCESS || (node.type !== CanvasNodeType.Image && node.type !== CanvasNodeType.Video && node.type !== CanvasNodeType.Audio)) return;
            const saveKey = `${taskId}:${node.id}:${domainProjectId || "personal"}`;
            if (autoSavedTaskIdsRef.current.has(saveKey)) return;
            autoSavedTaskIdsRef.current.add(saveKey);
            void saveGeneratedAsset(node, taskId).catch((error) => {
                autoSavedTaskIdsRef.current.delete(saveKey);
                message.warning("生成结果已保留，但项目资产同步失败，请稍后从项目资产入口重试");
            });
        });
    }, [domainProjectId, message, nodes, projectLoaded, saveGeneratedAsset]);

    return {
        bindGenerationTask,
        cancelNodeTask,
        confirmStopGeneration,
        finishGenerationRequest,
        openNodeTaskDetails,
        clearRunningNodes,
        finishRunningNode,
        runningNodeIds,
        startRunningNode,
        setTaskDetail,
        startGenerationRequest,
        taskDetail,
        taskDetailLoading,
        taskDetailLogs,
    };
}
