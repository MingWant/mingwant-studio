import { memo, useCallback, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { App, Button, Input, Segmented, Tooltip } from "antd";
import copyToClipboard from "copy-to-clipboard";
import { Copy, FolderOpen, History, KeyRound, Link2, LoaderCircle, PlugZap, Plus, RefreshCw, RotateCcw, Terminal, Trash2 } from "lucide-react";
import { motion } from "motion/react";

import { canvasThemes } from "@/lib/canvas-theme";
import { createClientId } from "@/lib/client-id";
import { useThemeStore } from "@/stores/use-theme-store";
import { useUserStore } from "@/stores/use-user-store";
import { imageToDataUrl } from "@/services/image-storage";
import { CanvasNodeType } from "@/types/canvas";
import { buildCanvasAgentImagePreview } from "@/lib/canvas/canvas-agent-image";
import { consumeCanvasAgentLaunchCredentials, consumeLegacyQueryRejection, isLocalCanvasAgentEndpoint, persistCanvasAgentCredentials, readCanvasAgentSessionCredentials } from "@/lib/canvas/canvas-agent-launch";
import { useCanvasAgentStore, type AgentAttachment, type AgentChatItem, type AgentEventLog, type AgentPanelTab, type AgentPendingToolCall, type AgentThreadSummary } from "@/stores/canvas/use-canvas-agent-store";
import { previewCanvasAgentOps, summarizeCanvasAgentOps, type CanvasAgentOp, type CanvasAgentSnapshot } from "@/lib/canvas/canvas-agent-ops";
import { isProjectAgentReadTool, isProjectAgentToolName, runProjectAgentTool } from "@/services/api/project-agent-tools";
import { AgentChatComposer, AgentChatMessage, AgentPanelTabs, AgentPendingToolCard, AgentWorkingMessage, type CanvasAgentChatAttachment } from "./canvas-agent-chat-ui";
import { openCanvasAgentEvents, type CanvasAgentEventConnection } from "./canvas-local-agent-events";
import { CanvasAgentStateSyncQueue, postCanvasAgentToolResult } from "./canvas-local-agent-transport";

const PANEL_MOTION_SECONDS = 0.5;
const MAX_ATTACHMENTS = 6;
const MAX_ATTACHMENT_PAYLOAD_BYTES = 28 * 1024 * 1024;
const DEFAULT_AGENT_URL = "http://127.0.0.1:17371";
const AGENT_CONNECT_STEPS = [
    { title: "从仓库安装插件", text: "插件暂未上架公共目录，请按项目 README 添加仓库 marketplace；安装后新建 Codex 对话。" },
    { title: "打开画布连接", text: "回到这里点击连接，网页会自动读取本机 Agent 配置。" },
    { title: "手动启动备用", text: "如果自动发现失败，请按插件说明启动本地 Agent 后再回到这里连接。" },
];

type AgentEventPayload = {
    agent?: string;
    type?: string;
    thread_id?: string;
    item?: AgentEventItem;
    error?: { message?: string };
    message?: string;
    usage?: Record<string, unknown>;
};
type AgentEventItem = { id?: string; type?: string; text?: unknown; message?: unknown; server?: string; tool?: string; status?: string; arguments?: unknown; result?: unknown; error?: { message?: string } };

type AgentLogContext = { endpoint: string; connected: boolean; enabled: boolean; activity: string; waiting: boolean; sending: boolean; messages: number; pendingTool?: string };
type AgentWorkspace = { canvasId: string; workspacePath: string; activeThreadId?: string };
type AgentThreadsResponse = { ok?: boolean; workspace?: AgentWorkspace; data?: AgentThreadSummary[] };
type AgentThreadResponse = { ok?: boolean; workspace?: AgentWorkspace; thread?: AgentThreadSummary; messages?: AgentChatItem[] };
type AgentConfigResponse = { ok?: boolean; url?: string; hasToken?: boolean };

export const CanvasLocalAgentPanel = memo(function CanvasLocalAgentPanel({ snapshot, canUndoOps, undoOpsCount = 0, collapsed, embedded, headless, autoConnect, onApplyOps, onUndoOps }: { snapshot: CanvasAgentSnapshot; canUndoOps: boolean; undoOpsCount?: number; collapsed?: boolean; embedded?: boolean; headless?: boolean; autoConnect?: boolean; onApplyOps: (ops: CanvasAgentOp[]) => CanvasAgentSnapshot; onUndoOps: () => CanvasAgentSnapshot | null }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const user = useUserStore((state) => state.user);
    const { message, modal } = App.useApp();
    const { width, url, token, connected, enabled, prompt, attachments, sending, waiting, messages, eventLogs, threads, activeThreadId, workspacePath, loadingThreads, activeTab, confirmTools, activity, connectError, pendingTool, setAgentState, addMessage: pushMessage, addEventLog: pushEventLog, clearEventLogs } = useCanvasAgentStore();
    const [resizing, setResizing] = useState(false);
    const listRef = useRef<HTMLDivElement>(null);
    const snapshotRef = useRef(snapshot);
    const confirmToolsRef = useRef(confirmTools);
    const pendingToolRef = useRef<AgentPendingToolCall | null>(null);
    const onApplyOpsRef = useRef(onApplyOps);
    const autoConnectRef = useRef(false);
    const connectedRef = useRef(false);
    const errorLoggedRef = useRef(false);
    const eventSourceRef = useRef<CanvasAgentEventConnection | null>(null);
    const stateSyncErrorRef = useRef("");
    const stateSyncQueueRef = useRef(new CanvasAgentStateSyncQueue());
    const attachmentUrlsRef = useRef(new Set<string>());
    const clientIdRef = useRef(createClientId());
    const endpoint = useMemo(() => url.trim().replace(/\/$/, ""), [url]);
    const syncAgentState = useCallback((targetEndpoint: string, targetToken: string, clientId: string, nextSnapshot: CanvasAgentSnapshot) => stateSyncQueueRef.current.enqueue({ endpoint: targetEndpoint, token: targetToken, clientId }, nextSnapshot), []);
    const reportAgentBridgeFailure = useCallback((title: string, error: unknown) => {
        const detail = error instanceof Error ? error.message : "本地 Agent 通信失败";
        const text = `${title}：${detail}`;
        eventSourceRef.current?.close();
        eventSourceRef.current = null;
        connectedRef.current = false;
        setAgentState({ enabled: false, connected: false, activity: "Agent 通信失败", connectError: text, waiting: false, sending: false });
        pushEventLog({ id: createId(), time: new Date().toLocaleString(), title, text, raw: agentErrorDetail(error) });
        if (!headless) message.error(text);
        return text;
    }, [headless, message, pushEventLog, setAgentState]);
    const loadThreads = useCallback(async () => {
        const projectId = snapshotRef.current.projectId;
        if ((!connectedRef.current && !useCanvasAgentStore.getState().connected) || !projectId) return;
        setAgentState({ loadingThreads: true });
        try {
            const data = await fetchAgentJson<AgentThreadsResponse>(endpoint, token, `/agent/codex/threads?canvasId=${encodeURIComponent(projectId)}`);
            const current = useCanvasAgentStore.getState();
            setAgentState({
                threads: data.data || [],
                workspacePath: data.workspace?.workspacePath || current.workspacePath,
                activeThreadId: data.workspace?.activeThreadId || current.activeThreadId,
            });
            const nextThreadId = data.workspace?.activeThreadId || current.activeThreadId;
            if (nextThreadId && !current.messages.length) {
                const thread = await fetchAgentJson<AgentThreadResponse>(endpoint, token, `/agent/codex/threads/${encodeURIComponent(nextThreadId)}?canvasId=${encodeURIComponent(projectId)}`);
                setAgentState({ messages: normalizeHistoryMessages(thread.messages || []) });
            }
        } catch (error) {
            addEventLog("读取历史失败", error);
        } finally {
            setAgentState({ loadingThreads: false });
        }
    }, [endpoint, setAgentState, token]);

    useEffect(() => {
        snapshotRef.current = snapshot;
    }, [snapshot]);
    useEffect(() => {
        confirmToolsRef.current = confirmTools;
    }, [confirmTools]);
    useEffect(() => {
        pendingToolRef.current = pendingTool;
    }, [pendingTool]);
    useEffect(() => {
        onApplyOpsRef.current = onApplyOps;
    }, [onApplyOps]);
    useEffect(() => {
        if (activeTab !== "chat") return;
        const frame = requestAnimationFrame(() => listRef.current?.scrollTo({ top: listRef.current.scrollHeight }));
        return () => cancelAnimationFrame(frame);
    }, [activeTab, activeThreadId, messages, pendingTool, waiting]);
    useEffect(() => () => attachmentUrlsRef.current.forEach((url) => URL.revokeObjectURL(url)), []);

    useEffect(() => {
        if (!enabled || !token.trim()) return;
        if (!persistCanvasAgentCredentials(endpoint, token)) {
            pushEventLog({ id: createId(), time: new Date().toLocaleString(), title: "凭据未持久化", text: "浏览器拒绝会话存储，Token 只保留在当前页面内存中" });
        }
        const clientId = clientIdRef.current;
        let disposed = false;
        const source = openCanvasAgentEvents({ endpoint, token, clientId }, {
            onEvent: (event) => {
                if (disposed) return;
                if (event.type === "hello") {
                    errorLoggedRef.current = false;
                    setAgentState({ connected: false, activity: "正在同步画布", connectError: "" });
                    void syncAgentState(endpoint, token, clientId, snapshotRef.current).then(() => {
                        if (disposed) return;
                        stateSyncErrorRef.current = "";
                        connectedRef.current = true;
                        setAgentState({ connected: true, activity: "已连接", connectError: "", messages: useCanvasAgentStore.getState().messages.filter((item) => !isConnectionErrorMessage(item)) });
                        if (!headless) message.success("本地 Agent 已连接，画布状态已同步");
                    }).catch((error) => {
                        if (disposed) return;
                        disposed = true;
                        const text = error instanceof Error ? error.message : "画布状态同步失败";
                        stateSyncErrorRef.current = text;
                        errorLoggedRef.current = true;
                        connectedRef.current = false;
                        source.close();
                        if (eventSourceRef.current === source) eventSourceRef.current = null;
                        setAgentState({ enabled: false, connected: false, activity: "画布同步失败", connectError: text, waiting: false, sending: false });
                        pushEventLog({ id: createId(), time: new Date().toLocaleString(), title: "初始画布同步失败", text, raw: agentErrorDetail(error) });
                        if (!headless) message.error(`${text}，已停止连接以避免 Agent 使用旧画布`);
                    });
                    return;
                }
                if (event.type === "tool_call") {
                    const data = parseEventData<AgentPendingToolCall>(event.data);
                    if (data) void handleToolCall(endpoint, token, data).catch((error) => reportAgentBridgeFailure("处理工具调用失败", error));
                    return;
                }
                if (event.type === "agent_event") {
                    const data = parseEventData<AgentEventPayload>(event.data);
                    if (data) handleAgentEvent(data);
                    return;
                }
                if (event.type === "agent_log") {
                    const text = parseEventData<{ text?: unknown }>(event.data)?.text;
                    addEventLog("日志", text, text);
                    return;
                }
                if (event.type === "agent_error") {
                    const errorMessage = parseEventData<{ message?: unknown }>(event.data)?.message;
                    setAgentState({ activity: "出错", waiting: false });
                    addMessage({ role: "error", title: "错误", text: normalizeText(errorMessage) });
                    addEventLog("错误", errorMessage, errorMessage);
                    return;
                }
                if (event.type === "agent_done") {
                    setAgentState({ activity: "完成", waiting: false, sending: false });
                    void loadThreads();
                }
            },
            onError: (error) => {
                if (disposed) return;
                disposed = true;
                const wasConnected = connectedRef.current;
                const text = wasConnected ? "本地 Agent 连接失败或已断开" : error.message || "连接失败，请检查地址和 token";
                if (!errorLoggedRef.current || wasConnected) {
                    addEventLog(wasConnected ? "连接断开" : "连接失败", { endpoint, error: text });
                    if (!headless) message.error(text);
                }
                errorLoggedRef.current = true;
                connectedRef.current = false;
                clearAgentSession({ enabled: false, activity: wasConnected ? "连接断开" : "连接失败", connected: false, connectError: text });
                source.close();
                if (eventSourceRef.current === source) eventSourceRef.current = null;
            },
        });
        eventSourceRef.current = source;
        return () => {
            disposed = true;
            source.close();
            if (eventSourceRef.current === source) eventSourceRef.current = null;
            connectedRef.current = false;
            setAgentState({ connected: false });
        };
    }, [enabled, endpoint, headless, loadThreads, message, pushEventLog, setAgentState, syncAgentState, token]);

    useEffect(() => {
        if (connected) void loadThreads();
    }, [connected, loadThreads, snapshot.projectId]);

    useEffect(() => {
        if (!connected) return;
        const timer = setTimeout(() => {
            void syncAgentState(endpoint, token, clientIdRef.current, snapshot).then(() => {
                if (!stateSyncErrorRef.current) return;
                stateSyncErrorRef.current = "";
                const current = useCanvasAgentStore.getState();
                setAgentState({ connectError: "", activity: current.activity === "画布同步失败" ? "已连接" : current.activity });
            }).catch((error) => {
                const text = error instanceof Error ? error.message : "画布状态同步失败";
                stateSyncErrorRef.current = text;
                reportAgentBridgeFailure("画布同步失败，已停止本地 Agent 连接", error);
            });
        }, 300);
        return () => clearTimeout(timer);
    }, [connected, endpoint, reportAgentBridgeFailure, setAgentState, snapshot, syncAgentState, token]);

    const sendPrompt = async () => {
        const text = prompt.trim();
        const files = attachments;
        const requestPrompt = promptWithAttachments(text, files);
        if (!connected || !requestPrompt || sending || waiting) return;
        if (attachmentPayloadBytes(files) > MAX_ATTACHMENT_PAYLOAD_BYTES) {
            addMessage({ role: "error", title: "图片过大", text: "图片附件超过 30MB，请删减后再发送。" });
            return;
        }
        setAgentState({ activity: "发送中", sending: true, waiting: true });
        addMessage({ role: "user", text: text || "发送了图片", attachments: files });
        addEventLog("用户发送", { text, attachments: files.map(({ name, type, size }) => ({ name, type, size })) });
        try {
            // 每轮开始前强制等待最新快照落到本地 Agent，禁止用旧画布继续推理。
            try {
                await syncAgentState(endpoint, token, clientIdRef.current, snapshotRef.current);
            } catch (error) {
                reportAgentBridgeFailure("发送前画布同步失败", error);
                throw error;
            }
            stateSyncErrorRef.current = "";
            const data = await fetchAgentJson<{ threadId?: string }>(endpoint, token, "/agent/codex/turn", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ prompt: requestPrompt, canvasId: snapshotRef.current.projectId, threadId: useCanvasAgentStore.getState().activeThreadId || undefined, attachments: files.map(({ name, type, dataUrl }) => ({ name, type, dataUrl })) }) });
            if (data.threadId) setAgentState({ activeThreadId: data.threadId });
            addEventLog("本地 Agent 已接收", { accepted: true });
            files.forEach((item) => {
                URL.revokeObjectURL(item.url);
                attachmentUrlsRef.current.delete(item.url);
            });
            setAgentState({ prompt: "", attachments: [] });
        } catch (error) {
            setAgentState({ activity: "发送失败", waiting: false });
            addMessage({ role: "error", title: "发送失败", text: error instanceof Error ? error.message : "发送失败" });
            addEventLog("发送失败", error);
        } finally {
            setAgentState({ sending: false });
        }
    };

    const addAttachments = async (files: FileList | File[] | null) => {
        if (!files) return;
        const images = Array.from(files).filter((file) => file.type.startsWith("image/"));
        const prev = useCanvasAgentStore.getState().attachments;
        try {
            const next = await Promise.all(images.slice(0, Math.max(0, MAX_ATTACHMENTS - prev.length)).map(async (file) => {
                const dataUrl = await readDataUrl(file);
                const url = URL.createObjectURL(file);
                attachmentUrlsRef.current.add(url);
                return { id: createId(), name: file.name, type: file.type, size: file.size, url, dataUrl };
            }));
            const merged = [...prev, ...next];
            if (attachmentPayloadBytes(merged) > MAX_ATTACHMENT_PAYLOAD_BYTES) {
                next.forEach((item) => {
                    URL.revokeObjectURL(item.url);
                    attachmentUrlsRef.current.delete(item.url);
                });
                addMessage({ role: "error", title: "图片过大", text: "图片附件最多约 30MB。" });
                return;
            }
            if (next.length) setAgentState({ attachments: merged });
        } catch (error) {
            addMessage({ role: "error", title: "图片读取失败", text: error instanceof Error ? error.message : "图片读取失败" });
        }
    };

    const removeAttachment = (id: string) => {
        const removed = attachments.find((item) => item.id === id);
        if (removed) {
            URL.revokeObjectURL(removed.url);
            attachmentUrlsRef.current.delete(removed.url);
        }
        setAgentState({ attachments: attachments.filter((item) => item.id !== id) });
    };

    const handleToolCall = async (endpoint: string, token: string, payload: AgentPendingToolCall) => {
        if (confirmToolsRef.current && (payload.name === "canvas_apply_ops" || (isProjectAgentToolName(payload.name) && !isProjectAgentReadTool(payload.name)))) {
            if (pendingToolRef.current) {
                try {
                    await postCanvasAgentToolResult({ endpoint, token, clientId: clientIdRef.current }, { requestId: payload.requestId, error: "仍有待确认的画布工具调用" });
                } catch (error) {
                    reportAgentBridgeFailure("工具拒绝结果回传失败", error);
                }
                return;
            }
            pendingToolRef.current = payload;
            setAgentState({ pendingTool: payload, activity: "等待确认", waiting: false });
            addEventLog("等待确认", payload, payload);
            return;
        }
        await runToolCall(endpoint, token, payload);
    };

    const runToolCall = async (endpoint: string, token: string, payload: AgentPendingToolCall) => {
        try {
            const input = (payload.input || {}) as Record<string, unknown>;
            const projectToolName = isProjectAgentToolName(payload.name) ? payload.name : null;
            setAgentState({ activity: payload.name === "canvas_apply_ops" ? "执行画布操作" : projectToolName ? "执行项目工具" : "读取画布", waiting: true });
            addEventLog(toolName(payload.name), payload, payload);
            const result = payload.name === "canvas_apply_ops"
                ? onApplyOpsRef.current((input.ops || []) as CanvasAgentOp[])
                : payload.name === "canvas_get_image_annotations"
                  ? await readImageAnnotations(snapshotRef.current, input)
                  : projectToolName
                    ? await runProjectAgentTool(projectToolName, input, snapshotRef.current.domainProjectId)
                    : snapshotRef.current;
            if (payload.name === "canvas_apply_ops") {
                const nextSnapshot = result as CanvasAgentSnapshot;
                snapshotRef.current = nextSnapshot;
                try {
                    // 必须先同步新快照再确认工具完成，避免 Agent 立即基于旧状态发起下一步。
                    await syncAgentState(endpoint, token, clientIdRef.current, nextSnapshot);
                    stateSyncErrorRef.current = "";
                } catch (error) {
                    const detail = error instanceof Error ? error.message : "画布状态同步失败";
                    stateSyncErrorRef.current = detail;
                    throw new Error(`画布已在本地修改，但无法同步给 Agent：${detail}`);
                }
            }
            await postCanvasAgentToolResult({ endpoint, token, clientId: clientIdRef.current }, { requestId: payload.requestId, result });
            setAgentState({ activity: "工具完成", waiting: true });
            const visibleResult = payload.name === "canvas_get_image_annotations" ? omitAnnotationImageData(result) : result;
            addEventLog(`${toolName(payload.name)}完成`, visibleResult, visibleResult);
            addMessage({ role: "tool", title: `${toolName(payload.name)}完成`, text: payload.name === "canvas_apply_ops" ? summarizeCanvasAgentOps((input.ops || []) as CanvasAgentOp[]) || "画布操作" : "已完成", detail: { requestId: payload.requestId, name: payload.name, input, result: visibleResult } });
        } catch (error) {
            const errorText = error instanceof Error ? error.message : "画布操作失败";
            setAgentState({ activity: "工具失败", waiting: false });
            addMessage({ role: "tool", title: "工具失败", text: errorText, detail: payload });
            try {
                await postCanvasAgentToolResult({ endpoint, token, clientId: clientIdRef.current }, { requestId: payload.requestId, error: errorText });
                if (stateSyncErrorRef.current) reportAgentBridgeFailure("画布工具后的状态同步失败，已停止连接", error);
            } catch (reportError) {
                reportAgentBridgeFailure("工具失败结果回传失败", reportError);
            }
        }
    };

    const rejectPendingTool = async () => {
        if (!pendingTool) return;
        try {
            await postCanvasAgentToolResult({ endpoint, token, clientId: clientIdRef.current }, { requestId: pendingTool.requestId, error: "用户取消了画布工具调用" });
        } catch (error) {
            reportAgentBridgeFailure("拒绝工具结果回传失败", error);
            return;
        }
        setAgentState({ activity: "已取消", waiting: false });
        addMessage({ role: "tool", title: "拒绝执行", text: toolName(pendingTool.name), detail: { requestId: pendingTool.requestId, name: pendingTool.name, input: pendingTool.input } });
        pendingToolRef.current = null;
        setAgentState({ pendingTool: null });
    };

    const approvePendingTool = async () => {
        if (!pendingTool) return;
        const tool = pendingTool;
        pendingToolRef.current = null;
        setAgentState({ pendingTool: null });
        await runToolCall(endpoint, token, tool);
    };

    const undoLastTool = () => {
        const restored = onUndoOps();
        if (!restored) return;
        snapshotRef.current = restored;
        setAgentState({ activity: "已撤销" });
        addMessage({ role: "tool", title: "已撤销 Agent 批次", text: "已恢复到本次写回前的画布状态", detail: restored });
        if (connected) void syncAgentState(endpoint, token, clientIdRef.current, restored).then(() => {
            stateSyncErrorRef.current = "";
        }).catch((error) => reportAgentBridgeFailure("撤销后的画布同步失败", error));
    };

    const toggleAgentConnection = async () => {
        if (enabled) {
            clearAgentSession({ enabled: false, connected: false, activity: "离线", connectError: "" });
            return;
        }
        const launched = consumeCanvasAgentLaunchCredentials();
        const stored = readCanvasAgentSessionCredentials();
        const legacyQueryRejected = consumeLegacyQueryRejection();
        const availableToken = launched?.token || token.trim() || stored?.token || "";
        const discovered = availableToken ? null : await discoverAgentConfig(endpoint || DEFAULT_AGENT_URL);
        const nextEndpoint = (launched?.endpoint || stored?.endpoint || discovered?.url || endpoint || DEFAULT_AGENT_URL).trim().replace(/\/$/, "");
        const nextToken = availableToken.trim();
        if (launched?.source === "legacy-query" || legacyQueryRejected) {
            const text = "检测到旧版 Token 查询链接，已从地址栏清除；请更新 Codex 插件以改用安全启动链接";
            addEventLog("旧版启动链接", text);
            if (!headless) message.warning(text);
        }
        if (!nextEndpoint) {
            const text = "请填写本地 Agent 地址";
            setAgentState({ connectError: text });
            if (!headless) message.warning(text);
            return;
        }
        if (!nextToken) {
            const text = "没有发现本地 Agent，请先在 Codex 使用插件或手动启动 Canvas Agent";
            setAgentState({ connectError: text });
            if (!headless) message.warning(text);
            return;
        }
        if (!isLocalCanvasAgentEndpoint(nextEndpoint)) {
            const text = "本地 Agent 只允许连接 127.0.0.1、localhost 或 ::1";
            setAgentState({ connectError: text });
            if (!headless) message.warning(text);
            return;
        }
        errorLoggedRef.current = false;
        setAgentState({ url: nextEndpoint, token: nextToken, enabled: true, connected: false, activity: "连接中", connectError: "", activeTab: "setup" });
    };

    useEffect(() => {
        if (!autoConnect || autoConnectRef.current || enabled || connected) return;
        autoConnectRef.current = true;
        void toggleAgentConnection();
    }, [autoConnect, connected, enabled]);

    function clearAgentSession(patch: Parameters<typeof setAgentState>[0] = {}) {
        setAgentState({
            messages: [],
            threads: [],
            activeThreadId: "",
            workspacePath: "",
            loadingThreads: false,
            waiting: false,
            sending: false,
            pendingTool: null,
            ...patch,
        });
        pendingToolRef.current = null;
    }

    const startNewThread = async () => {
        const projectId = snapshotRef.current.projectId;
        if (!connected || !projectId) return;
        setAgentState({ loadingThreads: true });
        try {
            const data = await fetchAgentJson<AgentThreadResponse>(endpoint, token, "/agent/codex/threads/new", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ canvasId: projectId }) });
            setAgentState({ activeThreadId: data.thread?.id || data.workspace?.activeThreadId || "", messages: [], activeTab: "chat", activity: "新对话" });
            await loadThreads();
        } catch (error) {
            addEventLog("新建对话失败", error);
            message.error(error instanceof Error ? error.message : "新建对话失败");
        } finally {
            setAgentState({ loadingThreads: false });
        }
    };

    const resumeThread = async (threadId: string) => {
        const projectId = snapshotRef.current.projectId;
        if (!connected || !projectId || !threadId) return;
        setAgentState({ loadingThreads: true });
        try {
            const data = await fetchAgentJson<AgentThreadResponse>(endpoint, token, `/agent/codex/threads/${encodeURIComponent(threadId)}/resume`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ canvasId: projectId }) });
            setAgentState({ activeThreadId: data.thread?.id || threadId, messages: normalizeHistoryMessages(data.messages || []), activeTab: "chat", activity: "已恢复会话" });
            await loadThreads();
        } catch (error) {
            addEventLog("恢复对话失败", error);
            message.error(error instanceof Error ? error.message : "恢复对话失败");
        } finally {
            setAgentState({ loadingThreads: false });
        }
    };

    const deleteThread = async (threadId: string) => {
        const projectId = snapshotRef.current.projectId;
        if (!connected || !projectId || !threadId) return;
        setAgentState({ loadingThreads: true });
        try {
            await fetchAgentJson(endpoint, token, `/agent/codex/threads/${encodeURIComponent(threadId)}/delete`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ canvasId: projectId }) });
            const current = useCanvasAgentStore.getState();
            setAgentState({
                threads: current.threads.filter((thread) => thread.id !== threadId),
                activeThreadId: current.activeThreadId === threadId ? "" : current.activeThreadId,
                messages: current.activeThreadId === threadId ? [] : current.messages,
            });
            message.success("记录已删除");
        } catch (error) {
            addEventLog("删除对话失败", error);
            message.error(error instanceof Error ? error.message : "删除对话失败");
        } finally {
            setAgentState({ loadingThreads: false });
        }
    };

    const confirmDeleteThread = (thread: AgentThreadSummary) => {
        const label = thread.name || thread.preview || "未命名对话";
        modal.confirm({
            title: "删除对话记录",
            content: `确定删除「${label.length > 48 ? `${label.slice(0, 48)}...` : label}」吗？`,
            okText: "删除",
            okType: "danger",
            cancelText: "取消",
            onOk: () => deleteThread(thread.id),
        });
    };

    const startResize = (event: ReactPointerEvent<HTMLDivElement>) => {
        event.preventDefault();
        const startX = event.clientX;
        const startWidth = width;
        let nextWidth = startWidth;
        const onMove = (moveEvent: PointerEvent) => {
            nextWidth = clamp(startWidth + startX - moveEvent.clientX, 360, 760);
            setAgentState({ width: nextWidth });
        };
        const onUp = () => {
            try {
                localStorage.setItem("canvas-agent-panel-width", String(nextWidth));
            } catch {
                // 面板尺寸不是业务数据；浏览器禁止写偏好时保留当前内存宽度即可。
            }
            window.removeEventListener("pointermove", onMove);
            window.removeEventListener("pointerup", onUp);
            setResizing(false);
        };
        setResizing(true);
        window.addEventListener("pointermove", onMove);
        window.addEventListener("pointerup", onUp);
    };

    const addMessage = (item: Omit<AgentChatItem, "id">) => {
        const text = normalizeText(item.text);
        if (!text && !item.attachments?.length) return;
        const next = { ...item, id: `${Date.now()}-${Math.random()}`, text };
        const currentMessages = useCanvasAgentStore.getState().messages;
        if (next.streamId) {
            const index = currentMessages.findIndex((message) => message.streamId === next.streamId);
            if (index >= 0) {
                setAgentState({ messages: currentMessages.map((message, i) => i === index ? { ...message, ...next, id: message.id, text: next.text || message.text } : message) });
                return;
            }
        }
        const last = currentMessages.at(-1);
        if (last?.role === "assistant" && next.role === "assistant" && last.title === next.title) {
            const merged = mergeAgentText(last.text, next.text);
            if (merged === last.text) return;
            setAgentState({ messages: [...useCanvasAgentStore.getState().messages.slice(0, -1), { ...last, text: merged, meta: next.meta || last.meta }] });
            return;
        }
        pushMessage(next);
    };

    const addEventLog = (title: string, text: unknown, raw?: unknown) => {
        pushEventLog({ id: `${Date.now()}-${Math.random()}`, time: new Date().toLocaleTimeString(), title, text: normalizeText(text) || title, raw });
    };

    const handleAgentEvent = (event: AgentEventPayload) => {
        const visibleEvent = omitMcpImageData(event);
        if (shouldLogAgentEvent(visibleEvent)) addEventLog(eventTitle(visibleEvent), visibleEvent, visibleEvent);
        if (event.type === "thread.started" && event.thread_id) setAgentState({ activeThreadId: event.thread_id });
        const nextActivity = activityText(event);
        if (nextActivity) setAgentState({ activity: nextActivity });
        if (event.type === "turn.started") setAgentState({ waiting: true });
        if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "error") setAgentState({ waiting: false, sending: false });
        const item = formatAgentEvent(visibleEvent);
        if (item) {
            if (item.role === "error") setAgentState({ waiting: false, sending: false });
            addMessage(item);
        }
    };

    const content = (
        <>
            <AgentPanelTabs
                value={activeTab}
                theme={theme}
                items={[
                    { value: "setup", label: "连接", icon: <PlugZap className="size-3.5" /> },
                    { value: "chat", label: "对话" },
                    { value: "history", label: "历史", icon: <History className="size-3.5" />, count: threads.length },
                    { value: "log", label: "日志", icon: <Terminal className="size-3.5" />, count: eventLogs.length },
                ]}
                onChange={(activeTab) => {
                    setAgentState({ activeTab });
                    if (activeTab === "history") void loadThreads();
                }}
                right={
                    <>
                        <Button size="small" type="text" disabled={!canUndoOps} icon={<RotateCcw className="size-3.5" />} onClick={undoLastTool}>
                            撤销{undoOpsCount ? ` ${undoOpsCount}` : ""}
                        </Button>
                    </>
                }
            />

            {activeTab === "setup" ? (
                <AgentConnectView
                    theme={theme}
                    url={url}
                    token={token}
                    enabled={enabled}
                    connected={connected}
                    activity={activity}
                    connectError={connectError}
                    onUrlChange={(url) => setAgentState({ url, connectError: "" })}
                    onTokenChange={(token) => setAgentState({ token, connectError: "" })}
                    onToggleEnabled={toggleAgentConnection}
                />
            ) : activeTab === "history" ? (
                <AgentHistoryView
                    theme={theme}
                    threads={threads}
                    activeThreadId={activeThreadId}
                    workspacePath={workspacePath}
                    loading={loadingThreads}
                    connected={connected}
                    onRefresh={() => void loadThreads()}
                    onNewThread={() => void startNewThread()}
                    onResumeThread={(threadId) => void resumeThread(threadId)}
                    onDeleteThread={confirmDeleteThread}
                />
            ) : activeTab === "log" ? (
                <AgentLogView
                    logs={eventLogs}
                    theme={theme}
                    context={{ endpoint, connected, enabled, activity, waiting, sending, messages: messages.length, pendingTool: pendingTool?.name }}
                    onClear={clearEventLogs}
                    onCopied={(text) => message.success(text)}
                    onCopyBlocked={(text) => message.warning(text)}
                />
            ) : (
                <>
                    <div ref={listRef} className="thin-scrollbar min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
                        {messages.map((item) => (
                            <AgentChatMessage key={item.id} item={agentMessageToChatMessage(item)} theme={theme} user={user} />
                        ))}
                        {pendingTool ? <AgentPendingToolCard summary={summarizeCanvasAgentOps(pendingTool.input?.ops || []) || toolName(pendingTool.name)} detail={{ requestId: pendingTool.requestId, name: pendingTool.name, input: pendingTool.input, impact: previewCanvasAgentOps(pendingTool.input?.ops || [], snapshot) }} theme={theme} onReject={rejectPendingTool} onApprove={approvePendingTool} /> : null}
                        {waiting && !pendingTool ? <AgentWorkingMessage theme={theme} /> : null}
                    </div>
                    <AgentChatComposer
                        prompt={prompt}
                        attachments={attachments.map(agentAttachmentToChatAttachment)}
                        disabled={!connected}
                        sending={sending || waiting}
                        placeholder="询问 Codex，或让它操作画布"
                        theme={theme}
                        onPromptChange={(prompt) => setAgentState({ prompt })}
                        onSubmit={sendPrompt}
                        onAddFiles={addAttachments}
                        onRemoveAttachment={removeAttachment}
                        left={attachments.length ? <span className="text-[11px]" style={{ color: theme.node.muted }}>{formatBytes(attachmentPayloadBytes(attachments))} / 30MB</span> : null}
                    />
                </>
            )}
        </>
    );

    if (headless) return null;
    if (embedded) return content;

    return (
        <motion.div
            className="relative z-[70] flex h-full shrink-0"
            initial={{ width: 0, opacity: 0 }}
            animate={{ width: collapsed ? 0 : width + 1, opacity: collapsed ? 0 : 1 }}
            transition={{ duration: resizing ? 0 : PANEL_MOTION_SECONDS, ease: [0.22, 1, 0.36, 1] }}
            style={{ overflow: "clip", pointerEvents: collapsed ? "none" : undefined }}
        >
            <motion.aside
                className="relative flex h-full shrink-0 flex-col border-l"
                initial={{ x: 48 }}
                animate={{ x: collapsed ? 28 : 0 }}
                transition={{ duration: resizing ? 0 : PANEL_MOTION_SECONDS, ease: [0.22, 1, 0.36, 1] }}
                style={{ width, background: theme.node.panel, borderColor: theme.node.stroke, color: theme.node.text }}
            >
                <div className="absolute left-0 top-0 h-full w-1 cursor-col-resize transition hover:bg-current/20" onPointerDown={startResize} />
                {content}
            </motion.aside>
        </motion.div>
    );
});

