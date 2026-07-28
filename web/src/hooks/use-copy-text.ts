import { App } from "antd";
import copy from "copy-to-clipboard";

export function useCopyText() {
    const { message } = App.useApp();

    return async (value: string, successText = "已复制") => {
        let copied = false;

        // 现代剪贴板 API 能准确反映权限与写入结果；旧浏览器再回退到 execCommand。
        try {
            if (navigator.clipboard?.writeText) {
                await navigator.clipboard.writeText(value);
                copied = true;
            }
        } catch {
            copied = false;
        }

        if (!copied) {
            try {
                copied = await copy(value);
            } catch {
                copied = false;
            }
        }

        if (copied) {
            message.success(successText);
            return true;
        }
        message.error("复制失败，请手动选择内容复制");
        return false;
    };
}
