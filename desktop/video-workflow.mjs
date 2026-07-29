import path from "node:path";
import { randomUUID } from "node:crypto";
import { pathToFileURL } from "node:url";
import { app, clipboard, ipcMain, session, shell, WebContentsView, BrowserWindow } from "electron";

import { IPC_CHANNELS } from "./ipc-channels.mjs";
import { proxyMatchesAuthenticationChallenge } from "./proxy-config.mjs";
import { inspectCompletedVideo, isLikelyVideoDownload, nextDownloadPath, readCompletedVideo, stageVideoTask } from "./video-task-files.mjs";
import { videoProvider } from "./video-providers.mjs";
import { buildWorkflowSummary, presentWorkflowAccounts, presentWorkflowTasks } from "./video-workflow-state.mjs";
import { isSafePopupUrl, isSafeWebUrl, normalizeWorkflowId, safeDisplayUrl, safeOrigin, videoMimeType } from "./video-workflow-utils.mjs";

const ACCOUNT_RAIL_WIDTH = 264;
const TASK_CONSOLE_HEIGHT = 218;

export class DesktopVideoWorkflow {
    constructor({ mainWindow, allowedWebOrigin, accountStore, userDataDirectory, downloadsDirectory, workbenchPreloadPath, workbenchHtmlPath }) {
        this.mainWindow = mainWindow;
        this.allowedWebOrigin = allowedWebOrigin;
        this.accountStore = accountStore;
        this.userDataDirectory = userDataDirectory;
        this.downloadsDirectory = downloadsDirectory;
        this.workbenchPreloadPath = workbenchPreloadPath;
        this.workbenchHtmlPath = workbenchHtmlPath;
        this.workbenchDocumentUrl = pathToFileURL(workbenchHtmlPath).toString();
        this.workbenchWindow = null;
        this.workbenchLoadPromise = null;
        this.accountViews = new Map();
        this.attachedAccountId = null;
        this.attachedViewVisible = false;
        this.browserViewSuppressed = false;
        this.selectedAccountId = this.accountStore.list()[0]?.id || null;
        this.tasks = new Map();
        this.results = new Map();
        this.proxyLoginHandler = (event, webContents, _details, authInfo, callback) => this.handleProxyLogin(event, webContents, authInfo, callback);
        this.quitting = false;
    }

    registerIpc() {
        app.on("login", this.proxyLoginHandler);
        ipcMain.handle(IPC_CHANNELS.openWorkbench, async (event) => {
            this.assertMainSender(event);
            await this.openWorkbench();
        });
        ipcMain.handle(IPC_CHANNELS.workflowState, (event) => {
            this.assertMainSender(event);
            return this.workflowSummary();
        });
        ipcMain.handle(IPC_CHANNELS.startTask, (event, request) => {
            this.assertMainSender(event);
            return this.startTask(event.sender.id, request);
        });
        ipcMain.handle(IPC_CHANNELS.cancelTask, (event, taskId) => {
            this.assertMainSender(event);
            const task = this.requireTask(taskId);
            if (task.senderId !== event.sender.id) throw new Error("不能取消其他窗口创建的任务");
            this.rejectTask(task, new Error("桌面视频任务已取消"));
        });
        ipcMain.handle(IPC_CHANNELS.readResult, async (event, resultId) => {
            this.assertMainSender(event);
            const result = this.results.get(normalizeWorkflowId(resultId, "结果 ID"));
            if (!result || result.senderId !== event.sender.id) throw new Error("视频回填结果不存在或已过期");
            return (await readCompletedVideo(result.filePath)).bytes;
        });
        ipcMain.handle(IPC_CHANNELS.releaseResult, (event, resultId) => {
            this.assertMainSender(event);
            const id = normalizeWorkflowId(resultId, "结果 ID");
            const result = this.results.get(id);
            if (result?.senderId === event.sender.id) this.results.delete(id);
        });
        ipcMain.handle(IPC_CHANNELS.workbenchState, (event) => {
            this.assertWorkbenchSender(event);
            return this.workbenchState();
        });
        ipcMain.handle(IPC_CHANNELS.workbenchCommand, async (event, command) => {
            this.assertWorkbenchSender(event);
            await this.handleWorkbenchCommand(command);
            return this.workbenchState();
        });
    }

