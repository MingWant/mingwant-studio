import axios from "axios";
import { nanoid } from "nanoid";

import { generationErrorMessage } from "@/lib/generation-error";

export type TaskStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";
export type TaskBillingStatus = "reserved" | "running" | "settled" | "refunded" | "uncertain";
export type AgentSessionStatus = "active" | "completed" | "failed";

export type BackendEnvelope<T> = {
    code: number;
    data: T;
    msg: string;
};

export class TaskCenterRequestError extends Error {
    readonly status?: number;
    readonly retryable: boolean;

    constructor(message: string, options: { status?: number; retryable: boolean }) {
        super(message);
        this.name = "TaskCenterRequestError";
        this.status = options.status;
        this.retryable = options.retryable;
    }
}

export function isRetryableTaskCenterRequestError(error: unknown) {
    return error instanceof TaskCenterRequestError && error.retryable;
}

export type GenerationTask = {
    id: string;
    sessionId?: string;
    projectId?: string;
    type: string;
    status: TaskStatus;
    progress?: number;
    stage?: string;
    prompt: string;
    operation?: string;
    provider?: string;
    model?: string;
    providerRequestId?: string;
    errorCode?: string;
    previewUrl?: string;
    previewKind?: "image" | "video";
    deliveryRecoverable?: boolean;
    inputJson?: string;
    resultJson?: string;
    error?: string;
    attempts: number;
    nextPollAt?: string;
    startedAt?: string;
    completedAt?: string;
    createdAt: string;
    updatedAt: string;
    billing?: {
        amountMicrocredits: number;
        status: TaskBillingStatus;
    };
    created_at?: string;
    updated_at?: string;
};

export type ProviderTaskQueryResult = {
    task: GenerationTask;
    providerStatus: string;
    recovered: boolean;
    billingSettled: boolean;
};

export type TaskDeliveryRecoveryResult = {
    task: GenerationTask;
    billingSettled: boolean;
};

export type AgentSession = {
    id: string;
    projectId?: string;
    status: AgentSessionStatus;
    prompt: string;
    canvasSnapshotJson?: string;
    canvasOpsJson?: string;
    createdAt: string;
    updatedAt: string;
};

export type AgentMessage = {
    id: string;
    sessionId: string;
    role: "user" | "assistant" | "system" | "tool" | string;
    content: string;
    payload?: string;
    createdAt: string;
};

export type TaskResult = {
    id: string;
    taskId: string;
    sessionId?: string;
    kind: string;
    url?: string;
    payload?: string;
    createdAt: string;
};

export type SessionFile = {
    id: string;
    sessionId: string;
    fileName: string;
    mimeType: string;
	size: number;
    createdAt: string;
};

export type TaskLog = {
    id: string;
    taskId: string;
    level: "info" | "warn" | "error" | string;
    message: string;
    payload?: string;
    createdAt: string;
};

export type AgentSessionDetail = {
    session: AgentSession;
    messages: AgentMessage[];
    tasks: GenerationTask[];
    results: TaskResult[];
};

export type CreateSessionInput = {
    requestKey?: string;
    projectId?: string;
    prompt: string;
    canvasSnapshot?: Record<string, unknown>;
    references?: string[];
    config?: Record<string, unknown>;
    channelProbeTaskId?: string;
    toolProbeTaskId?: string;
    allowPaidStructureRepair?: boolean;
};

export type CreateTaskInput = {
    sessionId?: string;
    projectId?: string;
    type?: string;
    operation?: string;
    prompt: string;
    provider?: string;
    model?: string;
    sourceTaskId?: string;
    confirmNewProviderRequest?: boolean;
    input?: Record<string, unknown>;
};

type WaitForGenerationTaskOptions = {
    signal?: AbortSignal;
    intervalMs?: number;
    /** 仅限制页面跟踪时长；后台任务超时由服务端运行策略统一裁决。 */
    timeoutMs?: number;
    initialTask?: GenerationTask;
    onTaskUpdate?: (task: GenerationTask) => void;
};

