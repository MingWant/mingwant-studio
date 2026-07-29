import { existsSync } from "node:fs";
import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import path from "node:path";

import { normalizeVideoProvider, videoProvider } from "./video-providers.mjs";

const MAX_REFERENCE_FILES = 32;
const MAX_REFERENCE_FILE_BYTES = 256 * 1024 * 1024;
const MAX_REFERENCE_TOTAL_BYTES = 768 * 1024 * 1024;
const MAX_PROMPT_LENGTH = 100_000;
const MAX_RESULT_BYTES = 512 * 1024 * 1024;
const VIDEO_EXTENSIONS = new Set([".mp4", ".webm", ".mov", ".m4v"]);

export async function stageVideoTask(raw, userDataDirectory, downloadsDirectory) {
    const request = normalizeTaskRequest(raw);
    const inputDirectory = path.join(userDataDirectory, "video-workflow", "inputs", request.taskId);
    const outputDirectory = path.join(downloadsDirectory, "MingWant Studio", request.taskId);

    let totalBytes = 0;
    const preparedFiles = [];
    for (const [index, reference] of request.references.entries()) {
        if (!reference.data) continue;
        const bytes = toUint8Array(reference.data);
        if (bytes.byteLength > MAX_REFERENCE_FILE_BYTES) throw new Error(`参考素材“${reference.name}”超过 256MB，未加入桌面任务`);
        totalBytes += bytes.byteLength;
        if (totalBytes > MAX_REFERENCE_TOTAL_BYTES) throw new Error("单个桌面任务的参考素材总量不能超过 768MB");
        const fileName = `${String(index + 1).padStart(2, "0")}-${safeFileName(reference.name, reference.mimeType)}`;
        preparedFiles.push({ fileName, bytes });
    }

    // 全部素材通过大小与格式校验后再落盘，避免失败任务留下半套输入文件。
    await Promise.all([mkdir(inputDirectory, { recursive: true }), mkdir(outputDirectory, { recursive: true })]);
    for (const file of preparedFiles) await writeFile(path.join(inputDirectory, file.fileName), file.bytes);
    const writtenFiles = preparedFiles.map((file) => file.fileName);
    const links = request.references.map((reference) => reference.sourceUrl).filter(Boolean);
    if (links.length) await writeFile(path.join(inputDirectory, "参考链接.txt"), `${links.join("\n")}\n`, "utf8");
    await writeFile(path.join(inputDirectory, "生成说明.txt"), taskInstructions(request, writtenFiles, links), "utf8");

    return {
        taskId: request.taskId,
        projectId: request.projectId,
        nodeId: request.nodeId,
        provider: request.provider,
        title: request.title,
        prompt: request.prompt,
        settings: request.settings,
        createdAt: new Date().toISOString(),
        status: "waiting",
        accountId: null,
        inputDirectory,
        outputDirectory,
        referenceFileCount: writtenFiles.length,
        referenceLinkCount: links.length,
        download: null,
        downloadError: "",
    };
}

export function isLikelyVideoDownload(item) {
    const mimeType = String(item.getMimeType?.() || "").toLowerCase();
    const extension = path.extname(String(item.getFilename?.() || "")).toLowerCase();
    return mimeType.startsWith("video/") || VIDEO_EXTENSIONS.has(extension);
}

export function nextDownloadPath(directory, rawFileName, reportedMimeType) {
    const candidateName = safeFileName(rawFileName || "video.mp4", "video/mp4");
    const candidateExtension = path.extname(candidateName).toLowerCase();
    const fileName = VIDEO_EXTENSIONS.has(candidateExtension) ? candidateName : `${path.parse(candidateName).name}${videoExtensionForMime(reportedMimeType)}`;
    const parsed = path.parse(fileName);
    let candidate = path.join(directory, fileName);
    let index = 2;
    while (existsSync(candidate)) {
        candidate = path.join(directory, `${parsed.name}-${index}${parsed.ext}`);
        index += 1;
    }
    return candidate;
}

export async function inspectCompletedVideo(filePath) {
    const details = await stat(filePath);
    if (!details.isFile() || details.size <= 0) throw new Error("下载的视频文件为空或已丢失");
    if (details.size > MAX_RESULT_BYTES) throw new Error("下载的视频超过 512MB，当前桌面回填通道无法安全读取");
    return { size: details.size };
}

export async function readCompletedVideo(filePath) {
    const details = await inspectCompletedVideo(filePath);
    return { bytes: await readFile(filePath), size: details.size };
}

