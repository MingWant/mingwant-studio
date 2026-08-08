import { scopedLocalStorage, scopedSessionStorage } from "@/lib/user-scope";

export const AI_CONFIG_SESSION_SECRETS_KEY = "open_ai_canvas:ai_config_secrets:session";
export const AI_CONFIG_PERSISTENT_SECRETS_KEY = "open_ai_canvas:ai_config_secrets:persistent";

const LEGACY_SECRET_ID = "__legacy_default__";

type JsonRecord = Record<string, unknown>;
type SecretMap = Record<string, string>;

// 配置快照会被日志、诊断和浏览器扩展频繁读取；密钥必须与普通配置分仓，且新渠道默认只活到当前标签页会话结束。
export const aiConfigStorage = {
    getItem(name: string) {
        const raw = scopedLocalStorage.getItem(name);
        if (!raw) return null;
        let envelope: JsonRecord;
        try {
            envelope = parseRecord(raw, "AI 配置快照无法解析");
        } catch {
            // 配置快照损坏时不能让 Zustand hydration 阻断登录和画布初始化；
            // 密钥独立保存在会话/持久凭据区，清掉不可解析的普通快照后回到默认配置。
            scopedLocalStorage.removeItem(name);
            return null;
        }
        const config = persistedConfig(envelope);
        if (!config) return raw;

        const sessionSecrets = readSecrets(scopedSessionStorage, AI_CONFIG_SESSION_SECRETS_KEY);
        const persistentSecrets = readSecrets(scopedLocalStorage, AI_CONFIG_PERSISTENT_SECRETS_KEY);
        const channels = Array.isArray(config.channels) ? config.channels : [];
        let migrated = false;

        channels.forEach((value, index) => {
            const channel = asRecord(value);
            if (!channel || channel.scope === "system") return;
            const id = channelId(channel, index);
            const embedded = stringValue(channel.apiKey);
            if (embedded) {
                // 旧版本已经把密钥长期保存；升级时保留用户预期，但立刻移出普通配置快照并显示为已记住。
                persistentSecrets[id] = embedded;
                delete sessionSecrets[id];
                channel.rememberApiKey = true;
                migrated = true;
            } else if (channel.rememberApiKey !== true && !sessionSecrets[id] && persistentSecrets[id]) {
                // 修复旧版或异常中断留下的“密钥已持久化但标志缺失”，避免升级后静默丢失凭据。
                channel.rememberApiKey = true;
                migrated = true;
            }
            channel.apiKey = "";
        });

        const embeddedLegacy = stringValue(config.apiKey);
        if (embeddedLegacy) {
            persistentSecrets[LEGACY_SECRET_ID] = embeddedLegacy;
            migrated = true;
        }
        config.apiKey = "";

        if (migrated) {
            writeSecrets(scopedSessionStorage, AI_CONFIG_SESSION_SECRETS_KEY, sessionSecrets);
            writeSecrets(scopedLocalStorage, AI_CONFIG_PERSISTENT_SECRETS_KEY, persistentSecrets);
            scopedLocalStorage.setItem(name, JSON.stringify(envelope));
        }

        let firstUserKey = "";
        channels.forEach((value, index) => {
            const channel = asRecord(value);
            if (!channel || channel.scope === "system") return;
            const id = channelId(channel, index);
            const key = channel.rememberApiKey === true ? persistentSecrets[id] || "" : sessionSecrets[id] || "";
            channel.apiKey = key;
            if (!firstUserKey && key) firstUserKey = key;
        });
        config.apiKey = firstUserKey || persistentSecrets[LEGACY_SECRET_ID] || sessionSecrets[LEGACY_SECRET_ID] || "";
        return JSON.stringify(envelope);
    },

    setItem(name: string, value: string) {
        const envelope = parseRecord(value, "AI 配置快照无法序列化");
        const config = persistedConfig(envelope);
        if (!config) {
            scopedLocalStorage.setItem(name, value);
            return;
        }

        const sessionSecrets = readSecrets(scopedSessionStorage, AI_CONFIG_SESSION_SECRETS_KEY);
        const persistentSecrets = readSecrets(scopedLocalStorage, AI_CONFIG_PERSISTENT_SECRETS_KEY);
        const activeIds = new Set<string>();
        const channels = Array.isArray(config.channels) ? config.channels : [];

        channels.forEach((value, index) => {
            const channel = asRecord(value);
            if (!channel || channel.scope === "system") return;
            const id = channelId(channel, index);
            const key = stringValue(channel.apiKey);
            activeIds.add(id);
            if (!key) {
                delete sessionSecrets[id];
                delete persistentSecrets[id];
            } else if (channel.rememberApiKey === true) {
                persistentSecrets[id] = key;
                delete sessionSecrets[id];
            } else {
                sessionSecrets[id] = key;
                delete persistentSecrets[id];
            }
            channel.apiKey = "";
        });

        for (const id of Object.keys(sessionSecrets)) {
            if (id !== LEGACY_SECRET_ID && !activeIds.has(id)) delete sessionSecrets[id];
        }
        for (const id of Object.keys(persistentSecrets)) {
            if (id !== LEGACY_SECRET_ID && !activeIds.has(id)) delete persistentSecrets[id];
        }
        delete sessionSecrets[LEGACY_SECRET_ID];
        delete persistentSecrets[LEGACY_SECRET_ID];
        config.apiKey = "";

        // 凭据区写入失败时直接中止；任何分支都不会把密钥回退混入普通配置快照。
        writeSecrets(scopedSessionStorage, AI_CONFIG_SESSION_SECRETS_KEY, sessionSecrets);
        writeSecrets(scopedLocalStorage, AI_CONFIG_PERSISTENT_SECRETS_KEY, persistentSecrets);
        scopedLocalStorage.setItem(name, JSON.stringify(envelope));
    },

    removeItem(name: string) {
        scopedLocalStorage.removeItem(name);
        scopedSessionStorage.removeItem(AI_CONFIG_SESSION_SECRETS_KEY);
        scopedLocalStorage.removeItem(AI_CONFIG_PERSISTENT_SECRETS_KEY);
    },
};

