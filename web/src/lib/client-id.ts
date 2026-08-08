import { nanoid } from "nanoid";

/**
 * 浏览器在非安全的局域网 HTTP 页面中可能没有 randomUUID；客户端 ID 只需稳定唯一，
 * 不能让本地 Agent 或画布操作在真正发送前因为浏览器能力缺失而卡死。
 */
export function createClientId(prefix = "") {
    let value = "";
    try {
        value = globalThis.crypto?.randomUUID?.() || nanoid();
    } catch {
        value = "";
    }
    if (!value) value = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    return prefix ? `${prefix}${value}` : value;
}