function AgentLogView({ logs, theme, context, onClear, onCopied, onCopyBlocked }: { logs: AgentEventLog[]; theme: (typeof canvasThemes)[keyof typeof canvasThemes]; context: AgentLogContext; onClear: () => void; onCopied: (text: string) => void; onCopyBlocked: (text: string) => void }) {
    const [mode, setMode] = useState<"text" | "json">("text");
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const content = mode === "text" ? formatLogText(logs, context) : formatLogJson(logs, context);
    const lastError = [...logs].reverse().find((item) => /错误|失败|error/i.test(`${item.title}\n${item.text}`));
    const copy = async (value = content, tip = "日志已复制") => {
        if (await copyToClipboard(value)) {
            onCopied(tip);
            return;
        }
        textareaRef.current?.focus();
        textareaRef.current?.select();
        onCopyBlocked("已选中日志，请手动复制");
    };
    return (
        <div className="thin-scrollbar min-h-0 flex-1 overflow-y-auto p-4">
            <div className="flex min-h-full flex-col gap-3">
                <div>
                    <div className="text-base font-semibold leading-6">运行日志</div>
                </div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                    <Segmented size="small" value={mode} onChange={(value) => setMode(value as "text" | "json")} options={[{ label: "排查日志", value: "text" }, { label: "原始 JSON", value: "json" }]} />
                    <div className="flex items-center gap-2">
                        <span className="text-xs" style={{ color: theme.node.muted }}>{logs.length} 条</span>
                        <Button size="small" icon={<Copy className="size-3.5" />} onClick={() => void copy()}>复制</Button>
                        <Button size="small" disabled={!lastError} onClick={() => lastError && void copy(formatLogText([lastError], context), "最近错误已复制")}>最近错误</Button>
                        <Button size="small" danger type="text" icon={<Trash2 className="size-3.5" />} disabled={!logs.length} onClick={onClear}>清空</Button>
                    </div>
                </div>
                <textarea
                    ref={textareaRef}
                    readOnly
                    value={content}
                    className="thin-scrollbar min-h-[360px] flex-1 resize-none rounded-lg border bg-transparent p-3 font-mono text-xs leading-5 outline-none"
                    style={{ borderColor: theme.node.stroke, color: theme.node.text }}
                    onFocus={(event) => event.currentTarget.select()}
                />
            </div>
        </div>
    );
}

