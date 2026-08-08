import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";

const STATE_VERSION = 1;

function now() {
    return new Date().toISOString();
}

function clone(value) {
    return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

function normalizeDate(value) {
    if (typeof value !== "string" || !value || Number.isNaN(Date.parse(value))) return null;
    return new Date(value).toISOString();
}

function normalizeState(value) {
    if (!value || typeof value !== "object") throw new Error("Dola Adapter 状态文件格式无效");
    if (value.version !== STATE_VERSION) throw new Error(`Dola Adapter 状态文件版本不支持：${value.version}`);
    if (!Array.isArray(value.accounts) || !Array.isArray(value.tasks)) throw new Error("Dola Adapter 状态文件缺少账号或任务数组");
    return {
        version: STATE_VERSION,
        accounts: value.accounts,
        tasks: value.tasks,
    };
}

export class JsonStore {
    constructor(filePath) {
        this.filePath = path.resolve(filePath);
        this.state = { version: STATE_VERSION, accounts: [], tasks: [] };
        this.mutation = Promise.resolve();
    }

    async load() {
        await mkdir(path.dirname(this.filePath), { recursive: true });
        try {
            this.state = normalizeState(JSON.parse(await readFile(this.filePath, "utf8")));
        } catch (error) {
            if (error?.code !== "ENOENT") throw error;
            await this.persist();
        }
    }

    read(selector) {
        return selector(clone(this.state));
    }

    mutate(operation) {
        const run = this.mutation.then(async () => {
            const result = await operation(this.state);
            await this.persist();
            return clone(result);
        }, async () => {
            const result = await operation(this.state);
            await this.persist();
            return clone(result);
        });
        this.mutation = run.then(() => undefined, () => undefined);
        return run;
    }

    async persist() {
        await mkdir(path.dirname(this.filePath), { recursive: true });
        const temporary = `${this.filePath}.${process.pid}.${randomUUID()}.tmp`;
        await writeFile(temporary, `${JSON.stringify(this.state, null, 2)}\n`, "utf8");
        await rename(temporary, this.filePath);
    }

    account(id) {
        return this.read((state) => state.accounts.find((item) => item.id === id) || null);
    }

    accounts() {
        return this.read((state) => state.accounts);
    }

    task(id) {
        return this.read((state) => state.tasks.find((item) => item.id === id) || null);
    }

    tasks() {
        return this.read((state) => state.tasks);
    }

    taskByRequestKey(requestKey) {
        return this.read((state) => state.tasks.find((item) => item.requestKey === requestKey) || null);
    }

    async addAccount({ name, profileKey, cdpUrl = "" }) {
        return this.mutate((state) => {
            const timestamp = now();
            const account = {
                id: randomUUID(),
                name,
                profileKey,
                cdpUrl,
                enabled: true,
                state: "needs_login",
                quotaRemaining: null,
                quotaResetAt: null,
                cooldownUntil: null,
                lastErrorCode: "",
                lastErrorAt: null,
                lastUsedAt: null,
                leaseOwner: "",
                leaseExpiresAt: null,
                createdAt: timestamp,
                updatedAt: timestamp,
            };
            state.accounts.push(account);
            return account;
        });
    }

    async updateAccount(id, patch) {
        return this.mutate((state) => {
            const account = state.accounts.find((item) => item.id === id);
            if (!account) throw new Error("Dola 账号不存在");
            Object.assign(account, patch, { updatedAt: now() });
            return account;
        });
    }

    async addTask(task) {
        return this.mutate((state) => {
            const requestKey = String(task.requestKey || "").trim();
            if (requestKey && state.tasks.some((item) => item.requestKey === requestKey)) {
                const error = new Error("同一幂等键已经提交，禁止重复创建 Dola 任务");
                error.code = "duplicate_request";
                throw error;
            }
            const timestamp = now();
            const item = {
                id: randomUUID(),
                status: "queued",
                stage: "queued",
                accountId: "",
                triedAccountIds: [],
                attempt: 0,
                conversationId: "",
                localMessageId: "",
                questionId: "",
                providerMessageId: "",
                providerVid: "",
                providerStatus: "",
                requestQuery: {},
                pollAnchor: Number.MAX_SAFE_INTEGER,
                quotaExhaustedAfterAccept: false,
                nextPollAt: timestamp,
                resultPath: "",
                resultMimeType: "",
                resultBytes: 0,
                errorCode: "",
                error: "",
                createdAt: timestamp,
                updatedAt: timestamp,
                ...task,
                requestKey,
            };
            state.tasks.push(item);
            return item;
        });
    }

    async updateTask(id, patch) {
        return this.mutate((state) => {
            const task = state.tasks.find((item) => item.id === id);
            if (!task) throw new Error("Dola 任务不存在");
            Object.assign(task, patch, { updatedAt: now() });
            return task;
        });
    }

    async recoverAfterRestart() {
        return this.mutate((state) => {
            for (const task of state.tasks) {
                if (task.status === "submitting") {
                    task.status = "failed";
                    task.stage = "uncertain";
                    task.errorCode = "provider_state_uncertain";
                    task.error = "Adapter 重启时提交状态不明确，未自动重复调用 Dola；请人工确认后再处理。";
                    task.updatedAt = now();
                }
            }
            for (const account of state.accounts) {
                account.leaseOwner = "";
                account.leaseExpiresAt = null;
                if (account.state === "busy") account.state = "healthy";
                account.updatedAt = now();
            }
            return state;
        });
    }
}

export function publicAccount(account) {
    return {
        id: account.id,
        name: account.name,
        enabled: account.enabled,
        state: account.state,
        quotaRemaining: account.quotaRemaining,
        quotaResetAt: account.quotaResetAt,
        cooldownUntil: account.cooldownUntil,
        lastErrorCode: account.lastErrorCode,
        lastErrorAt: account.lastErrorAt,
        lastUsedAt: account.lastUsedAt,
        hasPersistentProfile: Boolean(account.profileKey),
        cdpUrl: account.cdpUrl || "",
        createdAt: account.createdAt,
        updatedAt: account.updatedAt,
    };
}
