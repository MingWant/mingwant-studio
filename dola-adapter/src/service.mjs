import path from "node:path";
import { AccountPool } from "./account-pool.mjs";
import { DolaBrowserWorker } from "./browser-worker.mjs";
import { isTransientBrowserError, sanitizeProviderError } from "./dola-protocol.mjs";

function timestamp() {
    return new Date().toISOString();
}

function errorWithCode(code, message) {
    const error = new Error(message);
    error.code = code;
    error.publicMessage = message;
    return error;
}

function numberValue(value, fallback) {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : fallback;
}

function normalizeRatio(value) {
    const ratio = String(value ?? "16:9").trim();
    if (!["1:1", "16:9", "9:16", "4:3", "3:4"].includes(ratio)) throw errorWithCode("invalid_request", "ratio 只支持 1:1、16:9、9:16、4:3 或 3:4");
    return ratio;
}

function normalizeResolution(value) {
    const resolution = String(value || "720").trim().toLowerCase().replace(/p$/, "");
    if (!["480", "720", "1080"].includes(resolution)) throw errorWithCode("invalid_request", "resolution 只支持 480P、720P 或 1080P");
    return `${resolution}P`;
}

function normalizeDuration(value) {
    const duration = numberValue(value, 10);
    if (!Number.isInteger(duration) || duration < 1 || duration > 15) throw errorWithCode("invalid_request", "duration 必须是 1 到 15 秒的整数");
    return duration;
}

function upstreamModel(value) {
    const model = String(value || "dola-seedance-2.5").trim();
    if (!model || model.length > 120) throw errorWithCode("invalid_request", "model 无效");
    if (/seedance[_-]?2\.5/i.test(model)) return "seedance_v2.5";
    if (/dola/i.test(model)) return "seedance_v2.5";
    throw errorWithCode("unsupported_model", "当前 Dola Adapter 只支持 Seedance 2.5 文本生视频");
}

export function normalizeVideoRequest(body) {
    if (!body || typeof body !== "object" || Array.isArray(body)) throw errorWithCode("invalid_request", "请求体必须是 JSON 对象");
    const input = body.input && typeof body.input === "object" ? body.input : body;
    const prompt = String(input.prompt || body.prompt || "").trim();
    if (!prompt) throw errorWithCode("invalid_request", "prompt 不能为空");
    if (prompt.length > 8_000) throw errorWithCode("invalid_request", "prompt 不能超过 8000 个字符");
    const media = Array.isArray(input.media) ? input.media.filter(Boolean) : [];
    if (media.length > 0) throw errorWithCode("unsupported_input", "当前 MVP 只支持文本生视频，图生视频上传链路尚未启用");
    const parameters = body.parameters && typeof body.parameters === "object" ? body.parameters : {};
    const model = String(body.model || "dola-seedance-2.5").trim();
    return {
        model: model || "dola-seedance-2.5",
        upstreamModel: upstreamModel(model),
        prompt,
        ratio: normalizeRatio(parameters.ratio || parameters.aspect_ratio),
        resolution: normalizeResolution(parameters.resolution || parameters.resolution_name),
        duration: normalizeDuration(parameters.duration ?? body.seconds),
    };
}

function publicTask(task, config) {
    const response = {
        id: task.id,
        object: "video",
        model: task.model,
        status: task.status === "succeeded" ? "SUCCEEDED" : task.status === "failed" ? "FAILED" : "PROCESSING",
        created_at: Math.floor(Date.parse(task.createdAt) / 1_000),
    };
    if (task.status === "succeeded") response.object = `${config.publicBaseUrl.replace(/\/$/, "")}/v1/files/${encodeURIComponent(task.id)}`;
    if (task.status === "failed") response.error = { code: task.errorCode || "provider_error", message: task.error || "视频生成失败" };
    return response;
}

function retryAt(milliseconds) {
    return new Date(Date.now() + milliseconds).toISOString();
}

function requireSafeResultPath(filePath, resultRoot) {
    const root = path.resolve(resultRoot);
    const candidate = path.resolve(filePath);
    if (candidate !== root && !candidate.startsWith(`${root}${path.sep}`)) throw errorWithCode("not_found", "视频结果不存在");
    return candidate;
}

export class DolaAdapterService {
    constructor({ store, config }) {
        this.store = store;
        this.config = config;
        this.accounts = new AccountPool(store, config);
        this.browser = new DolaBrowserWorker(config);
        this.timer = null;
        this.pumping = false;
        this.activeTasks = new Set();
    }

    async start() {
        await this.store.recoverAfterRestart();
        await this.browser.start();
        this.timer = setInterval(() => void this.pump(), 1_000);
        void this.pump();
    }