    async startTask(senderId, request) {
        const staged = await stageVideoTask(request, this.userDataDirectory, this.downloadsDirectory);
        request = null;
        if (this.tasks.has(staged.taskId)) throw new Error("桌面视频任务已经存在");
        const completion = new Promise((resolve, reject) => {
            this.tasks.set(staged.taskId, { ...staged, senderId, resolve, reject });
        });
        try {
            await this.openWorkbench();
            await this.scheduleTasks();
            this.sendWorkbenchState();
        } catch (error) {
            this.rejectTask(this.tasks.get(staged.taskId), error);
        }
        return completion;
    }

    async openWorkbench() {
        if (!this.workbenchWindow || this.workbenchWindow.isDestroyed()) this.createWorkbenchWindow();
        await this.workbenchLoadPromise;
        this.workbenchWindow.show();
        this.workbenchWindow.focus();
        if (this.selectedAccountId) await this.selectAccount(this.selectedAccountId, false);
        this.sendWorkbenchState();
    }

    createWorkbenchWindow() {
        this.workbenchWindow = new BrowserWindow({
            width: 1440,
            height: 940,
            minWidth: 980,
            minHeight: 700,
            show: false,
            backgroundColor: "#0b0d12",
            title: "明想网页视频工作台",
            autoHideMenuBar: true,
            webPreferences: {
                preload: this.workbenchPreloadPath,
                nodeIntegration: false,
                contextIsolation: true,
                sandbox: true,
                webSecurity: true,
            },
        });
        this.workbenchLoadPromise = this.workbenchWindow.loadFile(this.workbenchHtmlPath);
        this.workbenchWindow.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
        this.workbenchWindow.webContents.on("will-navigate", (event, url) => {
            if (url !== this.workbenchDocumentUrl) event.preventDefault();
        });
        this.workbenchWindow.once("ready-to-show", () => this.workbenchWindow?.show());
        this.workbenchWindow.on("resize", () => this.layoutAttachedView());
        this.workbenchWindow.on("close", (event) => {
            if (this.quitting) return;
            event.preventDefault();
            this.workbenchWindow?.hide();
        });
        this.workbenchWindow.on("closed", () => {
            this.workbenchWindow = null;
            this.workbenchLoadPromise = null;
            this.attachedAccountId = null;
            this.attachedViewVisible = false;
            this.browserViewSuppressed = false;
        });
    }

    async handleWorkbenchCommand(command) {
        if (!command || typeof command !== "object" || typeof command.type !== "string") throw new Error("工作台命令无效");
        switch (command.type) {
            case "account:add": {
                const account = await this.accountStore.add(command.name, command.provider, command.proxy);
                await this.selectAccount(account.id);
                await this.scheduleTasks();
                break;
            }
            case "account:select":
                await this.selectAccount(command.accountId);
                break;
            case "account:rename":
                await this.accountStore.update(this.requireIdleAccount(command.accountId).id, { name: command.name });
                break;
            case "account:set-enabled":
                await this.accountStore.update(this.requireIdleAccount(command.accountId).id, { enabled: command.enabled });
                await this.scheduleTasks();
                break;
            case "account:set-needs-login":
                await this.accountStore.update(this.requireIdleAccount(command.accountId).id, { needsLogin: command.needsLogin });
                await this.scheduleTasks();
                break;
            case "account:set-proxy": {
                const accountId = this.requireIdleAccount(command.accountId).id;
                const result = await this.accountStore.setProxy(accountId, command.proxy);
                if (result.changed) await this.reloadAccountWithProxy(accountId);
                await this.scheduleTasks();
                break;
            }
            case "account:clear-session":
                await this.clearAccountSession(this.requireIdleAccount(command.accountId).id);
                break;
            case "account:remove":
                await this.removeAccount(this.requireIdleAccount(command.accountId).id);
                await this.scheduleTasks();
                break;
            case "browser:back":
            case "browser:forward":
            case "browser:reload":
            case "browser:home":
                await this.handleBrowserCommand(command.type);
                break;
            case "browser:set-visible":
                this.setBrowserViewVisible(command.visible !== false);
                break;
            case "task:copy-prompt": {
                const task = this.requireTask(command.taskId);
                clipboard.writeText(task.prompt);
                break;
            }
            case "task:open-inputs": {
                const task = this.requireTask(command.taskId);
                const error = await shell.openPath(task.inputDirectory);
                if (error) throw new Error(error);
                break;
            }
            case "task:accept-download":
                await this.acceptTaskDownload(this.requireTask(command.taskId));
                break;
            case "task:ignore-download": {
                const task = this.requireTask(command.taskId);
                task.download = null;
                task.downloadError = "";
                task.status = "active";
                break;
            }
            case "task:cancel":
                this.rejectTask(this.requireTask(command.taskId), new Error("用户在网页视频工作台取消了任务"));
                break;
            default:
                throw new Error("不支持的工作台命令");
        }
        this.sendWorkbenchState();
    }

