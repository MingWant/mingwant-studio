import localforage from "localforage";
import type { StateStorage } from "zustand/middleware";

import { scopedStorageKey } from "@/lib/user-scope";

localforage.config({
    name: "infinite-canvas",
    storeName: "app_state",
});

export const localForageStorage: StateStorage = {
    getItem: async (name) => {
        if (typeof window === "undefined") return null;
        const key = scopedStorageKey(name);
        try {
            return (await localforage.getItem<string>(key)) || null;
        } catch {
            try {
                return window.localStorage.getItem(key);
            } catch {
                // 本地缓存不可用时按空缓存启动，服务端数据和当前内存状态仍可继续使用。
                return null;
            }
        }
    },
    setItem: async (name, value) => {
        if (typeof window === "undefined") return;
        const key = scopedStorageKey(name);
        try {
            await localforage.setItem(key, value);
        } catch {
            try {
                window.localStorage.setItem(key, value);
            } catch {
                // 本地缓存属于可选持久化；不可写时不阻断当前会话的业务操作。
            }
        }
    },
    removeItem: async (name) => {
        if (typeof window === "undefined") return;
        const key = scopedStorageKey(name);
        try {
            await localforage.removeItem(key);
        } catch {
            try {
                window.localStorage.removeItem(key);
            } catch {
                // 清理缓存失败不影响内存中的当前状态。
            }
        }
    },
};
