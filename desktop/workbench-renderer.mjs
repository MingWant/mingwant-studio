const api = window.mingwantWorkbench;

if (!api) throw new Error("网页视频工作台桥接未加载");

const elements = {
    addForm: document.querySelector("#add-account-form"),
    accountName: document.querySelector("#account-name"),
    accountProvider: document.querySelector("#account-provider"),
    newAccountProxy: document.querySelector("#new-account-proxy"),
    accountList: document.querySelector("#account-list"),
    accountSummary: document.querySelector("#account-summary"),
    queueList: document.querySelector("#queue-list"),
    queueSummary: document.querySelector("#queue-summary"),
    selectedName: document.querySelector("#selected-account-name"),
    selectedUrl: document.querySelector("#selected-account-url"),
    selectedStatus: document.querySelector("#selected-status"),
    taskCard: document.querySelector("#task-card"),
    taskEmpty: document.querySelector(".task-empty"),
    taskContent: document.querySelector(".task-content"),
    taskTitle: document.querySelector("#task-title"),
    taskState: document.querySelector("#task-state"),
    taskPrompt: document.querySelector("#task-prompt"),
    taskParameters: document.querySelector("#task-parameters"),
    copyPrompt: document.querySelector("#copy-prompt"),
    openInputs: document.querySelector("#open-inputs"),
    acceptDownload: document.querySelector("#accept-download"),
    ignoreDownload: document.querySelector("#ignore-download"),
    cancelTask: document.querySelector("#cancel-task"),
    browserBack: document.querySelector("#browser-back"),
    browserForward: document.querySelector("#browser-forward"),
    browserReload: document.querySelector("#browser-reload"),
    browserHome: document.querySelector("#browser-home"),
    renameAccount: document.querySelector("#rename-account"),
    configureProxy: document.querySelector("#configure-proxy"),
    toggleLoginState: document.querySelector("#toggle-login-state"),
    toggleAccount: document.querySelector("#toggle-account"),
    clearAccount: document.querySelector("#clear-account"),
    deleteAccount: document.querySelector("#delete-account"),
    proxyDialog: document.querySelector("#proxy-dialog"),
    proxyForm: document.querySelector("#proxy-form"),
    proxyDialogTitle: document.querySelector("#proxy-dialog-title"),
    proxyDialogSubtitle: document.querySelector("#proxy-dialog-subtitle"),
    proxyServer: document.querySelector("#proxy-server"),
    proxyUsername: document.querySelector("#proxy-username"),
    proxyPassword: document.querySelector("#proxy-password"),
    proxyUseDirect: document.querySelector("#proxy-use-direct"),
    proxyDialogCancel: document.querySelector("#proxy-dialog-cancel"),
    proxyDialogClose: document.querySelector("#proxy-dialog-close"),
    toast: document.querySelector("#toast"),
};

let state = emptyState();
let toastTimer = null;
let newAccountProxyDraft = directProxyDraft();
let proxyDialogTarget = null;

elements.addForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const name = elements.accountName.value.trim();
    const succeeded = await runCommand({ type: "account:add", name, provider: elements.accountProvider.value, proxy: newAccountProxyDraft });
    if (succeeded) {
        elements.accountName.value = "";
        newAccountProxyDraft = directProxyDraft();
        renderNewAccountProxy();
    }
});

elements.newAccountProxy.addEventListener("click", () => void openProxyDialog({ kind: "new", proxy: newAccountProxyDraft }));

bindCommand(elements.browserBack, () => ({ type: "browser:back" }));
bindCommand(elements.browserForward, () => ({ type: "browser:forward" }));
bindCommand(elements.browserReload, () => ({ type: "browser:reload" }));
bindCommand(elements.browserHome, () => ({ type: "browser:home" }));

elements.renameAccount.addEventListener("click", async () => {
    const account = selectedAccount();
    if (!account) return;
    const name = window.prompt("输入新的账号名称", account.name)?.trim();
    if (name) await runCommand({ type: "account:rename", accountId: account.id, name });
});

elements.configureProxy.addEventListener("click", () => {
    const account = selectedAccount();
    if (account) void openProxyDialog({ kind: "account", accountId: account.id, accountName: account.name, proxy: account.proxy });
});

