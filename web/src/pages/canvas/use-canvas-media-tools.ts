import { useCallback, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { App } from "antd";
import { nanoid } from "nanoid";

import type { CanvasImageAnnotationPayload } from "@/components/canvas/canvas-node-annotation-dialog";
import type { CanvasImageCropRect } from "@/components/canvas/canvas-node-crop-dialog";
import type { CanvasImageMaskEditPayload } from "@/components/canvas/canvas-node-mask-edit-dialog";
import type { CanvasImageSplitParams } from "@/components/canvas/canvas-node-split-dialog";
import type { CanvasImageUpscaleParams } from "@/components/canvas/canvas-node-upscale-dialog";
import type { CanvasImageAngleParams } from "@/components/canvas/canvas-node-angle-dialog";
import type { CanvasImageEmotionPayload } from "@/components/canvas/canvas-node-emotion-panel";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { cropDataUrl, splitDataUrl, upscaleDataUrl } from "@/lib/canvas/canvas-image-data";
import { imageMetadata, videoMetadata } from "@/lib/canvas/canvas-generation-task-sync";
import { appendCanvasNodesWithFrameExpansion } from "@/lib/canvas/canvas-frame";
import { buildAngleLabel, buildAnglePrompt, createCanvasNode } from "@/lib/canvas/canvas-project-domain";
import {
    buildGenerationConfig,
    buildImageGenerationMetadata,
    nodeReferenceImage,
    isGenerationCanceled,
    runBackendCanvasGenerationTask,
} from "@/lib/canvas/canvas-project-generation";
import { fitNodeSize } from "@/lib/canvas/canvas-node-size";
import { placeCanvasNodeGroup, placeCanvasNodeInContext } from "@/lib/canvas/canvas-layout";
import { compositeEmotionImage, emotionGenerationSize } from "@/lib/canvas/canvas-emotion";
import { captureVideoLastFrame } from "@/lib/canvas/canvas-video-frame";
import { mergeVideos, type MergeVideoProgress } from "@/lib/canvas/canvas-video-merge";
import { navigateToSettings } from "@/lib/settings-navigation";
import { storeGeneratedVideo } from "@/services/api/video";
import { getMediaBlob } from "@/services/file-storage";
import { imageToDataUrl, uploadImage } from "@/services/image-storage";
import type { GenerationTask } from "@/services/api/task-center";
import { defaultConfig, resolveModelRequestConfig, useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type ContextMenuState } from "@/types/canvas";
import type { StartCanvasUploadStatus } from "./use-canvas-upload";

type UseCanvasMediaToolsOptions = {
    projectId: string;
    nodesRef: { current: CanvasNodeData[] };
    connectionsRef: { current: CanvasConnection[] };
    selectedNodeIdsRef: { current: Set<string> };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setDialogNodeId: Dispatch<SetStateAction<string | null>>;
    setContextMenu: Dispatch<SetStateAction<ContextMenuState | null>>;
    setHoveredNodeId: Dispatch<SetStateAction<string | null>>;
    setToolbarNodeId: Dispatch<SetStateAction<string | null>>;
    setRunningNode: (nodeId: string) => void;
    clearRunningNode: (nodeId: string) => void;
    startUploadStatus: StartCanvasUploadStatus;
    startGenerationRequest: (targetNodeId: string, originNodeId: string, runningId?: string, controller?: AbortController) => AbortController;
    finishGenerationRequest: (targetNodeId: string, controller: AbortController) => void;
    bindGenerationTask: (targetNodeId: string, task: GenerationTask) => void;
};

const NODE_STATUS_LOADING = "loading" as const;
const NODE_STATUS_SUCCESS = "success" as const;
const NODE_STATUS_ERROR = "error" as const;
const VIDEO_NODE_MAX_WIDTH = 420;
const VIDEO_NODE_MAX_HEIGHT = 420;
const IMAGE_PROMPT_REVERSE_PRESET = `请根据参考图片反推一段适合用于 AI 生图的提示词。

要求：
1. 只输出提示词正文，不要解释。
2. 覆盖主体、构图、风格、光线、色彩、材质、镜头和氛围。
3. 尽量写成可直接用于生图模型的完整提示词。`;

export function useCanvasMediaTools({
    projectId,
    nodesRef,
    connectionsRef,
    selectedNodeIdsRef,
    setNodes,
    setConnections,
    setSelectedNodeIds,
    setSelectedConnectionId,
    setDialogNodeId,
    setContextMenu,
    setHoveredNodeId,
    setToolbarNodeId,
    setRunningNode,
    clearRunningNode,
    startUploadStatus,
    startGenerationRequest,
    finishGenerationRequest,
    bindGenerationTask,
}: UseCanvasMediaToolsOptions) {
    const { message } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);
    const extractingVideoFrameNodeIdRef = useRef<string | null>(null);
    const mergeVideoRunningRef = useRef(false);
    const [cropNodeId, setCropNodeId] = useState<string | null>(null);
    const [annotationNodeId, setAnnotationNodeId] = useState<string | null>(null);
    const [maskEditNodeId, setMaskEditNodeId] = useState<string | null>(null);
    const [splitNodeId, setSplitNodeId] = useState<string | null>(null);
    const [upscaleNodeId, setUpscaleNodeId] = useState<string | null>(null);
    const [angleNodeId, setAngleNodeId] = useState<string | null>(null);
    const [emotionNodeId, setEmotionNodeId] = useState<string | null>(null);
    const [extractingVideoFrameNodeId, setExtractingVideoFrameNodeId] = useState<string | null>(null);
    const [mergeVideoProgress, setMergeVideoProgress] = useState<MergeVideoProgress | null>(null);

    const createImageReversePromptNodes = useCallback((node: CanvasNodeData) => {
        if (node.type !== CanvasNodeType.Image || !node.metadata?.content) {
            message.warning("图片节点为空，无法反推提示词");
            return;
        }
        const gap = 96;
        const textSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Text];
        const configSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Config];
        const groupSize = { width: textSpec.width + gap + configSpec.width, height: Math.max(textSpec.height, configSpec.height) };
        const groupPosition = { x: node.position.x + node.width + gap, y: node.position.y + node.height / 2 - groupSize.height / 2 };
        const centerY = groupPosition.y + groupSize.height / 2;
        const initialTextNode = {
            ...createCanvasNode(CanvasNodeType.Text, { x: groupPosition.x + textSpec.width / 2, y: centerY }, { content: IMAGE_PROMPT_REVERSE_PRESET, prompt: IMAGE_PROMPT_REVERSE_PRESET, status: NODE_STATUS_SUCCESS, fontSize: 14 }),
            title: "反推提示词",
        };
        const initialConfigNode = {
            ...createCanvasNode(CanvasNodeType.Config, { x: initialTextNode.position.x + initialTextNode.width + gap + configSpec.width / 2, y: centerY }, {
                generationMode: "text",
                model: effectiveConfig.textModel || effectiveConfig.model || defaultConfig.textModel,
                count: 1,
                composerContent: `参考图片：@[node:${node.id}]\n任务说明：@[node:${initialTextNode.id}]`,
            }),
            title: "反推提示词配置",
        };
        const [textNode, configNode] = placeCanvasNodeGroup(nodesRef.current, [initialTextNode, initialConfigNode], 44, node);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [textNode, configNode]));
        setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: configNode.id }, { id: nanoid(), fromNodeId: textNode.id, toNodeId: configNode.id }]);
        setSelectedNodeIds(new Set([configNode.id]));
        setSelectedConnectionId(null);
        setDialogNodeId(configNode.id);
        setContextMenu(null);
    }, [effectiveConfig.model, effectiveConfig.textModel, message, nodesRef, setConnections, setContextMenu, setDialogNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds]);

    const cropImageNode = useCallback(async (node: CanvasNodeData, crop: CanvasImageCropRect) => {
        if (!node.metadata?.content) return;
        const cropped = await cropDataUrl(node.metadata.content, crop);
        const image = await uploadImage(cropped);
        const width = Math.min(node.width, Math.max(220, image.width));
        const height = width * (image.height / image.width);
        const childId = nanoid();
        const child = placeCanvasNodeInContext(nodesRef.current, { id: childId, type: CanvasNodeType.Image, title: "Cropped Image", position: { x: node.position.x + node.width + 96, y: node.position.y }, width, height, metadata: { ...imageMetadata(image), prompt: node.metadata?.prompt } }, undefined, node);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [child]));
        setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
        setSelectedNodeIds(new Set([childId]));
        setDialogNodeId(childId);
        setCropNodeId(null);
    }, [nodesRef, setConnections, setDialogNodeId, setNodes, setSelectedNodeIds]);

    const saveAnnotatedImageNode = useCallback(async (node: CanvasNodeData, payload: CanvasImageAnnotationPayload) => {
        try {
            const [image, mask] = await Promise.all([uploadImage(payload.markedDataUrl), uploadImage(payload.maskDataUrl)]);
            const size = fitNodeSize(image.width, image.height, node.width, node.height);
            const childId = nanoid();
            const child = placeCanvasNodeInContext(nodesRef.current, {
                id: childId,
                type: CanvasNodeType.Image,
                title: `标注 · ${payload.instruction.slice(0, 24)}`,
                position: { x: node.position.x + node.width + 96, y: node.position.y },
                width: size.width,
                height: size.height,
                metadata: {
                    ...imageMetadata(image),
                    prompt: node.metadata?.prompt,
                    imageAnnotation: {
                        sourceNodeId: node.id,
                        instruction: payload.instruction,
                        mask: { url: mask.url, storageKey: mask.storageKey },
                    },
                },
            }, undefined, node);
            setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [child]));
            setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setSelectedConnectionId(null);
            setDialogNodeId(null);
            setAnnotationNodeId(null);
            message.success("标注节点已保存，可交给 Canvas Agent 执行修改");
        } catch (error) {
            message.error(error instanceof Error ? error.message : "标注保存失败");
        }
    }, [message, nodesRef, setConnections, setDialogNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds]);

    const extractVideoLastFrame = useCallback(async (node: CanvasNodeData) => {
        const content = node.metadata?.content;
        if (!content || extractingVideoFrameNodeIdRef.current) return;
        const progress = startUploadStatus("截取视频尾帧", "读取视频资源");
        extractingVideoFrameNodeIdRef.current = node.id;
        setExtractingVideoFrameNodeId(node.id);
        try {
            const storedBlob = node.metadata?.storageKey ? await getMediaBlob(node.metadata.storageKey).catch(() => null) : null;
            progress.update("定位并绘制最后一帧", 2);
            const frameBlob = await captureVideoLastFrame(storedBlob || content);
            progress.update("保存尾帧图片并创建节点", 3);
            const image = await uploadImage(frameBlob);
            const size = fitNodeSize(image.width, image.height, node.width, node.height);
            const childId = nanoid();
            const child = placeCanvasNodeInContext(nodesRef.current, {
                id: childId,
                type: CanvasNodeType.Image,
                title: `尾帧 · ${node.title || "视频"}`,
                position: { x: node.position.x + node.width + 96, y: node.position.y },
                width: size.width,
                height: size.height,
                metadata: { ...imageMetadata(image), prompt: node.metadata?.prompt, workflowKind: node.metadata?.workflowKind, workflowTitle: node.metadata?.workflowTitle, shotIndex: node.metadata?.shotIndex },
            }, undefined, node);
            setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [child]));
            setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setSelectedConnectionId(null);
            setHoveredNodeId(null);
            setToolbarNodeId(null);
            progress.done("尾帧图片已创建");
        } catch (error) {
            const details = error instanceof Error ? error.message : "尾帧截取失败";
            progress.fail(details);
            message.error(details);
        } finally {
            extractingVideoFrameNodeIdRef.current = null;
            setExtractingVideoFrameNodeId(null);
        }
    }, [message, nodesRef, setConnections, setHoveredNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds, setToolbarNodeId, startUploadStatus]);

    const mergeVideosByIds = useCallback(async (videoNodeIds: string[]) => {
        if (mergeVideoRunningRef.current) return;
        const requestedIds = new Set(videoNodeIds);
        const requestedVideoNodes = nodesRef.current.filter((node) => requestedIds.has(node.id) && node.type === CanvasNodeType.Video);
        const targetSlots = requestedVideoNodes.filter((node) => !node.metadata?.content && node.metadata?.videoEditOperation === "concat");
        if (targetSlots.length > 1) {
            message.warning("一次只能选择一个空白成片槽位");
            return;
        }
        const targetSlot = targetSlots[0];
        const videos = requestedVideoNodes
            .filter((node) => Boolean(node.metadata?.content))
            .sort((left, right) => {
                const leftShot = left.metadata?.shotIndex ?? Number.MAX_SAFE_INTEGER;
                const rightShot = right.metadata?.shotIndex ?? Number.MAX_SAFE_INTEGER;
                return leftShot - rightShot || left.position.y - right.position.y || left.position.x - right.position.x;
            });
        if (videos.length < 2) {
            message.warning("请至少选择两个已有视频");
            return;
        }
        mergeVideoRunningRef.current = true;
        setMergeVideoProgress({ phase: "reading", progress: 0 });
        try {
            const blob = await mergeVideos(videos.map((node) => ({ id: node.id, url: node.metadata?.content, storageKey: node.metadata?.storageKey })), setMergeVideoProgress);
            setMergeVideoProgress({ phase: "encoding", progress: 98 });
            const uploaded = await storeGeneratedVideo({ blob });
            const size = fitNodeSize(uploaded.width || 1280, uploaded.height || 720, VIDEO_NODE_MAX_WIDTH, VIDEO_NODE_MAX_HEIGHT);
            const left = Math.max(...videos.map((node) => node.position.x + node.width)) + 120;
            const top = Math.min(...videos.map((node) => node.position.y));
            // 电商模板可预放空白 concat 槽位；用户把槽位与源视频一起选中时，结果原位回填以保留编排和 QA 连线。
            let mergedNode = targetSlot
                ? {
                      ...targetSlot,
                      metadata: {
                          ...targetSlot.metadata,
                          ...videoMetadata(uploaded),
                          videoEditOperation: "concat" as const,
                          status: NODE_STATUS_SUCCESS,
                      },
                  }
                : createCanvasNode(CanvasNodeType.Video, { x: left + size.width / 2, y: top + size.height / 2 }, {
                      ...videoMetadata(uploaded),
                      prompt: `按选中顺序合并 ${videos.length} 段视频`,
                      workflowKind: "final",
                      workflowTitle: "合并成片",
                      videoEditOperation: "concat",
                      status: NODE_STATUS_SUCCESS,
                  });
            if (!targetSlot) {
                mergedNode.title = `合并成片 · ${videos.length} 段`;
                mergedNode.width = size.width;
                mergedNode.height = size.height;
                const sharedParentSource = videos.every((video) => video.parentId === videos[0]?.parentId) ? videos[0] : undefined;
                mergedNode = placeCanvasNodeInContext(nodesRef.current, mergedNode, { x: left, y: top }, sharedParentSource);
            }
            const existingConnectionKeys = new Set(connectionsRef.current.map((connection) => `${connection.fromNodeId}:${connection.toNodeId}`));
            const links = videos
                .filter((node) => !existingConnectionKeys.has(`${node.id}:${mergedNode.id}`))
                .map((node) => ({ id: nanoid(), fromNodeId: node.id, toNodeId: mergedNode.id }));
            const nextNodes = targetSlot
                ? nodesRef.current.map((node) => (node.id === targetSlot.id ? mergedNode : node))
                : appendCanvasNodesWithFrameExpansion(nodesRef.current, [mergedNode]);
            const nextConnections = [...connectionsRef.current, ...links];
            nodesRef.current = nextNodes;
            connectionsRef.current = nextConnections;
            setNodes(nextNodes);
            setConnections(nextConnections);
            const selection = new Set([mergedNode.id]);
            selectedNodeIdsRef.current = selection;
            setSelectedNodeIds(selection);
            setSelectedConnectionId(null);
            setDialogNodeId(null);
            setMergeVideoProgress({ phase: "encoding", progress: 100 });
            message.success(targetSlot ? `已合并 ${videos.length} 段视频并填入成片槽位` : `已合并 ${videos.length} 段视频，成片节点已添加`);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "视频合并失败");
        } finally {
            mergeVideoRunningRef.current = false;
            window.setTimeout(() => setMergeVideoProgress(null), 700);
        }
    }, [connectionsRef, message, nodesRef, selectedNodeIdsRef, setConnections, setDialogNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds]);

    const mergeSelectedVideos = useCallback(() => mergeVideosByIds(Array.from(selectedNodeIdsRef.current)), [mergeVideosByIds, selectedNodeIdsRef]);

    const splitImageNode = useCallback(async (node: CanvasNodeData, params: CanvasImageSplitParams) => {
        if (!node.metadata?.content) return;
        setSplitNodeId(null);
        const pieces = await splitDataUrl(node.metadata.content, params);
        const gap = 16;
        const cellWidth = node.width / params.columns;
        const cellHeight = node.height / params.rows;
        const groupPosition = { x: node.position.x + node.width + 96, y: node.position.y };
        const initialChildNodes = await Promise.all(pieces.map(async (piece) => {
            const image = await uploadImage(piece.dataUrl);
            return {
                id: nanoid(),
                type: CanvasNodeType.Image,
                title: `${node.title || "图片"} ${piece.row + 1}-${piece.column + 1}`,
                position: { x: groupPosition.x + piece.column * (cellWidth + gap), y: groupPosition.y + piece.row * (cellHeight + gap) },
                width: cellWidth,
                height: cellHeight,
                metadata: { ...imageMetadata(image), prompt: node.metadata?.prompt },
            } satisfies CanvasNodeData;
        }));
        const childNodes = placeCanvasNodeGroup(nodesRef.current, initialChildNodes, 44, node);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, childNodes));
        setConnections((current) => [...current, ...childNodes.map((child) => ({ id: nanoid(), fromNodeId: node.id, toNodeId: child.id }))]);
        setSelectedNodeIds(new Set(childNodes.map((child) => child.id)));
        setSelectedConnectionId(null);
        setDialogNodeId(null);
        message.success(`已切分为 ${childNodes.length} 个子节点`);
    }, [message, nodesRef, setConnections, setDialogNodeId, setNodes, setSelectedConnectionId, setSelectedNodeIds]);

    const maskEditImageNode = useCallback(async (node: CanvasNodeData, payload: CanvasImageMaskEditPayload) => {
        if (!node.metadata?.content) return;
        const generationConfig = { ...buildGenerationConfig(effectiveConfig, node, "image"), count: "1", size: node.metadata?.size || "auto" };
        if (!isAiConfigReady(generationConfig, generationConfig.model)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        const userPrompt = payload.prompt.trim();
        const prompt = `只修改蒙版透明区域，其他区域保持不变。${userPrompt}`;
        const childId = nanoid();
        const source = nodeReferenceImage(node);
        if (!source) return;
        const annotationNode = payload.annotationNodeId ? nodesRef.current.find((item) => item.id === payload.annotationNodeId) : null;
        const resultOrigin = annotationNode || node;
        const generationMetadata = buildImageGenerationMetadata("edit", generationConfig, 1, [source]);
        const childNode = placeCanvasNodeInContext(nodesRef.current, { id: childId, type: CanvasNodeType.Image, title: userPrompt.slice(0, 32) || "局部编辑结果", position: { x: resultOrigin.position.x + resultOrigin.width + 96, y: resultOrigin.position.y }, width: node.width, height: node.height, metadata: { prompt, status: NODE_STATUS_LOADING, ...generationMetadata, imageAnnotationResultOf: annotationNode?.id } }, undefined, resultOrigin);
        setMaskEditNodeId(null);
        setRunningNode(childId);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [childNode]));
        setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }, ...(annotationNode ? [{ id: nanoid(), fromNodeId: annotationNode.id, toNodeId: childId }] : [])]);
        setSelectedNodeIds(new Set([childId]));
        setSelectedConnectionId(null);
        setDialogNodeId(childId);
        const controller = startGenerationRequest(childId, node.id, childId);
        try {
            const result = await runBackendCanvasGenerationTask({ projectId, nodeId: childId, mode: "image", prompt, config: generationConfig, referenceImages: [source], mask: { id: `${node.id}-mask`, name: "mask.png", type: "image/png", dataUrl: payload.maskDataUrl }, signal: controller.signal, metadata: { sourceNodeId: node.id, edit: annotationNode ? "annotation" : "mask", annotationNodeId: annotationNode?.id }, onTaskCreated: (task) => bindGenerationTask(childId, task) });
            const image = result.images?.[0];
            if (!image?.dataUrl) throw new Error("后端任务没有返回图片");
            const uploaded = await uploadImage(image.dataUrl);
            const size = fitNodeSize(uploaded.width, uploaded.height, node.width, node.height);
            setNodes((current) => current.map((item) => item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt, ...generationMetadata } } : item));
        } catch (error) {
            if (isGenerationCanceled(error)) return;
            const details = error instanceof Error ? error.message : "局部修改失败";
            message.error(details);
            setNodes((current) => current.map((item) => item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails: details } } : item));
        } finally {
            finishGenerationRequest(childId, controller);
        clearRunningNode(childId);
        }
    }, [bindGenerationTask, clearRunningNode, effectiveConfig, finishGenerationRequest, isAiConfigReady, message, nodesRef, projectId, setConnections, setDialogNodeId, setNodes, setRunningNode, setSelectedConnectionId, setSelectedNodeIds, startGenerationRequest]);

    const editAnnotatedImageNode = useCallback(async (annotationNodeId: string, promptOverride?: string) => {
        const annotationNode = nodesRef.current.find((node) => node.id === annotationNodeId);
        const annotation = annotationNode?.metadata?.imageAnnotation;
        if (!annotationNode || annotationNode.type !== CanvasNodeType.Image || !annotation) {
            message.error("没有找到可执行的图片标注节点");
            return;
        }
        const sourceNode = nodesRef.current.find((node) => node.id === annotation.sourceNodeId);
        if (!sourceNode || sourceNode.type !== CanvasNodeType.Image || !sourceNode.metadata?.content) {
            message.error("标注对应的原图已不存在，无法执行修改");
            return;
        }
        try {
            const maskDataUrl = await imageToDataUrl(annotation.mask);
            if (!maskDataUrl) throw new Error("标注遮罩读取失败");
            await maskEditImageNode(sourceNode, {
                prompt: promptOverride?.trim() || annotation.instruction,
                maskDataUrl,
                annotationNodeId,
            });
        } catch (error) {
            message.error(error instanceof Error ? error.message : "标注修改启动失败");
        }
    }, [maskEditImageNode, message, nodesRef]);

    const upscaleImageNode = useCallback(async (node: CanvasNodeData, params: CanvasImageUpscaleParams) => {
        if (!node.metadata?.content) return;
        setUpscaleNodeId(null);
        const upscaled = await upscaleDataUrl(node.metadata.content, params);
        const image = await uploadImage(upscaled);
        const size = fitNodeSize(image.width, image.height);
        const childId = nanoid();
        const child = placeCanvasNodeInContext(nodesRef.current, { id: childId, type: CanvasNodeType.Image, title: "Upscaled Image", position: { x: node.position.x + node.width + 96, y: node.position.y }, width: size.width, height: size.height, metadata: { ...imageMetadata(image), prompt: node.metadata?.prompt } }, undefined, node);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [child]));
        setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
        setSelectedNodeIds(new Set([childId]));
        setDialogNodeId(childId);
    }, [nodesRef, setConnections, setDialogNodeId, setNodes, setSelectedNodeIds]);

    const generateAngleNode = useCallback(async (node: CanvasNodeData, params: CanvasImageAngleParams) => {
        if (!node.metadata?.content) return;
        const generationConfig = { ...buildGenerationConfig(effectiveConfig, node, "image"), count: "1" };
        if (!isAiConfigReady(generationConfig, generationConfig.model)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
        const childId = nanoid();
        const imageSpec = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
        const title = buildAngleLabel(params);
        const prompt = buildAnglePrompt(params);
        const source = nodeReferenceImage(node);
        if (!source) return;
        const generationMetadata = buildImageGenerationMetadata("edit", generationConfig, 1, [source]);
        const childNode = placeCanvasNodeInContext(nodesRef.current, { id: childId, type: CanvasNodeType.Image, title, position: { x: node.position.x + node.width + 96, y: node.position.y }, width: imageSpec.width, height: imageSpec.height, metadata: { prompt, status: NODE_STATUS_LOADING, ...generationMetadata } }, undefined, node);
        setAngleNodeId(null);
        setRunningNode(childId);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [childNode]));
        setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
        setSelectedNodeIds(new Set([childId]));
        setDialogNodeId(childId);
        const controller = startGenerationRequest(childId, node.id, childId);
        try {
            const result = await runBackendCanvasGenerationTask({ projectId, nodeId: childId, mode: "image", prompt, config: generationConfig, referenceImages: [source], signal: controller.signal, metadata: { sourceNodeId: node.id, edit: "angle" }, onTaskCreated: (task) => bindGenerationTask(childId, task) });
            const image = result.images?.[0];
            if (!image?.dataUrl) throw new Error("后端任务没有返回图片");
            const uploaded = await uploadImage(image.dataUrl);
            const size = fitNodeSize(uploaded.width, uploaded.height, imageSpec.width, imageSpec.height);
            setNodes((current) => current.map((item) => item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt, ...generationMetadata } } : item));
        } catch (error) {
            if (isGenerationCanceled(error)) return;
            const details = error instanceof Error ? error.message : "生成失败";
            setNodes((current) => current.map((item) => item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails: details } } : item));
        } finally {
            finishGenerationRequest(childId, controller);
        clearRunningNode(childId);
        }
    }, [bindGenerationTask, clearRunningNode, effectiveConfig, finishGenerationRequest, isAiConfigReady, nodesRef, projectId, setConnections, setDialogNodeId, setNodes, setRunningNode, setSelectedNodeIds, startGenerationRequest]);

    const generateEmotionNode = useCallback(async (node: CanvasNodeData, payload: CanvasImageEmotionPayload) => {
        if (!node.metadata?.content) return;
        const baseConfig = buildGenerationConfig(effectiveConfig, node, "image");
        const providerSize = emotionGenerationSize(payload.editRegion);
        const generationConfig = { ...baseConfig, count: "1", size: providerSize, quality: !baseConfig.quality || baseConfig.quality === "auto" ? "high" : baseConfig.quality };
        if (!isAiConfigReady(generationConfig, generationConfig.model)) { navigateToSettings({ continueCreation: true }); return; }
        if (resolveModelRequestConfig(generationConfig, generationConfig.model).interfaceType !== "openai-image") {
            message.error("表情编辑需要支持蒙版的 OpenAI Images 渠道，当前渠道已拒绝整图重绘");
            return;
        }
        const source = nodeReferenceImage(node);
        if (!source) return;
        const editReference = {
            id: `${node.id}-${payload.presetId}-edit-region`,
            name: "emotion-edit-region.png",
            type: "image/png",
            dataUrl: payload.sourceDataUrl,
        };
        const characterReference = {
            id: `${node.id}-${payload.presetId}-character`,
            name: `${payload.characterName}-face.jpg`,
            type: "image/jpeg",
            dataUrl: payload.characterDataUrl,
        };
        const childId = nanoid();
        const generationMetadata = { ...buildImageGenerationMetadata("edit", generationConfig, 1, [source]), size: `${payload.imageWidth}x${payload.imageHeight}` };
        const emotionEdit = { sourceNodeId: node.id, characterName: payload.characterName, presetId: payload.presetId, intimacy: payload.intimacy, arousal: payload.arousal, label: payload.label, faceBox: payload.faceBox, editRegion: payload.editRegion, sourceWidth: payload.imageWidth, sourceHeight: payload.imageHeight, providerSize };
        const childNode = placeCanvasNodeInContext(nodesRef.current, { id: childId, type: CanvasNodeType.Image, title: `${payload.characterName} · ${payload.label}`, position: { x: node.position.x + node.width + 96, y: node.position.y }, width: node.width, height: node.height, metadata: { prompt: payload.prompt, status: NODE_STATUS_LOADING, ...generationMetadata, emotionEdit } }, undefined, node);
        setEmotionNodeId(null);
        setRunningNode(childId);
        setNodes((current) => appendCanvasNodesWithFrameExpansion(current, [childNode]));
        setConnections((current) => [...current, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
        setSelectedNodeIds(new Set([childId]));
        setSelectedConnectionId(null);
        setDialogNodeId(childId);
        const controller = startGenerationRequest(childId, node.id, childId);
        try {
            const result = await runBackendCanvasGenerationTask({ projectId, nodeId: childId, mode: "image", prompt: payload.prompt, config: generationConfig, referenceImages: [editReference, characterReference], mask: { id: `${node.id}-emotion-mask`, name: "emotion-mask.png", type: "image/png", dataUrl: payload.maskDataUrl }, signal: controller.signal, metadata: { sourceNodeId: node.id, edit: "emotion", emotion: emotionEdit }, onTaskCreated: (task) => bindGenerationTask(childId, task) });
            const image = result.images?.[0];
            if (!image?.dataUrl) throw new Error("后端任务没有返回图片");
            const composited = await compositeEmotionImage(node.metadata.content, image.dataUrl, payload.editRegion, payload.faceBox);
            const uploaded = await uploadImage(composited);
            const size = fitNodeSize(uploaded.width, uploaded.height, node.width, node.height);
            setNodes((current) => current.map((item) => item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt: payload.prompt, ...generationMetadata, emotionEdit } } : item));
        } catch (error) {
            if (isGenerationCanceled(error)) return;
            const details = error instanceof Error ? error.message : "表情生成失败";
            message.error(details);
            setNodes((current) => current.map((item) => item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails: details } } : item));
        } finally { finishGenerationRequest(childId, controller); clearRunningNode(childId); }
    }, [bindGenerationTask, clearRunningNode, effectiveConfig, finishGenerationRequest, isAiConfigReady, message, nodesRef, projectId, setConnections, setDialogNodeId, setNodes, setRunningNode, setSelectedConnectionId, setSelectedNodeIds, startGenerationRequest]);

    return {
        angleNodeId,
        emotionNodeId,
        annotationNodeId,
        createImageReversePromptNodes,
        cropImageNode,
        cropNodeId,
        editAnnotatedImageNode,
        extractVideoLastFrame,
        extractingVideoFrameNodeId,
        generateAngleNode,
        maskEditImageNode,
        maskEditNodeId,
        mergeSelectedVideos,
        mergeVideosByIds,
        mergeVideoProgress,
        saveAnnotatedImageNode,
        setAngleNodeId,
        generateEmotionNode,
        setEmotionNodeId,
        setAnnotationNodeId,
        setCropNodeId,
        setMaskEditNodeId,
        setSplitNodeId,
        setUpscaleNodeId,
        splitImageNode,
        splitNodeId,
        upscaleImageNode,
        upscaleNodeId,
    };
}
