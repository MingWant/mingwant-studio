export const MINGWANT_PROMPT_CATEGORIES = ["AI助手", "二创", "分镜", "图片", "推理提示词", "视频", "角色提取", "转换"] as const;

export type MingWantPromptCategory = (typeof MINGWANT_PROMPT_CATEGORIES)[number];

export type MingWantPromptTemplate = {
    id: string;
    name: string;
    category: MingWantPromptCategory;
    categoryLabel: string;
    description: string;
    load: () => Promise<string>;
};

const categoryLabels: Record<MingWantPromptCategory, string> = {
    AI助手: "AI 助手",
    二创: "二次创作",
    分镜: "分镜生成",
    图片: "图片提示词",
    推理提示词: "推理提示词",
    视频: "视频提示词",
    角色提取: "角色提取",
    转换: "格式转换",
};

const rawPromptModules = import.meta.glob<string>("/src/content/mingwant-prompts/**/*.txt", {
    query: "?raw",
    import: "default",
});

const promptCache = new Map<string, string>();

export const mingwantPromptTemplates: MingWantPromptTemplate[] = Object.entries(rawPromptModules)
    .flatMap(([path, load]) => {
        const matched = path.match(/mingwant-prompts\/([^/]+)\/([^/]+)\.txt$/u);
        if (!matched) return [];
        const category = matched[1] as MingWantPromptCategory;
        if (!MINGWANT_PROMPT_CATEGORIES.includes(category)) return [];
        const name = matched[2];
        return [{
            id: `mingwant:${category}/${name}`,
            name,
            category,
            categoryLabel: categoryLabels[category],
            description: promptDescription(category, name),
            load,
        }];
    })
    .sort((left, right) => {
        const categoryOrder = MINGWANT_PROMPT_CATEGORIES.indexOf(left.category) - MINGWANT_PROMPT_CATEGORIES.indexOf(right.category);
        return categoryOrder || left.name.localeCompare(right.name, "zh-CN");
    });

export async function loadMingWantPrompt(template: MingWantPromptTemplate) {
    const cached = promptCache.get(template.id);
    if (cached !== undefined) return cached;
    const content = await template.load();
    if (typeof content !== "string" || !content.trim()) throw new Error(`提示词“${template.name}”内容为空`);
    promptCache.set(template.id, content);
    return content;
}

export function extractMingWantPromptVariables(content: string) {
    const variables = new Set<string>();
    for (const matched of content.matchAll(/\{\{\s*([^{}]+?)\s*\}\}/gu)) variables.add(matched[1].trim());
    return [...variables];
}

function promptDescription(category: MingWantPromptCategory, name: string) {
    if (category === "AI助手") return `用于“${name}”方向的策划、改写与文案增强。`;
    if (category === "二创") return "用于分析参考内容并完成结构化二次创作。";
    if (category === "分镜") return `用于“${name}”题材的镜头拆解与连续分镜生成。`;
    if (category === "图片") return `用于“${name}”题材的画面描述与图片提示词生成。`;
    if (category === "推理提示词") return `用于“${name}”模型或场景的提示词推导与执行约束。`;
    if (category === "视频") return `用于“${name}”题材的时序动作与视频提示词生成。`;
    if (category === "角色提取") return "用于从完整文案中提取角色、时期与视觉设定。";
    return `用于“${name}”的结构化内容转换。`;
}
