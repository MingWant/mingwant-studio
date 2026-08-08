import { publicAccount } from "./store.mjs";
import { normalizeBrowserEndpoint } from "./config.mjs";

const ELIGIBLE_STATES = new Set(["healthy", "cooldown", "quota_exhausted"]);

function timestamp() {
    return new Date().toISOString();
}

function isPast(value) {
    return !value || Date.parse(value) <= Date.now();
}

function sortableDate(value) {
    return Date.parse(value || "1970-01-01T00:00:00.000Z") || 0;
}

function localDateParts(date, timeZone) {
    const parts = new Intl.DateTimeFormat("en-CA", {
        timeZone,
        calendar: "gregory",
        numberingSystem: "latn",
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hourCycle: "h23",
    }).formatToParts(date);
    return Object.fromEntries(parts.filter((part) => part.type !== "literal").map((part) => [part.type, Number(part.value)]));
}

function nextDailyResetAt(config, from = new Date()) {
    const local = localDateParts(from, config.quotaTimeZone);
    for (let dayOffset = 0; dayOffset < 3; dayOffset += 1) {
        const guess = Date.UTC(local.year, local.month - 1, local.day + dayOffset, config.quotaResetHour, 0, 0);
        const displayed = localDateParts(new Date(guess), config.quotaTimeZone);
        const displayedAsUTC = Date.UTC(displayed.year, displayed.month - 1, displayed.day, displayed.hour, displayed.minute, displayed.second);
        const candidate = new Date(guess - (displayedAsUTC - guess));
        if (candidate.getTime() > from.getTime()) return candidate.toISOString();
    }
    return new Date(from.getTime() + 24 * 60 * 60 * 1_000).toISOString();
}

function quotaHasReset(account) {
    return account.state === "quota_exhausted" && account.quotaResetAt && isPast(account.quotaResetAt);
}

function clearExpiredQuota(account) {
    if (!quotaHasReset(account)) return;
    account.state = "healthy";
    account.quotaRemaining = null;
    account.quotaResetAt = null;
}

export class AccountPool {
    constructor(store, config) {
        this.store = store;
        this.config = config;
    }

    list() {
        return this.store.accounts().map((account) => {
            const view = { ...account };
            clearExpiredQuota(view);
            return publicAccount(view);
        });
    }

    async add(name, profileKey, cdpUrl = "") {
        const normalizedName = String(name || "").trim().slice(0, 80);
        if (!normalizedName) throw new Error("账号名称不能为空");
        const normalizedProfileKey = String(profileKey || "").trim().replace(/[\\/]/g, "");
        if (!normalizedProfileKey || normalizedProfileKey === "." || normalizedProfileKey === "..") throw new Error("账号浏览器分区标识无效");
        return this.store.addAccount({ name: normalizedName, profileKey: normalizedProfileKey, cdpUrl: normalizeBrowserEndpoint(cdpUrl) });
    }

    async update(id, patch) {
        const account = this.store.account(id);
        if (!account) throw new Error("Dola 账号不存在");
        if (account.leaseOwner && (Object.hasOwn(patch, "enabled") || Object.hasOwn(patch, "profileKey") || Object.hasOwn(patch, "cdpUrl"))) throw new Error("账号正在执行任务，请先等待任务结束");
        const next = {};
        if (Object.hasOwn(patch, "name")) next.name = String(patch.name || "").trim().slice(0, 80);
        if (Object.hasOwn(patch, "enabled")) {
            next.enabled = Boolean(patch.enabled);
            if (!next.enabled && !Object.hasOwn(patch, "state")) next.state = "disabled";
            if (next.enabled && account.state === "disabled" && !Object.hasOwn(patch, "state")) next.state = "needs_login";
        }
        if (Object.hasOwn(patch, "state")) {
            const state = String(patch.state || "").trim();
            if (!["healthy", "needs_login", "quota_exhausted", "disabled"].includes(state)) throw new Error("账号状态不允许由管理接口直接设置");
            next.state = state;
        }
        if (Object.hasOwn(patch, "profileKey")) {
            const profileKey = String(patch.profileKey || "").trim().replace(/[\\/]/g, "");
            if (!profileKey || profileKey === "." || profileKey === "..") throw new Error("账号浏览器分区标识无效");
            next.profileKey = profileKey;
            next.state = "needs_login";
            next.quotaRemaining = null;
            next.quotaResetAt = null;
        }
        if (Object.hasOwn(patch, "cdpUrl")) {
            next.cdpUrl = normalizeBrowserEndpoint(patch.cdpUrl);
            next.state = "needs_login";
        }
        if (next.state === "healthy") {
            next.quotaRemaining = null;
            next.quotaResetAt = null;
            next.cooldownUntil = null;
        } else if (next.state === "quota_exhausted") {
            next.quotaRemaining = 0;
            next.quotaResetAt = nextDailyResetAt(this.config);
        }
        if (!next.name && Object.hasOwn(patch, "name")) throw new Error("账号名称不能为空");
        return this.store.updateAccount(id, next);
    }

