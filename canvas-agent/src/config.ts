import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export const DEFAULT_PORT = 17371;
export const CONFIG_DIR = path.join(os.homedir(), ".infinite-canvas");
export const CONFIG_FILE = path.join(CONFIG_DIR, "canvas-agent.json");
export const VERSION = readPackageVersion();
export const AGENT_PROMPT = [
    "你正在帮助用户操作明想 MingWant Studio 网页画布。需要改动画布时优先使用已配置的 infinite-canvas MCP 工具：先 canvas_get_state 读取当前画布，再根据任务使用 canvas_create_text_node、canvas_generate_text、canvas_generate_image、canvas_generate_video、canvas_generate_audio、canvas_create_generation_flow、canvas_create_config_node、canvas_run_generation、canvas_update_node、canvas_connect_nodes 等通用工具；复杂批量改动再用 canvas_apply_ops，删除连线可用 delete_connections。",
    "当前画布关联制作项目时先用 project_get_context 读取项目事实；涉及具体章节正文时再用 project_get_unit 读取完整原文，然后用 project_create_unit、project_update_unit、project_create_or_update_shots、project_link_shot_asset 和工作流工具保存业务结果，不能把章节摘要、对话记忆或画布节点当成项目事实来源。",
    "短剧分镜由你直接构造 project_create_or_update_shots 的结构化工具参数，不要先调用 canvas_generate_text 让另一个模型返回 JSON，也不要让用户手工复制 JSON。长分镜每批保存 3 到 5 镜；参数校验失败时按错误中的字段路径修正当前批次，若提示此前已有镜头保存则先重新读取项目上下文再继续。",
    "图片节点带有 imageAnnotation 时先用 canvas_get_image_annotations 查看修改指令和标注图，再调用 canvas_edit_image_annotation；彩色标记只用于理解区域，不能作为结果内容。需要生成内容时直接调用对应生成工具，不要绑定特定业务场景。电商内容必须服从肖像授权、商品事实和广告宣称边界，不要模拟鼠标点击。",
].join("\n");

export type CanvasWorkspaceConfig = { workspacePath: string; activeThreadId?: string; pinnedThreadIds?: string[] };
export type CanvasAgentConfig = { url: string; token: string; origins?: string[]; canvases?: Record<string, CanvasWorkspaceConfig> };

export function loadConfig(create = false): CanvasAgentConfig {
    try {
        return JSON.parse(fs.readFileSync(CONFIG_FILE, "utf8")) as CanvasAgentConfig;
    } catch {
        const config = { url: `http://127.0.0.1:${Number(process.env.PORT) || DEFAULT_PORT}`, token: crypto.randomBytes(18).toString("hex") };
        if (create) saveConfig(config);
        return config;
    }
}

export function saveConfig(config: CanvasAgentConfig) {
    fs.mkdirSync(CONFIG_DIR, { recursive: true });
    fs.writeFileSync(CONFIG_FILE, JSON.stringify(config, null, 2));
}

export function ensureCanvasWorkspace(config: CanvasAgentConfig, canvasId: string) {
    const id = safeSegment(canvasId || "default");
    config.canvases ||= {};
    const current = config.canvases[id];
    if (current?.workspacePath) {
        fs.mkdirSync(resolveWorkspacePath(current.workspacePath), { recursive: true });
        return { canvasId: id, ...current, workspacePath: resolveWorkspacePath(current.workspacePath) };
    }
    const workspacePath = path.join(CONFIG_DIR, "codex-workspaces", id);
    config.canvases[id] = { workspacePath };
    fs.mkdirSync(workspacePath, { recursive: true });
    saveConfig(config);
    return { canvasId: id, workspacePath };
}

export function updateCanvasWorkspace(config: CanvasAgentConfig, canvasId: string, patch: Partial<CanvasWorkspaceConfig>) {
    const current = ensureCanvasWorkspace(config, canvasId);
    const workspacePath = patch.workspacePath ? resolveWorkspacePath(patch.workspacePath) : current.workspacePath;
    const next = { ...current, ...patch, workspacePath };
    config.canvases ||= {};
    config.canvases[current.canvasId] = { workspacePath: next.workspacePath, activeThreadId: next.activeThreadId, pinnedThreadIds: next.pinnedThreadIds };
    fs.mkdirSync(workspacePath, { recursive: true });
    saveConfig(config);
    return { canvasId: current.canvasId, ...config.canvases[current.canvasId] };
}

function resolveWorkspacePath(value: string) {
    if (value === "~") return os.homedir();
    if (value.startsWith("~/")) return path.join(os.homedir(), value.slice(2));
    return path.resolve(value);
}

function safeSegment(value: string) {
    return value.replace(/[^a-zA-Z0-9._-]/g, "-").slice(0, 120) || "default";
}

function readPackageVersion() {
    try {
        const pkg = JSON.parse(fs.readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { version?: string };
        return pkg.version || "0.0.0";
    } catch {
        return "0.0.0";
    }
}
