import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { normalizeBrowserEndpoint } from "./config.mjs";
import { parseDolaCompletion, extractCreationFromChain, isCreationReady, maxChainIndex, sanitizeProviderError } from "./dola-protocol.mjs";

const DOLA_ASSET_HOSTS = ["dola.com", "ibyteimg.com"];
const CHAIN_QUERY_KEYS = [
    "version_code", "language", "device_platform", "doubao_device_platform", "aid", "real_aid", "pkg_type",
    "device_id", "pc_version", "doubao_pc_version", "web_id", "tea_uuid", "region", "sys_region", "samantha_web",
    "web_platform", "use-olympus-account", "web_tab_id",
];

function workerError(code, publicMessage, transient = false) {
    const error = new Error(publicMessage);
    error.code = code;
    error.publicMessage = publicMessage;
    error.transient = transient;
    return error;
}

function allowedDolaAssetURL(value) {
    try {
        const url = new URL(value);
        return ["http:", "https:"].includes(url.protocol) && DOLA_ASSET_HOSTS.some((host) => url.hostname === host || url.hostname.endsWith(`.${host}`));
    } catch {
        return false;
    }
}

function profilePath(root, profileKey) {
    const base = path.resolve(root);
    const candidate = path.resolve(base, profileKey);
    if (candidate !== base && !candidate.startsWith(`${base}${path.sep}`)) throw workerError("invalid_profile_path", "账号浏览器分区路径不在允许目录内");
    return candidate;
}

function requestQueryFromURL(value) {
    const url = new URL(value);
    const query = {};
    for (const key of CHAIN_QUERY_KEYS) {
        const item = url.searchParams.get(key);
        if (item) query[key] = item;
    }
    return query;
}

function requestMessageText(request) {
    try {
        const body = JSON.parse(request.postData() || "{}");
        const message = Array.isArray(body.messages) ? body.messages.at(-1) : null;
        return String(message?.content_block?.[0]?.content?.text_block?.text || "");
    } catch {
        return "";
    }
}

function isLoginText(value) {
    return /(?:sign\s*in|log\s*in|login|登录|重新登录)/i.test(String(value || ""));
}

async function visibleLocator(page, selectors) {
    for (const selector of selectors) {
        const locator = page.locator(selector).first();
        if (await locator.count() && await locator.isVisible().catch(() => false)) return locator;
    }
    return null;
}

export class DolaBrowserWorker {
    constructor(config) {
        this.config = config;
        this.chromium = null;
        this.runtimes = new Map();
        this.startError = null;
    }

    async start() {
        try {
            const playwright = await import("playwright");
            this.chromium = playwright.chromium;
        } catch (error) {
            this.startError = workerError("browser_dependency_missing", "Dola 浏览器 Worker 未安装 Playwright；请在 dola-adapter 目录安装依赖");
            this.startError.cause = error;
        }
    }

    isReady() {
        return Boolean(this.chromium);
    }

    runtimeIsAlive(runtime) {
        if (!runtime) return false;
        if (runtime.browser && !runtime.browser.isConnected()) return false;
        return Boolean(runtime.context && !runtime.context.isClosed() && runtime.page && !runtime.page.isClosed());
    }