    async claim(taskId, excludedAccountIds = []) {
        const excluded = new Set(excludedAccountIds);
        return this.store.mutate((state) => {
            const candidates = state.accounts.filter((account) => {
                if (!account.enabled || account.leaseOwner) return false;
                const quotaReset = quotaHasReset(account);
                if (excluded.has(account.id) && !quotaReset) return false;
                if (!ELIGIBLE_STATES.has(account.state)) return false;
                if (account.state === "cooldown" && !isPast(account.cooldownUntil)) return false;
                if (account.state === "quota_exhausted" && (!account.quotaResetAt || !isPast(account.quotaResetAt))) return false;
                return true;
            }).sort((left, right) => sortableDate(left.lastUsedAt || left.createdAt) - sortableDate(right.lastUsedAt || right.createdAt));
            const account = candidates[0];
            if (!account) return null;
            clearExpiredQuota(account);
            if (account.state === "cooldown" && isPast(account.cooldownUntil)) account.cooldownUntil = null;
            const now = Date.now();
            account.state = "busy";
            account.leaseOwner = taskId;
            account.leaseExpiresAt = new Date(now + this.config.leaseMs).toISOString();
            account.lastUsedAt = new Date(now).toISOString();
            account.updatedAt = timestamp();
            return account;
        });
    }

    async claimExisting(taskId, accountId) {
        return this.store.mutate((state) => {
            const account = state.accounts.find((item) => item.id === accountId);
            if (!account || !account.enabled) return null;
            const leaseExpired = !account.leaseOwner || isPast(account.leaseExpiresAt);
            if (account.leaseOwner && account.leaseOwner !== taskId && !leaseExpired) return null;
            if (["needs_login", "disabled", "blocked"].includes(account.state)) return null;
            clearExpiredQuota(account);
            account.state = account.state === "quota_exhausted" ? "quota_exhausted" : "busy";
            account.leaseOwner = taskId;
            account.leaseExpiresAt = new Date(Date.now() + this.config.leaseMs).toISOString();
            account.updatedAt = timestamp();
            return account;
        });
    }

    async renew(taskId, accountId) {
        return this.store.mutate((state) => {
            const account = state.accounts.find((item) => item.id === accountId && item.leaseOwner === taskId);
            if (!account) return false;
            account.leaseExpiresAt = new Date(Date.now() + this.config.leaseMs).toISOString();
            account.updatedAt = timestamp();
            return true;
        });
    }

    async release(taskId, accountId, outcome = "healthy") {
        return this.store.mutate((state) => {
            const account = state.accounts.find((item) => item.id === accountId && item.leaseOwner === taskId);
            if (!account) return null;
            account.leaseOwner = "";
            account.leaseExpiresAt = null;
            if (outcome === "quota") {
                account.state = "quota_exhausted";
                account.quotaRemaining = 0;
                account.quotaResetAt = nextDailyResetAt(this.config);
            } else if (outcome === "needs_login") {
                account.state = "needs_login";
            } else if (outcome === "cooldown") {
                account.state = "cooldown";
                account.cooldownUntil = new Date(Date.now() + 60_000).toISOString();
            } else if (account.enabled) {
                account.state = "healthy";
                account.cooldownUntil = null;
            }
            account.updatedAt = timestamp();
            return account;
        });
    }

    async noteQuota(taskId, accountId, remaining) {
        return this.store.mutate((state) => {
            const account = state.accounts.find((item) => item.id === accountId && item.leaseOwner === taskId);
            if (!account) return null;
            account.quotaRemaining = Number.isInteger(remaining) ? remaining : 0;
            if (account.quotaRemaining <= 0) {
                account.state = "quota_exhausted";
                account.quotaResetAt = nextDailyResetAt(this.config);
            } else {
                account.quotaResetAt = null;
            }
            account.updatedAt = timestamp();
            return account;
        });
    }

    async markError(accountId, code, outcome = "cooldown") {
        const account = this.store.account(accountId);
        if (!account) return null;
        const patch = { lastErrorCode: String(code || "provider_error").slice(0, 80), lastErrorAt: timestamp() };
        if (!account.leaseOwner) {
            patch.state = outcome === "needs_login" ? "needs_login" : outcome === "quota" ? "quota_exhausted" : "cooldown";
            if (patch.state === "cooldown") patch.cooldownUntil = new Date(Date.now() + 60_000).toISOString();
            if (patch.state === "quota_exhausted") {
                patch.quotaRemaining = 0;
                patch.quotaResetAt = nextDailyResetAt(this.config);
            }
        }
        return this.store.updateAccount(accountId, patch);
    }
}
