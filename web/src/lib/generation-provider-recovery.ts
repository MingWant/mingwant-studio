import type { GenerationTask } from "@/services/api/task-center";

export function hasRecoverableProviderResult(task: GenerationTask) {
    if (task.billing?.status === "refunded") return false;
    if (!task.providerRequestId) return false;
    if (task.type.startsWith("canvas_video") || task.type.startsWith("video_")) return true;
    return task.type.startsWith("canvas_image") && task.providerRequestId.startsWith("xai-file:");
}

export function canQueryProviderTask(task: GenerationTask) {
    return (task.status === "failed" || task.status === "cancelled") && hasRecoverableProviderResult(task);
}
