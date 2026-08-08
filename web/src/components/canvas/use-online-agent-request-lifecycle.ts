import { useCallback, useEffect, useRef, useState } from "react";
import { useBlocker } from "react-router";

export type OnlineAgentProtectedPhase = "running" | "tool_approval" | null;

export type OnlineAgentRequestIdentity = {
    sessionId: string;
    callNumber: number;
    model: string;
};

export type OnlineAgentBlockedAction = "collapse" | "navigate";

type ActiveOnlineAgentRequest = {
    identity: OnlineAgentRequestIdentity;
    controller: AbortController;
    stopReason: "user" | "unmount" | null;
};

type OnlineAgentRequestLifecycleOptions = {
    protectedPhase: OnlineAgentProtectedPhase;
    confirmStop: (identity: OnlineAgentRequestIdentity) => Promise<boolean>;
    warnBlocked: (action: OnlineAgentBlockedAction, phase: Exclude<OnlineAgentProtectedPhase, null>, identity: OnlineAgentRequestIdentity | null) => void;
};

export class OnlineAgentRequestStoppedError extends Error {
    readonly callNumber: number;
    readonly reason: "user" | "unmount";

    constructor(callNumber: number, reason: "user" | "unmount") {
        super("在线 Agent 请求已停止等待");
        this.name = "OnlineAgentRequestStoppedError";
        this.callNumber = callNumber;
        this.reason = reason;
    }
}

/**
 * 浏览器只能中断当前连接，不能承诺供应商停止计费，因此把停止、离开保护和请求身份放在同一生命周期中。
 */
export function useOnlineAgentRequestLifecycle({ protectedPhase, confirmStop, warnBlocked }: OnlineAgentRequestLifecycleOptions) {
    const activeRequestRef = useRef<ActiveOnlineAgentRequest | null>(null);
    const mountedRef = useRef(true);
    const stopConfirmationRef = useRef(false);
    const protectedPhaseRef = useRef(protectedPhase);
    const [activeRequest, setActiveRequest] = useState<OnlineAgentRequestIdentity | null>(null);
    protectedPhaseRef.current = protectedPhase;

    const navigationBlocker = useBlocker(Boolean(protectedPhase));

    useEffect(() => {
        if (navigationBlocker.state !== "blocked") return;
        const phase = protectedPhaseRef.current;
        navigationBlocker.reset();
        if (phase) warnBlocked("navigate", phase, activeRequestRef.current?.identity || null);
    }, [navigationBlocker, warnBlocked]);

    useEffect(() => {
        if (!protectedPhase) return;
        const warnBeforeUnload = (event: BeforeUnloadEvent) => {
            event.preventDefault();
            event.returnValue = "";
        };
        window.addEventListener("beforeunload", warnBeforeUnload);
        return () => window.removeEventListener("beforeunload", warnBeforeUnload);
    }, [protectedPhase]);

    useEffect(() => {
        mountedRef.current = true;
        return () => {
            mountedRef.current = false;
            const active = activeRequestRef.current;
            if (!active) return;
            active.stopReason = "unmount";
            active.controller.abort();
            activeRequestRef.current = null;
        };
    }, []);

    const runRequest = useCallback(async <T,>(identity: OnlineAgentRequestIdentity, request: (signal: AbortSignal) => Promise<T>) => {
        if (!mountedRef.current) throw new OnlineAgentRequestStoppedError(identity.callNumber, "unmount");
        if (activeRequestRef.current) throw new Error("在线 Agent 已有模型请求正在等待，请勿并行发送");
        const active: ActiveOnlineAgentRequest = { identity, controller: new AbortController(), stopReason: null };
        activeRequestRef.current = active;
        setActiveRequest(identity);
        try {
            return await request(active.controller.signal);
        } catch (error) {
            if (active.stopReason) throw new OnlineAgentRequestStoppedError(identity.callNumber, active.stopReason);
            throw error;
        } finally {
            if (activeRequestRef.current === active) {
                activeRequestRef.current = null;
                setActiveRequest(null);
            }
        }
    }, []);

    const stopRequest = useCallback(async () => {
        const active = activeRequestRef.current;
        if (!active || stopConfirmationRef.current) return false;
        stopConfirmationRef.current = true;
        try {
            if (!await confirmStop(active.identity)) return false;
            if (activeRequestRef.current !== active) return false;
            active.stopReason = "user";
            active.controller.abort();
            return true;
        } finally {
            stopConfirmationRef.current = false;
        }
    }, [confirmStop]);

    const allowCollapse = useCallback(() => {
        const phase = protectedPhaseRef.current;
        if (!phase) return true;
        warnBlocked("collapse", phase, activeRequestRef.current?.identity || null);
        return false;
    }, [warnBlocked]);

    return {
        activeRequest,
        allowCollapse,
        runRequest,
        stopRequest,
    };
}