function AgentConnectView({ theme, url, token, enabled, connected, activity, connectError, onUrlChange, onTokenChange, onToggleEnabled }: { theme: (typeof canvasThemes)[keyof typeof canvasThemes]; url: string; token: string; enabled: boolean; connected: boolean; activity: string; connectError: string; onUrlChange: (value: string) => void; onTokenChange: (value: string) => void; onToggleEnabled: () => void }) {
    const { message } = App.useApp();
    const statusText = connectError ? "连接失败" : connected ? activity : enabled ? "连接中" : "未连接";
    const statusColor = connectError ? "#dc2626" : connected ? "#16a34a" : enabled ? "#d97706" : theme.node.muted;
    return (
        <div className="thin-scrollbar min-h-0 flex-1 overflow-y-auto p-4">
            <div className="space-y-4">
                <div>
                    <div className="text-base font-semibold leading-6">连接本地 Agent</div>
                    <div className="mt-1 text-xs leading-5" style={{ color: theme.node.muted }}>
                        安装仓库自带的 Codex 插件后，画布会优先自动连接本机 Agent。
                    </div>
                </div>
                <div className="space-y-2">
                    {AGENT_CONNECT_STEPS.map((step) => (
                        <div key={step.title} className="rounded-lg px-3 py-2.5">
                            <div className="text-sm font-medium leading-5">{step.title}</div>
                            <div className="mt-1 text-xs leading-5" style={{ color: theme.node.muted }}>{step.text}</div>
                        </div>
                    ))}
                </div>
                <div className="rounded-lg border p-3" style={{ borderColor: theme.node.stroke }}>
                    <div className="flex flex-wrap items-start justify-between gap-3">
                        <div className="min-w-0 flex-1">
                            <div className="flex min-w-0 items-center gap-2">
                                <span className="shrink-0 text-sm font-medium leading-5">网页连接</span>
                                <span className="inline-flex min-w-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] leading-4" style={{ borderColor: connected || enabled || connectError ? statusColor : theme.node.stroke, color: statusColor }}>
                                    <span className="size-1.5 shrink-0 rounded-full" style={{ background: statusColor }} />
                                    <span className="truncate">{statusText}</span>
                                </span>
                            </div>
                            <div className="mt-1 text-xs leading-5" style={{ color: theme.node.muted }}>
                                默认自动读取 Local URL 和 Connect token，失败时再手动填写。
                            </div>
                        </div>
                        <Button className="!h-8 !px-3" type={enabled ? "default" : "primary"} icon={<PlugZap className="size-4" />} onClick={onToggleEnabled}>
                            {enabled ? "断开" : "连接"}
                        </Button>
                    </div>
                    <div className="mt-3 grid gap-2.5">
                        <label className="grid gap-1.5">
                            <span className="flex items-center gap-1.5 text-xs font-medium" style={{ color: theme.node.muted }}>
                                <Link2 className="size-3.5" />
                                本地地址
                                <span className="font-normal opacity-70">Local URL</span>
                            </span>
                            <Input size="large" prefix={<Link2 className="mr-1 size-4" style={{ color: theme.node.faint }} />} value={url} onChange={(event) => onUrlChange(event.target.value)} placeholder="例如 http://127.0.0.1:17371" />
                        </label>
                        <label className="grid gap-1.5">
                            <span className="flex items-center gap-1.5 text-xs font-medium" style={{ color: theme.node.muted }}>
                                <KeyRound className="size-3.5" />
                                连接 Token
                                <span className="font-normal opacity-70">Connect token</span>
                            </span>
                            <Input.Password size="large" prefix={<KeyRound className="mr-1 size-4" style={{ color: theme.node.faint }} />} value={token} onChange={(event) => onTokenChange(event.target.value)} placeholder="自动发现，或手动填入 Connect token" />
                        </label>
                        {connectError ? (
                            <div className="rounded-md border px-2.5 py-2 text-xs leading-5" style={{ borderColor: "rgba(220,38,38,.35)", color: "#dc2626" }}>
                                {connectError}
                            </div>
                        ) : null}
                    </div>
                </div>
            </div>
        </div>
    );
}

