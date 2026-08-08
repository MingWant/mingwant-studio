const AGENT_URL_STORAGE_KEY = "canvas-agent-url";
const AGENT_TOKEN_STORAGE_KEY = "canvas-agent-token";
const LEGACY_QUERY_REJECTION_KEY = "canvas-agent-legacy-query-rejected";

export type CanvasAgentLaunchCredentials = {
    endpoint: string;
    token: string;
    source: "fragment" | "legacy-query" | "session";
};

export function consumeCanvasAgentLaunchCredentials(): CanvasAgentLaunchCredentials | null {
    if (typeof window === "undefined") return null;
    const url = new URL(window.location.href);
    const fragment = new URLSearchParams(url.hash.replace(/^#/, ""));
    const endpoint = fragment.get("agentUrl")?.trim() || "";
    const token = fragment.get("agentToken")?.trim() || "";
    const legacyQueryPresent = url.searchParams.has("agentUrl") || url.searchParams.has("agentToken");

    if (endpoint && token) {
        // Token 只允许从 fragment 接管；即使旧查询参数同时存在，也先从地址栏清除，
        // 避免它继续进入浏览器历史、Referer 或代理日志。
        persistCanvasAgentCredentials(endpoint, token);
        clearLaunchCredentialsFromUrl(url, fragment);
        return { endpoint, token, source: "fragment" };
    }

    if (legacyQueryPresent) {
        // 旧版查询链接可能已经出现在历史或日志中，但不能再把其中的 Token 当成凭据使用。
        // 只清理并返回来源，让界面提示用户更新插件；本次不会写入 sessionStorage。
        clearLaunchCredentialsFromUrl(url, fragment);
        markLegacyQueryRejected();
        return { endpoint: "", token: "", source: "legacy-query" };
    }

    if (endpoint || token) {
        // fragment 不完整时也要清理，避免半截凭据残留在地址栏；不作任何连接尝试。
        clearLaunchCredentialsFromUrl(url, fragment);
    }
    return null;
}

export function hasCanvasAgentLaunchCredentials() {
    if (typeof window === "undefined") return false;
    const url = new URL(window.location.href);
    const fragment = new URLSearchParams(url.hash.replace(/^#/, ""));
    let legacyQueryRejected = false;
    try {
        legacyQueryRejected = window.sessionStorage.getItem(LEGACY_QUERY_REJECTION_KEY) === "1";
    } catch {
        legacyQueryRejected = false;
    }
    return Boolean(
        (fragment.get("agentUrl")?.trim() && fragment.get("agentToken")?.trim())
        || fragment.has("agentUrl")
        || fragment.has("agentToken")
        || url.searchParams.has("agentUrl")
        || url.searchParams.has("agentToken")
        || legacyQueryRejected,
    );
}

export function readCanvasAgentSessionCredentials(): CanvasAgentLaunchCredentials | null {
    if (typeof window === "undefined") return null;
    clearLegacyPersistentToken();
    try {
        const token = window.sessionStorage.getItem(AGENT_TOKEN_STORAGE_KEY)?.trim() || "";
        const endpoint = (window.sessionStorage.getItem(AGENT_URL_STORAGE_KEY) || window.localStorage.getItem(AGENT_URL_STORAGE_KEY) || "").trim();
        return endpoint && token ? { endpoint, token, source: "session" } : null;
    } catch {
        return null;
    }
}

export function persistCanvasAgentCredentials(endpoint: string, token: string) {
    if (typeof window === "undefined") return false;
    clearLegacyPersistentToken();
    let sessionPersisted = false;
    try {
        // Token 只活在当前浏览器会话；地址不是凭据，可保留以便下次连接。
        window.sessionStorage.setItem(AGENT_URL_STORAGE_KEY, endpoint);
        window.sessionStorage.setItem(AGENT_TOKEN_STORAGE_KEY, token);
        sessionPersisted = window.sessionStorage.getItem(AGENT_URL_STORAGE_KEY) === endpoint && window.sessionStorage.getItem(AGENT_TOKEN_STORAGE_KEY) === token;
    } catch {
        sessionPersisted = false;
    }
    try {
        window.localStorage.setItem(AGENT_URL_STORAGE_KEY, endpoint);
    } catch {
        // 地址不是凭据；无法记住地址不影响当前会话连接。
    }
    return sessionPersisted;
}

export function readStoredCanvasAgentEndpoint(fallback: string) {
    if (typeof window === "undefined") return fallback;
    try {
        return window.sessionStorage.getItem(AGENT_URL_STORAGE_KEY) || window.localStorage.getItem(AGENT_URL_STORAGE_KEY) || fallback;
    } catch {
        return fallback;
    }
}

export function readStoredCanvasAgentToken() {
    return readCanvasAgentSessionCredentials()?.token || "";
}

export function consumeLegacyQueryRejection() {
    if (typeof window === "undefined") return false;
    try {
        const rejected = window.sessionStorage.getItem(LEGACY_QUERY_REJECTION_KEY) === "1";
        window.sessionStorage.removeItem(LEGACY_QUERY_REJECTION_KEY);
        return rejected;
    } catch {
        return false;
    }
}

export function isLocalCanvasAgentEndpoint(value: string) {
    try {
        const url = new URL(value);
        return (url.protocol === "http:" || url.protocol === "https:") && ["127.0.0.1", "localhost", "[::1]", "::1"].includes(url.hostname.toLowerCase());
    } catch {
        return false;
    }
}

function clearLegacyPersistentToken() {
    try {
        window.localStorage.removeItem(AGENT_TOKEN_STORAGE_KEY);
    } catch {
        // 浏览器禁用持久存储时，令牌仍只存在当前页面内存，不阻断本地连接。
    }
}

function markLegacyQueryRejected() {
    try {
        window.sessionStorage.setItem(LEGACY_QUERY_REJECTION_KEY, "1");
    } catch {
        // 会话存储不可用时仍已从地址栏清除查询凭据；只是不再跨登录跳转保留升级提示。
    }
}

function clearLaunchCredentialsFromUrl(url: URL, fragment: URLSearchParams) {
    fragment.delete("agentUrl");
    fragment.delete("agentToken");
    url.searchParams.delete("agentUrl");
    url.searchParams.delete("agentToken");
    url.hash = fragment.toString() ? `#${fragment.toString()}` : "";
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
}
