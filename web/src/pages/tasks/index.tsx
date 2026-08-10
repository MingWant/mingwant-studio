import { Alert, App, Button, Drawer, Form, Input, Modal, Progress, Segmented, Select, Space, Table, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Eye, FileText, FolderKanban, Image as ImageIcon, Play, Plus, RefreshCw, RotateCcw, Search, Video, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";

import { ListToolbar, PageHeader, TableSurface, WorkspacePage } from "@/components/layout/workspace-page";
import { WorkspaceErrorState, WorkspaceState } from "@/components/layout/workspace-state";
import { formatCredits } from "@/constant/credits";
import { isGenerationCostUncertainError } from "@/lib/canvas/canvas-generation-batch";
import { syncGenerationTaskToCanvasStore } from "@/lib/canvas/canvas-generation-task-sync";
import { prefersShortCinematicDelivery, resolveChannelProbeReadiness } from "@/lib/channel-probe-readiness";
import { CONTENT_MODERATION_ERROR_CODE, generationErrorMessage, isContentModerationError } from "@/lib/generation-error";
import { canQueryProviderTask, hasRecoverableProviderResult } from "@/lib/generation-provider-recovery";
import { formatTaskKind, operationOptions, statusLabel } from "@/lib/generation-task-display";
import { listProjects, type ProjectSummary } from "@/services/api/projects";
import { cancelGenerationTask, createAgentSession, createGenerationTask, listGenerationTasks, listTaskLogs, queryFailedProviderTask, queryGenerationTask, recoverGenerationTaskDelivery, retryGenerationTask, type CreateTaskInput, type GenerationTask, type TaskLog, type TaskStatus } from "@/services/api/task-center";
import { useCanvasStore } from "@/stores/canvas/use-canvas-store";
import { hasSystemModelPrice, modelOptionName, resolveModelChannel, resolveModelRequestConfig, useConfigStore, useEffectiveConfig, type AiConfig } from "@/stores/use-config-store";

const taskTableClassName = "app-data-table";
type TaskStatusFilter = "all" | "failed" | "active" | "succeeded";

function taskStatusFilter(value: string | null): TaskStatusFilter {
    return value === "failed" || value === "active" || value === "succeeded" ? value : "all";
}

export default function TasksPage() {
    const { message, modal } = App.useApp();
    const [searchParams, setSearchParams] = useSearchParams();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);
    const projects = useCanvasStore((state) => state.projects);
    const [form] = Form.useForm<CreateTaskInput & { operation: string }>();
    const [tasks, setTasks] = useState<GenerationTask[]>([]);
    const [domainProjects, setDomainProjects] = useState<ProjectSummary[]>([]);
    const [loading, setLoading] = useState(false);
    const [loadedOnce, setLoadedOnce] = useState(false);
    const [loadError, setLoadError] = useState("");
    const [actingId, setActingId] = useState("");
    const [createOpen, setCreateOpen] = useState(false);
    const [creating, setCreating] = useState(false);
    const statusFilter = taskStatusFilter(searchParams.get("status"));
    const setStatusFilter = useCallback((value: TaskStatusFilter) => {
        const next = new URLSearchParams(searchParams);
        next.set("status", value);
        setSearchParams(next, { replace: true });
    }, [searchParams, setSearchParams]);
    const [keyword, setKeyword] = useState("");
    const [projectFilter, setProjectFilter] = useState("all");
    const [page, setPage] = useState(1);
    const [pageSize, setPageSize] = useState(20);
    const [detailTask, setDetailTask] = useState<GenerationTask | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [taskLogs, setTaskLogs] = useState<TaskLog[]>([]);
    const [logsLoading, setLogsLoading] = useState(false);
    const [mediaPreview, setMediaPreview] = useState<{ url: string; kind: "image" | "video"; title: string } | null>(null);
    const syncedCanvasTaskIdsRef = useRef(new Set<string>());
    const tasksRef = useRef<GenerationTask[]>([]);

    const canvasById = useMemo(() => new Map(projects.map((project) => [project.id, project])), [projects]);
    const domainProjectNameById = useMemo(() => new Map(domainProjects.map((item) => [item.project.id, item.project.name])), [domainProjects]);
    const projectOptions = useMemo(() => projects.map((project) => {
        const projectName = project.projectId ? domainProjectNameById.get(project.projectId) : "";
        return { label: projectName ? `${project.title || "未命名画布"} · ${projectName}` : project.title || "未命名画布", value: project.id };
    }), [domainProjectNameById, projects]);
    const filteredTasks = useMemo(() => tasks.filter((task) => {
        if (statusFilter === "all") return true;
        if (statusFilter === "active") return task.status === "queued" || task.status === "running";
        if (statusFilter === "failed") return task.status === "failed" || task.status === "cancelled";
        if (statusFilter === "succeeded") return task.status === "succeeded";
        return false;
    }).filter((task) => {
        if (projectFilter !== "all" && task.projectId !== projectFilter) return false;
        const query = keyword.trim().toLowerCase();
        const context = getTaskCanvasContext(task, canvasById, domainProjectNameById);
        return !query || `${task.prompt} ${task.model || ""} ${formatTaskKind(task)} ${context.canvasName} ${context.projectName}`.toLowerCase().includes(query);
    }), [canvasById, domainProjectNameById, keyword, projectFilter, statusFilter, tasks]);

    useEffect(() => {
        let cancelled = false;
        void listProjects().then((result) => {
            if (!cancelled) setDomainProjects(result.projects);
        }).catch(() => undefined);
        return () => {
            cancelled = true;
        };
    }, []);

    useEffect(() => {
        const maxPage = Math.max(1, Math.ceil(filteredTasks.length / pageSize));
        if (page > maxPage) setPage(maxPage);
    }, [filteredTasks.length, page, pageSize]);

    const syncCompletedCanvasTasks = useCallback(async (items: GenerationTask[]) => {
        const pendingTaskIds = new Set(
            useCanvasStore
                .getState()
                .projects.flatMap((project) => project.nodes)
                .filter((node) => node.metadata?.taskId && (node.metadata.status !== "success" || !node.metadata.content))
                .map((node) => node.metadata!.taskId!),
        );
        const candidates = items.filter((task) => task.status === "succeeded" && pendingTaskIds.has(task.id) && task.projectId && task.type.startsWith("canvas_") && !syncedCanvasTaskIdsRef.current.has(task.id));
        await Promise.all(
            candidates.map(async (task) => {
                syncedCanvasTaskIdsRef.current.add(task.id);
                try {
                    const detail = task.resultJson ? task : await queryGenerationTask(task.id);
                    await syncGenerationTaskToCanvasStore(detail);
                } catch {
                    syncedCanvasTaskIdsRef.current.delete(task.id);
                }
            }),
        );
    }, []);

    const loadTasks = useCallback(async (showLoading = false) => {
        if (showLoading) setLoading(true);
        setLoadError("");
        try {
            const next = await listGenerationTasks();
            setTasks((current) => reconcileTaskSummaries(current, next));
            setLoadedOnce(true);
            void syncCompletedCanvasTasks(next);
            return next;
        } catch (error) {
            setLoadError(error instanceof Error ? error.message : "任务加载失败");
            return undefined;
        } finally {
            if (showLoading) setLoading(false);
        }
    }, [syncCompletedCanvasTasks]);

    const openTaskDetail = useCallback(
        async (task: GenerationTask) => {
            setDetailTask(task);
            setTaskLogs([]);
            setDetailLoading(true);
            setLogsLoading(true);
            try {
                const [detail, logs] = await Promise.all([queryGenerationTask(task.id, { includeBilling: true }), listTaskLogs(task.id)]);
                setDetailTask(detail);
                setTaskLogs(logs);
                if (await syncGenerationTaskToCanvasStore(detail)) message.success("已同步到画布");
            } catch (error) {
                message.error(error instanceof Error ? error.message : "任务详情加载失败");
            } finally {
                setDetailLoading(false);
                setLogsLoading(false);
            }
        },
        [message],
    );

    useEffect(() => {
        tasksRef.current = tasks;
    }, [tasks]);

    useEffect(() => {
        let stopped = false;
        let timer = 0;
        const poll = async (initial = false) => {
            const next = await loadTasks(initial);
            if (stopped) return;
            const items = next || tasksRef.current;
            const hasActiveTasks = items.some((task) => task.status === "queued" || task.status === "running");
            timer = window.setTimeout(() => void poll(false), document.hidden ? 60_000 : hasActiveTasks ? 10_000 : 60_000);
        };
        const handleVisibility = () => {
            if (document.hidden) return;
            window.clearTimeout(timer);
            void poll(false);
        };
        void poll(true);
        document.addEventListener("visibilitychange", handleVisibility);
        return () => {
            stopped = true;
            window.clearTimeout(timer);
            document.removeEventListener("visibilitychange", handleVisibility);
        };
    }, [loadTasks]);

    const runAction = useCallback(async (id: string, action: "retry" | "cancel", confirmNewProviderRequest = false) => {
        setActingId(id);
        try {
            const next = action === "retry" ? await retryGenerationTask(id, confirmNewProviderRequest) : await cancelGenerationTask(id);
            setTasks((items) => items.map((item) => (item.id === id ? { ...item, ...next, billing: undefined } : item)));
            if (action === "retry") {
                setStatusFilter("active");
                setPage(1);
            }
            window.dispatchEvent(new CustomEvent("wallet:updated"));
            if (!(await loadTasks(false))) message.warning("操作已成功，但任务列表刷新失败，请稍后手动刷新");
            if (action === "retry") {
                message.success("任务已重新入队");
            } else {
                const cancellationDetails = next.error?.trim();
                if (next.status === "failed") message.error(cancellationDetails || "任务已在取消生效前失败");
                else if (cancellationDetails && (isGenerationCostUncertainError(cancellationDetails) || /核对|失败/.test(cancellationDetails))) message.warning(cancellationDetails);
                else message.success(cancellationDetails || "任务已取消");
            }
        } catch (error) {
            window.dispatchEvent(new CustomEvent("wallet:updated"));
            // 刷新函数会自行降级；无论刷新结果如何，都保留真正的写操作错误。
            await loadTasks(false);
            message.error(error instanceof Error ? error.message : "操作失败");
        } finally {
            setActingId("");
        }
    }, [loadTasks, message, setStatusFilter]);

    const retryTask = useCallback((task: GenerationTask) => {
        if (task.deliveryRecoverable) {
            message.warning("该任务已有可恢复的本地结果检查点，请先恢复已保存结果；恢复不会再次调用供应商");
            return;
        }
        if (canQueryProviderTask(task)) {
            message.warning("上一笔任务仍有可查询的上游结果，请先打开详情并手动查询；无法恢复时再创建新的请求");
            return;
        }
        const billingPending = task.billing?.status === "reserved" || task.billing?.status === "running" || task.billing?.status === "uncertain";
        const uncertainProviderFailure = billingPending || isGenerationCostUncertainError(task.error);
        const mayUseStructureRepair = task.type === "agent_storyboard" || task.type === "agent_storyboard_rows";
        modal.confirm({
            title: uncertainProviderFailure ? "确认已核对原任务费用？" : "确认重新生成？",
            content: uncertainProviderFailure
                ? `原任务的费用状态仍可能待核对。继续会提交新的生成尝试并可能重复计费${mayUseStructureRepair ? "；若分镜结构仍不合法，还可能按原任务授权再发起 1 次修复请求" : ""}。确认后系统保留旧记录，不再要求先找管理员处理。`
                : mayUseStructureRepair
                    ? "重新生成会先发起 1 次文本请求；若分镜结构校验失败，将按原任务授权再发起最多 1 次修复请求。自定义 API Key 可能产生最多 2 次供应商调用费用。"
                    : hasRecoverableProviderResult(task)
                        ? "此任务已有上游任务 ID。继续会清除旧的恢复状态并创建新的供应商请求，可能产生新费用；如只想取回旧结果，请先使用“手动查询任务”。"
                        : "重新生成会创建一笔新的供应商请求并可能产生费用。请确认已经检查原任务失败原因和渠道配置。",
            okText: "重新生成",
            cancelText: "取消",
            onOk: () => runAction(task.id, "retry", true),
        });
    }, [message, modal, runAction]);

    const recoverTaskDelivery = useCallback(async (task: GenerationTask) => {
        setActingId(task.id);
        try {
            const result = await recoverGenerationTaskDelivery(task.id);
            setDetailTask((current) => current?.id === task.id ? result.task : current);
            setTasks((items) => items.map((item) => item.id === task.id ? { ...item, ...result.task } : item));
            window.dispatchEvent(new CustomEvent("wallet:updated"));
            try {
                await syncGenerationTaskToCanvasStore(result.task);
            } catch (syncError) {
                message.warning(syncError instanceof Error ? `任务已恢复，但本地画布回填失败：${syncError.message}` : "任务已恢复，但本地画布回填失败");
            }
            try {
                setTaskLogs(await listTaskLogs(task.id));
            } catch {
                message.warning("任务已恢复，但日志刷新失败");
            }
            void loadTasks(false);
            if (result.billingSettled) message.success("已从本地检查点恢复任务，未再次调用供应商");
            else message.warning("已从本地检查点恢复任务，未再次调用供应商；积分结算仍需管理员核对");
        } catch (error) {
            await loadTasks(false);
            try {
                const latest = await queryGenerationTask(task.id);
                setDetailTask((current) => current?.id === task.id ? latest : current);
            } catch {
                // 主错误已经包含恢复结果；详情读回失败只保留列表中的最新状态。
            }
            message.error(error instanceof Error ? error.message : "恢复已保存结果失败");
        } finally {
            setActingId("");
        }
    }, [loadTasks, message]);

    const confirmCancelTask = useCallback((task: GenerationTask) => {
        modal.confirm({
            title: task.status === "running" ? "取消正在运行的任务？" : "取消排队任务？",
            content: task.status === "running"
                ? "系统会停止本地跟踪并请求后端取消，但不能保证供应商立即停止。原请求可能仍在执行并产生费用，取消后费用将进入待核对状态，请勿立即重试。"
                : "排队任务通常会在调用供应商前取消并退回冻结积分；若任务恰好已经开始，供应商仍可能执行并产生费用，届时请先核对费用再重试。",
            okText: "确认取消",
            cancelText: "继续等待",
            centered: true,
            okButtonProps: { danger: true },
            onOk: () => runAction(task.id, "cancel"),
        });
    }, [modal, runAction]);

    const queryProviderTask = async (task: GenerationTask) => {
        const waitText = providerQueryScheduleText(task.nextPollAt);
        if (waitText && waitText !== "现在可以再次查询") {
            message.info(`上游任务仍在处理中，${waitText}；当前不会访问供应商`);
            return;
        }
        setActingId(task.id);
        try {
            const result = await queryFailedProviderTask(task.id);
            try {
                setTaskLogs(await listTaskLogs(task.id));
            } catch (logError) {
                message.warning(logError instanceof Error ? `任务状态已查询，但日志刷新失败：${logError.message}` : "任务状态已查询，但日志刷新失败");
            }
            if (!result.recovered) {
                setDetailTask((current) => current?.id === task.id ? result.task : current);
                setTasks((items) => items.map((item) => (item.id === task.id ? { ...item, ...result.task } : item)));
                const nextQuery = providerQueryScheduleText(result.task.nextPollAt);
                message.info(`上游结果仍在准备中${result.providerStatus ? `（${result.providerStatus}）` : ""}${nextQuery ? `；${nextQuery}` : ""}`);
                return;
            }
            setDetailTask(result.task);
            setTasks((items) => items.map((item) => (item.id === task.id ? { ...item, ...result.task } : item)));
            window.dispatchEvent(new CustomEvent("wallet:updated"));
            void loadTasks(false);
            try {
                await syncGenerationTaskToCanvasStore(result.task);
            } catch (syncError) {
                const detail = syncError instanceof Error ? `：${syncError.message}` : "";
                const billingNotice = result.billingSettled ? "" : "，且积分结算需要管理员核对";
                message.warning(`上游结果已恢复${billingNotice}，但本地画布回填失败${detail}`);
                return;
            }
            if (result.billingSettled) message.success("已获取上游结果，任务已恢复并完成结算");
            else message.warning("结果已恢复，但积分结算需要管理员核对");
        } catch (error) {
            await loadTasks(false);
            try {
                const latest = await queryGenerationTask(task.id);
                setDetailTask((current) => current?.id === task.id ? latest : current);
            } catch {
                // 上游查询的主错误优先展示；详情会在下一次轮询或手动打开时重读。
            }
            message.error(error instanceof Error ? error.message : "查询上游任务失败");
        } finally {
            setActingId("");
        }
    };

    const submitTask = async () => {
        const values = await form.validateFields();
        let confirmedTextModel = "";
        let confirmedChannelProbeTaskId = "";
        let confirmedToolProbeTaskId = "";
        let confirmedAgentRequestConfig: ReturnType<typeof resolveModelRequestConfig> | undefined;
        if (values.operation === "agent_session") {
            confirmedTextModel = values.model?.trim() || effectiveConfig.textModel || effectiveConfig.model;
            if (!isAiConfigReady(effectiveConfig, confirmedTextModel)) {
                message.error("请先在设置里配置可用的文本模型、Base URL 和 API Key");
                return;
            }
            const requestConfig = resolveModelRequestConfig(effectiveConfig, confirmedTextModel);
            // 费用确认前已经冻结渠道、协议和凭据；确认后不能再按裸模型名重新解析同名渠道。
            confirmedAgentRequestConfig = requestConfig;
            const channel = (requestConfig.resolvedChannelId && effectiveConfig.channels.find((item) => item.id === requestConfig.resolvedChannelId)) || resolveModelChannel(effectiveConfig, confirmedTextModel);
            const modelLabel = modelOptionName(requestConfig.model) || requestConfig.model;
            if (channel.scope === "system" && !hasSystemModelPrice(channel, modelLabel)) {
                modal.warning({
                    title: "系统模型尚未配置用户积分价格",
                    content: `LLM 测活本身不扣积分，但任务中心创建 Agent 分镜需要先完成该模型的用户积分定价。本次没有创建任务或调用供应商，请联系管理员在模型管理中设置价格后再试。`,
                    okText: "知道了",
                    centered: true,
                });
                return;
            }
            const readiness = resolveChannelProbeReadiness(channel, modelLabel, requestConfig.interfaceType);
            if (prefersShortCinematicDelivery(modelLabel, requestConfig.interfaceType)) {
                const continueWithRisk = await new Promise<boolean>((resolve) => modal.confirm({
                    title: "当前模型存在长请求超时风险",
                    content: "这个兼容模型的长 JSON 分镜和工具请求可能在上游网关超时。测活只供管理员诊断，不会阻止用户调用；你仍可继续创建任务，若实际失败可按费用状态确认后重试，也可以回到画布使用手动交付降低等待风险。",
                    okText: "继续创建",
                    cancelText: "取消",
                    centered: true,
                    onOk: () => resolve(true),
                    onCancel: () => resolve(false),
                }));
                if (!continueWithRisk) return;
            }
            confirmedChannelProbeTaskId = readiness.probeTaskId || "";
            confirmedToolProbeTaskId = readiness.toolProbeTaskId || "";
            const confirmed = await new Promise<boolean>((resolve) => modal.confirm({
                title: "确认创建 Agent 分镜任务",
                content: `模型：${modelLabel}。测活状态仅供管理员诊断，本次即使未测活或最近一次测活失败，仍会按当前配置发起请求；若上游实际失败，系统会保留真实错误和费用状态，用户可在确认后重试。系统先发起 1 次文本请求；若分镜结构校验失败，将自动发起最多 1 次修复请求。自定义 API Key 可能产生最多 2 次供应商调用费用。`,
                okText: "确认并允许一次修复",
                cancelText: "取消",
                onOk: () => resolve(true),
                onCancel: () => resolve(false),
            }));
            if (!confirmed) return;
        }
        setCreating(true);
        try {
            if (values.operation === "agent_session") {
                const requestConfig = confirmedAgentRequestConfig;
                if (!requestConfig) throw new Error("任务配置已失效，请重新打开创建窗口并确认模型");
                const detail = await createAgentSession({ projectId: values.projectId, prompt: values.prompt, config: backendProviderConfig(requestConfig), channelProbeTaskId: confirmedChannelProbeTaskId || undefined, toolProbeTaskId: confirmedToolProbeTaskId || undefined, allowPaidStructureRepair: true });
                setTasks((items) => [...detail.tasks, ...items]);
            } else {
                const videoModel = values.model?.trim() || effectiveConfig.videoModel || effectiveConfig.model;
                if (values.operation !== "compare_versions" && !isAiConfigReady(effectiveConfig, videoModel)) {
                    message.error("请先在设置里配置可用的视频模型、Base URL 和 API Key");
                    return;
                }
                const requestConfig = resolveModelRequestConfig(effectiveConfig, videoModel);
                const channel = (requestConfig.resolvedChannelId && effectiveConfig.channels.find((item) => item.id === requestConfig.resolvedChannelId)) || resolveModelChannel(effectiveConfig, videoModel);
                if (channel.scope === "system" && !hasSystemModelPrice(channel, modelOptionName(requestConfig.model) || requestConfig.model)) {
                    message.error("当前系统视频模型尚未配置用户积分价格；本次没有创建任务或调用供应商，请联系管理员先完成模型定价。");
                    return;
                }
                const task = await createGenerationTask({
                    projectId: values.projectId,
                    type: `video_${values.operation}`,
                    operation: values.operation,
                    prompt: values.prompt,
                    provider: values.operation === "compare_versions" ? "internal-agent" : "openai-compatible",
                    model: values.operation === "compare_versions" ? "version-router" : requestConfig.model,
                    input: {
                        source: "tasks-page",
                        mode: values.operation === "compare_versions" ? "workflow" : "video",
                        prompt: buildVideoOperationPrompt(values.operation, values.prompt),
                        config: values.operation === "compare_versions" ? undefined : backendProviderConfig(requestConfig),
                        metadata: { videoEditOperation: values.operation },
                    },
                });
                setTasks((items) => [task, ...items]);
            }
            setStatusFilter("active");
            setPage(1);
            setCreateOpen(false);
            form.resetFields();
            message.success("任务已创建");
        } catch (error) {
            message.error(error instanceof Error ? error.message : "任务创建失败");
        } finally {
            setCreating(false);
        }
    };

    const columns = useMemo<ColumnsType<GenerationTask>>(
        () => [
            {
                title: "画布（项目）",
                width: 190,
                responsive: ["lg"],
                render: (_, task) => {
                    const context = getTaskCanvasContext(task, canvasById, domainProjectNameById);
                    return (
                        <div className="min-w-0">
                            <div className="truncate text-xs font-medium text-foreground/82" title={context.canvasName}>{context.canvasName}</div>
                            <div className="mt-1 flex min-w-0 items-center gap-1 text-[10px] text-foreground/42" title={context.projectName || "未加入项目"}>
                                <FolderKanban className="size-3 shrink-0" />
                                <span className="truncate">{context.projectName || "未加入项目"}</span>
                            </div>
                        </div>
                    );
                },
            },
            {
                title: "类型｜模型",
                width: 180,
                responsive: ["md"],
                render: (_, task) => <div className="min-w-0"><div className="truncate text-xs font-medium text-foreground/78">{formatTaskKind(task)}</div><div className="mt-1 truncate text-[10px] text-foreground/42" title={formatModelName(task)}>{formatModelName(task)}</div></div>,
            },
            {
                title: "任务名称",
                dataIndex: "prompt",
                width: 340,
                render: (prompt, task) => {
                    const context = getTaskCanvasContext(task, canvasById, domainProjectNameById);
                    return <div className="flex min-w-0 items-start gap-3 md:items-center">
                        <TaskPreviewThumbnail task={task} onOpen={() => task.previewUrl && setMediaPreview({ url: task.previewUrl, kind: task.previewKind === "video" ? "video" : "image", title: prompt || formatTaskKind(task) })} />
                        <div className="min-w-0 flex-1">
                            <div title={prompt} className="line-clamp-2 max-w-full break-words text-xs font-medium leading-5 text-foreground/88">{prompt || "未命名任务"}</div>
                            <div className={`mt-1 hidden truncate text-[10px] md:block ${task.status === "failed" ? "text-red-600 dark:text-red-400" : task.status === "cancelled" ? "text-amber-600 dark:text-amber-400" : "text-foreground/38"}`} title={task.error ? generationErrorMessage(task.error) : undefined}>
                                {task.status === "failed" || task.status === "cancelled" ? taskAttentionReason(task) : task.stage || statusLabel[task.status]}
                            </div>
                            <div className="mt-2 space-y-2 md:hidden">
                                <div className="truncate text-[10px] text-foreground/45">{formatTaskKind(task)} · {formatModelName(task)}</div>
                                <div className="truncate text-[10px] text-foreground/38">{context.canvasName}{context.projectName ? ` · ${context.projectName}` : ""}</div>
                                <div className="flex items-center justify-between gap-3">
                                    <div className="flex min-w-0 items-center gap-2 text-[11px]"><span className={`size-1.5 shrink-0 rounded-full ${statusDotClassName(task.status)}`} /><span>{statusLabel[task.status]}</span><TaskBilling billing={task.billing} error={task.error} /></div>
                                    <Space size={0}>
                                        <Button type="text" size="small" aria-label="查看详情" icon={<Eye className="size-3.5" />} onClick={() => openTaskDetail(task)} />
                                        {task.deliveryRecoverable
                                            ? <Button type="text" size="small" aria-label="恢复已保存结果" title="不调用供应商，恢复本地已保存结果" icon={<RefreshCw className="size-3.5" />} loading={actingId === task.id} onClick={() => void recoverTaskDelivery(task)} />
                                            : (task.status === "failed" || task.status === "cancelled") && task.type !== "channel_health_probe" ? <Button type="text" size="small" aria-label="重新生成" icon={<RotateCcw className="size-3.5" />} loading={actingId === task.id} disabled={task.errorCode === CONTENT_MODERATION_ERROR_CODE || isContentModerationError(task.error)} onClick={() => retryTask(task)} /> : null}
                                        {task.status === "queued" || task.status === "running" ? <Button type="text" size="small" aria-label="取消任务" danger icon={<X className="size-3.5" />} loading={actingId === task.id} onClick={() => confirmCancelTask(task)} /> : null}
                                    </Space>
                                </div>
                            </div>
                        </div>
                    </div>;
                },
            },
            {
                title: "状态",
                width: 135,
                responsive: ["md"],
                render: (_, task) => (
                    <div className="min-w-0">
                        <div className="flex items-center gap-2 text-xs font-medium">
                            <span className={`size-1.5 shrink-0 rounded-full ${statusDotClassName(task.status)}`} />
                            <span>{statusLabel[task.status]}</span>
                            {task.status === "queued" || task.status === "running" ? <span className="ml-auto text-[10px] tabular-nums text-foreground/42">{task.progress || 0}%</span> : null}
                        </div>
                        {task.status === "queued" || task.status === "running" ? <Progress className="!mb-0 !mt-1.5 block" percent={task.progress || 0} showInfo={false} size={[90, 3]} strokeColor="var(--workspace-accent)" /> : null}
                    </div>
                ),
            },
            {
                title: "重试",
                dataIndex: "attempts",
                width: 75,
                align: "center",
                responsive: ["lg"],
                render: (attempts: number) => <span className="text-xs tabular-nums text-foreground/55">{Math.max(0, (attempts || 1) - 1)} 次</span>,
            },
            {
                title: "创建时间",
                dataIndex: "createdAt",
                width: 145,
                responsive: ["lg"],
                render: (value) => <TaskDate value={value} />,
            },
            {
                title: "消耗积分",
                width: 130,
                align: "right",
                responsive: ["md"],
                render: (_, task) => <TaskBilling billing={task.billing} error={task.error} />,
            },
            {
                title: "操作",
                width: 168,
                align: "right",
                responsive: ["md"],
                render: (_, task) => (
                    <Space size={2}>
                        <Button type="text" size="small" icon={<Eye className="size-3.5" />} onClick={() => openTaskDetail(task)}>详情</Button>
                        {task.deliveryRecoverable
                            ? <Button type="text" size="small" icon={<RefreshCw className="size-3.5" />} loading={actingId === task.id} onClick={() => void recoverTaskDelivery(task)}>恢复结果</Button>
                            : (task.status === "failed" || task.status === "cancelled") && task.type !== "channel_health_probe" ? <Button type="text" size="small" aria-label="重新生成" title={task.errorCode === CONTENT_MODERATION_ERROR_CODE || isContentModerationError(task.error) ? "内容审核未通过，请修改提示词后新建任务" : "重新生成"} icon={<RotateCcw className="size-3.5" />} loading={actingId === task.id} disabled={task.errorCode === CONTENT_MODERATION_ERROR_CODE || isContentModerationError(task.error)} onClick={() => retryTask(task)}>重试</Button> : null}
                        {task.status === "queued" || task.status === "running" ? <Button type="text" size="small" aria-label="取消任务" danger icon={<X className="size-3.5" />} loading={actingId === task.id} onClick={() => confirmCancelTask(task)}>取消</Button> : null}
                    </Space>
                ),
            },
        ],
        [actingId, canvasById, confirmCancelTask, domainProjectNameById, openTaskDetail, recoverTaskDelivery, retryTask],
    );

    return (
        <>
            <WorkspacePage grid>
                <PageHeader
                    icon="tasks"
                    title="任务中心"
                    description="先处理失败任务，再跟踪运行进度和检查生成结果。"
                    meta={<span className="text-xs text-foreground/45">{loadedOnce ? `${filteredTasks.length} 个任务` : "等待首次同步"}{loading ? " · 正在同步" : ""}</span>}
                    actions={(
                        <>
                            <Button icon={<RefreshCw className={`size-3.5 ${loading ? "animate-spin" : ""}`} />} onClick={() => void loadTasks(true)}>刷新</Button>
                            <Button type="primary" icon={<Plus className="size-3.5" />} onClick={() => setCreateOpen(true)}>新建任务</Button>
                        </>
                    )}
                />
                <ListToolbar active={Boolean(keyword || projectFilter !== "all")} onReset={() => { setKeyword(""); setProjectFilter("all"); setPage(1); }}>
                    <Input id="task-search" name="taskSearch" allowClear className="app-list-search" prefix={<Search className="size-4 text-foreground/40" />} value={keyword} placeholder="搜索任务、模型、画布或项目" onChange={(event) => { setKeyword(event.target.value); setPage(1); }} />
                    <Select className="w-full sm:w-48" value={projectFilter} onChange={(value) => { setProjectFilter(value); setPage(1); }} options={[{ label: "全部画布", value: "all" }, ...projectOptions]} />
                    <Segmented
                        size="small"
                        value={statusFilter}
                        onChange={(value) => { setStatusFilter(value as typeof statusFilter); setPage(1); }}
                        options={[
                            { label: `全部 ${tasks.length}`, value: "all" },
                            { label: `需要处理 ${tasks.filter((task) => task.status === "failed" || task.status === "cancelled").length}`, value: "failed" },
                            { label: `运行中 ${tasks.filter((task) => task.status === "queued" || task.status === "running").length}`, value: "active" },
                            { label: `已完成 ${tasks.filter((task) => task.status === "succeeded").length}`, value: "succeeded" },
                        ]}
                    />
                </ListToolbar>

                {!loadedOnce && !loading && loadError ? (
                    <WorkspaceErrorState title="任务列表加载失败" description={loadError} onRetry={() => void loadTasks(true)} />
                ) : (
                    <>
                {loadedOnce && loadError ? <Alert type="warning" showIcon message="任务刷新失败，仍显示上次成功同步的结果" description={loadError} action={<Button size="small" onClick={() => void loadTasks(true)}>重试</Button>} /> : null}
                <TableSurface className="task-table-surface">
                    <Table
                        rowKey="id"
                        size="middle"
                        className={taskTableClassName}
                        columns={columns}
                        dataSource={filteredTasks}
                        loading={loading || !loadedOnce}
                        rowClassName={() => "task-table-row align-middle"}
                        tableLayout="fixed"
                        pagination={{ current: page, pageSize, total: filteredTasks.length, showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: (total, range) => `${range[0]}-${range[1]} / 共 ${total} 个任务`, onChange: (nextPage, nextPageSize) => { setPage(nextPageSize !== pageSize ? 1 : nextPage); setPageSize(nextPageSize); } }}
                        scroll={{ x: 1280 }}
                        locale={{ emptyText: <WorkspaceState compact title={taskEmptyState(statusFilter).title} description={taskEmptyState(statusFilter).description} /> }}
                    />
                </TableSurface>
                    </>
                )}
            </WorkspacePage>
            <Modal title="新建异步生成任务" open={createOpen} onCancel={() => setCreateOpen(false)} onOk={submitTask} confirmLoading={creating} okText="创建任务">
                <Form form={form} layout="vertical" initialValues={{ operation: "agent_session" }}>
                    <Form.Item name="operation" label="任务类型" rules={[{ required: true, message: "请选择任务类型" }]}>
                        <Select options={operationOptions} />
                    </Form.Item>
                    <Form.Item name="prompt" label="创作指令" rules={[{ required: true, message: "请输入创作指令" }]}>
                        <Input.TextArea rows={5} placeholder="描述短剧、MV、TVC 或要执行的视频编辑操作" />
                    </Form.Item>
                    <Form.Item name="projectId" label="绑定画布">
                        <Select allowClear showSearch optionFilterProp="label" options={projectOptions} placeholder={projectOptions.length ? "可选，选择要绑定的画布" : "暂无本地画布"} />
                    </Form.Item>
                    <Form.Item name="model" label="目标模型">
                        <Input placeholder="可选，例如 seedance、kling、wan、nano-banana" />
                    </Form.Item>
                </Form>
            </Modal>
            <Drawer title="任务详情" open={Boolean(detailTask)} onClose={() => setDetailTask(null)} size="large" destroyOnHidden>
                {detailTask ? (
                    <div className="space-y-5">
                        <div className="grid border-y border-border text-sm sm:grid-cols-2">
                            <InfoItem label="状态" value={statusLabel[detailTask.status]} />
                            <InfoItem label="画布名称" value={getTaskCanvasContext(detailTask, canvasById, domainProjectNameById).canvasName} />
                            <InfoItem label="任务类型" value={formatTaskKind(detailTask)} />
                            <InfoItem label="模型" value={formatModelName(detailTask)} />
                            <InfoItem label="尝试次数" value={`第 ${detailTask.attempts || 1} 次`} />
                            <InfoItem label="创建时间" value={formatDate(detailTask.createdAt)} />
                            {detailTask.nextPollAt ? <InfoItem label="下次可查询" value={providerQueryScheduleText(detailTask.nextPollAt) || formatDate(detailTask.nextPollAt)} /> : null}
                        </div>
                        {canQueryProviderTask(detailTask) || detailTask.deliveryRecoverable ? <div className="flex justify-end gap-2">
                            {detailTask.deliveryRecoverable ? <Button type="primary" icon={<RefreshCw className="size-4" />} loading={actingId === detailTask.id} onClick={() => void recoverTaskDelivery(detailTask)}>恢复已保存结果</Button> : null}
                            {canQueryProviderTask(detailTask) && !detailTask.deliveryRecoverable ? <Button icon={<RefreshCw className="size-4" />} title={providerQueryScheduleText(detailTask.nextPollAt)} loading={actingId === detailTask.id} onClick={() => void queryProviderTask(detailTask)}>手动查询任务</Button> : null}
                        </div> : null}
                        {detailTask.error ? <pre className="max-h-28 overflow-auto whitespace-pre-wrap border-l-2 border-red-500 bg-red-50 px-3 py-2 text-xs text-red-700 dark:bg-red-950/30 dark:text-red-300">{generationErrorMessage(detailTask.error)}</pre> : null}
                        <TaskResultMedia value={detailTask.resultJson} taskType={detailTask.type} />
                        <DetailBlock title="输入" value={detailLoading ? "详情加载中..." : formatTaskJson(detailTask.inputJson)} />
                        <DetailBlock title="结果" value={detailLoading ? "详情加载中..." : formatTaskJson(detailTask.resultJson)} />
                        <div>
                            <Typography.Text strong>日志</Typography.Text>
                            <div className="mt-2 max-h-60 overflow-auto rounded-lg bg-slate-950 p-3 text-xs text-slate-100">
                                {logsLoading ? "日志加载中..." : taskLogs.length ? taskLogs.map((log) => `[${new Date(log.createdAt).toLocaleString()}] ${log.level.toUpperCase()} ${log.message}${log.payload ? `\n${generationErrorMessage(log.payload)}` : ""}`).join("\n\n") : "暂无日志"}
                            </div>
                        </div>
                    </div>
                ) : null}
            </Drawer>
            <Modal
                title={<span className="block truncate pr-8">{mediaPreview?.title || "生成结果预览"}</span>}
                open={Boolean(mediaPreview)}
                onCancel={() => setMediaPreview(null)}
                footer={null}
                centered
                width="min(1040px, calc(100vw - 32px))"
                destroyOnHidden
                className="task-media-preview-modal"
            >
                {mediaPreview?.kind === "video"
                    ? <video src={mediaPreview.url} className="max-h-[76vh] w-full bg-black object-contain" controls playsInline preload="metadata" />
                    : mediaPreview ? <img src={mediaPreview.url} alt={mediaPreview.title} className="max-h-[76vh] w-full bg-black object-contain" /> : null}
            </Modal>
        </>
    );
}