function AgentHistoryView({ theme, threads, activeThreadId, workspacePath, loading, connected, onRefresh, onNewThread, onResumeThread, onDeleteThread }: { theme: (typeof canvasThemes)[keyof typeof canvasThemes]; threads: AgentThreadSummary[]; activeThreadId: string; workspacePath: string; loading: boolean; connected: boolean; onRefresh: () => void; onNewThread: () => void; onResumeThread: (threadId: string) => void; onDeleteThread: (thread: AgentThreadSummary) => void }) {
    return (
        <div className="thin-scrollbar min-h-0 flex-1 overflow-y-auto p-3">
            <div className="space-y-3">
                <div className="flex min-w-0 items-center gap-2 text-xs" style={{ color: theme.node.muted }}>
                    <FolderOpen className="size-3.5 shrink-0" />
                    <span className="shrink-0">工作空间</span>
                    <span className="min-w-0 truncate" title={workspacePath}>{workspacePath || "默认画布目录"}</span>
                </div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="text-sm" style={{ color: theme.node.muted }}>
                        {threads.length ? `${threads.length} 条历史` : connected ? "暂无历史" : "未连接"}
                    </div>
                    <div className="flex items-center gap-2">
                        <Button size="small" icon={<RefreshCw className={`size-3.5 ${loading ? "animate-spin" : ""}`} />} disabled={!connected || loading} onClick={onRefresh}>
                            刷新
                        </Button>
                        <Button size="small" type="primary" icon={<Plus className="size-3.5" />} disabled={!connected || loading} onClick={onNewThread}>
                            新对话
                        </Button>
                    </div>
                </div>
                <div className="space-y-2">
                    {threads.map((thread) => {
                        const active = thread.id === activeThreadId;
                        return (
                            <div key={thread.id} className="rounded-lg border px-2.5 py-1.5 transition" style={{ borderColor: active ? theme.node.text : theme.node.stroke, background: "transparent", color: theme.node.text }}>
                                <div className="flex items-center gap-2">
                                    <div className="min-w-0 flex-1">
                                        <div className="flex min-w-0 items-center gap-1.5">
                                            {active ? <span className="shrink-0 text-[10px] font-medium" style={{ color: theme.node.text }}>当前</span> : null}
                                            <div className="truncate text-sm font-medium leading-5">{thread.name || thread.preview || "未命名对话"}</div>
                                        </div>
                                        <div className="truncate text-[11px] leading-4 opacity-65">{thread.preview || thread.id}</div>
                                    </div>
                                    <div className="flex shrink-0 items-center gap-1">
                                        <span className="text-[10px] opacity-55">{formatThreadTime(thread.updatedAt || thread.createdAt)}</span>
                                        <Button size="small" className="!h-6 !px-2" disabled={loading} onClick={() => onResumeThread(thread.id)}>
                                            进入
                                        </Button>
                                        <Tooltip title="删除记录">
                                            <Button size="small" danger type="text" className="!h-6 !w-6 !min-w-6" disabled={loading} icon={<Trash2 className="size-3.5" />} onClick={() => onDeleteThread(thread)} />
                                        </Tooltip>
                                    </div>
                                </div>
                            </div>
                        );
                    })}
                    {!threads.length ? (
                        <div className="px-3 py-8 text-center text-sm" style={{ color: theme.node.muted }}>
                            {connected ? "当前工作空间还没有对话记录" : "连接本地 Agent 后显示历史记录"}
                        </div>
                    ) : null}
                </div>
            </div>
        </div>
    );
}