elements.toggleLoginState.addEventListener("click", async () => {
    const account = selectedAccount();
    if (account) await runCommand({ type: "account:set-needs-login", accountId: account.id, needsLogin: !account.needsLogin });
});

elements.toggleAccount.addEventListener("click", async () => {
    const account = selectedAccount();
    if (account) await runCommand({ type: "account:set-enabled", accountId: account.id, enabled: !account.enabled });
});

elements.clearAccount.addEventListener("click", async () => {
    const account = selectedAccount();
    if (!account || !window.confirm(`清空“${account.name}”的 Cookie、缓存和站点存储？此操作需要重新登录。`)) return;
    await runCommand({ type: "account:clear-session", accountId: account.id });
});

elements.deleteAccount.addEventListener("click", async () => {
    const account = selectedAccount();
    if (!account || !window.confirm(`删除“${account.name}”并清空其本地登录态？此操作不可撤销。`)) return;
    await runCommand({ type: "account:remove", accountId: account.id });
});

elements.proxyForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!proxyDialogTarget) return;
    let proxy = {
        server: elements.proxyServer.value.trim(),
        username: elements.proxyUsername.value.trim(),
        password: elements.proxyPassword.value,
        preservePassword: !elements.proxyPassword.value
            && Boolean(proxyDialogTarget.proxy?.hasPassword)
            && elements.proxyUsername.value.trim() === proxyDialogTarget.proxy?.username,
    };
    if (!proxy.server) proxy = directProxyDraft();
    if (proxyDialogTarget.kind === "new") {
        newAccountProxyDraft = { ...proxy, hasPassword: Boolean(proxy.password) };
        renderNewAccountProxy();
        closeProxyDialog();
        return;
    }
    const currentProxy = proxyDialogTarget.proxy;
    if (!proxy.password && proxy.server === (currentProxy?.server || "") && proxy.username === (currentProxy?.username || "")) {
        closeProxyDialog();
        return;
    }
    const succeeded = await runCommand({ type: "account:set-proxy", accountId: proxyDialogTarget.accountId, proxy });
    if (succeeded) closeProxyDialog();
});

elements.proxyUseDirect.addEventListener("click", async () => {
    if (!proxyDialogTarget) return;
    if (proxyDialogTarget.kind === "new") {
        newAccountProxyDraft = directProxyDraft();
        renderNewAccountProxy();
        closeProxyDialog();
        return;
    }
    if (proxyDialogTarget.proxy && !window.confirm(`将“${proxyDialogTarget.accountName}”切换为直连？下一次加载会使用本机出口 IP，并要求重新确认登录。`)) return;
    const succeeded = await runCommand({ type: "account:set-proxy", accountId: proxyDialogTarget.accountId, proxy: directProxyDraft() });
    if (succeeded) closeProxyDialog();
});

elements.proxyDialogCancel.addEventListener("click", closeProxyDialog);
elements.proxyDialogClose.addEventListener("click", closeProxyDialog);
elements.proxyDialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeProxyDialog();
});

bindTaskCommand(elements.copyPrompt, "task:copy-prompt");
bindTaskCommand(elements.openInputs, "task:open-inputs");
bindTaskCommand(elements.acceptDownload, "task:accept-download");
bindTaskCommand(elements.ignoreDownload, "task:ignore-download");

elements.cancelTask.addEventListener("click", async () => {
    const task = selectedTask();
    if (!task || !window.confirm(`取消“${task.title}”并释放账号？已经下载的文件不会删除。`)) return;
    await runCommand({ type: "task:cancel", taskId: task.id });
});

api.onStateChanged((nextState) => {
    state = nextState;
    render();
});

try {
    state = await api.getState();
    render();
} catch (error) {
    showToast(errorMessage(error), true);
}

function render() {
    renderNewAccountProxy();
    renderAccounts();
    renderQueue();
    renderSelectedAccount();
    renderTask();
}

