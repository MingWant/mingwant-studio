import { isGenerationCostUncertainError } from "@/lib/canvas/canvas-generation-batch";
import { queryGenerationTask, type GenerationTask } from "@/services/api/task-center";

export type GenerationRetryInspection = {
    sourceTask?: GenerationTask;
    blockedReason?: string;
    costUncertain: boolean;
};

/** 重试前必须读取后台权威状态；查询失败也不能降级成“允许新请求”。 */
export async function inspectGenerationRetry(sourceTaskId?: string): Promise<GenerationRetryInspection> {
    if (!sourceTaskId) return { costUncertain: false };
    let sourceTask: GenerationTask;
    try {
        sourceTask = await queryGenerationTask(sourceTaskId, { includeBilling: true });
    } catch (error) {
        const detail = error instanceof Error ? `：${error.message}` : "";
        throw new Error(`无法核对原任务状态${detail}。为避免重复调用和计费，本次未重新生成，请先到任务中心确认原任务。`);
    }
    if (sourceTask.status === "succeeded") {
        return { sourceTask, costUncertain: false, blockedReason: "原后台任务已经成功，请先到任务中心恢复结果，不要重复调用供应商。" };
    }
    if (sourceTask.status === "queued" || sourceTask.status === "running") {
        return { sourceTask, costUncertain: true, blockedReason: "原后台任务仍在排队或运行，请继续跟踪原任务，不要重复提交。" };
    }
    const billingPending = sourceTask.billing?.status === "reserved" || sourceTask.billing?.status === "running" || sourceTask.billing?.status === "uncertain";
    if (billingPending) {
        const canRecoverVideo = Boolean(sourceTask.providerRequestId) && (sourceTask.type.startsWith("canvas_video") || sourceTask.type.startsWith("video_"));
        if (canRecoverVideo) {
            return { sourceTask, costUncertain: true, blockedReason: "原视频任务已有上游任务，请先在任务详情手动查询；无法恢复时再创建新的请求。" };
        }
        // 文本、图片和音频没有可查询的上游任务时，不再把用户锁死在管理员审核。
        // 调用方会弹出一次明确的重复计费确认，后端仍会再次校验确认事实。
        return { sourceTask, costUncertain: true };
    }
    if (sourceTask.status !== "failed" && sourceTask.status !== "cancelled") {
        return { sourceTask, costUncertain: true, blockedReason: "原任务状态不允许重新生成，请先到任务中心核对。" };
    }
    return { sourceTask, costUncertain: isGenerationCostUncertainError(sourceTask.error) };
}