function reconcileTaskSummaries(current: GenerationTask[], next: GenerationTask[]) {
    if (current.length !== next.length) return next;
    const currentById = new Map(current.map((task) => [task.id, task]));
    let changed = false;
    const reconciled = next.map((task) => {
        const previous = currentById.get(task.id);
        if (previous?.updatedAt === task.updatedAt && previous.previewUrl === task.previewUrl && previous.providerRequestId === task.providerRequestId && previous.billing?.status === task.billing?.status && previous.billing?.amountMicrocredits === task.billing?.amountMicrocredits) return previous;
        changed = true;
        return task;
    });
    return changed ? reconciled : current;
}

function TaskResultMedia({ value, taskType }: { value?: string; taskType: string }) {
    const urls = resultMediaUrls(value);
    if (!urls.length) return null;
    return (
        <div>
            <Typography.Text strong>生成结果</Typography.Text>
            <div className="mt-2 grid max-h-[360px] grid-cols-2 gap-2 overflow-auto rounded-lg bg-stone-950 p-2 md:grid-cols-3">
                {urls.map((url, index) => isVideoResult(url, taskType)
                    ? <video key={`${url}-${index}`} src={url} className="aspect-video w-full rounded-md bg-black object-contain" controls preload="metadata" />
                    : <img key={`${url}-${index}`} src={url} alt={`生成结果 ${index + 1}`} className="aspect-square w-full rounded-md bg-black object-contain" />)}
            </div>
        </div>
    );
}