    async ensureCdpRuntime(account) {
        const rawEndpoint = account.cdpUrl || this.config.browserCdpUrl;
        if (!rawEndpoint) throw workerError("browser_cdp_not_configured", "未配置普通 Chrome 的 CDP 地址");
        // CDP 会直接接管本机浏览器页面；账号和端点必须一一对应，避免串用登录态。
        let endpoint;
        try {
            endpoint = normalizeBrowserEndpoint(rawEndpoint, false);
        } catch {
            throw workerError("browser_cdp_invalid", "Dola 浏览器 CDP 地址无效，只允许连接本机 Chrome/Edge");
        }
        const conflict = [...this.runtimes.values()].find((item) => item.externalBrowser && item.cdpEndpoint === endpoint && item.accountId !== account.id && this.runtimeIsAlive(item));
        if (conflict) throw workerError("browser_cdp_in_use", "同一个普通浏览器会话不能同时绑定多个 Dola 账号");
        let browser;
        try {
            browser = await this.chromium.connectOverCDP(endpoint, { timeout: this.config.browserTimeoutMs });
        } catch {
            throw workerError("browser_cdp_unavailable", "无法连接普通 Chrome，请先启动带远程调试的 Dola 工作台", true);
        }
        try {
            const context = browser.contexts()[0];
            if (!context) throw workerError("browser_cdp_empty", "普通 Chrome 没有可接管的浏览器页面");
            const origin = new URL(this.config.homeUrl).origin;
            const page = context.pages().find((item) => {
                try { return new URL(item.url()).origin === origin; } catch { return false; }
            }) || context.pages()[0] || await context.newPage();
            const runtime = { accountId: account.id, browser, externalBrowser: true, cdpEndpoint: endpoint, context, page, requestQuery: {}, playInfoByVid: new Map() };
            this.attachPage(runtime, page);
            browser.on("disconnected", () => {
                if (this.runtimes.get(account.id) === runtime) this.runtimes.delete(account.id);
            });
            this.runtimes.set(account.id, runtime);
            if (!page.url() || !page.url().startsWith(origin)) {
                await page.goto(this.config.homeUrl, { waitUntil: "domcontentloaded", timeout: this.config.browserTimeoutMs });
            }
            return runtime;
        } catch (error) {
            await browser.close().catch(() => undefined);
            if (error?.code) throw error;
            throw workerError("browser_cdp_empty", "普通 Chrome 没有可接管的浏览器页面");
        }
    }

    async ensureRuntime(account) {
        if (!this.chromium) throw this.startError || workerError("browser_unavailable", "Dola 浏览器 Worker 当前不可用");
        const existing = this.runtimes.get(account.id);
        if (this.runtimeIsAlive(existing)) return existing;
        if (existing?.externalBrowser && existing.browser?.isConnected()) await existing.browser.close().catch(() => undefined);
        else if (existing?.context) await existing.context.close().catch(() => undefined);
        this.runtimes.delete(account.id);
        if (this.config.browserMode === "cdp") return this.ensureCdpRuntime(account);
        const profile = profilePath(this.config.profileRoot, account.profileKey);
        await mkdir(profile, { recursive: true });
        const launchOptions = {
            headless: this.config.headless,
            acceptDownloads: true,
            viewport: null,
        };
        // Google 登录会拒绝部分非品牌 Chromium；使用本机正式浏览器通道改善兼容性，
        // 不注入隐藏自动化特征，也不绕过第三方登录风控。
        if (this.config.browserChannel) launchOptions.channel = this.config.browserChannel;
        const context = await this.chromium.launchPersistentContext(profile, launchOptions);
        const page = context.pages()[0] || await context.newPage();
        const runtime = { accountId: account.id, context, page, requestQuery: {}, playInfoByVid: new Map() };
        this.attachPage(runtime, page);
        this.runtimes.set(account.id, runtime);
        if (!page.url() || !page.url().startsWith(new URL(this.config.homeUrl).origin)) {
            await page.goto(this.config.homeUrl, { waitUntil: "domcontentloaded", timeout: this.config.browserTimeoutMs });
        }
        return runtime;
    }

    attachPage(runtime, page) {
        page.on("response", (response) => {
            void this.capturePlayInfo(runtime, response);
        });
    }

    async capturePlayInfo(runtime, response) {
        try {
            const url = new URL(response.url());
            if (url.pathname !== "/samantha/video/get_play_info") return;
            const requestBody = response.request().postData() || "";
            const vid = JSON.parse(requestBody).vid;
            const payload = await response.json();
            const main = payload?.data?.play_infos?.find((item) => typeof item?.main === "string")?.main || "";
            if (vid && allowedDolaAssetURL(main)) runtime.playInfoByVid.set(String(vid), main);
        } catch (error) {
            runtime.lastPlayInfoError = sanitizeProviderError(error);
        }
    }