    async selectAccount(accountId, copyPrompt = true) {
        const account = this.requireAccount(accountId);
        const view = await this.ensureAccountView(account.id);
        if (!this.workbenchWindow || this.workbenchWindow.isDestroyed()) return;
        if (this.attachedAccountId && this.attachedAccountId !== account.id && this.attachedViewVisible) {
            const attached = this.accountViews.get(this.attachedAccountId)?.view;
            if (attached) this.workbenchWindow.contentView.removeChildView(attached);
            this.attachedViewVisible = false;
        }
        if (this.attachedAccountId !== account.id) {
            this.attachedAccountId = account.id;
            this.attachedViewVisible = false;
        }
        if (!this.browserViewSuppressed && !this.attachedViewVisible) {
            this.workbenchWindow.contentView.addChildView(view);
            this.attachedViewVisible = true;
        }
        this.selectedAccountId = account.id;
        this.layoutAttachedView();
        const task = this.activeTaskForAccount(account.id);
        if (copyPrompt && task) clipboard.writeText(task.prompt);
        this.sendWorkbenchState();
    }

    async ensureAccountView(accountId) {
        const existing = this.accountViews.get(accountId);
        if (existing && !existing.view.webContents.isDestroyed()) return existing.view;
        if (existing) {
            existing.session.removeListener("will-download", existing.downloadHandler);
            for (const childWindow of existing.childWindows) childWindow.close();
            this.accountViews.delete(accountId);
            if (this.attachedAccountId === accountId) {
                this.attachedAccountId = null;
                this.attachedViewVisible = false;
            }
        }
        const account = this.requireAccount(accountId);
        const provider = videoProvider(account.provider);
        const accountSession = session.fromPartition(`persist:mingwant-video-${account.id}`, { cache: true });
        await this.applyAccountProxy(accountSession, account.proxy);
        accountSession.setPermissionRequestHandler((_webContents, _permission, callback) => callback(false));
        accountSession.setPermissionCheckHandler(() => false);
        const view = new WebContentsView({
            webPreferences: {
                session: accountSession,
                nodeIntegration: false,
                contextIsolation: true,
                sandbox: true,
                webSecurity: true,
                safeDialogs: true,
                navigateOnDragDrop: false,
            },
        });
        const downloadHandler = (_event, item) => this.captureDownload(accountId, item);
        const runtime = { view, session: accountSession, url: provider.homeUrl, loadError: "", childWindows: new Set(), downloadHandler };
        this.accountViews.set(accountId, runtime);
        view.webContents.setWindowOpenHandler(({ url }) => {
            if (!isSafePopupUrl(url)) return { action: "deny" };
            return {
                action: "allow",
                overrideBrowserWindowOptions: {
                    parent: this.workbenchWindow || undefined,
                    autoHideMenuBar: true,
                    webPreferences: { session: accountSession, nodeIntegration: false, contextIsolation: true, sandbox: true, webSecurity: true },
                },
            };
        });
        view.webContents.on("will-navigate", (event, url) => {
            if (!isSafeWebUrl(url)) event.preventDefault();
        });
        view.webContents.on("did-create-window", (childWindow) => {
            runtime.childWindows.add(childWindow);
            childWindow.webContents.on("will-navigate", (event, url) => {
                if (!isSafeWebUrl(url)) event.preventDefault();
            });
            childWindow.once("closed", () => runtime.childWindows.delete(childWindow));
        });
        const updateUrl = (_event, url) => {
            runtime.url = url;
            runtime.loadError = "";
            this.sendWorkbenchState();
        };
        view.webContents.on("did-navigate", updateUrl);
        view.webContents.on("did-navigate-in-page", updateUrl);
        view.webContents.on("did-fail-load", (_event, code, description, url, isMainFrame) => {
            if (!isMainFrame || code === -3) return;
            runtime.url = url;
            runtime.loadError = description || "网页加载失败";
            this.sendWorkbenchState();
        });
        accountSession.on("will-download", downloadHandler);
        void view.webContents.loadURL(provider.homeUrl).catch((error) => {
            runtime.loadError = error instanceof Error ? error.message : "网页加载失败";
            this.sendWorkbenchState();
        });
        return view;
    }

