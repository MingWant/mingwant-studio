const ACTIVE_USER_SCOPE_KEY = "infinite-canvas:active-user-scope";
const GUEST_SCOPE = "guest";
// 账号作用域只代表当前标签页已经由 Backend 确认的身份；不能放进
// localStorage，否则两个标签页登录不同账号时会互相覆盖，随后把配置和测活
// 记录写进另一账号的本地分区。真正的配置仍按 userId 保存在 localStorage，
// 这里只把“当前标签页选中了谁”留在 sessionStorage。
let memoryUserScope = GUEST_SCOPE;

export function getActiveUserScope() {
    if (typeof window === "undefined") return memoryUserScope;
    try {
        const stored = window.sessionStorage.getItem(ACTIVE_USER_SCOPE_KEY);
        if (stored) memoryUserScope = stored;
    } catch {
        // 隐私模式或扩展拦截存储时不能让测活提交键、配置读取和页面初始化抛出未处理异常；
        // 退回 guest 只影响本地持久化作用域，服务端仍以登录用户鉴权和提交键校验为准。
    }
    return memoryUserScope;
}

export function setActiveUserScope(userId?: string | null) {
    memoryUserScope = userId || GUEST_SCOPE;
    if (typeof window === "undefined") return;
    try {
        window.sessionStorage.setItem(ACTIVE_USER_SCOPE_KEY, memoryUserScope);
    } catch {
        // 无法写入作用域时继续使用当前内存身份；依赖持久缓存的功能会按未验证/无缓存安全降级。
    }
}

export function scopedStorageKey(name: string) {
    return `${name}:user:${getActiveUserScope()}`;
}

export const scopedLocalStorage = {
    getItem: (name: string) => {
        if (typeof window === "undefined") return null;
        try {
            return window.localStorage.getItem(scopedStorageKey(name));
        } catch {
            // 配置缓存不可用时交给调用方走未验证/默认配置，不能让页面初始化或 Agent 请求直接抛错。
            return null;
        }
    },
    setItem: (name: string, value: string) => {
        if (typeof window === "undefined") return;
        try {
            window.localStorage.setItem(scopedStorageKey(name), value);
        } catch {
            // 隐私模式或扩展拦截只会放弃跨刷新缓存；当前内存状态仍由 store 继续维护。
        }
    },
    removeItem: (name: string) => {
        if (typeof window === "undefined") return;
        try {
            window.localStorage.removeItem(scopedStorageKey(name));
        } catch {
            // 清理缓存失败不应阻断退出、切换账号或重新测活。
        }
    },
};

export const scopedSessionStorage = {
    getItem: (name: string) => {
        if (typeof window === "undefined") return null;
        try {
            return window.sessionStorage.getItem(scopedStorageKey(name));
        } catch {
            return null;
        }
    },
    setItem: (name: string, value: string) => {
        if (typeof window === "undefined") return;
        try {
            window.sessionStorage.setItem(scopedStorageKey(name), value);
        } catch {
            // 会话缓存不可用时保持内存态；依赖持久测活诊断的入口会安全降级为未验证提示，
            // 但不会因此阻止用户在明确费用边界后调用模型。
        }
    },
    removeItem: (name: string) => {
        if (typeof window === "undefined") return;
        try {
            window.sessionStorage.removeItem(scopedStorageKey(name));
        } catch {
            // 退出或切换账号时清理失败不应影响服务端会话操作。
        }
    },
};