function resultMediaUrls(value?: string) {
    if (!value) return [];
    let parsed: unknown;
    try {
        parsed = JSON.parse(value);
    } catch {
        parsed = value;
    }
    const urls: string[] = [];
    const visit = (item: unknown, key = "") => {
        if (typeof item === "string") {
            const isInlineMedia = /^(data:image\/|data:video\/)/.test(item);
            const isBackendResource = /^\/api\/resources\/[A-Za-z0-9_-]+\/file(?:$|[?#])/.test(item);
            const isMediaPath = /\.(png|jpe?g|webp|gif|avif|mp4|webm|mov)(?:$|\?)/i.test(item);
            const isNamedMediaUrl = /^(https?:|blob:)/.test(item) && /(url|image|video|result|output|media)/i.test(key);
            if ((isInlineMedia || isBackendResource || isMediaPath || isNamedMediaUrl) && !urls.includes(item)) urls.push(item);
            return;
        }
        if (Array.isArray(item)) return item.forEach((value) => visit(value, key));
        if (item && typeof item === "object") Object.entries(item).forEach(([field, value]) => visit(value, field));
    };
    visit(parsed);
    return urls.slice(0, 12);
}

function isVideoResult(value: string, taskType: string) {
    return value.startsWith("data:video/") || /\.(mp4|webm|mov)(?:$|\?)/i.test(value) || taskType.includes("video");
}

function TaskPreviewThumbnail({ task, onOpen }: { task: GenerationTask; onOpen: () => void }) {
    const isVideo = task.previewKind === "video";
    const fallbackVideo = task.type.includes("video");
    if (!task.previewUrl) {
        const Icon = fallbackVideo ? Video : task.type.includes("image") ? ImageIcon : FileText;
        return <span className="grid h-12 w-[68px] shrink-0 place-items-center rounded-md border border-border/70 bg-muted/35 text-foreground/28"><Icon className="size-4" /></span>;
    }
    return (
        <button type="button" onClick={onOpen} className="group relative h-12 w-[68px] shrink-0 overflow-hidden rounded-md border border-border/80 bg-black focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-label={isVideo ? "放大预览生成视频" : "放大预览生成图片"}>
            {isVideo
                ? <video src={task.previewUrl} width={68} height={48} muted playsInline preload="metadata" className="h-full w-full object-cover" />
                : <img src={task.previewUrl} alt="" width={68} height={48} loading="lazy" className="h-full w-full object-cover" />}
            <span className="absolute inset-0 grid place-items-center bg-black/0 text-white opacity-0 transition-[background-color,opacity] duration-150 group-hover:bg-black/30 group-hover:opacity-100 group-focus-visible:bg-black/30 group-focus-visible:opacity-100">
                {isVideo ? <Play className="size-4 fill-current" /> : <Eye className="size-4" />}
            </span>
        </button>
    );
}

function getTaskCanvasContext(task: GenerationTask, canvasById: Map<string, { title: string; projectId?: string }>, projectNameById: Map<string, string>) {
    if (!task.projectId) return { canvasName: "未绑定画布", projectName: "" };
    const canvas = canvasById.get(task.projectId);
    if (canvas) return { canvasName: canvas.title || "未命名画布", projectName: canvas.projectId ? projectNameById.get(canvas.projectId) || "" : "" };
    const projectName = projectNameById.get(task.projectId);
    return projectName ? { canvasName: "项目级任务", projectName } : { canvasName: "画布已移除", projectName: "" };
}

function taskAttentionReason(task: GenerationTask) {
    if (task.errorCode === CONTENT_MODERATION_ERROR_CODE || isContentModerationError(task.error)) return "内容审核未通过，请修改输入后新建任务";
    if (task.error) return generationErrorMessage(task.error);
    if (task.status === "cancelled") return "任务已取消；重新生成前请先核对供应商状态和费用";
    return task.stage || "生成失败，打开详情查看原因";
}

function taskEmptyState(status: TaskStatusFilter) {
    if (status === "all") return { title: "还没有任务", description: "新提交的生成会在这里显示状态和实时进度。" };
    if (status === "active") return { title: "没有运行中的任务", description: "新提交的生成会在这里显示排队状态和实时进度。" };
    if (status === "succeeded") return { title: "还没有已完成任务", description: "生成成功后，结果预览和积分消耗会保留在这里。" };
    return { title: "目前没有需要处理的任务", description: "失败或取消的生成会出现在这里，并提供原因和可用操作。" };
}

function statusDotClassName(status: TaskStatus) {
    if (status === "succeeded") return "bg-emerald-500";
    if (status === "running") return "bg-amber-500";
    if (status === "queued") return "bg-blue-500";
    if (status === "failed") return "bg-red-500";
    return "bg-foreground/30";
}

function TaskDate({ value }: { value?: string }) {
    if (!value) return <span className="text-xs text-foreground/38">-</span>;
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return <span className="text-xs text-foreground/38">-</span>;
    return <div className="text-xs tabular-nums text-foreground/62"><div>{date.toLocaleDateString()}</div><div className="mt-1 text-[10px] text-foreground/38">{date.toLocaleTimeString()}</div></div>;
}

function TaskBilling({ billing, error }: { billing?: GenerationTask["billing"]; error?: string }) {
    const costUncertain = isGenerationCostUncertainError(error) || billing?.status === "uncertain";
    if (!billing) {
        return costUncertain ? <span className="text-xs font-medium text-amber-600 dark:text-amber-300">待核对</span> : <span className="text-xs text-foreground/30">-</span>;
    }
    const amount = formatCredits(billing.amountMicrocredits);
    const note = costUncertain ? "待核对" : billing.status === "settled" ? "已结算" : billing.status === "refunded" ? "已退回" : "预计";
    return <div className="text-right"><div className="text-xs font-medium tabular-nums text-foreground/78">{amount}</div><div className={`mt-1 text-[10px] ${costUncertain ? "text-amber-600 dark:text-amber-300" : "text-foreground/38"}`}>{note}</div></div>;
}

function formatModelName(task: GenerationTask) {
    const raw = (task.model || task.provider || "").trim();
    const model = raw.includes("::") ? raw.split("::").pop()?.trim() || raw : raw;

    if (!model) return "工作流";
    if (model === "version-router") return "版本对比工作流";
    if (model === "workflow-router") return "工作流路由";
    if (model === "internal-agent") return "内置工作流";
    if (model === "openai-compatible") return "OpenAI 兼容接口";
    return model;
}

function formatDate(value?: string) {
    if (!value) return "-";
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function providerQueryScheduleText(value?: string) {
    if (!value) return "";
    const next = new Date(value);
    if (Number.isNaN(next.getTime())) return "";
    const seconds = Math.ceil((next.getTime() - Date.now()) / 1_000);
    if (seconds > 0) return `约 ${seconds} 秒后可再次查询`;
    return "现在可以再次查询";
}

function InfoItem({ label, value }: { label: string; value: string }) {
    return (
        <div className="min-w-0 border-b border-border px-0 py-2.5">
            <Typography.Text type="secondary" className="block text-xs">
                {label}
            </Typography.Text>
            <Typography.Text className="block truncate text-sm" title={value}>
                {value}
            </Typography.Text>
        </div>
    );
}

function DetailBlock({ title, value }: { title: string; value: string }) {
    return (
        <div>
            <Typography.Text strong>{title}</Typography.Text>
            <pre className="mt-2 max-h-60 overflow-auto rounded-md bg-slate-950 p-3 text-xs leading-5 text-slate-100">{value}</pre>
        </div>
    );
}

function formatTaskJson(value?: string) {
    if (!value) return "无";
    try {
        return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
        return value;
    }
}

function backendProviderConfig(config: ReturnType<typeof resolveModelRequestConfig>) {
    return {
        // 系统渠道必须把发送前解析出的 ID 一并交给 Backend；只带代理 Base URL 会让旧配置依赖字符串推断。
        // 测活 ID 仍可随任务保存供管理员审计，但不作为用户调用的必要授权证明。
        channelId: config.channelId,
        apiFormat: config.apiFormat,
        interfaceType: config.interfaceType,
        baseUrl: config.baseUrl,
        apiKey: config.apiKey,
        model: config.model,
        size: config.size,
        quality: config.quality,
        transparentBackground: config.transparentBackground,
        count: config.count,
        videoSeconds: config.videoSeconds,
        vquality: config.vquality,
        videoGenerateAudio: config.videoGenerateAudio,
        videoWatermark: config.videoWatermark,
        audioVoice: config.audioVoice,
        audioFormat: config.audioFormat,
        audioSpeed: config.audioSpeed,
        audioInstructions: config.audioInstructions,
        systemPrompt: config.systemPrompt,
    };
}

function buildVideoOperationPrompt(operation: string, prompt: string) {
    const operationLabel = operationOptions.find((item) => item.value === operation)?.label || "其他视频操作";
    if (operation === "compare_versions") return `请对以下视频结果版本做对比分析，输出推荐版本、差异点和修改建议：\n${prompt}`;
    return `视频编辑任务：${operationLabel}\n创作要求：${prompt}`;
}