    async reloadAccountWithProxy(accountId) {
        const runtime = this.accountViews.get(accountId);
        if (!runtime) return;
        runtime.view.webContents.stop();
        for (const childWindow of runtime.childWindows) childWindow.close();
        runtime.childWindows.clear();
        await this.applyAccountProxy(runtime.session, this.requireAccount(accountId).proxy);
        // Chromium 可能继续复用旧 IP 的连接，切换代理后必须主动关闭连接池再重新加载。
        await runtime.session.closeAllConnections();
        const homeUrl = videoProvider(this.requireAccount(accountId).provider).homeUrl;
        runtime.url = homeUrl;
        runtime.loadError = "";
        void runtime.view.webContents.loadURL(homeUrl).catch((error) => {
            runtime.loadError = error instanceof Error ? error.message : "代理网络下网页加载失败";
            this.sendWorkbenchState();
        });
    }

    async applyAccountProxy(accountSession, proxy) {
        if (!proxy) {
            await accountSession.setProxy({ mode: "direct" });
            return;
        }
        await accountSession.setProxy({
            mode: "fixed_servers",
            proxyRules: proxy.server,
            proxyBypassRules: "<local>",
        });
    }

    handleProxyLogin(event, webContents, authInfo, callback) {
        if (!authInfo?.isProxy) return;
        const entry = [...this.accountViews.entries()].find(([_accountId, runtime]) => {
            if (runtime.view.webContents.id === webContents.id) return true;
            return [...runtime.childWindows].some((childWindow) => childWindow.webContents.id === webContents.id);
        });
        if (!entry) return;
        const [accountId, runtime] = entry;
        const account = this.accountStore.get(accountId);
        if (!account?.proxy || !proxyMatchesAuthenticationChallenge(account.proxy.server, authInfo)) return;
        try {
            const credentials = this.accountStore.proxyCredentials(accountId);
            if (!credentials) return;
            event.preventDefault();
            callback(credentials.username, credentials.password);
        } catch (error) {
            event.preventDefault();
            callback();
            runtime.loadError = error instanceof Error ? error.message : "代理认证失败";
            this.sendWorkbenchState();
        }
    }

    captureDownload(accountId, item) {
        const task = this.activeTaskForAccount(accountId);
        if (!task || !isLikelyVideoDownload(item)) return;
        const savePath = nextDownloadPath(task.outputDirectory, item.getFilename(), item.getMimeType());
        item.setSavePath(savePath);
        item.once("done", async (_event, state) => {
            const current = this.tasks.get(task.taskId);
            if (!current || current.accountId !== accountId) return;
            if (state !== "completed") {
                current.downloadError = `视频下载未完成：${state}`;
                this.sendWorkbenchState();
                return;
            }
            try {
                const file = await inspectCompletedVideo(savePath);
                current.download = { filePath: savePath, fileName: path.basename(savePath), mimeType: videoMimeType(savePath, item.getMimeType()), bytes: file.size };
                current.downloadError = "";
                current.status = "downloaded";
            } catch (error) {
                current.downloadError = error instanceof Error ? error.message : "无法读取下载的视频";
            }
            this.sendWorkbenchState();
        });
    }

    async acceptTaskDownload(task) {
        if (!task.download) throw new Error("当前任务还没有可回填的视频下载");
        const file = await inspectCompletedVideo(task.download.filePath);
        const account = this.requireAccount(task.accountId);
        const resultId = randomUUID();
        this.results.set(resultId, { senderId: task.senderId, filePath: task.download.filePath });
        task.resolve({
            resultId,
            fileName: task.download.fileName,
            mimeType: task.download.mimeType || "video/mp4",
            bytes: file.size,
            accountId: account.id,
            accountName: account.name,
            provider: task.provider,
        });
        this.tasks.delete(task.taskId);
        await this.scheduleTasks();
    }

