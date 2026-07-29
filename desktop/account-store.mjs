import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";

import { normalizeProxyPassword, normalizeProxyServer, normalizeProxyUsername } from "./proxy-config.mjs";
import { normalizeVideoProvider } from "./video-providers.mjs";

const MAX_ACCOUNT_COUNT = 100;
const MAX_ACCOUNT_NAME_LENGTH = 40;

export class AccountStore {
    constructor(userDataDirectory, { encryptSecret, decryptSecret } = {}) {
        this.directory = path.join(userDataDirectory, "video-workflow");
        this.filePath = path.join(this.directory, "accounts.json");
        this.encryptSecret = encryptSecret;
        this.decryptSecret = decryptSecret;
        this.accounts = [];
        this.mutation = Promise.resolve();
    }

    async load() {
        await mkdir(this.directory, { recursive: true });
        try {
            const parsed = JSON.parse(await readFile(this.filePath, "utf8"));
            if (!Array.isArray(parsed?.accounts)) throw new Error("账号池配置缺少 accounts 数组");
            if (parsed.accounts.length > MAX_ACCOUNT_COUNT) throw new Error(`账号池配置超过 ${MAX_ACCOUNT_COUNT} 个账号`);
            const accounts = parsed.accounts.map(normalizeStoredAccount);
            if (accounts.some((account) => !account)) throw new Error("账号池配置包含无效账号 ID");
            if (new Set(accounts.map((account) => account.id)).size !== accounts.length) throw new Error("账号池配置包含重复账号 ID");
            this.accounts = accounts;
        } catch (error) {
            if (error?.code !== "ENOENT") {
                if (error instanceof SyntaxError) throw new Error(`账号池配置损坏，请检查 ${this.filePath}`, { cause: error });
                throw error;
            }
            this.accounts = [];
        }
        return this.list();
    }

    list() {
        return this.accounts.map(presentAccount);
    }

    get(accountId) {
        const account = this.accounts.find((item) => item.id === accountId);
        return account ? presentAccount(account) : null;
    }

    async add(name, provider, proxyInput) {
        return this.mutate(async () => {
            if (this.accounts.length >= MAX_ACCOUNT_COUNT) throw new Error(`账号数量不能超过 ${MAX_ACCOUNT_COUNT} 个`);
            const now = new Date().toISOString();
            const account = {
                id: randomUUID(),
                name: normalizeAccountName(name, this.accounts.length + 1),
                provider: normalizeVideoProvider(provider),
                proxy: buildProxyConfig(proxyInput, null, this.encryptSecret),
                enabled: true,
                // 新分区没有可复用登录态，先阻止调度，避免任务抢占尚未登录的账号。
                needsLogin: true,
                createdAt: now,
                lastUsedAt: null,
            };
            const nextAccounts = [...this.accounts, account];
            await this.persist(nextAccounts);
            this.accounts = nextAccounts;
            return presentAccount(account);
        });
    }

    async update(accountId, patch) {
        return this.mutate(async () => {
            const index = this.accounts.findIndex((account) => account.id === accountId);
            if (index < 0) throw new Error("账号不存在");
            const current = this.accounts[index];
            const next = {
                ...current,
                ...(Object.hasOwn(patch, "name") ? { name: normalizeAccountName(patch.name, index + 1) } : {}),
                ...(Object.hasOwn(patch, "enabled") ? { enabled: Boolean(patch.enabled) } : {}),
                ...(Object.hasOwn(patch, "needsLogin") ? { needsLogin: Boolean(patch.needsLogin) } : {}),
                ...(Object.hasOwn(patch, "lastUsedAt") ? { lastUsedAt: normalizeDate(patch.lastUsedAt) } : {}),
            };
            const nextAccounts = this.accounts.map((account, accountIndex) => accountIndex === index ? next : account);
            await this.persist(nextAccounts);
            this.accounts = nextAccounts;
            return presentAccount(next);
        });
    }

    async setProxy(accountId, proxyInput) {
        return this.mutate(async () => {
            const index = this.accounts.findIndex((account) => account.id === accountId);
            if (index < 0) throw new Error("账号不存在");
            const current = this.accounts[index];
            const proxy = buildProxyConfig(proxyInput, current.proxy, this.encryptSecret);
            if (sameProxyConfig(current.proxy, proxy)) return { account: presentAccount(current), changed: false };
            const next = {
                ...current,
                proxy,
                // IP 变化后旧登录态可能失效，必须由用户在新网络环境中重新确认后才能接收任务。
                needsLogin: true,
            };
            const nextAccounts = this.accounts.map((account, accountIndex) => accountIndex === index ? next : account);
            await this.persist(nextAccounts);
            this.accounts = nextAccounts;
            return { account: presentAccount(next), changed: true };
        });
    }

