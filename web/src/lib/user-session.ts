import { getSystemChannels, type AuthSessionPayload } from "@/services/api/auth";
import { localForageStorage } from "@/lib/localforage-storage";
import { appQueryClient } from "@/lib/query-client";
import { aiConfigStorage, clearSessionAiConfigSecrets } from "@/lib/ai-config-storage";
import { getActiveUserScope, scopedLocalStorage, setActiveUserScope } from "@/lib/user-scope";
import { CANVAS_STORE_KEY, flushCanvasStorePersistence, useCanvasStore } from "@/stores/canvas/use-canvas-store";
import { ASSET_STORE_KEY, useAssetStore } from "@/stores/use-asset-store";
import { CONFIG_STORE_KEY, defaultConfig, normalizeConfigSnapshot, useConfigStore } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";
import { installRemoteUserDataAutoSync, resetRemoteUserDataSync, syncRemoteUserData } from "@/services/user-data-sync";

export async function applyUserSession(payload: AuthSessionPayload) {
    const previousUserId = useUserStore.getState().user?.id || "";
    const nextUserId = payload.user?.id || "";
    const previousScope = getActiveUserScope();
    useUserStore.getState().setHydrated(false);
    try {
        // Query key 不携带用户 ID；身份变化时必须取消并清空旧账号请求，避免跨账号复用内存数据。
        if (previousUserId !== nextUserId) appQueryClient.clear();
        resetRemoteUserDataSync();
        await flushCanvasStorePersistence();
        if (previousScope !== (nextUserId || "guest")) {
            // 即使登录态已过期，也先把旧版配置中的明文密钥迁出普通快照，再清理会话级凭据。
            try {
                aiConfigStorage.getItem(CONFIG_STORE_KEY);
            } catch (error) {
                console.warn("旧版 AI 配置密钥迁移失败，未读取密钥正文", error);
            }
            clearSessionAiConfigSecrets();
        }
        setActiveUserScope(payload.user?.id);
        const [persistedCanvas, persistedAssets] = await Promise.all([
            localForageStorage.getItem(CANVAS_STORE_KEY),
            localForageStorage.getItem(ASSET_STORE_KEY),
        ]);
        const persistedConfig = scopedLocalStorage.getItem(CONFIG_STORE_KEY);
        useUserStore.getState().setUser(payload.user);
        useUserStore.getState().setRuntimeLimits(payload.runtimeLimits);
        await Promise.all([useCanvasStore.persist.rehydrate(), useAssetStore.persist.rehydrate(), useConfigStore.persist.rehydrate()]);
        // Zustand 在目标 scope 没有快照时会保留旧内存，必须显式恢复该 scope 的空状态。
        if (!persistedCanvas) useCanvasStore.setState({ projects: [] });
        if (!persistedAssets) useAssetStore.setState({ assets: [] });
        if (!persistedConfig) {
            // 只有首次配置缺失时才生成能力推荐；已有配置中的空数组代表用户明确清空。
            const initialSystemConfig = {
                ...defaultConfig,
                channels: payload.systemChannels || [],
                imageModels: undefined,
                videoModels: undefined,
                textModels: undefined,
                audioModels: undefined,
            };
            useConfigStore.getState().replaceConfig(normalizeConfigSnapshot({ config: initialSystemConfig }).config);
        } else {
            useConfigStore.getState().mergeSystemChannels(payload.systemChannels || []);
        }
        installRemoteUserDataAutoSync();
        if (payload.user?.id) await syncRemoteUserData(payload.user.id);
        else resetRemoteUserDataSync();
    } finally {
        useUserStore.getState().setHydrated(true);
    }
}

export async function refreshSystemChannels() {
    // 系统模型由后端统一维护，后台变更后只刷新这一层，避免重跑整套用户数据同步。
    const payload = await getSystemChannels();
    useConfigStore.getState().mergeSystemChannels(payload.channels || []);
}