function agentMessageToChatMessage(item: AgentChatItem) {
    return { ...item, attachments: item.attachments?.map(agentAttachmentToChatAttachment) };
}

function agentAttachmentToChatAttachment(item: AgentAttachment): CanvasAgentChatAttachment {
    return { id: item.id, name: item.name, url: item.dataUrl || item.url };
}

function formatAgentEvent(event: AgentEventPayload): Omit<AgentChatItem, "id"> | null {
    const item = event.item;
    if (event.type === "item.completed" && item?.type === "error") return { role: "error", title: "错误", text: normalizeText(item.message), detail: item };
    if ((event.type === "item.updated" || event.type === "item.completed") && item?.type === "agent_message") return { role: "assistant", title: "Codex", text: stringText(item.text), meta: usageText(event), streamId: item.id };
    if (event.type === "item.completed" && isMcpToolItem(item) && isReadTool(String(item?.tool || ""))) return { role: "tool", title: `${toolName(String(item?.tool || ""))}完成`, text: item?.error?.message || toolSummary(item), detail: toolDetail(item) };
    const text = eventText(event);
    if (text) return { role: "assistant", title: "Codex", text, meta: usageText(event) };
    return null;
}

function parseEventData<T>(data: string) {
    try {
        return JSON.parse(data) as T;
    } catch {
        throw new Error("本地 Agent 事件 JSON 无效，已停止连接");
    }
}

