import { nanoid } from "nanoid";

import { createCanvasNode } from "@/lib/canvas/canvas-project-domain";
import { normalizeModelOptionValue, resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";

const RUNNINGHUB_TEST_PROMPT = [
    "subject_definitions:",
    "<Subject 1> 是 <Picture 1> 中的人物，保持面部、发型、服装和体型一致。",
    "<Picture 2> 提供人物补充角度和造型参考。",
    "<Picture 3> 提供场景、光线和色彩参考。",
    "",
    "summary:",
    "[reference generation] <Subject 1> 在 <Picture 3> 的场景中自然跳舞，动作连贯，人物身份稳定。",
    "",
    "retention_analysis:",
    "三张参考图中的人物身份、服装、场景结构和主要光线保持稳定。",
    "",
    "detailed_description:",
    "单一连续镜头，中景构图。人物自然起舞，手部、面部和身体运动协调，镜头轻微缓慢推进。",
    "",
    "overall_soundscape:",
    "自然环境声与轻快节奏，声音和动作同步。",
    "",
    "non_diegetic_music:",
    "轻量背景音乐。",
].join("\n");

/** 优先复用当前选择，其次选择账号模型目录里的第一条 RunningHub 视频模型。 */
export function findRunningHubWorkflowModel(config: AiConfig) {
    const candidates = Array.from(new Set([config.videoModel, ...config.videoModels, ...config.models].filter(Boolean)));
    for (const candidate of candidates) {
        const normalized = normalizeModelOptionValue(candidate, config.channels);
        if (!normalized) continue;
        const request = resolveModelRequestConfig(config, normalized);
        if (request.interfaceType === "runninghub-workflow") return normalized;
    }
    return "";
}

export function createRunningHubWorkflowTemplate(model: string) {
    const frame = createFrame("RunningHub · MiniMax H3 All-in-One 测试", 0, 0, 2400, 1040);
    const guide = createTextNode(
        "测试步骤",
        40,
        70,
        560,
        250,
        [
            "# RunningHub 安全测试",
            "1. 先把 3 张有效图片拖入三个图片槽位。",
            "2. 视频/音频分支只有在 RHWorkspace 启用并重新保存发布后才填写。",
            "3. 打开生成配置，确认模型仍是 RunningHub 工作流。",
            "4. 首次测试保持已验证的 480×640、5 秒、seed 0、24fps。",
            "5. 点击生成配置中的“生成”；不要直接做纯文本测试。",
        ].join("\n"),
        frame.id,
    );
    const prompt = createTextNode("MiniMax H3 测试提示词", 40, 350, 560, 560, RUNNINGHUB_TEST_PROMPT, frame.id);
    const images = [0, 1, 2].map((index) => createMediaNode(
        CanvasNodeType.Image,
        `参考图片 ${index + 1} · 当前模板必填`,
        660 + index * 350,
        90,
        310,
        230,
        frame.id,
        `拖入一张当前可访问的参考图片；对应 RunningHub LoadImage 节点 ${[16, 19, 22][index]}。`,
    ));
    const video = createMediaNode(
        CanvasNodeType.Video,
        "参考视频 1 · 分支启用后可填",
        660,
        390,
        500,
        282,
        frame.id,
        "对应 LoadVideo 节点 24；RHWorkspace 中节点 23–24 未启用时请保持为空。",
    );
    const audio = createMediaNode(
        CanvasNodeType.Audio,
        "参考音频 1 · 分支启用后可填",
        1200,
        390,
        510,
        150,
        frame.id,
        "对应 LoadAudio 节点 26；RHWorkspace 中节点 25–26 未启用时请保持为空。",
    );
    const config = createCanvasNode(CanvasNodeType.Config, { x: 2020, y: 390 }, {
        generationMode: "video",
        videoEditOperation: "workflow",
        model,
        size: "480x640",
        seconds: "5",
        vquality: "480",
        runningHubParameters: {
            "9:seed": "0",
            "11:fps": "24",
        },
        workflowKind: "reference_set",
        workflowTitle: "RunningHub All-in-One 生成配置",
        workflowDescription: "节点 12 产出后立即抢停，不等待第二轮流程自然结束",
    });
    config.title = "RunningHub · All-in-One 生成配置";
    config.position = { x: 1790, y: 180 };
    config.width = 460;
    config.height = 410;
    config.parentId = frame.id;
    const resultGuide = createTextNode(
        "生成与结果说明",
        1790,
        650,
        460,
        230,
        "素材和提示词会同时进入左侧生成配置。系统监听 SaveVideo 节点 12 并先保存 MP4；轻量节点 73 拆分首段结果后，节点 69 会用非 32 对齐尺寸按预期失败，从而在二采前终止。只有失败节点确认为 69 才会交付；节点 48 只是断点失效时的取消兜底。",
        frame.id,
    );

    const connections: CanvasConnection[] = [];
    [prompt, ...images, video, audio].forEach((node) => connect(connections, node, config));
    return {
        nodes: [frame, guide, prompt, ...images, video, audio, config, resultGuide],
        connections,
        viewport: { x: 60, y: 100, k: 0.62 },
    };
}

function createTextNode(title: string, x: number, y: number, width: number, height: number, content: string, parentId: string) {
    const node = createCanvasNode(CanvasNodeType.Text, { x: x + width / 2, y: y + height / 2 }, {
        content,
        status: "success",
        workflowKind: "reference_set",
        workflowTitle: title,
        fontSize: 14,
    });
    node.title = title;
    node.position = { x, y };
    node.width = width;
    node.height = height;
    node.parentId = parentId;
    return node;
}

function createMediaNode(type: CanvasNodeType.Image | CanvasNodeType.Video | CanvasNodeType.Audio, title: string, x: number, y: number, width: number, height: number, parentId: string, description: string) {
    const node = createCanvasNode(type, { x: x + width / 2, y: y + height / 2 }, {
        content: "",
        prompt: description,
        status: "idle",
        workflowKind: "reference_set",
        workflowTitle: title,
        workflowDescription: description,
    });
    node.title = title;
    node.position = { x, y };
    node.width = width;
    node.height = height;
    node.parentId = parentId;
    return node;
}

function createFrame(title: string, x: number, y: number, width: number, height: number) {
    const node = createCanvasNode(CanvasNodeType.Frame, { x: x + width / 2, y: y + height / 2 }, {
        frame: { collapsed: false, expandedWidth: width, expandedHeight: height },
    });
    node.title = title;
    node.position = { x, y };
    node.width = width;
    node.height = height;
    return node;
}

function connect(connections: CanvasConnection[], from: CanvasNodeData, to: CanvasNodeData) {
    connections.push({ id: nanoid(), fromNodeId: from.id, toNodeId: to.id });
}