    async openLogin(account) {
        if (this.config.browserMode !== "cdp" && this.config.headless) throw workerError("headless_login_unavailable", "当前启用了无头浏览器，请将 DOLA_HEADLESS=false 后打开登录页");
        const runtime = await this.ensureRuntime(account);
        await runtime.page.goto(this.config.homeUrl, { waitUntil: "domcontentloaded", timeout: this.config.browserTimeoutMs });
        await runtime.page.bringToFront().catch(() => undefined);
        return { url: runtime.page.url() };
    }

    async assertComposer(runtime) {
        const page = runtime.page;
        await page.waitForLoadState("domcontentloaded", { timeout: this.config.browserTimeoutMs }).catch(() => undefined);
        const input = await visibleLocator(page, ["textarea", '[contenteditable="true"]', '[role="textbox"]']);
        if (input) return input;
        const body = await page.locator("body").innerText({ timeout: 5_000 }).catch(() => "");
        if (isLoginText(body)) throw workerError("needs_login", "Dola 账号需要重新登录");
        throw workerError("composer_unavailable", "Dola 网页工作台未找到可用的消息输入框");
    }

    async fillInput(input, text) {
        const tagName = await input.evaluate((node) => node.tagName.toLowerCase()).catch(() => "");
        if (tagName === "textarea" || tagName === "input") {
            await input.fill(text);
            return;
        }
        await input.click();
        await input.press("ControlOrMeta+A");
        await input.type(text, { delay: 1 });
    }

    async clickSend(page) {
        const buttons = page.locator("button:visible");
        const count = Math.min(await buttons.count(), 100);
        for (let index = count - 1; index >= 0; index -= 1) {
            const button = buttons.nth(index);
            const label = `${await button.innerText().catch(() => "")} ${await button.getAttribute("aria-label").catch(() => "")}`;
            if (/(?:send|发送|submit|生成)/i.test(label)) {
                await button.click();
                return;
            }
        }
        throw workerError("send_control_unavailable", "Dola 网页工作台未找到发送按钮");
    }

    buildPrompt(task) {
        // Dola 的网页入口会把这类文本和 chat_ability 一起提交；文本保留为可见的人工操作语义，真正的参数由下方路由补进同一次浏览器请求。
        return `Generated video: 生成视频，${task.resolution.toLowerCase()}，${task.prompt}, ${task.ratio}`;
    }

    async waitForCompletionResponse(page, expectedText) {
        return page.waitForResponse((response) => {
            try {
                const url = new URL(response.url());
                if (url.pathname !== "/chat/completion") return false;
                const requestText = requestMessageText(response.request());
                return !expectedText || requestText === expectedText || requestText.includes(expectedText);
            } catch {
                return false;
            }
        }, { timeout: this.config.browserTimeoutMs });
    }

