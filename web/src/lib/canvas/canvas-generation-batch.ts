import { confirmsProviderWasNotCalled } from "@/lib/provider-request-error";
import type { CanvasGenerationBatch, CanvasGenerationBatchStatus } from "@/types/canvas";

const TASK_CAPACITY_MESSAGE = /同时排队或运行的任务最多 \d+ 个/;

export function isGenerationTaskCapacityError(error: unknown) {
    return error instanceof Error && TASK_CAPACITY_MESSAGE.test(error.message);
}

export function isGenerationCostUncertainError(error: unknown) {
    const message = error instanceof Error ? error.message : String(error || "");
    if (confirmsProviderWasNotCalled(message)) return false;
    return /(?:^|\D)(?:504|524)(?:\D|$)|费用.{0,8}(?:不确定|待核对|待确认)|扣费状态不确定|可能(?:已经|仍在).{0,12}(?:产生费用|计费)|重复(?:调用|生成|计费|扣费)|未再次调用供应商|核对.{0,16}(?:供应商后台|账单)|(?:需|需要).{0,8}(?:管理员)?核对|积分退回失败|计费状态更新失败|连接中断|等待超时|gateway timeout|deadline exceeded|connection reset|unexpected eof|i\/o timeout/i.test(message);
}

export function generationBatchStatus(batch: CanvasGenerationBatch): CanvasGenerationBatchStatus {
    const statuses = batch.items.map((item) => item.status);
    if (statuses.length > 0 && statuses.every((status) => status === "succeeded")) return "completed";
    if (statuses.some((status) => status === "waiting" || status === "submitting" || status === "queued" || status === "running")) {
        return statuses.some((status) => status !== "waiting") ? "running" : "queued";
    }
    if (statuses.some((status) => status === "failed")) return "partial_failed";
    return "cancelled";
}