    async scheduleTasks() {
        if (this.quitting) return;
        const accounts = this.accountStore.list()
            .filter((account) => account.enabled && !account.needsLogin && !this.activeTaskForAccount(account.id))
            .sort((left, right) => String(left.lastUsedAt || left.createdAt).localeCompare(String(right.lastUsedAt || right.createdAt)));
        const waiting = [...this.tasks.values()].filter((task) => task.status === "waiting").sort((left, right) => left.createdAt.localeCompare(right.createdAt));
        for (const task of waiting) {
            const accountIndex = accounts.findIndex((account) => account.provider === task.provider);
            if (accountIndex < 0) continue;
            const [account] = accounts.splice(accountIndex, 1);
            task.accountId = account.id;
            task.status = "active";
            try {
                await this.accountStore.update(account.id, { lastUsedAt: new Date().toISOString() });
                await this.ensureAccountView(account.id);
            } catch (error) {
                this.rejectTask(task, error);
                continue;
            }
            if (this.selectedAccountId === account.id) clipboard.writeText(task.prompt);
            if (!this.selectedAccountId || !this.activeTaskForAccount(this.selectedAccountId)) await this.selectAccount(account.id);
        }
        this.sendWorkbenchState();
    }

    rejectTask(task, error) {
        if (!task || !this.tasks.has(task.taskId)) return;
        this.tasks.delete(task.taskId);
        task.reject(error instanceof Error ? error : new Error("桌面视频任务失败"));
        void this.scheduleTasks();
        this.sendWorkbenchState();
    }

    async clearAccountSession(accountId) {
        const view = await this.ensureAccountView(accountId);
        const runtime = this.accountViews.get(accountId);
        for (const childWindow of runtime.childWindows) childWindow.close();
        runtime.childWindows.clear();
        await runtime.session.clearStorageData();
        await runtime.session.clearCache();
        await this.accountStore.update(accountId, { needsLogin: true });
        await view.webContents.loadURL(videoProvider(this.requireAccount(accountId).provider).homeUrl);
    }

    async removeAccount(accountId) {
        const runtime = this.accountViews.get(accountId);
        const accountSession = runtime?.session || session.fromPartition(`persist:mingwant-video-${accountId}`, { cache: true });
        if (runtime) {
            const wasAttached = this.attachedAccountId === accountId;
            if (wasAttached && this.attachedViewVisible && this.workbenchWindow && !this.workbenchWindow.isDestroyed()) {
                this.workbenchWindow.contentView.removeChildView(runtime.view);
            }
            if (wasAttached) {
                this.attachedAccountId = null;
                this.attachedViewVisible = false;
            }
            for (const childWindow of runtime.childWindows) childWindow.close();
            runtime.childWindows.clear();
            runtime.session.removeListener("will-download", runtime.downloadHandler);
            runtime.view.webContents.close();
            this.accountViews.delete(accountId);
        }
        await accountSession.clearStorageData();
        await accountSession.clearCache();
        await this.accountStore.remove(accountId);
        this.selectedAccountId = this.accountStore.list()[0]?.id || null;
        if (this.selectedAccountId) await this.selectAccount(this.selectedAccountId, false);
    }

    async handleBrowserCommand(type) {
        if (!this.selectedAccountId) throw new Error("请先选择账号");
        const view = await this.ensureAccountView(this.selectedAccountId);
        const history = view.webContents.navigationHistory;
        if (type === "browser:back" && history.canGoBack()) history.goBack();
        else if (type === "browser:forward" && history.canGoForward()) history.goForward();
        else if (type === "browser:reload") view.webContents.reload();
        else if (type === "browser:home") await view.webContents.loadURL(videoProvider(this.requireAccount(this.selectedAccountId).provider).homeUrl);
    }

    setBrowserViewVisible(visible) {
        this.browserViewSuppressed = !visible;
        if (!this.workbenchWindow || this.workbenchWindow.isDestroyed() || !this.attachedAccountId) return;
        const view = this.accountViews.get(this.attachedAccountId)?.view;
        if (!view) return;
        if (visible && !this.attachedViewVisible) {
            this.workbenchWindow.contentView.addChildView(view);
            this.attachedViewVisible = true;
            this.layoutAttachedView();
        } else if (!visible && this.attachedViewVisible) {
            this.workbenchWindow.contentView.removeChildView(view);
            this.attachedViewVisible = false;
        }
    }

