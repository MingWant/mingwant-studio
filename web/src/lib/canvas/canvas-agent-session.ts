import {
    agentSessionFailureMessage,
    createAgentSession,
    isRetryableTaskCenterRequestError,
    queryAgentSession,
    type AgentSessionDetail,
    type CreateSessionInput,
} from "@/services/api/task-center";

const CINEMATIC_SESSION_POLL_INTERVAL_MS = 5_000;
const CINEMATIC_SESSION_MAX_RETRY_INTERVAL_MS = 30_000;

export class AgentSessionTrackingError extends Error {
    constructor(message: string) {
        super(message);
        this.name = "AgentSessionTrackingError";
    }
}

type CinematicSessionWaitOptions = {
    signal?: AbortSignal;
};

type CreateCinematicSessionOptions = CinematicSessionWaitOptions & {
    onCreated?: (detail: AgentSessionDetail) => void;
};

export async function createCinematicAgentSession(input: CreateSessionInput, options: CreateCinematicSessionOptions = {}) {
    let created: AgentSessionDetail;
    try {
        created = await createAgentSession(input, { signal: options.signal });
    } catch (error) {
        if (isRetryableTaskCenterRequestError(error)) {
            const detail = error instanceof Error ? error.message : "创建响应无法确认";
            throw new AgentSessionTrackingError(`无法确认后端是否已经创建影视 Agent 会话：${detail}。原创建标识已保留，系统不会生成新标识；请勿重新提交，恢复服务或网络后重新打开画布继续接管。`);
        }
        throw error;
    }
    throwIfAborted(options.signal);
    options.onCreated?.(created);
    return waitForCinematicAgentSession(created, options);
}

export async function resumeCinematicAgentSession(id: string, options: CinematicSessionWaitOptions = {}) {
    throwIfAborted(options.signal);
    const detail = await queryCinematicAgentSession(id, options);
    return waitForCinematicAgentSession(detail, options);
}

export function cinematicAgentSessionOpsJson(detail: AgentSessionDetail) {
    if (detail.session.status !== "completed") throw new Error("后端影视 Agent 会话尚未完成");
    if (!detail.session.canvasOpsJson) throw new Error("后端影视 Agent 没有返回画布操作");
    return detail.session.canvasOpsJson;
}

export function isAgentSessionPollingAbort(error: unknown) {
    return error instanceof Error && error.name === "AbortError";
}

export function isAgentSessionTrackingError(error: unknown): error is AgentSessionTrackingError {
    return error instanceof AgentSessionTrackingError;
}

async function waitForCinematicAgentSession(initialDetail: AgentSessionDetail, options: CinematicSessionWaitOptions) {
    let detail = initialDetail;
    // 后端任务策略才是运行时限的唯一事实源；页面只跟踪持久会话，不能在四分钟左右抢先判失败。
    for (;;) {
        throwIfAborted(options.signal);
        if (detail.session.status === "completed") return detail;
        if (detail.session.status === "failed") throw new Error(agentSessionFailureMessage(detail));
        await abortableDelay(CINEMATIC_SESSION_POLL_INTERVAL_MS, options.signal);
        detail = await queryCinematicAgentSession(initialDetail.session.id, options);
    }
}

async function queryCinematicAgentSession(id: string, options: CinematicSessionWaitOptions) {
    let failureCount = 0;
    for (;;) {
        throwIfAborted(options.signal);
        try {
            return await queryAgentSession(id, { signal: options.signal });
        } catch (error) {
            throwIfAborted(options.signal);
            if (!isRetryableTaskCenterRequestError(error)) {
                const detail = error instanceof Error ? error.message : "状态查询失败";
                throw new AgentSessionTrackingError(`无法继续查询后端影视 Agent 会话：${detail}。后台任务状态未知，系统已保留原会话绑定；请勿重新提交，恢复登录或服务后重新打开画布继续跟踪。`);
            }
            const retryDelay = Math.min(CINEMATIC_SESSION_POLL_INTERVAL_MS * (2 ** Math.min(failureCount, 3)), CINEMATIC_SESSION_MAX_RETRY_INTERVAL_MS);
            failureCount += 1;
            await abortableDelay(retryDelay, options.signal);
        }
    }
}

function abortableDelay(ms: number, signal?: AbortSignal) {
    return new Promise<void>((resolve, reject) => {
        throwIfAborted(signal);
        const finish = () => {
            signal?.removeEventListener("abort", abort);
            resolve();
        };
        const abort = () => {
            window.clearTimeout(timer);
            reject(new DOMException("Aborted", "AbortError"));
        };
        const timer = window.setTimeout(finish, ms);
        signal?.addEventListener("abort", abort, { once: true });
    });
}

function throwIfAborted(signal?: AbortSignal) {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
}
