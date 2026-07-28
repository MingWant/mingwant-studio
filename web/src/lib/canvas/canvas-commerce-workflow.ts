import { nanoid } from "nanoid";

import { createCanvasNode, createStoryboardRow } from "@/lib/canvas/canvas-project-domain";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type CanvasWorkflowKind, type StoryboardRow } from "@/types/canvas";

type CreativeFormat = {
    code: string;
    title: string;
    hook: string;
    scene: string;
};

type SellingPoint = {
    code: string;
    label: string;
};

const creativeFormats: CreativeFormat[] = [
    { code: "A", title: "痛点反转", hook: "先抛出真实使用痛点，再用商品完成反转", scene: "强钩子近景 + 使用前后情绪变化" },
    { code: "B", title: "艺人体验", hook: "以第一人称真实体验建立信任", scene: "自然口播 + 生活化实测" },
    { code: "C", title: "场景种草", hook: "把商品放进高频生活场景", scene: "情境表演 + 商品特写" },
    { code: "D", title: "对比证明", hook: "用可核验的对比展示差异", scene: "同机位对比 + 细节证据" },
    { code: "E", title: "限时转化", hook: "先给购买理由，再明确行动指令", scene: "利益点口播 + 清晰 CTA" },
];

const sellingPoints: SellingPoint[] = [
    { code: "1", label: "卖点 1｜核心功能" },
    { code: "2", label: "卖点 2｜使用体验" },
    { code: "3", label: "卖点 3｜价格/权益" },
];

const negativePrompt = "避免换脸漂移、五官变形、额外手指、商品结构变化、Logo 与包装文字乱码、口型错位、肢体穿模、虚假功效暗示、未授权人物或场景。";

