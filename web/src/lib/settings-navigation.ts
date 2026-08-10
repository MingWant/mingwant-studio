import { modelSelectionNeedsReselection, type AiConfig } from "@/stores/use-config-store";

export type SettingsSection = "channels" | "models" | "preferences" | "storage";

export const CANVAS_MODEL_RESELECTION_REQUIRED_MESSAGE = "当前选择的渠道或模型已被删除、下架或暂不可用，请直接在画布的模型选择器中重新选择可用模型；本次未打开自定义渠道设置。";

export function settingsPath(section: SettingsSection = "channels", continueCreation = false) {
    const params = new URLSearchParams({ section });
    if (continueCreation) params.set("continue", "1");
    return `/settings?${params.toString()}`;
}

/**
 * 画布深层组件没有路由上下文出口时统一跳转到正式设置页，避免重新引入全局配置弹窗。
 */
export function navigateToSettings(options?: { section?: SettingsSection; continueCreation?: boolean }) {
    const to = settingsPath(options?.section, options?.continueCreation);
    const event = new CustomEvent<{ to: string }>("workspace:navigate", { detail: { to }, cancelable: true });
    if (window.dispatchEvent(event)) window.location.assign(to);
}

/**
 * 已失效的渠道模型必须留在画布中显式重选；尤其不能把已删除的系统模型
 * 交给通用“缺少配置”兜底，再误导用户去填写自定义渠道。
 */
export function handleUnavailableCanvasModel(config: AiConfig, model: string, onReselectionRequired: (content: string) => void) {
    if (modelSelectionNeedsReselection(config, model)) {
        onReselectionRequired(CANVAS_MODEL_RESELECTION_REQUIRED_MESSAGE);
        return "reselect" as const;
    }
    navigateToSettings({ section: "models", continueCreation: true });
    return "settings" as const;
}