    async stop() {
        if (this.timer) clearInterval(this.timer);
        this.timer = null;
        await this.browser.close();
    }

    health() {
        const accounts = this.accounts.list();
        const tasks = this.store.tasks();
        return {
            browserReady: this.browser.isReady(),
            browserError: this.browser.startError?.code || "",
            browserMode: this.config.browserMode,
            cdpConfigured: Boolean(this.config.browserCdpUrl),
            accounts: {
                total: accounts.length,
                healthy: accounts.filter((item) => item.state === "healthy").length,
                busy: accounts.filter((item) => item.state === "busy").length,
                needsLogin: accounts.filter((item) => item.state === "needs_login").length,
                quotaExhausted: accounts.filter((item) => item.state === "quota_exhausted").length,
            },
            tasks: {
                queued: tasks.filter((item) => item.status === "queued").length,
                processing: tasks.filter((item) => item.status === "processing" || item.status === "submitting").length,
            },
        };
    }

    async createVideo(body, requestKey = "") {
        const normalized = normalizeVideoRequest(body);
        const key = String(requestKey || "").trim();
        if (key.length > 120) throw errorWithCode("invalid_request", "Idempotency-Key 不能超过 120 个字符");
        const task = await this.store.addTask({
            requestKey: key,
            model: normalized.model,
            upstreamModel: normalized.upstreamModel,
            prompt: normalized.prompt,
            ratio: normalized.ratio,
            resolution: normalized.resolution,
            duration: normalized.duration,
        });
        void this.pump();
        return publicTask(task, this.config);
    }

    getVideo(id) {
        const task = this.store.task(id);
        if (!task) throw errorWithCode("not_found", "视频任务不存在");
        return publicTask(task, this.config);
    }

    resultFile(id) {
        const task = this.store.task(id);
        if (!task || task.status !== "succeeded" || !task.resultPath) throw errorWithCode("not_found", "视频结果不存在");
        const resultPath = requireSafeResultPath(task.resultPath, this.config.resultRoot);
        return { path: resultPath, mimeType: task.resultMimeType || "video/mp4", bytes: task.resultBytes || 0 };
    }

    async processQueued(task) {
        const account = await this.accounts.claim(task.id, task.triedAccountIds);
        if (!account) {
            await this.store.updateTask(task.id, { stage: "waiting_account", nextPollAt: retryAt(5_000), errorCode: "", error: "等待可用 Dola 账号" });
            return;
        }
        const tried = [...new Set([...(task.triedAccountIds || []), account.id])];
        await this.store.updateTask(task.id, {
            status: "submitting",
            stage: "submitting",
            accountId: account.id,
            triedAccountIds: tried,
            attempt: Number(task.attempt || 0) + 1,
            nextPollAt: null,
            errorCode: "",
            error: "",
        });
        try {
            const accepted = await this.browser.submitTextVideo(account, task);
            await this.store.updateTask(task.id, {
                status: "processing",
                stage: "polling_conversation",
                conversationId: accepted.conversationId,
                localMessageId: accepted.localMessageId,
                questionId: accepted.questionId,
                providerMessageId: accepted.providerMessageId,
                requestQuery: accepted.requestQuery,
                quotaExhaustedAfterAccept: accepted.quotaExhaustedAfterAccept,
                providerStatus: "accepted",
                nextPollAt: timestamp(),
            });
            if (accepted.quotaExhaustedAfterAccept) await this.accounts.noteQuota(task.id, account.id, accepted.quotaRemaining);
        } catch (error) {
            const normalized = sanitizeProviderError(error);
            if (normalized.code === "quota_exhausted") {
                await this.accounts.release(task.id, account.id, "quota");
                if (tried.length < this.config.maxAccountAttempts) {
                    await this.store.updateTask(task.id, { status: "queued", stage: "rotate_account", accountId: "", nextPollAt: timestamp(), errorCode: normalized.code, error: "当前 Dola 账号额度不足，准备切换下一个账号" });
                    return;
                }
            } else if (normalized.code === "needs_login" || normalized.code === "verification_required") {
                await this.accounts.release(task.id, account.id, "needs_login");
            } else {
                // 提交响应丢失时无法证明上游未收到请求；不自动换号，避免重复消耗额度。
                await this.accounts.release(task.id, account.id, "cooldown");
                normalized.code = normalized.code === "provider_error" ? "provider_state_uncertain" : normalized.code;
                normalized.message = normalized.code === "provider_state_uncertain" ? "Dola 提交状态不明确，未自动换号重试，请人工确认上游任务" : normalized.message;
            }
            await this.store.updateTask(task.id, { status: "failed", stage: "failed", accountId: "", errorCode: normalized.code, error: normalized.message, nextPollAt: null });
        }
    }

