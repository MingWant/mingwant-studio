const { contextBridge, ipcRenderer } = require("electron");

// 沙箱预加载保持自包含，只向可信 Web 主页面暴露完成视频回填所需的最小 IPC 面。
const channels = Object.freeze({
    openWorkbench: "mingwant-desktop:open-video-workbench",
    workflowState: "mingwant-desktop:video-workflow-state",
    startTask: "mingwant-desktop:start-video-task",
    cancelTask: "mingwant-desktop:cancel-video-task",
    readResult: "mingwant-desktop:read-video-result",
    releaseResult: "mingwant-desktop:release-video-result",
});

contextBridge.exposeInMainWorld("mingwantDesktop", {
    version: 1,
    openVideoWorkbench: () => ipcRenderer.invoke(channels.openWorkbench),
    getVideoWorkflowState: () => ipcRenderer.invoke(channels.workflowState),
    startVideoTask: (request) => {
        const pending = ipcRenderer.invoke(channels.startTask, request);
        request = null;
        return pending;
    },
    cancelVideoTask: (taskId) => ipcRenderer.invoke(channels.cancelTask, taskId),
    readVideoResult: (resultId) => ipcRenderer.invoke(channels.readResult, resultId),
    releaseVideoResult: (resultId) => ipcRenderer.invoke(channels.releaseResult, resultId),
});
