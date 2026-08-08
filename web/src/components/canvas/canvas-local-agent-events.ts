const MAX_EVENT_BUFFER_CHARS = 32 * 1024 * 1024;

export type CanvasAgentEventConnection = {
    close: () => void;
};

export type CanvasAgentEventMessage = {
    type: string;
    data: string;
};

export function openCanvasAgentEvents(
    target: { endpoint: string; token: string; clientId: string },
    handlers: { onEvent: (event: CanvasAgentEventMessage) => void; onError: (error: Error) => void },
): CanvasAgentEventConnection {
    const controller = new AbortController();
    let closed = false;
    void consumeCanvasAgentEvents(target, controller.signal, handlers.onEvent).catch((error) => {
        if (!closed) {
            closed = true;
            controller.abort();
            handlers.onError(error instanceof Error ? error : new Error("本地 Agent 事件流异常"));
        }
    });
    return {
        close: () => {
            closed = true;
            controller.abort();
        },
    };
}

async function consumeCanvasAgentEvents(target: { endpoint: string; token: string; clientId: string }, signal: AbortSignal, onEvent: (event: CanvasAgentEventMessage) => void) {
    const response = await fetch(`${target.endpoint}/events?clientId=${encodeURIComponent(target.clientId)}`, {
        headers: {
            Accept: "text/event-stream",
            "X-Canvas-Agent-Token": target.token,
        },
        cache: "no-store",
        signal,
    });
    if (!response.ok) {
        const payload = (await response.json().catch(() => null)) as { error?: string; msg?: string } | null;
        throw new Error(payload?.error || payload?.msg || `本地 Agent 事件连接失败（HTTP ${response.status}）`);
    }
    if (!response.body || !response.headers.get("content-type")?.toLowerCase().includes("text/event-stream")) {
        throw new Error("本地 Agent 未返回事件流");
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
        while (true) {
            const { done, value } = await reader.read();
            if (done) throw new Error("本地 Agent 事件流已结束");
            buffer += decoder.decode(value, { stream: true });
            if (buffer.length > MAX_EVENT_BUFFER_CHARS) throw new Error("本地 Agent 单个事件超过 32MB，已停止连接");
            let boundary = eventBoundary(buffer);
            while (boundary) {
                const block = buffer.slice(0, boundary.index);
                buffer = buffer.slice(boundary.index + boundary.length);
                const event = parseCanvasAgentEventBlock(block);
                if (event) onEvent(event);
                boundary = eventBoundary(buffer);
            }
        }
    } finally {
        await reader.cancel().catch(() => undefined);
        reader.releaseLock();
    }
}

function eventBoundary(value: string) {
    const match = /\r?\n\r?\n/.exec(value);
    return match ? { index: match.index, length: match[0].length } : null;
}

export function parseCanvasAgentEventBlock(block: string): CanvasAgentEventMessage | null {
    let type = "message";
    const data: string[] = [];
    for (const line of block.split(/\r?\n/)) {
        if (!line || line.startsWith(":")) continue;
        const separator = line.indexOf(":");
        const field = separator >= 0 ? line.slice(0, separator) : line;
        const value = separator >= 0 ? line.slice(separator + 1).replace(/^ /, "") : "";
        if (field === "event") type = value || "message";
        if (field === "data") data.push(value);
    }
    return data.length ? { type, data: data.join("\n") } : null;
}
