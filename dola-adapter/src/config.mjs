import path from "node:path";
import process from "node:process";
import { randomUUID } from "node:crypto";

const LOCAL_BROWSER_HOSTS = new Set(["127.0.0.1", "[::1]", "::1"]);

function env(name, fallback = "") {
    const value = process.env[name];
    return typeof value === "string" && value.trim() !== "" ? value.trim() : fallback;
}

function positiveInteger(name, fallback, minimum, maximum) {
    const value = Number.parseInt(env(name), 10);
    if (!Number.isFinite(value) || value < minimum || value > maximum) return fallback;
    return value;
}

function positiveDuration(name, fallback, minimum, maximum) {
    const raw = env(name);
    if (!raw) return fallback;
    const match = /^(\d+)\s*(ms|s|m|h)$/i.exec(raw);
    if (!match) return fallback;
    const unit = match[2].toLowerCase();
    const multiplier = unit === "ms" ? 1 : unit === "s" ? 1_000 : unit === "m" ? 60_000 : 3_600_000;
    const value = Number(match[1]) * multiplier;
    return Number.isFinite(value) && value >= minimum && value <= maximum ? value : fallback;
}

export function normalizeBrowserEndpoint(value, allowEmpty = true) {
    const raw = String(value || "").trim();
    if (!raw && allowEmpty) return "";
    let url;
    try {
        url = new URL(raw);
    } catch {
        throw new Error("DOLA 浏览器 CDP 地址不是有效 URL");
    }
    // CDP 等同于浏览器控制权，只允许回环地址，避免管理字段把控制端口暴露到网络。
    if (!["http:", "https:", "ws:", "wss:"].includes(url.protocol) || !LOCAL_BROWSER_HOSTS.has(url.hostname)) {
        throw new Error("DOLA 浏览器 CDP 地址只能连接本机 Chrome/Edge");
    }
    return url.toString();
}

export function loadConfig() {
    const port = positiveInteger("DOLA_ADAPTER_PORT", 8787, 1, 65_535);
    const host = env("DOLA_ADAPTER_HOST", "127.0.0.1");
    const quotaTimeZone = env("DOLA_QUOTA_TIME_ZONE", "Asia/Hong_Kong");
    try {
        new Intl.DateTimeFormat("en-CA", { timeZone: quotaTimeZone }).format();
    } catch {
        throw new Error("DOLA_QUOTA_TIME_ZONE 不是有效的 IANA 时区");
    }
    const dataDir = path.resolve(env("DOLA_ADAPTER_DATA_DIR", path.join(process.cwd(), ".local", "dola-adapter")));
    const profileRoot = path.resolve(env("DOLA_ADAPTER_PROFILE_ROOT", path.join(dataDir, "profiles")));
    const resultRoot = path.resolve(env("DOLA_ADAPTER_RESULT_ROOT", path.join(dataDir, "results")));
    const browserMode = env("DOLA_BROWSER_MODE", "managed").toLowerCase();
    if (!["managed", "cdp"].includes(browserMode)) throw new Error("DOLA_BROWSER_MODE 只支持 managed 或 cdp");
    const browserChannel = env("DOLA_BROWSER_CHANNEL", "").toLowerCase();
    if (browserChannel && !["chrome", "msedge"].includes(browserChannel)) throw new Error("DOLA_BROWSER_CHANNEL 只支持 chrome 或 msedge");
    const browserCdpUrl = normalizeBrowserEndpoint(env("DOLA_BROWSER_CDP_URL"));
    const apiKey = env("DOLA_ADAPTER_API_KEY");
    const adminKey = env("DOLA_ADAPTER_ADMIN_KEY");
    if (!apiKey) throw new Error("DOLA_ADAPTER_API_KEY 未配置");
    if (!adminKey) throw new Error("DOLA_ADAPTER_ADMIN_KEY 未配置");

    return {
        host,
        port,
        publicBaseUrl: env("DOLA_ADAPTER_PUBLIC_BASE_URL", `http://127.0.0.1:${port}`),
        apiKey,
        adminKey,
        dataDir,
        stateFile: path.join(dataDir, "state.json"),
        profileRoot,
        resultRoot,
        browserMode,
        browserChannel,
        browserCdpUrl,
        homeUrl: env("DOLA_HOME_URL", "https://www.dola.com/"),
        quotaTimeZone,
        quotaResetHour: positiveInteger("DOLA_QUOTA_RESET_HOUR", 0, 0, 23),
        headless: env("DOLA_HEADLESS", "false").toLowerCase() === "true",
        pollIntervalMs: positiveDuration("DOLA_POLL_INTERVAL", 15_000, 2_000, 120_000),
        leaseMs: positiveDuration("DOLA_ACCOUNT_LEASE", 45 * 60_000, 60_000, 24 * 3_600_000),
        taskTimeoutMs: positiveDuration("DOLA_TASK_TIMEOUT", 45 * 60_000, 60_000, 24 * 3_600_000),
        browserTimeoutMs: positiveDuration("DOLA_BROWSER_TIMEOUT", 60_000, 5_000, 10 * 60_000),
        maxBodyBytes: positiveInteger("DOLA_MAX_BODY_BYTES", 2 * 1024 * 1024, 64 * 1024, 16 * 1024 * 1024),
        maxDownloadBytes: positiveInteger("DOLA_MAX_DOWNLOAD_BYTES", 512 * 1024 * 1024, 1 * 1024 * 1024, 4 * 1024 * 1024 * 1024),
        maxAccountAttempts: positiveInteger("DOLA_MAX_ACCOUNT_ATTEMPTS", 8, 1, 100),
        instanceId: `${process.pid}:${randomUUID()}`,
    };
}
