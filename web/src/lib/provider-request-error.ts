export function isProviderBillingUncertainStatus(status: number | undefined) {
    if (status === 408 || status === 499) return true;
    if (status === 501) return false;
    return typeof status === "number" && status >= 500 && status <= 599;
}

export function providerBillingUncertainMessage(status?: number) {
    const statusText = status ? `（HTTP ${status}）` : "";
    return `上游网关或模型服务异常${statusText}：原请求可能仍在服务端执行并产生费用，请勿立即重试，请先核对供应商后台、任务或账单`;
}

export function providerConnectionUncertainMessage() {
    return "模型连接中断：原请求可能仍在供应商服务端执行并产生费用，请勿立即重试，请先核对供应商后台、任务或账单";
}

export function confirmsProviderWasNotCalled(message: string) {
    const value = message.trim();
    return value.includes("调用供应商前") || value.includes("尚未调用供应商") || value.includes("没有调用供应商") || value.includes("尚未发出上游请求") || value.includes("没有发出上游请求") || /尚未创建计费订单.{0,16}(?:发出上游请求|调用供应商)/.test(value) || /未创建计费订单.{0,16}(?:发出上游请求|调用供应商)/.test(value);
}

/** Backend 已经完成计费边界判断时保留原文，避免通用 5xx 文案覆盖请求编号和可执行处置。 */
export function confirmsProviderBillingUncertain(message: string) {
    const value = message.trim();
    if (!value.includes("请勿立即重试")) return false;
    return (
        /费用.{0,10}(?:不确定|待核对|待确认)/.test(value) ||
        /(?:请求状态不确定|可能(?:已经|仍在)?执行).{0,24}(?:产生费用|计费)/.test(value) ||
        /可能(?:已经|仍在).{0,18}(?:产生费用|计费)/.test(value)
    );
}
