import { useCallback, type Dispatch, type SetStateAction } from "react";
import { App } from "antd";

import { buildNodeGenerationContext, hydrateNodeGenerationContext } from "@/components/canvas/canvas-node-generation";
import type { CanvasNodeGenerationMode } from "@/components/canvas/canvas-node-prompt-panel";
import { buildGenerationConfig, isGenerationCanceled, supportsVideoReferenceAudio } from "@/lib/canvas/canvas-project-generation";
import { isGenerationTaskCapacityError } from "@/lib/canvas/canvas-generation-batch";
import { hasPendingCanvasGenerationTask } from "@/lib/canvas/canvas-generation-task-state";
import { expandSkillMentions } from "@/lib/canvas/canvas-skill-mentions";
import { generationFailureMetadata } from "@/lib/generation-error";
import { navigateToSettings } from "@/lib/settings-navigation";
import type { UpdreamSkill } from "@/services/api/skills";
import type { GenerationTask } from "@/services/api/task-center";
import { useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";

import { executeImageGeneration } from "./canvas-image-generation-executor";
import { executeAudioGeneration, executeVideoGeneration } from "./canvas-media-generation-executors";
import { executeTextGeneration } from "./canvas-text-generation-executor";

type UseCanvasGenerationExecutorOptions = {
    projectId: string;
    domainProjectId?: string;
    activatedSkills: UpdreamSkill[];
    nodesRef: { current: CanvasNodeData[] };
    connectionsRef: { current: CanvasConnection[] };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setDialogNodeId: Dispatch<SetStateAction<string | null>>;
    revealGeneratedNodes?: (nodeIds: string[]) => void;
    setRunningNode: (nodeId: string) => void;
    clearRunningNode: (nodeId: string) => void;
    startGenerationRequest: (targetNodeId: string, originNodeId: string, runningId?: string, controller?: AbortController) => AbortController;
    finishGenerationRequest: (targetNodeId: string, controller: AbortController) => void;
    bindGenerationTask: (targetNodeId: string, task: GenerationTask) => void;
};

const NODE_STATUS_IDLE = "idle" as const;
const NODE_STATUS_LOADING = "loading" as const;
const NODE_STATUS_ERROR = "error" as const;

export type CanvasNodeGenerationOptions = {
    controller?: AbortController;
    waitForTaskCapacity?: boolean;
    sourceTaskId?: string;
    confirmNewProviderRequest?: boolean;
    /** 批量调度在供应商调用前失败时需要终止当前条目，避免提交状态被反复放回等待队列。 */
    onPreflightFailure?: (errorDetails: string) => void;
};

export function useCanvasGenerationExecutor({
    projectId,
    domainProjectId,
    activatedSkills,
    nodesRef,
    connectionsRef,
    setNodes,
    setConnections,
    setSelectedNodeIds,
    setSelectedConnectionId,
    setDialogNodeId,
    revealGeneratedNodes,
    setRunningNode,
    clearRunningNode,
    startGenerationRequest,
    finishGenerationRequest,
    bindGenerationTask,
}: UseCanvasGenerationExecutorOptions) {
    const { message } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);

    return useCallback(
        async (nodeId: string, mode: CanvasNodeGenerationMode, prompt: string, options?: CanvasNodeGenerationOptions) => {
            const reportPreflightFailure = (errorDetails: string) => {
                if (!options?.onPreflightFailure || options.controller?.signal.aborted) return;
                options.onPreflightFailure(errorDetails);
            };
            const sourceNode = nodesRef.current.find((node) => node.id === nodeId);
            if (hasPendingCanvasGenerationTask(sourceNode)) {
                message.warning("该节点仍绑定排队中或运行中的后台任务，请等待结果或从任务详情取消，勿重复提交。");
                return;
            }
            if (sourceNode?.type === CanvasNodeType.Video && sourceNode.metadata?.videoEditOperation === "concat") {
                const errorDetails = "合并成片不调用模型生成；请同时选择至少 2 段源视频和这个槽位，再点“合并选中视频”";
                message.info(errorDetails);
                reportPreflightFailure(errorDetails);
                return;
            }
            let generationConfig: ReturnType<typeof buildGenerationConfig>;
            try {
                generationConfig = buildGenerationConfig(effectiveConfig, sourceNode, mode);
            } catch {
                const errorDetails = "生成模型参数异常，请重新选择模型后再试";
                reportPreflightFailure(errorDetails);
                message.error(errorDetails);
                return;
            }
            if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                reportPreflightFailure("生成模型配置已变化，请重新确认模型后再提交");
                navigateToSettings({ continueCreation: true });
                return;
            }

            setRunningNode(nodeId);
            const controller = startGenerationRequest(nodeId, nodeId, nodeId, options?.controller);
            const sourceTextContent = sourceNode?.type === CanvasNodeType.Text ? sourceNode.metadata?.content?.trim() || "" : "";
            const editingTextNode = mode === "text" && Boolean(sourceTextContent);
            const isPreparingEmptyImage = mode === "image" && sourceNode?.type === CanvasNodeType.Image && !sourceNode.metadata?.content;
            if (isPreparingEmptyImage) {
                setNodes((current) =>
                    current.map((node) =>
                        node.id === nodeId
                            ? {
                                  ...node,
                                  metadata: {
                                      ...node.metadata,
                                      prompt,
                                      status: NODE_STATUS_LOADING,
                                      taskStage: "正在准备生成任务",
                                      taskProgress: 0,
                                      taskCreatedAt: new Date().toISOString(),
                                      errorDetails: undefined,
                                      generationErrorCode: undefined,
                                      failedPromptFingerprint: undefined,
                                  },
                              }
                            : node,
                    ),
                );
            }

            let rawGenerationContext: Awaited<ReturnType<typeof hydrateNodeGenerationContext>>;
            try {
                rawGenerationContext = await hydrateNodeGenerationContext(
                    buildNodeGenerationContext(nodeId, nodesRef.current, connectionsRef.current, editingTextNode ? `请根据要求修改以下文本。\n\n原文：\n${sourceTextContent}\n\n修改要求：\n${prompt}` : prompt),
                    projectId,
                    domainProjectId,
                    mode,
                    mode === "video" && supportsVideoReferenceAudio(generationConfig),
                );
            } catch (error) {
                const errorDetails = error instanceof Error ? error.message : "生成任务准备失败";
                reportPreflightFailure(errorDetails);
                if (isPreparingEmptyImage) {
                    setNodes((current) => current.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: controller.signal.aborted ? NODE_STATUS_IDLE : NODE_STATUS_ERROR, taskStage: undefined, taskProgress: undefined, taskCreatedAt: undefined, errorDetails: controller.signal.aborted ? undefined : errorDetails } } : node)));
                }
                finishGenerationRequest(nodeId, controller);
                clearRunningNode(nodeId);
                if (!controller.signal.aborted) message.error(errorDetails);
                return;
            }

            const expandedPrompt = expandSkillMentions(rawGenerationContext.prompt, activatedSkills);
            const effectivePrompt = expandedPrompt.trim();
            const generationContext = { ...rawGenerationContext, prompt: effectivePrompt };
            if (mode === "audio" && generationContext.characterReferences.length) {
                if (generationContext.characterReferences.length !== 1) {
                    reportPreflightFailure("角色配音一次只能引用一个角色卡");
                    finishGenerationRequest(nodeId, controller);
                    clearRunningNode(nodeId);
                    message.error("角色配音一次只能引用一个角色卡");
                    return;
                }
                const voice = generationContext.resolvedCharacterVoices[0];
                if (!voice) {
                    reportPreflightFailure("角色尚未绑定可用声音，无法创建角色配音任务");
                    finishGenerationRequest(nodeId, controller);
                    clearRunningNode(nodeId);
                    message.error("角色尚未绑定可用声音，无法创建角色配音任务");
                    return;
                }
                generationConfig = { ...generationConfig, audioVoice: voice.voiceKey, audioInstructions: [voice.instructions, generationConfig.audioInstructions].filter(Boolean).join("；") };
            }
            if (controller.signal.aborted) {
                if (isPreparingEmptyImage) setNodes((current) => current.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_IDLE, taskStage: undefined, taskProgress: undefined, taskCreatedAt: undefined } } : node)));
                finishGenerationRequest(nodeId, controller);
                clearRunningNode(nodeId);
                return;
            }

            const markSourceStatus = sourceNode?.type !== CanvasNodeType.Image && !editingTextNode;
            const statusPrompt = sourceNode?.type === CanvasNodeType.Config ? effectivePrompt : prompt;
            if (!effectivePrompt && (mode === "text" || mode === "audio")) {
                reportPreflightFailure("生成提示词为空");
                finishGenerationRequest(nodeId, controller);
                clearRunningNode(nodeId);
                return;
            }
            if (markSourceStatus) setNodes((current) => current.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, prompt: statusPrompt, status: NODE_STATUS_LOADING, errorDetails: undefined, generationErrorCode: undefined, failedPromptFingerprint: undefined } } : node)));

            let pendingNodeIds: string[] = [];
            const execution = {
                projectId,
                nodesRef,
                nodeId,
                sourceNode,
                prompt,
                effectivePrompt,
                generationConfig,
                generationContext,
                controller,
                sourceTaskId: options?.sourceTaskId,
                confirmNewProviderRequest: options?.confirmNewProviderRequest,
                editingTextNode,
                setNodes,
                setConnections,
                setSelectedNodeIds,
                setSelectedConnectionId,
                setDialogNodeId,
                revealGeneratedNodes: options?.waitForTaskCapacity ? undefined : revealGeneratedNodes,
                startGenerationRequest,
                finishGenerationRequest,
                bindGenerationTask,
                showError: (content: string) => message.error(content),
                registerPendingNodeIds: (nodeIds: string[]) => {
                    pendingNodeIds = nodeIds;
                },
            };

            try {
                if (mode === "image") await executeImageGeneration(execution);
                else if (mode === "video") await executeVideoGeneration(execution);
                else if (mode === "audio") await executeAudioGeneration(execution);
                else await executeTextGeneration(execution);
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const failure = generationFailureMetadata(error, prompt);
                if (options?.waitForTaskCapacity && isGenerationTaskCapacityError(error)) {
                    setNodes((current) => current.map((node) => {
                        if (node.id !== nodeId && !pendingNodeIds.includes(node.id)) return node;
                        const metadata = { ...(node.metadata || {}), status: NODE_STATUS_IDLE, errorDetails: undefined };
                        delete metadata.taskId;
                        delete metadata.taskStatus;
                        delete metadata.taskProgress;
                        delete metadata.taskStage;
                        delete metadata.taskCreatedAt;
                        delete metadata.taskUpdatedAt;
                        delete metadata.taskRecoveryUncertain;
                        return { ...node, metadata };
                    }));
                    return;
                }
                message.error(failure.errorDetails);
                setNodes((current) => current.map((node) => (node.id === nodeId || pendingNodeIds.includes(node.id) ? (node.id === nodeId && !markSourceStatus ? node : { ...node, metadata: { ...node.metadata, status: NODE_STATUS_ERROR, ...failure } }) : node)));
            } finally {
                finishGenerationRequest(nodeId, controller);
                clearRunningNode(nodeId);
            }
        },
        [activatedSkills, bindGenerationTask, clearRunningNode, domainProjectId, effectiveConfig, finishGenerationRequest, isAiConfigReady, message, nodesRef, connectionsRef, projectId, revealGeneratedNodes, setConnections, setDialogNodeId, setNodes, setRunningNode, setSelectedConnectionId, setSelectedNodeIds, startGenerationRequest],
    );
}
