import { useEffect, useState } from "react";
import { App, Button, Descriptions, Drawer, Empty, Skeleton, Tag } from "antd";
import { RefreshCw } from "lucide-react";

import { getAdminApiLog, queryAdminApiLogTask, type ApiCallLog } from "@/services/api/auth";

export function ApiLogDetailDrawer({ logId, onClose, onTaskQueried }: { logId: string | null; onClose: () => void; onTaskQueried?: () => void }) {
    const { message, modal } = App.useApp();
    const [log, setLog] = useState<ApiCallLog | null>(null);
    const [loading, setLoading] = useState(false);
    const [queryingTask, setQueryingTask] = useState(false);
    useEffect(() => {
        if (!logId) return;
        let active = true;
        setLoading(true);
        setLog(null);
        void getAdminApiLog(logId)
            .then((result) => active && setLog(result.log))
            .catch((error) => active && message.error(error instanceof Error ? error.message : "读取请求详情失败"))
            .finally(() => active && setLoading(false));
        return () => {
            active = false;
        };
    }, [logId, message]);
    const canQueryTask = Boolean(log?.capability === "video" && log.taskId && log.providerRequestId);
    const queryTask = () => {
        if (!log || !canQueryTask) return;
        modal.confirm({
            title: "确认人工查询上游视频任务？",
            content: "系统只会查询这条日志所属的当前任务，不会重新创建上游任务；若已生成成功，将恢复任务结果并结算对应的未决积分。",
            okText: "查询并恢复",
            cancelText: "取消",
            onOk: async () => {
                setQueryingTask(true);
                try {
                    const result = await queryAdminApiLogTask(log.id);
                    if (!result.recovered) {
                        const nextPollAt = result.task.nextPollAt ? new Date(result.task.nextPollAt) : null;
                        const nextQuery = nextPollAt && !Number.isNaN(nextPollAt.getTime()) ? `；请在 ${nextPollAt.toLocaleString("zh-CN", { hour12: false })} 后再查` : "";
                        message.info(`上游任务仍在处理中${result.providerStatus ? `（${result.providerStatus}）` : ""}${nextQuery}`);
                    }
                    else if (result.billingSettled) message.success("任务已恢复并完成积分结算");
                    else message.warning("任务已恢复，但积分结算仍需人工核对");
                    onTaskQueried?.();
                } catch (error) {
                    message.error(error instanceof Error ? error.message : "人工查询上游任务失败");
                } finally {
                    setQueryingTask(false);
                }
            },
        });
    };
    const items = log
        ? [
              ["时间", new Date(log.createdAt).toLocaleString("zh-CN", { hour12: false })],
              ["状态", <Tag color={log.status === "succeeded" ? "success" : "error"}>{log.status === "succeeded" ? "成功" : "失败"}</Tag>],
              ["用户 ID", log.userId],
              ["任务 ID", log.taskId || "--"],
              ["渠道", log.channelName || log.channelId || "--"],
              ["模型", log.model || "--"],
              ["请求阶段", log.requestKind || "--"],
              ["供应商任务 ID", log.providerRequestId || "--"],
              ["方法与路径", `${log.method} ${log.path}`],
              ["HTTP 状态", String(log.statusCode || "--")],
              ["耗时", `${log.durationMs} ms`],
              ["渠道并发上限", log.concurrencyLimit ? String(log.concurrencyLimit) : "--"],
              ["Token", log.usageAvailable ? `${log.inputTokens} 输入 / ${log.outputTokens} 输出 / ${log.cachedTokens} 缓存` : "未返回"],
              ["错误码", log.errorCode || "--"],
              ["错误详情", log.error || "--"],
              ["上游地址", log.upstreamUrl || "--"],
          ].map(([label, children], index) => ({ key: String(index), label, children }))
        : [];
    return (
        <Drawer title="请求详情" extra={canQueryTask ? <Button icon={<RefreshCw className="size-4" />} loading={queryingTask} onClick={queryTask}>查询并恢复任务</Button> : null} open={Boolean(logId)} onClose={onClose} size="min(760px, 100vw)" destroyOnHidden>
            {loading ? <Skeleton active paragraph={{ rows: 10 }} /> : log ? <Descriptions bordered size="small" column={1} items={items} /> : <Empty description="没有请求详情" />}
        </Drawer>
    );
}