    async submitTextVideo(account, task) {
        const runtime = await this.ensureRuntime(account);
        const input = await this.assertComposer(runtime);
        await this.fillInput(input, this.buildPrompt(task));
        const expectedText = this.buildPrompt(task);
        let requestPatched = false;
        let routeError = null;
        const routeHandler = async (route) => {
            const request = route.request();
            const requestText = requestMessageText(request);
            if (requestText !== expectedText && !requestText.includes(expectedText)) {
                await route.continue();
                return;
            }
            try {
                const body = JSON.parse(request.postData() || "{}");
                body.chat_ability = {
                    ability_type: 17,
                    ability_param: JSON.stringify({ ratio: task.ratio, model: task.upstreamModel, duration: task.duration }),
                };
                requestPatched = true;
                await route.continue({ postData: JSON.stringify(body) });
            } catch (error) {
                routeError = error;
                await route.abort();
            }
        };
        await runtime.page.route("**/chat/completion**", routeHandler);
        let response;
        try {
            let responseWait = this.waitForCompletionResponse(runtime.page, expectedText);
            let pressed = false;
            try {
                await input.press("Enter");
                pressed = true;
            } catch {
                // 只有 Enter 动作本身失败才改用按钮；已经发出请求后超时不能再次点击发送。
                responseWait = this.waitForCompletionResponse(runtime.page, expectedText);
                await this.clickSend(runtime.page);
            }
            try {
                response = await responseWait;
            } catch (error) {
                if (routeError) throw workerError("request_not_sent", "Dola 视频请求未能安全发送，未自动换号重试");
                throw workerError("provider_state_uncertain", pressed ? "Dola 视频提交状态不明确，未自动重试" : "Dola 视频发送控件未返回提交结果");
            }
            if (!requestPatched) throw workerError("provider_state_uncertain", "Dola 视频参数未能附加到网页请求，未自动重试");
        } finally {
            await runtime.page.unroute("**/chat/completion**", routeHandler).catch(() => undefined);
        }
        if (!response) {
            throw workerError("provider_state_uncertain", "Dola 视频提交状态不明确，未自动重试");
        }
        if (!response.ok()) {
            if ([401, 403].includes(response.status())) throw workerError("needs_login", "Dola 账号会话已失效，请重新登录");
            if (response.status() === 429) throw workerError("rate_limited", "Dola 账号暂时被限流", true);
            throw workerError("provider_rejected", `Dola 提交请求被拒绝（HTTP ${response.status()}）`);
        }
        const responseText = await response.text();
        const parsed = parseDolaCompletion(responseText);
        runtime.requestQuery = requestQueryFromURL(response.request().url());
        if (parsed.quotaBeforeAccept) throw workerError("quota_exhausted", "Dola 账号额度不足，未接受本次生成");
        if (!parsed.accepted) {
            const code = parsed.errorCode || "provider_state_uncertain";
            throw workerError(code, code === "needs_login" ? "Dola 账号需要重新登录" : "Dola 提交结果不明确，未自动重试");
        }
        return {
            conversationId: parsed.conversationId,
            localMessageId: parsed.localMessageId,
            questionId: parsed.questionId,
            providerMessageId: parsed.providerMessageId,
            requestQuery: runtime.requestQuery,
            quotaRemaining: parsed.quotaRemaining,
            quotaExhaustedAfterAccept: parsed.quotaExhaustedAfterAccept,
        };
    }

    async queryChain(account, task) {
        const runtime = await this.ensureRuntime(account);
        const url = new URL("/im/chain/single", this.config.homeUrl);
        for (const [key, value] of Object.entries(task.requestQuery || runtime.requestQuery || {})) url.searchParams.set(key, value);
        const body = {
            cmd: 3100,
            uplink_body: {
                pull_singe_chain_uplink_body: {
                    conversation_id: task.conversationId,
                    anchor_index: Number.isSafeInteger(task.pollAnchor) ? task.pollAnchor : Number.MAX_SAFE_INTEGER,
                    conversation_type: 3,
                    direction: 1,
                    limit: 20,
                    ext: { sync_scenario: "SendBot|flow.agent.creation", pull_single_chain_scene: "multi_device_red_dot_sync" },
                    filter: { index_list: [] },
                    evaluate_ab_params: "",
                    evaluate_common_params: "",
                },
            },
            sequence_id: randomUUID(),
            channel: 2,
            version: "1",
        };
        let result;
        try {
            result = await runtime.page.evaluate(async ({ requestURL, requestBody }) => {
                const response = await fetch(requestURL, {
                    method: "POST",
                    credentials: "include",
                    headers: { accept: "application/json, text/plain, */*", "content-type": "application/json" },
                    body: JSON.stringify(requestBody),
                });
                return { status: response.status, text: await response.text() };
            }, { requestURL: url.toString(), requestBody: body });
        } catch {
            throw workerError("poll_transient", "Dola 会话查询暂时失败", true);
        }
        if (result.status === 401 || result.status === 403) throw workerError("needs_login", "Dola 账号会话已失效，请重新登录");
        if (result.status === 408 || result.status === 429 || result.status >= 500) throw workerError("poll_transient", "Dola 会话查询暂时失败", true);
        let payload;
        try {
            payload = JSON.parse(result.text);
        } catch {
            throw workerError("poll_protocol_error", "Dola 会话查询返回格式无效");
        }
        const creation = extractCreationFromChain(payload);
        return { creation, maxIndex: maxChainIndex(payload) };
    }

