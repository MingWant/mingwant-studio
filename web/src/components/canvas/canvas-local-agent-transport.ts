import type { CanvasAgentSnapshot } from "@/lib/canvas/canvas-agent-ops";

const AGENT_BRIDGE_TIMEOUT_MS = 15_000;

export type CanvasAgentBridgeTarget = {
    endpoint: string;
    token: string;
    clientId: string;
};

export type CanvasAgentToolResultBody = {
    requestId: string;
    result?: unknown;
    error?: string;
};

/**
 * 状态写入必须串行，避免较慢的旧快照在较新的快照之后到达本地 Agent。
 * 单次失败不会阻塞后续重试，但调用方仍会收到该次失败并停止当前发送或工具链。
 */
export class CanvasAgentStateSyncQueue {
    private tail: Promise<void> = Promise.resolve();

    enqueue(target: CanvasAgentBridgeTarget, snapshot: CanvasAgentSnapshot) {
        let body: string;
        try {
            body = JSON.stringify(snapshot);
        } catch (error) {
            return Promise.reject(new Error(error instanceof Error ? `画布状态序列化失败：${error.message}` : "画布状态序列化失败"));
        }
        const task = this.tail.catch(() => undefined).then(() => postAgentJSON(target, "/canvas/state", body, "同步画布状态"));
        this.tail = task;
        return task;
    }
}

export function postCanvasAgentToolResult(target: CanvasAgentBridgeTarget, body: CanvasAgentToolResultBody) {
    return postAgentJSON(target, "/canvas/result", JSON.stringify(body), "回传工具结果");
}

async function postAgentJSON(target: CanvasAgentBridgeTarget, path: string, body: string, action: string) {
    const controller = new AbortController();
    const timer = globalThis.setTimeout(() => controller.abort(), AGENT_BRIDGE_TIMEOUT_MS);
    try {
        const response = await fetch(`${target.endpoint}${path}?clientId=${encodeURIComponent(target.clientId)}`, {
            method: "POST",
            headers: { "content-type": "application/json", "X-Canvas-Agent-Token": target.token },
            body,
            signal: controller.signal,
        });
        const payload = (await response.json().catch(() => null)) as { ok?: boolean; error?: string; msg?: string } | null;
        if (!response.ok) throw new Error(payload?.error || payload?.msg || `${action}失败（HTTP ${response.status}）`);
        if (payload?.ok !== true) throw new Error(`${action}响应格式异常`);
    } catch (error) {
        if (error instanceof Error && error.name === "AbortError") throw new Error(`${action}超时，请检查本地 Agent 连接`);
        if (error instanceof Error) throw error;
        throw new Error(`${action}失败`);
    } finally {
        globalThis.clearTimeout(timer);
    }
}