export function clearSessionAiConfigSecrets() {
    scopedSessionStorage.removeItem(AI_CONFIG_SESSION_SECRETS_KEY);
}

function persistedConfig(envelope: JsonRecord) {
    const state = asRecord(envelope.state);
    return state ? asRecord(state.config) : null;
}

function channelId(channel: JsonRecord, index: number) {
    return stringValue(channel.id) || (index === 0 ? "default" : `channel-${index + 1}`);
}

function readSecrets(storage: Pick<StorageLike, "getItem">, name: string): SecretMap {
    const raw = storage.getItem(name);
    if (!raw) return {};
    try {
        const parsed = JSON.parse(raw);
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
        return Object.fromEntries(Object.entries(parsed).filter((entry): entry is [string, string] => typeof entry[1] === "string" && Boolean(entry[1])));
    } catch {
        return {};
    }
}

function writeSecrets(storage: Pick<StorageLike, "setItem" | "removeItem">, name: string, secrets: SecretMap) {
    if (!Object.keys(secrets).length) {
        storage.removeItem(name);
        return;
    }
    storage.setItem(name, JSON.stringify(secrets));
}

type StorageLike = {
    getItem(name: string): string | null;
    setItem(name: string, value: string): void;
    removeItem(name: string): void;
};

function parseRecord(raw: string, message: string): JsonRecord {
    try {
        const parsed = JSON.parse(raw);
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as JsonRecord;
    } catch {
        // 统一转成无敏感正文的错误，避免配置快照被诊断日志原样带出。
    }
    throw new Error(message);
}

function asRecord(value: unknown): JsonRecord | null {
    return value && typeof value === "object" && !Array.isArray(value) ? value as JsonRecord : null;
}

function stringValue(value: unknown) {
    return typeof value === "string" && value.trim() ? value : "";
}