    async processProcessing(task) {
        if (!task.accountId || !task.conversationId) {
            await this.store.updateTask(task.id, { status: "failed", stage: "uncertain", errorCode: "provider_state_uncertain", error: "Dola 任务缺少会话恢复信息，未自动重建请求" });
            return;
        }
        if (Date.now() - Date.parse(task.createdAt) > this.config.taskTimeoutMs) {
            await this.accounts.release(task.id, task.accountId, task.quotaExhaustedAfterAccept ? "quota" : "cooldown");
            await this.store.updateTask(task.id, { status: "failed", stage: "uncertain", errorCode: "provider_timeout_uncertain", error: "Dola 任务超过本地等待时限，供应商可能仍在执行；未自动换号重试" });
            return;
        }
        const account = await this.accounts.claimExisting(task.id, task.accountId);
        if (!account) return;
        await this.accounts.renew(task.id, task.accountId);
        try {
            const result = await this.browser.queryChain(account, task);
            const patch = { nextPollAt: retryAt(this.config.pollIntervalMs), stage: "polling_conversation", providerStatus: "processing" };
            const currentAnchor = Number(task.pollAnchor);
            if (Number.isSafeInteger(result.maxIndex) && (!Number.isSafeInteger(currentAnchor) || currentAnchor === Number.MAX_SAFE_INTEGER || result.maxIndex > currentAnchor)) patch.pollAnchor = result.maxIndex;
            if (!result.creation || !isCreationReady(result.creation)) {
                await this.store.updateTask(task.id, patch);
                return;
            }
            await this.store.updateTask(task.id, { stage: "downloading_result", providerVid: result.creation.vid, providerStatus: String(result.creation.status), nextPollAt: null });
            const file = await this.browser.downloadVideo({ ...task, accountId: account.id }, result.creation);
            await this.store.updateTask(task.id, { status: "succeeded", stage: "completed", resultPath: file.filePath, resultMimeType: file.mimeType, resultBytes: file.bytes, providerVid: result.creation.vid, providerStatus: "succeeded", nextPollAt: null, errorCode: "", error: "" });
            try {
                await this.accounts.release(task.id, account.id, task.quotaExhaustedAfterAccept ? "quota" : "healthy");
            } catch (error) {
                // 结果已经持久化成功，不能因账号状态收尾失败把可交付视频改写成失败；保留服务端可检索的账号收尾错误。
                console.error("Dola Adapter 账号状态收尾失败", { taskId: task.id, accountId: account.id, code: sanitizeProviderError(error).code });
            }
        } catch (error) {
            const normalized = sanitizeProviderError(error);
            if (isTransientBrowserError(error)) {
                await this.accounts.renew(task.id, task.accountId);
                await this.store.updateTask(task.id, { nextPollAt: retryAt(this.config.pollIntervalMs), stage: "poll_retry", providerStatus: "polling", errorCode: normalized.code, error: "Dola 任务查询暂时失败，等待下一次查询" });
                return;
            }
            const releaseOutcome = normalized.code === "needs_login" ? "needs_login" : task.quotaExhaustedAfterAccept ? "quota" : "cooldown";
            await this.accounts.release(task.id, task.accountId, releaseOutcome);
            await this.store.updateTask(task.id, { status: "failed", stage: normalized.code === "needs_login" ? "needs_login" : "uncertain", accountId: "", nextPollAt: null, errorCode: normalized.code, error: normalized.message });
        }
    }

    async processTask(id) {
        const task = this.store.task(id);
        if (!task || !["queued", "processing"].includes(task.status)) return;
        if (task.status === "queued") await this.processQueued(task);
        else await this.processProcessing(task);
    }

    async pump() {
        if (this.pumping) return;
        this.pumping = true;
        try {
            const now = Date.now();
            const due = this.store.tasks().filter((task) => ["queued", "processing"].includes(task.status) && (!task.nextPollAt || Date.parse(task.nextPollAt) <= now));
            for (const task of due) {
                if (this.activeTasks.has(task.id)) continue;
                this.activeTasks.add(task.id);
                void this.processTask(task.id).catch((error) => {
                    const normalized = sanitizeProviderError(error);
                    void this.store.updateTask(task.id, { status: "failed", stage: "failed", errorCode: normalized.code, error: normalized.message, nextPollAt: null });
                }).finally(() => this.activeTasks.delete(task.id));
            }
        } finally {
            this.pumping = false;
        }
    }
}
