import { VIDEO_PROVIDERS, videoProvider } from "./video-providers.mjs";

export function buildWorkflowSummary(accounts, tasks, activeTaskForAccount) {
    const providers = Object.fromEntries(VIDEO_PROVIDERS.map((provider) => {
        const usableAccounts = accounts.filter((account) => account.provider === provider.id && account.enabled && !account.needsLogin);
        return [provider.id, {
            enabledAccountCount: usableAccounts.length,
            busyAccountCount: usableAccounts.filter((account) => Boolean(activeTaskForAccount(account.id))).length,
            queuedTaskCount: tasks.filter((task) => task.provider === provider.id && task.status === "waiting").length,
        }];
    }));
    return {
        enabledAccountCount: Object.values(providers).reduce((sum, state) => sum + state.enabledAccountCount, 0),
        busyAccountCount: Object.values(providers).reduce((sum, state) => sum + state.busyAccountCount, 0),
        queuedTaskCount: tasks.filter((task) => task.status === "waiting").length,
        providers,
    };
}

export function presentWorkflowAccounts(accounts, activeTaskForAccount) {
    return accounts.map((account) => {
        const task = activeTaskForAccount(account.id);
        const status = !account.enabled ? "disabled" : account.needsLogin ? "login" : task ? "busy" : "idle";
        return {
            ...account,
            providerName: videoProvider(account.provider).name,
            status,
            statusLabel: status === "disabled" ? "已停用" : status === "login" ? "需要登录" : status === "busy" ? "任务进行中" : "空闲",
            activeTaskId: task?.taskId || null,
        };
    });
}

export function presentWorkflowTasks(tasks) {
    return tasks.map((task) => ({
        id: task.taskId,
        title: task.title,
        prompt: task.prompt,
        settings: task.settings,
        status: task.status,
        accountId: task.accountId,
        provider: task.provider,
        providerName: videoProvider(task.provider).name,
        createdAt: task.createdAt,
        referenceFileCount: task.referenceFileCount,
        referenceLinkCount: task.referenceLinkCount,
        download: task.download ? { fileName: task.download.fileName, bytes: task.download.bytes } : null,
        downloadError: task.downloadError,
    }));
}
