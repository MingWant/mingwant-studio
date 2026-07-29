import path from "node:path";

export function normalizeWorkflowId(value, label) {
    if (typeof value !== "string" || !/^[0-9a-z_-]{6,160}$/i.test(value)) throw new Error(`${label}无效`);
    return value;
}

export function isSafeWebUrl(value) {
    try {
        return new URL(value).protocol === "https:";
    } catch {
        return false;
    }
}

export function isSafePopupUrl(value) {
    return value === "about:blank" || isSafeWebUrl(value);
}

export function safeOrigin(value) {
    try {
        return new URL(value).origin;
    } catch {
        return "";
    }
}

export function safeDisplayUrl(value) {
    try {
        const url = new URL(value);
        return `${url.origin}${url.pathname}`.slice(0, 320);
    } catch {
        return "";
    }
}

export function videoMimeType(filePath, reportedMimeType) {
    const mimeType = String(reportedMimeType || "").toLowerCase();
    if (mimeType.startsWith("video/")) return mimeType;
    const extension = path.extname(filePath).toLowerCase();
    if (extension === ".webm") return "video/webm";
    if (extension === ".mov") return "video/quicktime";
    if (extension === ".m4v") return "video/x-m4v";
    return "video/mp4";
}