const GENERATION_TASK_TRACKING_ABORT_REASON = "mingwant:task-already-cancelled";
const TASK_CONTROL_REQUEST_TIMEOUT_MS = 30_000;
const CREATE_SESSION_MAX_ATTEMPTS = 3;
const generationTaskTrackingStops = new Map<string, Set<() => void>>();

const api = axios.create({ baseURL: import.meta.env.VITE_CANVAS_BACKEND_URL || "/api", withCredentials: true });

async function request<T>(promise: Promise<{ data: BackendEnvelope<T> }>) {
    try {
        const response = await promise;
        if (response.data.code !== 0) throw new TaskCenterRequestError(response.data.msg || "请求失败", { retryable: false });
        return response.data.data;
    } catch (error) {
        if (error instanceof TaskCenterRequestError) throw error;
        if (axios.isAxiosError<BackendEnvelope<unknown>>(error)) {
            const status = error.response?.status;
            const retryable = status === undefined || status === 408 || status === 425 || status === 429 || status >= 500;
            throw new TaskCenterRequestError(error.response?.data?.msg || error.message || "请求失败", { status, retryable });
        }
        throw error;
    }
}

export async function createAgentSession(input: CreateSessionInput, options?: { signal?: AbortSignal }) {
    // 同一调用的所有传输重试复用请求键；即使首个响应丢失，服务端也只能返回原会话和原计费任务。
    const payload = { ...input, requestKey: input.requestKey?.trim() || nanoid() };
    for (let attempt = 0; attempt < CREATE_SESSION_MAX_ATTEMPTS; attempt += 1) {
        try {
            const detail = await request<AgentSessionDetail>(api.post("/create_session", payload, { signal: options?.signal }));
            detail.tasks.forEach((task) => notifyCanvasTaskCreated(task));
            return detail;
        } catch (error) {
            if (options?.signal?.aborted) throw new DOMException("Aborted", "AbortError");
            if (!isRetryableTaskCenterRequestError(error) || attempt + 1 >= CREATE_SESSION_MAX_ATTEMPTS) throw error;
            await delay(2_000 * (attempt + 1), options?.signal);
        }
    }
    throw new Error("创建影视 Agent 会话失败");
}

export function queryAgentSession(id: string, options?: { signal?: AbortSignal }) {
    return request<AgentSessionDetail>(api.get(`/query_session/${encodeURIComponent(id)}`, { signal: options?.signal }));
}

export function agentSessionFailureMessage(detail: AgentSessionDetail, fallback = "后端影视 Agent 会话失败") {
    for (let index = detail.tasks.length - 1; index >= 0; index -= 1) {
        const task = detail.tasks[index];
        if ((task.status === "failed" || task.status === "cancelled") && task.error?.trim()) return generationErrorMessage(task.error.trim());
    }
    for (let index = detail.messages.length - 1; index >= 0; index -= 1) {
        const message = detail.messages[index];
        if (message.role === "assistant" && message.content.trim()) return generationErrorMessage(message.content.trim());
    }
    return fallback;
}

export function downloadSessionResults(id: string) {
    return request<TaskResult[]>(api.get(`/download_results/${encodeURIComponent(id)}`));
}

export function uploadAgentFile(sessionId: string, file: File) {
    const formData = new FormData();
    formData.append("sessionId", sessionId);
    formData.append("file", file);
    return request<SessionFile>(api.post("/upload_file", formData));
}

export function createGenerationTask(input: CreateTaskInput) {
    return request<GenerationTask>(api.post("/tasks", input)).then((task) => {
        notifyCanvasTaskCreated(task);
        return task;
    });
}

export function listGenerationTasks(limit = 30, options?: { projectId?: string; activeOnly?: boolean }) {
    return request<GenerationTask[]>(api.get("/tasks", { params: { limit, projectId: options?.projectId, activeOnly: options?.activeOnly || undefined } })).then((tasks) => {
        if (!Array.isArray(tasks)) throw new Error("任务列表数据格式异常");
        return tasks;
    });
}