export function createCommerceWorkflowTemplate() {
    const frames = [
        createFrame("01 · 项目输入与授权边界", 0, 0, 2300, 1050),
        createFrame("02 · 5 × 3 创意矩阵", 2500, 0, 1500, 1050),
        createFrame("03 · 15 条成片分镜（每条约 30 秒）", 4200, 0, 3040, 2470),
        createFrame("04 · 首尾帧、分段生成与 15 条成片", 7460, 0, 2360, 2470),
        createFrame("05 · 质量门禁与交付", 10040, 0, 1840, 1050),
    ];
    const [inputFrame, matrixFrame, scriptsFrame, generationFrame, deliveryFrame] = frames;

    const briefNode = createTextNode(
        "项目 Brief",
        40,
        80,
        520,
        360,
        [
            "# 项目 Brief",
            "- 品牌 / 商品：[待填写]",
            "- 目标人群：[待填写]",
            "- 平台：抖音 / 视频号 / 小红书 / 其他",
            "- 目标：种草 / 点击 / 成交 / 直播预热",
            "- 画幅：9:16，单条约 30 秒",
            "- 交付：15 条成片 + 字幕稿 + 封面建议 + 生成记录",
            "- 禁用表达：[待填写]",
        ].join("\n"),
        "commerce_brief",
        inputFrame.id,
    );
    const rightsNode = createTextNode(
        "肖像授权与投放限制",
        590,
        80,
        520,
        360,
        [
            "# 肖像授权 / 合规边界",
            "- 授权主体与素材清单：[待填写]",
            "- 可投品牌 / 类目：[待填写]",
            "- 渠道、地区、期限：[待填写]",
            "- 可否生成新动作 / 新口播：[待填写]",
            "- 禁止场景、服装、竞品与敏感话题：[待填写]",
            "- 审批责任人：[待填写]",
            "\n生成前必须完成本节点；所有分镜与成片都要回查这些限制。",
        ].join("\n"),
        "commerce_rights",
        inputFrame.id,
    );
    const productNode = createTextNode(
        "商品事实与卖点证据",
        1140,
        80,
        520,
        360,
        [
            "# 商品准确度参考",
            "- 标准商品名 / SKU：[待填写]",
            "- 包装、Logo、颜色与规格：[待填写]",
            "- 卖点 1（有证据）：[待填写]",
            "- 卖点 2（有证据）：[待填写]",
            "- 卖点 3（有证据）：[待填写]",
            "- 价格、优惠、赠品及有效期：[待填写]",
            "- 不能宣称的功效：[待填写]",
            "\n只使用品牌确认过的事实，不让模型自行补全参数或功效。",
        ].join("\n"),
        "commerce_product",
        inputFrame.id,
    );
    const identityNode = createTextNode(
        "艺人身份锚点",
        1690,
        80,
        520,
        360,
        [
            "# 身份一致性锚点",
            "从 5 条授权视频中人工确认并填写：",
            "- 稳定五官、脸型、肤色与发型特征",
            "- 常用表情、视线、手势和站姿",
            "- 服装、饰品及可接受变化范围",
            "- 声线、语速、停顿与口头表达习惯",
            "- 不可复刻或不可生成的动作",
            "\n优先采用参考图 + 首尾帧方案；仅在模型能力和授权边界明确时使用数字分身。",
        ].join("\n"),
        "commerce_identity",
        inputFrame.id,
    );
    const sourceVideos = Array.from({ length: 5 }, (_, index) => {
        const node = createMediaPlaceholder(
            CanvasNodeType.Video,
            `真人授权素材 ${index + 1}`,
            40 + index * 440,
            520,
            400,
            225,
            "commerce_identity",
            inputFrame.id,
        );
        node.metadata = {
            ...node.metadata,
            prompt: `拖入客户提供的第 ${index + 1} 条约 1 分钟真人出镜视频。建议覆盖不同角度、表情、口型、手势和商品互动。`,
        };
        return node;
    });
    const inputGuideNode = createTextNode(
        "素材接入说明",
        40,
        800,
        2160,
        160,
        "把 5 条客户素材分别拖到上方视频占位节点；优先截取清晰正脸、侧脸、半身、手持商品和自然口播画面作为身份与商品参考。不要把未经授权的公开网络素材混入参考集。",
        "commerce_identity",
        inputFrame.id,
    );

    const matrixNode = createTextNode(
        "15 条创意矩阵",
        2540,
        80,
        1420,
        880,
        [
            "# 5 种创意形式 × 3 个真实卖点 = 15 条",
            "",
            "| 形式 | 卖点 1 | 卖点 2 | 卖点 3 |",
            "| --- | --- | --- | --- |",
            ...creativeFormats.map((format) => `| ${format.code} ${format.title} | ${format.code}1 | ${format.code}2 | ${format.code}3 |`),
            "",
            "## 共通结构",
            "1. 0–6 秒：画面和一句话同时给钩子，并建立真实问题",
            "2. 6–12 秒：商品出现，锁定包装与使用场景",
            "3. 12–18 秒：演示卖点，不跳步、不穿模",
            "4. 18–24 秒：只展示可核验证据或真实体验",
            "5. 24–30 秒：品牌确认过的权益与行动指令",
            "",
            "按 5 个 6 秒镜头分别生成，再按脚本顺序合并为约 30 秒成片；不要假设单个视频模型支持一次直出 30 秒。",
            "正式生成前，把所有方括号占位内容替换成品牌确认过的事实、台词和权益。",
            "每条内容必须有不同的开场、场景或叙事，不只替换一句口播。",
        ].join("\n"),
        "commerce_matrix",
        matrixFrame.id,
    );

    const scriptNodes: CanvasNodeData[] = [];
    const outputNodes: CanvasNodeData[] = [];
    creativeFormats.forEach((format, rowIndex) => {
        sellingPoints.forEach((point, columnIndex) => {
            const itemNumber = rowIndex * sellingPoints.length + columnIndex + 1;
            const code = `${format.code}${point.code}`;
            const scriptNode = createScriptNode(
                itemNumber,
                code,
                format,
                point,
                4240 + columnIndex * 990,
                80 + rowIndex * 465,
                scriptsFrame.id,
                [sourceVideos[rowIndex].id, identityNode.id, productNode.id, rightsNode.id],
            );
            scriptNodes.push(scriptNode);

            const outputNode = createMediaPlaceholder(
                CanvasNodeType.Video,
                `成片槽位 ${String(itemNumber).padStart(2, "0")} · ${code}`,
                7500 + columnIndex * 460,
                320 + rowIndex * 430,
                240,
                426,
                "commerce_generation",
                generationFrame.id,
            );
            outputNode.metadata = {
                ...outputNode.metadata,
                generationMode: "video",
                videoEditOperation: "concat",
                size: "720x1280",
                workflowDescription: "5 段 × 6 秒，按脚本顺序合并",
                prompt: `脚本 ${code} 的最终成片槽位。先从分镜表创建并生成 5 个 6 秒镜头，人物身份、商品包装、Logo、字幕和空间关系保持稳定；再同时选择镜头 1–5 和本空白槽位，点击“合并选中视频”，结果会直接填入本槽位。禁止添加未确认的价格、参数或功效。`,
            };
            outputNodes.push(outputNode);
        });
    });

    const keyframeGuideNode = createTextNode(
        "首尾帧批量策略",
        7500,
        80,
        1340,
        180,
        "先为每条脚本锁定专属总首帧和总尾帧，再按分镜行的生图提示词生成 5 张镜头锚点，并用参考图生成 5 个 6 秒视频。首帧负责钩子、身份与场景；尾帧负责商品外观、品牌信息和 CTA。复制右侧两个母版节点，为 15 条脚本分别生成并连接，不共用可能导致场景冲突的成片帧；最后同时选中按镜头顺序排列的 5 段视频与对应空白成片槽位，点击“合并选中视频”直接回填。",
        "commerce_keyframe",
        generationFrame.id,
    );
    const startFrameNode = createMediaPlaceholder(CanvasNodeType.Image, "首帧母版｜复制为每条脚本生成", 8880, 80, 260, 462, "commerce_keyframe", generationFrame.id);
    startFrameNode.metadata = {
        ...startFrameNode.metadata,
        generationMode: "image",
        size: "1024x1792",
        prompt: "根据所连接脚本生成 9:16 写实广告首帧：授权艺人身份准确、表情自然、3 秒钩子一眼可读、商品外观与品牌参考一致，保留字幕安全区。",
    };
    const endFrameNode = createMediaPlaceholder(CanvasNodeType.Image, "尾帧母版｜复制为每条脚本生成", 9180, 80, 260, 462, "commerce_keyframe", generationFrame.id);
    endFrameNode.metadata = {
        ...endFrameNode.metadata,
        generationMode: "image",
        size: "1024x1792",
        prompt: "根据所连接脚本生成 9:16 写实广告尾帧：商品包装、Logo、规格与品牌参考一致，艺人姿态自然，CTA 和权益仅使用已确认信息，保留平台按钮安全区。",
    };

    const qualityNode = createTextNode(
        "逐条质量门禁",
        10080,
        80,
        820,
        840,
        [
            "# 成片 QA（15 条逐条勾选）",
            "## P0｜任何一项失败必须重做",
            "- [ ] 肖像授权、品牌、渠道、期限与内容范围合规",
            "- [ ] 五官、脸型、发型、服装和声线身份一致",
            "- [ ] 商品形状、颜色、包装、Logo、规格与价格准确",
            "- [ ] 无虚假功效、绝对化用语或模型自编信息",
            "",
            "## P1｜影响真实感与转化",
            "- [ ] 口型、语气、表情和动作同步自然",
            "- [ ] 手指、抓握、接触、光影、反射与遮挡合理",
            "- [ ] 前 3 秒有钩子，5 秒内出现商品或明确利益点",
            "- [ ] 画面、字幕和口播共同服务同一个卖点",
            "- [ ] CTA 清楚但不过度承诺",
            "",
            "## P2｜交付一致性",
            "- [ ] 9:16、约 30 秒、无水印、无黑帧、音量达标",
            "- [ ] 15 条之间开场、场景和表达有真实差异",
            "- [ ] 已记录模型、参数、素材来源和审核结论",
        ].join("\n"),
        "commerce_quality",
        deliveryFrame.id,
    );
    const deliveryNode = createTextNode(
        "交付清单",
        10940,
        80,
        880,
        840,
        [
            "# 批次交付",
            "- [ ] 15 条 MP4 成片（统一命名 01–15）",
            "- [ ] 15 张封面 / 首帧",
            "- [ ] 15 份口播与字幕文本",
            "- [ ] 创意矩阵与每条对应卖点",
            "- [ ] 商品事实、权益版本与有效期快照",
            "- [ ] 肖像授权适用范围记录",
            "- [ ] 模型 / Provider / 参数 / 成本记录",
            "- [ ] QA 表、问题项、重做记录与最终审批人",
            "",
            "## 建议文件名",
            "`品牌_商品_艺人_创意编号_版本_日期.mp4`",
            "",
            "## 上线前",
            "由品牌方复核价格、权益、功效措辞和平台合规；任何临时活动信息变更都应重新导出对应版本。",
        ].join("\n"),
        "commerce_delivery",
        deliveryFrame.id,
    );

    const connections: CanvasConnection[] = [];
    [briefNode, rightsNode, productNode, identityNode, ...sourceVideos].forEach((node) => connect(connections, node, matrixNode));
    sourceVideos.forEach((node) => connect(connections, node, identityNode));
    scriptNodes.forEach((node, index) => {
        connect(connections, matrixNode, node);
        connect(connections, node, outputNodes[index]);
    });
    connect(connections, matrixNode, keyframeGuideNode);
    connect(connections, keyframeGuideNode, startFrameNode);
    connect(connections, keyframeGuideNode, endFrameNode);
    outputNodes.forEach((node) => connect(connections, node, qualityNode));
    connect(connections, qualityNode, deliveryNode);

    return {
        nodes: [
            ...frames,
            briefNode,
            rightsNode,
            productNode,
            identityNode,
            ...sourceVideos,
            inputGuideNode,
            matrixNode,
            ...scriptNodes,
            keyframeGuideNode,
            startFrameNode,
            endFrameNode,
            ...outputNodes,
            qualityNode,
            deliveryNode,
        ],
        connections,
        // 首屏只聚焦可立即填写的输入阶段，避免把 11880px 宽的完整流程缩成不可读缩略图。
        viewport: { x: 60, y: 100, k: 0.5 },
    };
}