function formatLogText(logs: AgentEventLog[], context: AgentLogContext) {
    const head = [
        "明想 MingWant Canvas Agent 诊断日志",
        `Canvas Agent: ${context.endpoint}`,
        `连接: ${context.connected ? "在线" : context.enabled ? "连接中" : "未启用"}`,
        `状态: ${context.activity}`,
        `waiting: ${context.waiting}`,
        `sending: ${context.sending}`,
        `messages: ${context.messages}`,
        `pendingTool: ${context.pendingTool ? toolName(context.pendingTool) : "none"}`,
        `logs: ${logs.length}`,
    ].join("\n");
    const body = logs.map((item, index) => {
        const detail = item.raw == null ? item.text : JSON.stringify(item.raw, null, 2);
        return [`#${index + 1} ${item.time} ${item.title}`, detail].filter(Boolean).join("\n");
    }).join("\n\n---\n\n");
    return [head, body || "暂无事件日志"].join("\n\n");
}

function formatLogJson(logs: AgentEventLog[], context: AgentLogContext) {
    return JSON.stringify({ context, logs: logs.map(({ time, title, text, raw }) => ({ time, title, text, raw })) }, null, 2);
}

function eventText(event: AgentEventPayload) {
    return event.type === "item.completed" && event.item?.type === "agent_message" ? stringText(event.item.text) : "";
}