    proxyCredentials(accountId) {
        const proxy = this.accounts.find((account) => account.id === accountId)?.proxy;
        if (!proxy?.username) return null;
        let password = "";
        if (proxy.encryptedPassword) {
            if (typeof this.decryptSecret !== "function") throw new Error("当前系统无法解密代理密码");
            try {
                password = normalizeProxyPassword(this.decryptSecret(proxy.encryptedPassword));
            } catch (error) {
                throw new Error("代理密码解密失败，请重新设置该账号的代理", { cause: error });
            }
        }
        return { username: proxy.username, password };
    }

    async remove(accountId) {
        return this.mutate(async () => {
            const index = this.accounts.findIndex((account) => account.id === accountId);
            if (index < 0) throw new Error("账号不存在");
            const removed = this.accounts[index];
            const nextAccounts = this.accounts.filter((_account, accountIndex) => accountIndex !== index);
            await this.persist(nextAccounts);
            this.accounts = nextAccounts;
            return presentAccount(removed);
        });
    }

    async persist(accounts = this.accounts) {
        await mkdir(this.directory, { recursive: true });
        const temporaryPath = `${this.filePath}.${process.pid}.${randomUUID()}.tmp`;
        await writeFile(temporaryPath, `${JSON.stringify({ version: 1, accounts }, null, 2)}\n`, "utf8");
        try {
            await rename(temporaryPath, this.filePath);
        } catch (error) {
            await rm(temporaryPath, { force: true }).catch(() => undefined);
            throw error;
        }
    }

    mutate(operation) {
        const pending = this.mutation.then(operation, operation);
        this.mutation = pending.then(() => undefined, () => undefined);
        return pending;
    }
}

function normalizeStoredAccount(value, index) {
    if (!value || typeof value !== "object" || !isAccountId(value.id)) return null;
    return {
        id: value.id,
        name: normalizeAccountName(value.name, index + 1),
        provider: normalizeVideoProvider(value.provider),
        proxy: normalizeStoredProxy(value.proxy),
        enabled: value.enabled !== false,
        needsLogin: value.needsLogin === true,
        createdAt: normalizeDate(value.createdAt) || new Date().toISOString(),
        lastUsedAt: normalizeDate(value.lastUsedAt),
    };
}

function buildProxyConfig(input, current, encryptSecret) {
    const parsed = normalizeProxyServer(input?.server);
    if (!parsed) {
        if (input?.username || input?.password || input?.preservePassword) throw new Error("填写代理认证信息前必须先填写代理地址");
        return null;
    }
    const username = normalizeProxyUsername(input?.username || parsed.username);
    const suppliedPassword = normalizeProxyPassword(input?.password || parsed.password);
    if (suppliedPassword && !username) throw new Error("保存代理密码前必须填写代理用户名");

    let encryptedPassword = "";
    if (suppliedPassword) {
        if (typeof encryptSecret !== "function") throw new Error("当前系统无法安全保存代理密码，请使用无认证代理");
        try {
            encryptedPassword = normalizeEncryptedPassword(encryptSecret(suppliedPassword));
        } catch (error) {
            throw new Error("代理密码无法写入系统安全存储", { cause: error });
        }
    } else if (input?.preservePassword === true) {
        if (!current?.encryptedPassword || current.server !== parsed.server || current.username !== username) throw new Error("代理地址或用户名改变后，请重新输入代理密码");
        encryptedPassword = current.encryptedPassword;
    }
    return { server: parsed.server, username, encryptedPassword };
}

function normalizeStoredProxy(value) {
    if (!value) return null;
    const parsed = normalizeProxyServer(value.server);
    if (!parsed) throw new Error("账号池配置中的代理地址无效");
    const username = normalizeProxyUsername(value.username);
    const encryptedPassword = username ? normalizeEncryptedPassword(value.encryptedPassword, true) : "";
    return { server: parsed.server, username, encryptedPassword };
}

function normalizeEncryptedPassword(value, optional = false) {
    if (optional && !value) return "";
    if (typeof value !== "string" || value.length > 16_384 || !/^[a-z\d+/]+={0,2}$/i.test(value)) throw new Error("账号池配置中的代理密码密文无效");
    return value;
}

function sameProxyConfig(left, right) {
    if (!left || !right) return left === right;
    return left.server === right.server && left.username === right.username && left.encryptedPassword === right.encryptedPassword;
}

function presentAccount(account) {
    return {
        ...account,
        proxy: account.proxy ? {
            server: account.proxy.server,
            username: account.proxy.username,
            hasPassword: Boolean(account.proxy.encryptedPassword),
        } : null,
    };
}

function normalizeAccountName(value, index) {
    const name = typeof value === "string" ? value.trim().slice(0, MAX_ACCOUNT_NAME_LENGTH) : "";
    return name || `账号 ${index}`;
}

function normalizeDate(value) {
    if (typeof value !== "string" || !value) return null;
    return Number.isNaN(Date.parse(value)) ? null : new Date(value).toISOString();
}

function isAccountId(value) {
    return typeof value === "string" && /^[0-9a-f-]{36}$/i.test(value);
}
