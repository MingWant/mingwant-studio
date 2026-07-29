const { contextBridge, ipcRenderer } = require("electron");

// 本地工作台只获得状态查询和白名单命令入口，远端账号容器不加载任何 preload。
const channels = Object.freeze({
    workbenchState: "mingwant-desktop:workbench-state",
    workbenchCommand: "mingwant-desktop:workbench-command",
    workbenchStateChanged: "mingwant-desktop:workbench-state-changed",
});

contextBridge.exposeInMainWorld("mingwantWorkbench", {
    getState: () => ipcRenderer.invoke(channels.workbenchState),
    command: (command) => ipcRenderer.invoke(channels.workbenchCommand, command),
    onStateChanged: (listener) => {
        const wrapped = (_event, state) => listener(state);
        ipcRenderer.on(channels.workbenchStateChanged, wrapped);
        return () => ipcRenderer.removeListener(channels.workbenchStateChanged, wrapped);
    },
});
