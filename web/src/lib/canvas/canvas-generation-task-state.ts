import type { CanvasNodeData } from "@/types/canvas";

/**
 * 本地运行状态会在刷新后丢失，后台任务绑定才是禁止重复提交的权威依据。
 * 成功/失败终态允许后续操作；成功但尚未回填内容的节点仍会保持 loading。
 */
export function hasPendingCanvasGenerationTask(node: CanvasNodeData | null | undefined) {
    const metadata = node?.metadata;
    if (metadata?.taskRecoveryUncertain) return true;
    if (!metadata?.taskId) return false;
    return metadata.status === "loading" || metadata.taskStatus === "queued" || metadata.taskStatus === "running";
}