    layoutAttachedView() {
        if (!this.workbenchWindow || this.workbenchWindow.isDestroyed() || !this.attachedAccountId) return;
        const view = this.accountViews.get(this.attachedAccountId)?.view;
        if (!view) return;
        const [width, height] = this.workbenchWindow.getContentSize();
        view.setBounds({ x: ACCOUNT_RAIL_WIDTH, y: TASK_CONSOLE_HEIGHT, width: Math.max(1, width - ACCOUNT_RAIL_WIDTH), height: Math.max(1, height - TASK_CONSOLE_HEIGHT) });
    }

    workflowSummary() {
        return buildWorkflowSummary(this.accountStore.list(), [...this.tasks.values()], (accountId) => this.activeTaskForAccount(accountId));
    }

    workbenchState() {
        const taskValues = [...this.tasks.values()];
        const accounts = presentWorkflowAccounts(this.accountStore.list(), (accountId) => this.activeTaskForAccount(accountId));
        const selectedRuntime = this.selectedAccountId ? this.accountViews.get(this.selectedAccountId) : null;
        const selectedView = selectedRuntime?.view;
        return {
            accounts,
            tasks: presentWorkflowTasks(taskValues),
            selectedAccountId: this.selectedAccountId,
            selectedAccountUrl: selectedRuntime?.loadError ? `${selectedRuntime.loadError} · ${safeDisplayUrl(selectedRuntime.url)}` : safeDisplayUrl(selectedRuntime?.url || ""),
            browser: {
                canGoBack: selectedView?.webContents.navigationHistory.canGoBack() || false,
                canGoForward: selectedView?.webContents.navigationHistory.canGoForward() || false,
            },
        };
    }

    sendWorkbenchState() {
        const webContents = this.workbenchWindow?.webContents;
        if (!webContents || webContents.isDestroyed() || webContents.isLoadingMainFrame()) return;
        webContents.send(IPC_CHANNELS.workbenchStateChanged, this.workbenchState());
    }

    activeTaskForAccount(accountId) {
        return [...this.tasks.values()].find((task) => task.accountId === accountId && task.status !== "waiting") || null;
    }

    requireTask(taskId) {
        const task = this.tasks.get(normalizeWorkflowId(taskId, "任务 ID"));
        if (!task) throw new Error("桌面视频任务不存在或已经结束");
        return task;
    }

    requireAccount(accountId) {
        const account = this.accountStore.get(normalizeWorkflowId(accountId, "账号 ID"));
        if (!account) throw new Error("账号不存在");
        return account;
    }

    requireIdleAccount(accountId) {
        const account = this.requireAccount(accountId);
        if (this.activeTaskForAccount(account.id)) throw new Error("账号正在执行视频任务，请先完成或取消任务");
        return account;
    }

    assertMainSender(event) {
        const senderOrigin = safeOrigin(event.senderFrame?.url || "");
        if (this.mainWindow.isDestroyed() || event.sender.id !== this.mainWindow.webContents.id || event.senderFrame !== event.sender.mainFrame || senderOrigin !== this.allowedWebOrigin) throw new Error("未授权的桌面应用调用");
    }

    assertWorkbenchSender(event) {
        if (!this.workbenchWindow || this.workbenchWindow.isDestroyed() || event.sender.id !== this.workbenchWindow.webContents.id || event.senderFrame !== event.sender.mainFrame || event.senderFrame.url !== this.workbenchDocumentUrl) throw new Error("未授权的工作台调用");
    }

    prepareToQuit() {
        this.quitting = true;
        app.removeListener("login", this.proxyLoginHandler);
        for (const task of [...this.tasks.values()]) this.rejectTask(task, new Error("桌面应用已经退出"));
        for (const runtime of this.accountViews.values()) {
            for (const childWindow of runtime.childWindows) childWindow.destroy();
            runtime.session.removeListener("will-download", runtime.downloadHandler);
            runtime.view.webContents.close();
        }
        this.accountViews.clear();
        this.attachedViewVisible = false;
        this.results.clear();
        this.workbenchWindow?.destroy();
    }
}