export function queryGenerationTask(id: string, options?: { includeBilling?: boolean; timeoutMs?: number }) {
    return request<GenerationTask>(api.get(`/tasks/${encodeURIComponent(id)}`, { params: { includeBilling: options?.includeBilling || undefined }, timeout: options?.timeoutMs }));
}

export function retryGenerationTask(id: string, confirmNewProviderRequest: boolean) {
    return request<GenerationTask>(api.post(`/tasks/${encodeURIComponent(id)}/retry`, { confirmNewProviderRequest }));
}

export function queryFailedProviderTask(id: string) {
    return request<ProviderTaskQueryResult>(api.post(`/tasks/${encodeURIComponent(id)}/query-provider`));
}

export function recoverGenerationTaskDelivery(id: string) {
    return request<TaskDeliveryRecoveryResult>(api.post(`/tasks/${encodeURIComponent(id)}/recover-delivery`));
}

export async function cancelGenerationTask(id: string) {
    try {
        return await request<GenerationTask>(api.post(`/tasks/${encodeURIComponent(id)}/cancel`, undefined, { timeout: TASK_CONTROL_REQUEST_TIMEOUT_MS }));
    } catch (cancelError) {
        const cancelDetail = cancelError instanceof Error ? cancelError.message : "取消请求失败";
        let latest: GenerationTask;
        try {
            // 写响应丢失不代表取消没有生效；先读回原任务，避免用户因假失败重复操作。
            latest = await queryGenerationTask(id, { timeoutMs: TASK_CONTROL_REQUEST_TIMEOUT_MS });
        } catch (queryError) {
            const queryDetail = queryError instanceof Error ? queryError.message : "任务状态查询失败";
            throw new Error(`后台取消结果无法确认：${cancelDetail}；${queryDetail}。原任务可能仍在供应商执行并产生费用，请到任务中心核对，勿立即重试。`);
        }
        if (latest.status === "cancelled" || latest.status === "failed") return latest;
        if (latest.status === "succeeded") throw new Error("停止请求到达前任务已经成功，不能再取消；请从任务中心恢复原结果，勿重新生成。");
        throw new Error(`后台取消尚未生效（任务仍为${latest.status === "running" ? "运行中" : "排队中"}）：${cancelDetail}。原任务可能继续执行并产生费用，请到任务中心核对，勿立即重试。`);
    }
}

/** 后台取消已经明确成功时，只终止本页轮询，避免 AbortSignal 再发送一次取消请求。 */
export function abortGenerationTaskTracking(taskId: string) {
    generationTaskTrackingStops.get(taskId)?.forEach((stop) => stop());
}

export function listTaskLogs(id: string) {
    return request<TaskLog[]>(api.get(`/tasks/${encodeURIComponent(id)}/logs`));
}

