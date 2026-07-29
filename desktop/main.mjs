import path from "node:path";
import { Buffer } from "node:buffer";
import { fileURLToPath } from "node:url";
import { app, BrowserWindow, Menu, safeStorage } from "electron";

import { AccountStore } from "./account-store.mjs";
import { DesktopVideoWorkflow } from "./video-workflow.mjs";

const desktopDirectory = path.dirname(fileURLToPath(import.meta.url));
const mainPreloadPath = path.join(desktopDirectory, "preload.cjs");
const workbenchPreloadPath = path.join(desktopDirectory, "workbench-preload.cjs");
const workbenchHtmlPath = path.join(desktopDirectory, "workbench.html");
const webUrl = normalizeWebUrl(process.env.MINGWANT_WEB_URL || "http://localhost:3000");

app.setName("MingWant Studio");
app.setAppUserModelId("studio.mingwant.desktop");

const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) {
    app.quit();
} else {
    let mainWindow = null;
    let videoWorkflow = null;
    let preparedToQuit = false;

    app.on("second-instance", () => {
        if (!mainWindow || mainWindow.isDestroyed()) return;
        if (mainWindow.isMinimized()) mainWindow.restore();
        mainWindow.show();
        mainWindow.focus();
    });

    app.on("before-quit", () => {
        if (preparedToQuit) return;
        preparedToQuit = true;
        videoWorkflow?.prepareToQuit();
    });

    await app.whenReady();
    const accountStore = new AccountStore(app.getPath("userData"), {
        encryptSecret: (value) => {
            if (!safeStorage.isEncryptionAvailable()) throw new Error("系统安全存储当前不可用");
            return safeStorage.encryptString(value).toString("base64");
        },
        decryptSecret: (value) => {
            if (!safeStorage.isEncryptionAvailable()) throw new Error("系统安全存储当前不可用");
            return safeStorage.decryptString(Buffer.from(value, "base64"));
        },
    });
    await accountStore.load();
    mainWindow = createMainWindow();
    videoWorkflow = new DesktopVideoWorkflow({
        mainWindow,
        allowedWebOrigin: new URL(webUrl).origin,
        accountStore,
        userDataDirectory: app.getPath("userData"),
        downloadsDirectory: app.getPath("downloads"),
        workbenchPreloadPath,
        workbenchHtmlPath,
    });
    videoWorkflow.registerIpc();
    installApplicationMenu();
    await mainWindow.loadURL(webUrl);

    app.on("window-all-closed", () => app.quit());

    function createMainWindow() {
        const window = new BrowserWindow({
            width: 1480,
            height: 960,
            minWidth: 1024,
            minHeight: 720,
            backgroundColor: "#0b0d12",
            title: "明想 MingWant Studio",
            webPreferences: {
                preload: mainPreloadPath,
                nodeIntegration: false,
                contextIsolation: true,
                sandbox: true,
                webSecurity: true,
            },
        });
        window.on("closed", () => {
            mainWindow = null;
            app.quit();
        });
        return window;
    }

    function installApplicationMenu() {
        const template = [
            {
                label: "明想",
                submenu: [
                    { label: "网页视频工作台", accelerator: "CmdOrCtrl+Shift+V", click: () => void videoWorkflow?.openWorkbench() },
                    { type: "separator" },
                    { role: "quit", label: "退出" },
                ],
            },
            {
                label: "编辑",
                submenu: [
                    { role: "undo", label: "撤销" },
                    { role: "redo", label: "重做" },
                    { type: "separator" },
                    { role: "cut", label: "剪切" },
                    { role: "copy", label: "复制" },
                    { role: "paste", label: "粘贴" },
                    { role: "selectAll", label: "全选" },
                ],
            },
            {
                label: "视图",
                submenu: [
                    { role: "reload", label: "刷新" },
                    { role: "togglefullscreen", label: "切换全屏" },
                ],
            },
        ];
        Menu.setApplicationMenu(Menu.buildFromTemplate(template));
    }
}

function normalizeWebUrl(value) {
    const url = new URL(value);
    if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("MINGWANT_WEB_URL 只允许 HTTP(S) 地址");
    return url.toString();
}
