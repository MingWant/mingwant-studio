import { useCallback, useEffect, useState } from "react";

const FOCUS_MODE_KEY = "canvas-focus-mode-v2";
const SMALL_SCREEN_BREAKPOINT = 1024;

// 小屏默认进入专注模式；宽屏记住用户选择，避免窗口变化时反复打断创作。
function readInitialPreference(): boolean {
    try {
        const stored = window.localStorage.getItem(FOCUS_MODE_KEY);
        if (stored !== null) return stored === "true";
    } catch {
        // 隐私模式禁止读取存储时回退到屏幕宽度策略。
    }
    return window.innerWidth < SMALL_SCREEN_BREAKPOINT;
}

export function useFocusMode() {
    const [userPreference, setUserPreference] = useState<boolean>(readInitialPreference);
    const [smallScreen, setSmallScreen] = useState<boolean>(() => window.innerWidth < SMALL_SCREEN_BREAKPOINT);

    const focusMode = smallScreen || userPreference;

    useEffect(() => {
        const handleResize = () => setSmallScreen(window.innerWidth < SMALL_SCREEN_BREAKPOINT);
        window.addEventListener("resize", handleResize);
        return () => window.removeEventListener("resize", handleResize);
    }, []);

    const persist = useCallback((next: boolean) => {
        setUserPreference(next);
        try {
            window.localStorage.setItem(FOCUS_MODE_KEY, String(next));
        } catch {
            // 本地存储不可用时仍允许本次会话切换专注模式。
        }
    }, []);

    const enterFocusMode = useCallback(() => persist(true), [persist]);
    const exitFocusMode = useCallback(() => persist(false), [persist]);
    const toggleFocusMode = useCallback(() => persist(!(smallScreen || userPreference)), [persist, smallScreen, userPreference]);

    return { focusMode, enterFocusMode, exitFocusMode, toggleFocusMode };
}
