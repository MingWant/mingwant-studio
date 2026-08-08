import { confirmsProviderBillingUncertain, confirmsProviderWasNotCalled } from "./provider-request-error";

/**
 * 把在线 Agent 的单轮失败统一收敛为可执行提示；费用不确定必须优先于“失败后重试”的直觉。
 */
export function onlineAgentFailureMessage(error: unknown, callNumber: number) {
    const raw = error instanceof Error ? error.message : String(error || "在线 Agent 请求失败");
    // 不能只靠 HTTP 状态或“超时”关键词判断；Backend 在 2xx 后流损坏、
    // 请求日志失败和响应交付失败时也会返回稳定的费用待核对语义。
    const notCalled = confirmsProviderWasNotCalled(raw);
    const uncertain = !notCalled && (confirmsProviderBillingUncertain(raw) || /(?:524|504|gateway|网关|连接中断|连接失败|请求状态不确定|结果不完整|可能(?:已经|仍在).{0,12}(?:执行|计费)|network|failed to fetch|timeout|超时)/i.test(raw));
    const handlingNotice = notCalled
        ? "本次模型请求在调用供应商前结束，没有发出该次上游请求；不会产生该次供应商费用。"
        : uncertain && !/(?:费用待核对|可能(?:已经|仍在).{0,18}(?:产生费用|计费)|请勿立即重试)/.test(raw)
            ? "该次请求可能仍在供应商服务端执行并产生费用，请先核对供应商后台或账单。"
            : "";
    return `在线 Agent 第 ${Math.max(1, callNumber)} 次模型调用失败：${raw} ${handlingNotice}本轮已停止，未自动发送下一次模型请求。`.trim();
}