function renderAccounts() {
    elements.accountSummary.textContent = `${state.accounts.length} 个`;
    if (!state.accounts.length) {
        elements.accountList.replaceChildren(messageNode("还没有账号。新增后会创建独立、持久化的网页分区。", "account-empty"));
        return;
    }
    elements.accountList.replaceChildren(...state.accounts.map((account) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = `account-item${account.id === state.selectedAccountId ? " selected" : ""}`;
        button.addEventListener("click", () => void runCommand({ type: "account:select", accountId: account.id }, false));

        const dot = document.createElement("span");
        dot.className = `account-dot status-${account.status}`;
        const copy = document.createElement("span");
        const name = document.createElement("strong");
        name.textContent = account.name;
        const status = document.createElement("small");
        status.textContent = `${account.providerName} · ${account.statusLabel} · ${account.proxy ? "代理" : "直连"}`;
        copy.append(name, status);
        const taskCount = document.createElement("span");
        taskCount.className = "task-count";
        taskCount.textContent = account.activeTaskId ? "1 项" : "";
        button.append(dot, copy, taskCount);
        return button;
    }));
}

function renderQueue() {
    const queued = state.tasks.filter((task) => task.status === "waiting");
    elements.queueSummary.textContent = `${queued.length} 项`;
    if (!queued.length) {
        elements.queueList.replaceChildren(messageNode("当前没有等待分配的任务", "queue-empty"));
        return;
    }
    elements.queueList.replaceChildren(...queued.map((task) => {
        const item = document.createElement("div");
        item.className = "queue-item";
        const title = document.createElement("strong");
        title.textContent = task.title;
        const time = document.createElement("span");
        time.textContent = `${task.providerName} · ${formatTime(task.createdAt)}`;
        item.append(title, time);
        return item;
    }));
}

function renderSelectedAccount() {
    const account = selectedAccount();
    const hasAccount = Boolean(account);
    elements.selectedName.textContent = account ? `${account.name} · ${account.providerName}` : "请选择账号";
    elements.selectedUrl.textContent = state.selectedAccountUrl || "尚未打开网页容器";
    elements.selectedStatus.className = `status-dot status-${account?.status || "disabled"}`;
    elements.browserBack.disabled = !state.browser?.canGoBack;
    elements.browserForward.disabled = !state.browser?.canGoForward;
    elements.browserReload.disabled = !hasAccount;
    elements.browserHome.disabled = !hasAccount;
    elements.renameAccount.disabled = !hasAccount;
    elements.configureProxy.disabled = !hasAccount || Boolean(account?.activeTaskId);
    elements.toggleLoginState.disabled = !hasAccount || Boolean(account?.activeTaskId);
    elements.toggleAccount.disabled = !hasAccount || Boolean(account?.activeTaskId);
    elements.clearAccount.disabled = !hasAccount || Boolean(account?.activeTaskId);
    elements.deleteAccount.disabled = !hasAccount || Boolean(account?.activeTaskId);
    elements.toggleLoginState.textContent = account?.needsLogin ? "标记已登录" : "标记需登录";
    elements.toggleAccount.textContent = account?.enabled ? "停用" : "启用";
    elements.configureProxy.textContent = account?.proxy ? "代理：已设置" : "代理：直连";
    elements.configureProxy.title = account?.proxy?.server || "当前账号使用直连";
}

function renderNewAccountProxy() {
    elements.newAccountProxy.textContent = newAccountProxyDraft.server ? "新账号网络：已设置代理" : "新账号网络：直连";
    elements.newAccountProxy.title = newAccountProxyDraft.server ? "添加账号时将在首次加载前应用所设代理" : "新账号首次加载时使用直连";
}

async function openProxyDialog(target) {
    proxyDialogTarget = target;
    const hidden = await runCommand({ type: "browser:set-visible", visible: false }, false);
    if (!hidden || proxyDialogTarget !== target) {
        if (proxyDialogTarget === target) proxyDialogTarget = null;
        return;
    }
    elements.proxyDialogTitle.textContent = target.kind === "new" ? "设置新账号网络" : "设置账号代理";
    elements.proxyDialogSubtitle.textContent = target.kind === "new" ? "保存后再添加账号，避免首次访问泄露直连 IP" : target.accountName;
    elements.proxyServer.value = target.proxy?.server || "";
    elements.proxyUsername.value = target.proxy?.username || "";
    elements.proxyPassword.value = target.kind === "new" ? target.proxy?.password || "" : "";
    elements.proxyPassword.placeholder = target.proxy?.hasPassword ? "已安全保存；留空保持不变" : "可选";
    elements.proxyUseDirect.disabled = !target.proxy?.server;
    elements.proxyDialog.showModal();
    elements.proxyServer.focus();
}