function usageText(event: AgentEventPayload) {
    const usage = event.usage;
    if (!usage || typeof usage !== "object") return undefined;
    const total = numberField(usage, "total_tokens");
    const input = numberField(usage, "input_tokens");
    const output = numberField(usage, "output_tokens");
    if (total) return `${total} tok`;
    if (input || output) return `${input || 0}/${output || 0} tok`;
    return undefined;
}

function activityText(event: AgentEventPayload) {
    const name = event.type || "";
    if (name === "thread.started") return "已创建会话";
    if (name === "turn.started") return "思考中";
    if (name === "turn.completed") return "完成";
    if (name === "turn.failed" || name === "error") return "出错";
    if (name === "item.started") return isMcpToolItem(event.item) ? `调用${toolName(String(event.item?.tool || ""))}` : "执行步骤";
    if (name === "item.completed") return isMcpToolItem(event.item) ? "工具完成" : "更新消息";
    return "";
}

function eventTitle(event: AgentEventPayload) {
    const item = event.item;
    if (event.type === "thread.started") return "已创建 Codex 会话";
    if (event.type === "turn.started") return "开始处理";
    if (event.type === "turn.completed") return "本轮完成";
    if (event.type === "stream.summary") return "流式摘要";
    if (event.type === "turn.failed" || event.type === "error") return "本轮失败";
    if (event.type === "item.started" && isMcpToolItem(item)) return `调用工具：${toolName(String(item?.tool || ""))}`;
    if (event.type === "item.completed" && isMcpToolItem(item)) return `工具完成：${toolName(String(item?.tool || ""))}`;
    if (event.type === "item.completed" && item?.type === "agent_message") return "Codex 回复";
    return event.type || "Codex 事件";
}

function shouldLogAgentEvent(event: AgentEventPayload) {
    const itemType = event.item?.type || "";
    return !["item.updated"].includes(event.type || "") && !["reasoning"].includes(itemType) && !(event.type === "item.started" && itemType === "agent_message");
}

function isConnectionErrorMessage(item: AgentChatItem) {
    return item.role === "error" && /连接失败|无法连接本地 Agent|本地 Agent 连接失败/.test(item.text);
}

async function readImageAnnotations(snapshot: CanvasAgentSnapshot, input: Record<string, unknown>) {
    const requestedNodeId = typeof input.nodeId === "string" ? input.nodeId : "";
    const selectedIds = new Set(snapshot.selectedNodeIds || []);
    if (!requestedNodeId && selectedIds.size === 0) throw new Error("请先选择标注节点或原图节点，或传入 nodeId");
    const annotations = snapshot.nodes.filter((node) => {
        const annotation = node.metadata?.imageAnnotation;
        if (node.type !== CanvasNodeType.Image || !annotation) return false;
        if (requestedNodeId) return node.id === requestedNodeId || annotation.sourceNodeId === requestedNodeId;
        return selectedIds.has(node.id) || selectedIds.has(annotation.sourceNodeId);
    });
    if (!annotations.length) throw new Error("当前范围没有图片标注；请先在图片工具栏完成标注并保存节点");
    if (annotations.length > 6) throw new Error("一次最多读取 6 个图片标注，请缩小选区");
    return {
        annotations: await Promise.all(annotations.map(async (node) => {
            const annotation = node.metadata!.imageAnnotation!;
            const source = snapshot.nodes.find((item) => item.id === annotation.sourceNodeId);
            const originalDataUrl = await imageToDataUrl({ url: node.metadata?.content, storageKey: node.metadata?.storageKey });
            if (!originalDataUrl) throw new Error(`标注图片「${node.title || node.id}」读取失败`);
            const dataUrl = await buildCanvasAgentImagePreview(originalDataUrl);
            return {
                nodeId: node.id,
                title: node.title,
                sourceNodeId: annotation.sourceNodeId,
                sourceTitle: source?.title,
                instruction: annotation.instruction,
                image: { mimeType: /^data:([^;,]+)/i.exec(dataUrl)?.[1] || node.metadata?.mimeType || "image/png", dataUrl },
            };
        })),
    };
}

function omitAnnotationImageData(result: unknown) {
    if (!result || typeof result !== "object" || Array.isArray(result)) return result;
    const value = result as Record<string, unknown>;
    const annotations = Array.isArray(value.annotations) ? value.annotations.map((item) => {
        if (!item || typeof item !== "object" || Array.isArray(item)) return item;
        const annotation = item as Record<string, unknown>;
        const image = annotation.image && typeof annotation.image === "object" && !Array.isArray(annotation.image) ? annotation.image as Record<string, unknown> : {};
        return { ...annotation, image: { mimeType: image.mimeType, includedAsMcpImage: typeof image.dataUrl === "string" } };
    }) : [];
    return { ...value, annotations };
}

function omitMcpImageData(event: AgentEventPayload): AgentEventPayload {
    const item = event.item;
    if (!isMcpToolItem(item) || !item?.result || typeof item.result !== "object" || Array.isArray(item.result)) return event;
    const result = item.result as Record<string, unknown>;
    if (!Array.isArray(result.content)) return event;
    return {
        ...event,
        item: {
            ...item,
            result: {
                ...result,
                content: result.content.map((block) => {
                    if (!block || typeof block !== "object" || Array.isArray(block)) return block;
                    const content = block as Record<string, unknown>;
                    return content.type === "image" ? { type: "image", mimeType: content.mimeType, included: typeof content.data === "string" } : block;
                }),
            },
        },
    };
}