    async waitCapturedPlayInfo(runtime, vid) {
        const deadline = Date.now() + 3_000;
        while (Date.now() < deadline) {
            const url = runtime.playInfoByVid.get(vid);
            if (url) return url;
            await new Promise((resolve) => setTimeout(resolve, 100));
        }
        return "";
    }

    async refreshPlayInfo(runtime, task, vid) {
        const existing = await this.waitCapturedPlayInfo(runtime, vid);
        if (existing) return existing;
        const responsePromise = runtime.page.waitForResponse((response) => {
            try {
                const url = new URL(response.url());
                if (url.pathname !== "/samantha/video/get_play_info") return false;
                const body = JSON.parse(response.request().postData() || "{}");
                return String(body.vid || "") === String(vid);
            } catch {
                return false;
            }
        }, { timeout: this.config.browserTimeoutMs }).catch(() => null);
        const conversationURL = new URL(`/chat/${encodeURIComponent(task.conversationId)}`, this.config.homeUrl).toString();
        try {
            await runtime.page.goto(conversationURL, { waitUntil: "domcontentloaded", timeout: this.config.browserTimeoutMs });
        } catch {
            // 页面导航失败时仍等待已经发出的 get_play_info；若没有响应，调用方按可重试的下载故障处理。
        }
        const response = await responsePromise;
        if (response) {
            if ([401, 403].includes(response.status())) throw workerError("needs_login", "Dola 账号会话已失效，请重新登录");
            if (response.status() === 408 || response.status() === 429 || response.status() >= 500) throw workerError("play_info_transient", "Dola 视频地址查询暂时失败", true);
            try {
                const payload = await response.json();
                const main = payload?.data?.play_infos?.find((item) => typeof item?.main === "string")?.main || "";
                if (allowedDolaAssetURL(main)) {
                    runtime.playInfoByVid.set(String(vid), main);
                    return main;
                }
            } catch {
                throw workerError("play_info_protocol_error", "Dola 视频地址返回格式无效");
            }
        }
        return this.waitCapturedPlayInfo(runtime, vid);
    }

    async downloadVideo(task, creation) {
        const runtime = this.runtimes.get(task.accountId);
        const captured = runtime ? await this.refreshPlayInfo(runtime, task, creation.vid) : "";
        const url = captured || creation.downloadUrl;
        if (!allowedDolaAssetURL(url)) throw workerError("result_url_missing", "Dola 视频已就绪，但没有可下载的签名地址");
        let response;
        try {
            response = await fetch(url, { headers: { accept: "video/mp4", referer: this.config.homeUrl } });
        } catch {
            throw workerError("download_transient", "Dola 视频下载暂时失败", true);
        }
        if (!response.ok) {
            if ([408, 429, 502, 503, 504].includes(response.status)) throw workerError("download_transient", "Dola 视频下载暂时失败", true);
            throw workerError("download_failed", `Dola 视频下载失败（HTTP ${response.status}）`);
        }
        const contentLength = Number(response.headers.get("content-length"));
        if (Number.isFinite(contentLength) && contentLength > this.config.maxDownloadBytes) throw workerError("download_too_large", "Dola 视频超过本地结果大小限制");
        let data;
        try {
            data = Buffer.from(await response.arrayBuffer());
        } catch {
            throw workerError("download_transient", "Dola 视频下载暂时失败", true);
        }
        if (!data.length) throw workerError("download_empty", "Dola 视频下载结果为空");
        if (data.length > this.config.maxDownloadBytes) throw workerError("download_too_large", "Dola 视频超过本地结果大小限制");
        const filePath = path.join(this.config.resultRoot, `${task.id}.mp4`);
        await mkdir(this.config.resultRoot, { recursive: true });
        await writeFile(filePath, data);
        return { filePath, mimeType: "video/mp4", bytes: data.length };
    }

    async close() {
        for (const runtime of this.runtimes.values()) {
            if (runtime.externalBrowser) await runtime.browser.close().catch(() => undefined);
            else await runtime.context.close().catch(() => undefined);
        }
        this.runtimes.clear();
    }
}