function closeProxyDialog() {
    if (elements.proxyDialog.open) elements.proxyDialog.close();
    elements.proxyServer.value = "";
    elements.proxyUsername.value = "";
    elements.proxyPassword.value = "";
    proxyDialogTarget = null;
    void runCommand({ type: "browser:set-visible", visible: true }, false);
}

function renderTask() {
    const task = selectedTask();
    elements.taskCard.classList.toggle("empty", !task);
    elements.taskEmpty.hidden = Boolean(task);
    elements.taskContent.hidden = !task;
    if (!task) return;

    elements.taskTitle.textContent = task.title;
    elements.taskState.textContent = task.downloadError ? `下载异常 · ${task.downloadError}` : task.download ? `已下载 · ${task.download.fileName}` : task.status === "waiting" ? "等待账号" : "人工生成中";
    elements.taskPrompt.textContent = task.prompt;
    const labels = [
        task.providerName,
        task.settings.durationSeconds && `${task.settings.durationSeconds} 秒`,
        task.settings.aspectRatio,
        task.settings.resolution,
        task.settings.generateAudio ? "生成声音" : "静音",
        `${task.referenceFileCount} 个本地素材`,
        task.referenceLinkCount ? `${task.referenceLinkCount} 个参考链接` : "",
    ].filter(Boolean);
    elements.taskParameters.replaceChildren(...labels.map((label) => {
        const pill = document.createElement("span");
        pill.textContent = label;
        return pill;
    }));
    elements.acceptDownload.hidden = !task.download;
    elements.ignoreDownload.hidden = !task.download;
    elements.openInputs.disabled = task.referenceFileCount + task.referenceLinkCount === 0;
}

function selectedAccount() {
    return state.accounts.find((account) => account.id === state.selectedAccountId) || null;
}

function selectedTask() {
    const account = selectedAccount();
    return state.tasks.find((task) => task.id === account?.activeTaskId) || (!account ? state.tasks.find((task) => task.status === "waiting") : null) || null;
}

function bindCommand(element, createCommand) {
    element.addEventListener("click", () => void runCommand(createCommand(), false));
}

function bindTaskCommand(element, type) {
    element.addEventListener("click", async () => {
        const task = selectedTask();
        if (task) await runCommand({ type, taskId: task.id });
    });
}

async function runCommand(command, notify = true) {
    try {
        const nextState = await api.command(command);
        if (nextState) {
            state = nextState;
            render();
        }
        if (notify && command.type !== "account:select") showToast(successMessage(command.type));
        return true;
    } catch (error) {
        showToast(errorMessage(error), true);
        return false;
    }
}

function successMessage(type) {
    if (type === "account:add") return "账号已添加";
    if (type === "account:set-proxy") return "代理已更新，请在新网络环境中确认登录";
    if (type === "task:copy-prompt") return "提示词已复制";
    if (type === "task:open-inputs") return "已打开参考素材目录";
    if (type === "task:accept-download") return "视频正在回填画布";
    if (type === "task:ignore-download") return "已忽略本次下载，可重新下载正确视频";
    return "操作已完成";
}

function showToast(message, error = false) {
    window.clearTimeout(toastTimer);
    elements.toast.textContent = message;
    elements.toast.className = `toast visible${error ? " error" : ""}`;
    toastTimer = window.setTimeout(() => {
        elements.toast.className = "toast";
    }, 3200);
}

function messageNode(text, className) {
    const node = document.createElement("div");
    node.className = className;
    node.textContent = text;
    return node;
}

function formatTime(value) {
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? "" : date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}

function errorMessage(error) {
    return error instanceof Error ? error.message : String(error || "操作失败");
}

function emptyState() {
    return { accounts: [], tasks: [], selectedAccountId: null, selectedAccountUrl: "", browser: { canGoBack: false, canGoForward: false } };
}

function directProxyDraft() {
    return { server: "", username: "", password: "", preservePassword: false, hasPassword: false };
}