function createScriptNode(
    itemNumber: number,
    code: string,
    format: CreativeFormat,
    point: SellingPoint,
    x: number,
    y: number,
    parentId: string,
    referenceNodeIds: string[],
) {
    const rows = createCommerceStoryboardRows(format, point, referenceNodeIds);
    const node = createCanvasNode(CanvasNodeType.Script, { x: x + 460, y: y + 200 }, {
        status: "idle",
        workflowKind: "commerce_script",
        workflowTitle: `${code} · ${format.title} × ${point.label}`,
        workflowDescription: "5 镜头 × 6 秒 / 约 30 秒 / 首尾帧优先",
        storyboardShotDuration: "auto",
        storyboardShotCount: "5",
        storyboard: {
            rows,
            visibleColumns: ["shotNumber", "durationSeconds", "plotDescription", "dialogue", "shotSize", "camera", "motion", "imageGenerationPrompt", "videoMotionPrompt", "negativePrompt"],
            referenceNodeIds,
        },
    });
    node.title = `${String(itemNumber).padStart(2, "0")} · ${code} ${format.title} × ${point.label}`;
    node.position = { x, y };
    node.width = 920;
    node.height = 400;
    node.parentId = parentId;
    return node;
}

function createCommerceStoryboardRows(format: CreativeFormat, point: SellingPoint, referenceNodeIds: string[]): StoryboardRow[] {
    const rowInputs = [
        { durationSeconds: 6, plotDescription: `首帧钩子：${format.hook}。艺人正面近景，在真实场景中第一眼交代冲突或利益点。`, dialogue: `[钩子] 你有没有遇到过 [真实痛点]？我在 [使用场景] 也很在意。`, shotSize: "近景", camera: "固定或轻微推进", motion: "自然抬眼并进入口播" },
        { durationSeconds: 6, plotDescription: `商品首次清晰出现；结合 ${format.scene}，包装、Logo、颜色和规格必须与商品参考一致，并引出 ${point.label}。`, dialogue: `[商品名] 这次让我注意到的是 [已确认的${point.label}]。`, shotSize: "中近景 + 商品特写", camera: "稳定跟拍后缓慢推近", motion: "自然手持商品，抓握与遮挡真实" },
        { durationSeconds: 6, plotDescription: `演示 ${point.label} 的使用过程。动作按真实步骤连续发生，不用无法验证的视觉奇观。`, dialogue: `[演示] 你看，按照 [真实步骤] 使用，就能看到 [可核验结果]。`, shotSize: "半身 + 细节特写", camera: "主机位与插入镜头", motion: "完整演示一次，不跳步" },
        { durationSeconds: 6, plotDescription: "给出可核验证据、真实主观体验或适用边界；不得虚构检测、销量、排名和医疗功效。", dialogue: `[证据] 对我来说最明显的是 [真实体验]，但 [适用边界/注意事项] 也要看清。`, shotSize: "中景", camera: "轻微横移", motion: "自然点头并展示细节" },
        { durationSeconds: 6, plotDescription: "尾帧锁定商品与品牌信息，使用已确认的价格/权益/活动期限，给出单一明确 CTA。", dialogue: `[CTA] 想解决 [需求]，可以先了解 [已确认权益]，活动以页面信息为准。`, shotSize: "商品 + 人物同框", camera: "固定收尾", motion: "自然指向商品或页面方向" },
    ];
    return rowInputs.map((row, index) => createStoryboardRow(index + 1, {
        ...row,
        lightingAndAtmosphere: "真实商业摄影，自然肤色，光向与商品反射一致",
        audioEffects: index === 0 ? "节奏明确但不压口播的开场音效" : "轻量环境声与低音量背景音乐",
        timeBeats: `${rowInputs.slice(0, index).reduce((sum, item) => sum + item.durationSeconds, 0)}–${rowInputs.slice(0, index + 1).reduce((sum, item) => sum + item.durationSeconds, 0)} 秒`,
        imageGenerationPrompt: `${format.title}广告镜头，${row.plotDescription}，9:16 写实真人商业摄影，授权艺人身份与商品参考严格一致，字幕安全区清晰。`,
        videoMotionPrompt: `${row.plotDescription}；人物自然口播：“${row.dialogue}”；${row.motion}；${row.camera}；保持人物五官、商品包装、Logo、服装、光影和空间连续性。负面约束：${negativePrompt}`,
        negativePrompt,
        referenceNodeIds,
    }));
}

function createTextNode(title: string, x: number, y: number, width: number, height: number, content: string, workflowKind: CanvasWorkflowKind, parentId: string) {
    const node = createCanvasNode(CanvasNodeType.Text, { x: x + width / 2, y: y + height / 2 }, {
        content,
        status: "success",
        workflowKind,
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

function createMediaPlaceholder(type: CanvasNodeType.Image | CanvasNodeType.Video, title: string, x: number, y: number, width: number, height: number, workflowKind: CanvasWorkflowKind, parentId: string) {
    const node = createCanvasNode(type, { x: x + width / 2, y: y + height / 2 }, {
        content: "",
        status: "idle",
        workflowKind,
        workflowTitle: title,
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