function normalizeTaskRequest(value) {
    if (!value || typeof value !== "object") throw new Error("桌面视频任务格式无效");
    const taskId = requiredString(value.taskId, 64, "任务 ID");
    if (!/^[0-9a-f-]{36}$/i.test(taskId)) throw new Error("桌面视频任务 ID 无效");
    const prompt = requiredString(value.prompt, MAX_PROMPT_LENGTH, "视频提示词");
    if (Array.isArray(value.references) && value.references.length > MAX_REFERENCE_FILES) throw new Error(`单个桌面任务最多携带 ${MAX_REFERENCE_FILES} 个参考素材`);
    const references = Array.isArray(value.references) ? value.references.map(normalizeReference) : [];
    return {
        taskId,
        projectId: requiredString(value.projectId, 160, "画布 ID"),
        nodeId: requiredString(value.nodeId, 160, "节点 ID"),
        provider: normalizeVideoProvider(value.provider),
        title: optionalString(value.title, 80) || "画布视频任务",
        prompt,
        settings: normalizeSettings(value.settings),
        references,
    };
}

function normalizeReference(value, index) {
    if (!value || typeof value !== "object") throw new Error(`第 ${index + 1} 个参考素材格式无效`);
    const mimeType = optionalString(value.mimeType, 120) || "application/octet-stream";
    const sourceUrl = optionalString(value.sourceUrl, 4_096);
    if (sourceUrl && !/^https?:\/\//i.test(sourceUrl)) throw new Error(`参考素材“${value.name || index + 1}”的外部链接不是 HTTP(S)`);
    return {
        name: optionalString(value.name, 180) || `reference-${index + 1}${extensionForMime(mimeType)}`,
        mimeType,
        data: value.data ?? null,
        sourceUrl,
    };
}

function normalizeSettings(value) {
    const settings = value && typeof value === "object" ? value : {};
    return {
        durationSeconds: optionalString(settings.durationSeconds, 20),
        aspectRatio: optionalString(settings.aspectRatio, 32),
        resolution: optionalString(settings.resolution, 32),
        generateAudio: settings.generateAudio !== false,
        watermark: settings.watermark === true,
    };
}

function taskInstructions(request, files, links) {
    return [
        `任务：${request.title}`,
        `网页平台：${videoProvider(request.provider).name}`,
        `时长：${request.settings.durationSeconds || "未指定"}`,
        `画幅：${request.settings.aspectRatio || "未指定"}`,
        `清晰度：${request.settings.resolution || "未指定"}`,
        `声音：${request.settings.generateAudio ? "生成" : "不生成"}`,
        `水印：${request.settings.watermark ? "保留" : "关闭"}`,
        `本地参考素材：${files.length} 个`,
        `外部参考链接：${links.length} 个`,
        "",
        "提示词：",
        request.prompt,
        "",
        "请在网页中人工确认参数并提交。生成完成后下载视频，再回到工作台确认回填。",
        "",
    ].join("\n");
}

function toUint8Array(value) {
    if (value instanceof Uint8Array) return value;
    if (value instanceof ArrayBuffer) return new Uint8Array(value);
    if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    throw new Error("参考素材二进制数据无效");
}

function safeFileName(value, mimeType) {
    const fallbackExtension = extensionForMime(mimeType);
    const cleaned = String(value || "reference")
        .replace(/[<>:\"/\\|?*\u0000-\u001f]/g, "-")
        .replace(/[. ]+$/g, "")
        .trim()
        .slice(0, 160);
    const name = cleaned || `reference${fallbackExtension}`;
    return path.extname(name) ? name : `${name}${fallbackExtension}`;
}

function extensionForMime(mimeType) {
    const normalized = String(mimeType || "").toLowerCase();
    if (normalized.includes("png")) return ".png";
    if (normalized.includes("jpeg") || normalized.includes("jpg")) return ".jpg";
    if (normalized.includes("webp")) return ".webp";
    if (normalized.includes("webm")) return ".webm";
    if (normalized.includes("quicktime")) return ".mov";
    if (normalized.startsWith("video/")) return ".mp4";
    if (normalized.includes("wav")) return ".wav";
    if (normalized.startsWith("audio/")) return ".mp3";
    return ".bin";
}

function videoExtensionForMime(mimeType) {
    const normalized = String(mimeType || "").toLowerCase();
    if (normalized.includes("webm")) return ".webm";
    if (normalized.includes("quicktime")) return ".mov";
    if (normalized.includes("x-m4v")) return ".m4v";
    return ".mp4";
}

function requiredString(value, maxLength, label) {
    if (typeof value !== "string") throw new Error(`${label}不能为空`);
    const normalized = value.trim();
    if (!normalized) throw new Error(`${label}不能为空`);
    if (normalized.length > maxLength) throw new Error(`${label}超过 ${maxLength} 个字符`);
    return normalized;
}

function optionalString(value, maxLength) {
    return typeof value === "string" ? value.trim().slice(0, maxLength) : "";
}