export async function waitForGenerationTask(id: string, options?: WaitForGenerationTaskOptions) {
    const startedAt = Date.now();
    const intervalMs = Math.max(1_000, options?.intervalMs ?? 5_000);
    const trackingDeadline = options?.timeoutMs === undefined ? undefined : startedAt + Math.max(1_000, options.timeoutMs);
    const sourceSignal = options?.signal;
    const trackingController = new AbortController();
    const forwardSourceAbort = () => trackingController.abort(sourceSignal?.reason);
    const stopTrackingOnly = () => trackingController.abort(GENERATION_TASK_TRACKING_ABORT_REASON);
    if (sourceSignal?.aborted) forwardSourceAbort();
    else sourceSignal?.addEventListener("abort", forwardSourceAbort, { once: true });
    const taskStops = generationTaskTrackingStops.get(id) || new Set<() => void>();
    taskStops.add(stopTrackingOnly);
    generationTaskTrackingStops.set(id, taskStops);
    const signal = trackingController.signal;
    let lastQueryError: unknown;
    let queryFailureCount = 0;
    let cancelPromise: Promise<GenerationTask> | undefined;
    const cancelAfterAbort = () => {
        cancelPromise ||= cancelGenerationTask(id).then((task) => {
            // 中止信号在画布生成链路中代表用户确认取消；取消终态必须回填，不能让节点伪装成可立即重试。
            options?.onTaskUpdate?.(task);
            window.dispatchEvent(new CustomEvent("wallet:updated"));
            return task;
        });
        return cancelPromise;
    };
    const trackingOnlyAbort = () => signal.reason === GENERATION_TASK_TRACKING_ABORT_REASON;
    const abortListener = () => {
        if (!trackingOnlyAbort()) void cancelAfterAbort().catch(() => undefined);
    };
    signal.addEventListener("abort", abortListener, { once: true });

    const finishCancellation = async () => {
        if (trackingOnlyAbort()) throw new DOMException("Aborted", "AbortError");
        try {
            await cancelAfterAbort();
        } catch (cancelError) {
            if (cancelError instanceof Error) throw cancelError;
            throw new Error("后台取消结果无法确认。原任务可能仍在供应商执行并产生费用，请到任务中心核对，勿立即重试。");
        }
        throw new DOMException("Aborted", "AbortError");
    };

    try {
        // 页面不能用一组固定分钟数抢先判失败；管理员可调整运行策略，只有后台终态才代表任务真正结束。
        for (;;) {
            if (trackingDeadline !== undefined && Date.now() >= trackingDeadline) {
                const detail = lastQueryError instanceof Error ? `最后一次状态同步失败：${lastQueryError.message}。` : "";
                throw new Error(`已停止等待任务状态。${detail}后台任务可能仍在执行并产生费用，请到任务中心查看原任务，勿立即重试。`);
            }
            if (signal.aborted) {
                throw new DOMException("Aborted", "AbortError");
            }
            let task: GenerationTask;
            try {
                task = await queryGenerationTask(id);
                lastQueryError = undefined;
                queryFailureCount = 0;
                options?.onTaskUpdate?.(task);
                // 显式取消可能在查询飞行期间完成；优先结束对应轮询，避免再把已处理的取消显示成一次生成错误。
                if (signal.aborted) throw new DOMException("Aborted", "AbortError");
            } catch (error) {
                lastQueryError = error;
                // 断网、限流和 5xx 可以继续跟踪；鉴权失败、404 与业务拒绝不会靠轮询自行恢复。
                if (!isRetryableTaskCenterRequestError(error)) throw error;
                const retryDelay = Math.min(intervalMs * (2 ** Math.min(queryFailureCount, 3)), 30_000);
                queryFailureCount += 1;
                await delay(retryDelay, signal);
                continue;
            }
            if (task.status === "succeeded") {
                window.dispatchEvent(new CustomEvent("wallet:updated"));
                return task;
            }
            if (task.status === "failed" || task.status === "cancelled") {
                window.dispatchEvent(new CustomEvent("wallet:updated"));
                throw new Error(task.error ? generationErrorMessage(task.error) : `任务${task.status === "cancelled" ? "已取消" : "失败"}`);
            }
            await delay(intervalMs, signal);
        }
    } catch (error) {
        if (signal.aborted) {
            if (error instanceof Error && error.name !== "AbortError") throw error;
            await finishCancellation();
        }
        throw error;
    } finally {
        signal.removeEventListener("abort", abortListener);
        sourceSignal?.removeEventListener("abort", forwardSourceAbort);
        taskStops.delete(stopTrackingOnly);
        if (!taskStops.size) generationTaskTrackingStops.delete(id);
    }
}

function delay(ms: number, signal?: AbortSignal) {
    return new Promise<void>((resolve, reject) => {
        if (signal?.aborted) {
            reject(new DOMException("Aborted", "AbortError"));
            return;
        }
        const onAbort = () => {
            window.clearTimeout(timer);
            reject(new DOMException("Aborted", "AbortError"));
        };
        const timer = window.setTimeout(() => {
            signal?.removeEventListener("abort", onAbort);
            resolve();
        }, ms);
        signal?.addEventListener("abort", onAbort, { once: true });
    });
}

function notifyCanvasTaskCreated(task: GenerationTask) {
    if (typeof window === "undefined" || !task.projectId) return;
    window.dispatchEvent(new CustomEvent("canvas:task-created", { detail: { task } }));
}