function toolName(name: string) {
    if (name === "canvas_apply_ops") return "画布操作";
    if (name === "canvas_get_state") return "读取画布";
    if (name === "canvas_get_selection") return "读取选区";
    if (name === "canvas_get_image_annotations") return "读取图片标注";
    if (name === "canvas_export_snapshot") return "导出快照";
    if (name === "canvas_create_node") return "创建节点";
    if (name === "canvas_create_text_node") return "创建文本";
    if (name === "canvas_create_text_nodes") return "批量创建文本";
    if (name === "canvas_create_config_node") return "创建生成配置";
    if (name === "canvas_create_image_prompt_flow") return "创建生图流程";
    if (name === "canvas_create_generation_flow") return "创建生成流程";
    if (name === "canvas_generate_text") return "生成文本";
    if (name === "canvas_generate_image") return "生成图片";
    if (name === "canvas_generate_video") return "生成视频";
    if (name === "canvas_generate_audio") return "生成音频";
    if (name === "canvas_update_node") return "更新节点";
    if (name === "canvas_update_node_text") return "更新文本";
    if (name === "canvas_move_nodes") return "移动节点";
    if (name === "canvas_resize_node") return "调整节点尺寸";
    if (name === "canvas_delete_nodes") return "删除节点";
    if (name === "canvas_connect_nodes") return "连接节点";
    if (name === "canvas_select_nodes") return "选择节点";
    if (name === "canvas_set_viewport") return "调整视口";
    if (name === "canvas_run_generation") return "触发生成";
    if (name === "canvas_edit_image_annotation") return "执行标注改图";
    if (name === "project_get_context") return "读取项目上下文";
    if (name === "project_list_units") return "读取项目章节";
    if (name === "project_get_unit") return "读取章节正文";
    if (name === "project_list_asset_versions") return "读取资产版本";
    if (name === "project_create_unit") return "新建项目分集";
    if (name === "project_update_unit") return "更新分集正文";
    if (name === "project_extract_asset_candidates") return "登记资产候选";
    if (name === "project_confirm_asset_candidate") return "确认资产候选";
    if (name === "project_create_or_update_shots") return "保存项目镜头";
    if (name === "project_link_shot_asset") return "关联镜头素材";
    if (name === "project_start_workflow_step") return "启动流程步骤";
    if (name === "project_update_workflow_step") return "保存流程结论";
    if (name === "project_link_asset") return "引用项目资产";
    if (name === "project_upsert_asset_version") return "创建资产版本";
    if (name === "project_register_task_output") return "登记任务产物";
    return name;
}

function isReadTool(name: string) {
    return name === "canvas_get_state" || name === "canvas_get_selection" || name === "canvas_get_image_annotations" || name === "canvas_export_snapshot" || isProjectAgentReadTool(name);
}

function isMcpToolItem(item?: AgentEventItem) {
    return item?.type === "mcp_tool_call";
}

function toolDetail(item?: AgentEventItem) {
    return { server: item?.server, tool: item?.tool, status: item?.status, arguments: item?.arguments, result: parseToolResult(item?.result), error: item?.error };
}

function toolSummary(item?: AgentEventItem) {
    const result = parseToolResult(item?.result);
    const nodeField = objectField(result, "nodes");
    const connectionField = objectField(result, "connections");
    const annotationField = objectField(result, "annotations");
    const nodes = Array.isArray(nodeField) ? nodeField : [];
    const connections = Array.isArray(connectionField) ? connectionField : [];
    if (Array.isArray(annotationField)) return `读取到 ${annotationField.length} 个图片标注`;
    if (Array.isArray(nodeField) || Array.isArray(connectionField)) return `读取到 ${nodes.length} 个节点，${connections.length} 条连线`;
    return "工具调用完成";
}

function parseToolResult(result: unknown) {
    const content = objectField(result, "content");
    const text = Array.isArray(content) ? content.map((item) => objectField(item, "text")).filter((item): item is string => typeof item === "string").join("\n") : "";
    try {
        return text ? JSON.parse(text) : result;
    } catch {
        return text || result;
    }
}

function normalizeText(value: unknown) {
    if (typeof value === "string") return value.trim();
    if (value instanceof Error) return value.message;
    if (value == null) return "";
    return JSON.stringify(value, null, 2);
}

function stringText(value: unknown) {
    return typeof value === "string" ? value : "";
}

function objectField(value: unknown, key: string) {
    return value && typeof value === "object" ? (value as Record<string, unknown>)[key] : undefined;
}

function numberField(value: unknown, key: string) {
    const field = objectField(value, key);
    return typeof field === "number" ? field : 0;
}

function mergeAgentText(prev: string, next: string) {
    if (!next || prev === next || prev.endsWith(next)) return prev;
    if (next.startsWith(prev)) return next;
    for (let size = Math.min(prev.length, next.length); size > 0; size--) {
        if (prev.endsWith(next.slice(0, size))) return `${prev}${next.slice(size)}`;
    }
    const half = Math.floor(prev.length / 2);
    if (prev.length > 12 && next.length > 12 && prev.slice(half) === next.slice(0, prev.length - half)) return prev;
    return `${prev}${next}`;
}

function promptWithAttachments(text: string, attachments: AgentAttachment[]) {
    if (!attachments.length) return text;
    const names = attachments.map((item) => item.name).join("、");
    return [text, `用户上传了 ${attachments.length} 张图片附件：${names}。`].filter(Boolean).join("\n\n");
}

function attachmentPayloadBytes(attachments: AgentAttachment[]) {
    return attachments.reduce((total, item) => total + item.dataUrl.length, 0);
}

function formatBytes(bytes: number) {
    return bytes > 1024 * 1024 ? `${(bytes / 1024 / 1024).toFixed(1)}MB` : `${Math.ceil(bytes / 1024)}KB`;
}

async function fetchAgentJson<T>(endpoint: string, token: string, path: string, init?: RequestInit) {
    const headers = new Headers(init?.headers);
    headers.set("X-Canvas-Agent-Token", token);
    const res = await fetch(`${endpoint}${path}`, { ...init, headers });
    const data = (await res.json().catch(() => ({}))) as T & { error?: string; msg?: string };
    if (!res.ok) throw new Error(data.error || data.msg || "本地 Agent 请求失败");
    return data;
}

async function discoverAgentConfig(endpoint: string) {
    try {
        const res = await fetch(`${endpoint}/config`);
        if (!res.ok) return null;
        const data = (await res.json()) as AgentConfigResponse;
        return data.ok ? data : null;
    } catch {
        return null;
    }
}

function normalizeHistoryMessages(messages: AgentChatItem[]) {
    return messages
        .map((item, index) => ({
            ...item,
            id: item.id || `history-${index}`,
            text: normalizeText(item.text),
        }))
        .filter((item) => item.text);
}

function formatThreadTime(value?: number) {
    if (!value) return "";
    return new Date(value * 1000).toLocaleString();
}

function createId() {
    return createClientId();
}

function agentErrorDetail(error: unknown) {
    return error instanceof Error ? { name: error.name, message: error.message } : { message: String(error || "未知错误") };
}

function clamp(value: number, min: number, max: number) {
    return Math.min(max, Math.max(min, value));
}

function readDataUrl(file: File) {
    return new Promise<string>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result || ""));
        reader.onerror = () => reject(reader.error || new Error("读取图片失败"));
        reader.readAsDataURL(file);
    });
}
