import type {
    StoryboardAssetBinding,
    StoryboardBoundary,
    StoryboardMotionAction,
    StoryboardMotionSpec,
    StoryboardRow,
    StoryboardSourceRef,
} from "@/types/canvas";

export const PROJECT_SHOT_DEFINITION_SCHEMA_V1 = "mingwant.short-drama.shot/v1";

export type ProjectShotDefinition = {
    schemaVersion: typeof PROJECT_SHOT_DEFINITION_SCHEMA_V1;
    shotCode: string;
    sourceRefs: StoryboardSourceRef[];
    purpose: string;
    informationChange: string;
    assetBindings: StoryboardAssetBinding[];
    startBoundary: StoryboardBoundary;
    endBoundary: StoryboardBoundary;
    motionSpec?: StoryboardMotionSpec;
    imagePrompt?: string;
    videoPrompt?: string;
    negativePrompt?: string;
};

export type ProjectShotDefinitionParseResult =
    | { definition: ProjectShotDefinition; warning?: undefined }
    | { definition?: undefined; warning: string };

export function parseProjectShotDefinition(raw: string | undefined): ProjectShotDefinitionParseResult {
    const text = raw?.trim() || "";
    if (!text || text === "{}") return degraded("项目镜头尚未包含结构化制作定义");

    let value: unknown;
    try {
        value = JSON.parse(text);
    } catch {
        return degraded("项目镜头的结构化定义不是有效 JSON");
    }
    if (!isRecord(value)) return degraded("项目镜头的结构化定义不是对象");
    if (stringValue(value.schemaVersion) !== PROJECT_SHOT_DEFINITION_SCHEMA_V1) return degraded("项目镜头的结构化定义版本不受支持");

    const shotCode = stringValue(value.shotCode);
    const purpose = stringValue(value.purpose);
    const informationChange = stringValue(value.informationChange);
    const sourceRefs = readSourceRefs(value.sourceRefs);
    const assetBindings = readAssetBindings(value.assetBindings);
    const startBoundary = readBoundary(value.startBoundary);
    const endBoundary = readBoundary(value.endBoundary);
    if (!shotCode || !purpose || !informationChange || !sourceRefs?.length || !assetBindings || !startBoundary || !endBoundary) {
        return degraded("项目镜头的结构化定义缺少编号、来源、目的、信息变化、资产绑定或起止边界");
    }

    const rawMotionSpec = value.motionSpec;
    let motionSpec: StoryboardMotionSpec | undefined;
    if (rawMotionSpec !== undefined && rawMotionSpec !== null) {
        const parsedMotionSpec = readMotionSpec(rawMotionSpec);
        if (!parsedMotionSpec) return degraded("项目镜头的视频运动说明格式无效");
        motionSpec = parsedMotionSpec;
    }

    return {
        definition: {
            schemaVersion: PROJECT_SHOT_DEFINITION_SCHEMA_V1,
            shotCode,
            sourceRefs,
            purpose,
            informationChange,
            assetBindings,
            startBoundary,
            endBoundary,
            motionSpec,
            imagePrompt: optionalString(value.imagePrompt),
            videoPrompt: optionalString(value.videoPrompt),
            negativePrompt: optionalString(value.negativePrompt),
        },
    };
}

export function projectShotDefinitionRowPatch(definition: ProjectShotDefinition): Partial<StoryboardRow> {
    return {
        shotCode: definition.shotCode,
        purpose: definition.purpose,
        informationChange: definition.informationChange,
        sourceRefs: definition.sourceRefs,
        assetBindings: definition.assetBindings,
        startBoundary: definition.startBoundary,
        endBoundary: definition.endBoundary,
        motionSpec: definition.motionSpec,
        camera: definition.motionSpec?.camera || "",
        motion: definition.motionSpec ? motionSpecActionSummary(definition.motionSpec) : "",
        timeBeats: definition.motionSpec?.timingPlan || "",
        imageGenerationPrompt: definition.imagePrompt || "",
        videoMotionPrompt: definition.videoPrompt || "",
        negativePrompt: definition.negativePrompt || "",
    };
}

export function projectShotGenerationBlockReason(row: Pick<StoryboardRow, "domainShotId" | "contractWarning">) {
    const warning = row.contractWarning?.trim();
    return row.domainShotId && warning ? warning : undefined;
}

export function storyboardRowGenerationContract(row: StoryboardRow, mode: "image" | "video" | "all") {
    const common = [
        row.domainShotId && `项目镜头 ID：${row.domainShotId}`,
        row.shotCode && `镜头编号：${row.shotCode}`,
        row.purpose && `观看目的：${row.purpose}`,
        mode !== "image" && row.informationChange && `信息变化：${row.informationChange}`,
        row.sourceRefs?.length && `来源：${row.sourceRefs.map(formatSourceRef).join("；")}`,
        row.assetBindings?.length && `资产版本：${row.assetBindings.map((binding) => `${binding.role}=${binding.assetVersionId}`).join("；")}`,
    ].filter(Boolean);
    const parts = common.length ? [`【镜头事实】\n${common.join("\n")}`] : [];

    if ((mode === "image" || mode === "all") && row.startBoundary) {
        parts.push(`【冻结画面边界】\n${formatBoundary(row.startBoundary)}`);
        parts.push("图片只表现上述单一开始瞬间，不描述动作过程、结束状态或时间推进。");
    }
    if (mode === "video" || mode === "all") {
        if (row.startBoundary) parts.push(`【开始边界】\n${formatBoundary(row.startBoundary)}`);
        if (row.motionSpec) parts.push(`【有序运动说明】\n${formatMotionSpec(row.motionSpec)}`);
        if (row.endBoundary) parts.push(`【结束边界】\n${formatBoundary(row.endBoundary)}`);
        if (row.startBoundary || row.endBoundary) parts.push("视频只能实现开始边界到结束边界的变化，不得改写身份、持物、空间关系或最终状态。");
    }
    return parts.join("\n\n");
}

function readSourceRefs(value: unknown): StoryboardSourceRef[] | null {
    if (!Array.isArray(value)) return null;
    const refs = value.map((item) => {
        if (!isRecord(item)) return null;
        const unitId = stringValue(item.unitId);
        const role = stringValue(item.role);
        if (!unitId || !role) return null;
        return { unitId, role, blockId: optionalString(item.blockId), unitUpdatedAt: optionalString(item.unitUpdatedAt) } satisfies StoryboardSourceRef;
    });
    return refs.some((item) => !item) ? null : refs as StoryboardSourceRef[];
}

function readAssetBindings(value: unknown): StoryboardAssetBinding[] | null {
    if (value === undefined || value === null) return [];
    if (!Array.isArray(value)) return null;
    const bindings = value.map((item) => {
        if (!isRecord(item)) return null;
        const assetVersionId = stringValue(item.assetVersionId);
        const role = stringValue(item.role);
        return assetVersionId && role ? { assetVersionId, role } satisfies StoryboardAssetBinding : null;
    });
    return bindings.some((item) => !item) ? null : bindings as StoryboardAssetBinding[];
}

function readBoundary(value: unknown): StoryboardBoundary | null {
    if (!isRecord(value)) return null;
    const boundary = {
        positions: stringList(value.positions),
        facing: stringList(value.facing),
        gaze: stringList(value.gaze),
        hands: stringList(value.hands),
        heldProps: stringList(value.heldProps),
        visibleState: stringList(value.visibleState),
    };
    return boundary.positions.length ? boundary : null;
}

function readMotionSpec(value: unknown): StoryboardMotionSpec | null {
    if (!isRecord(value)) return null;
    const startAnchor = stringValue(value.startAnchor);
    const performanceArc = stringValue(value.performanceArc);
    const camera = stringValue(value.camera);
    const timingPlan = stringValue(value.timingPlan);
    const endReport = stringValue(value.endReport);
    if (!startAnchor || !performanceArc || !camera || !timingPlan || !endReport || !Array.isArray(value.orderedSubjectMotion)) return null;
    const actions = value.orderedSubjectMotion.map(readMotionAction);
    if (actions.some((item, index) => !item || item.order !== index + 1)) return null;
    return {
        startAnchor,
        orderedSubjectMotion: actions as StoryboardMotionAction[],
        performanceArc,
        camera,
        environmentAndAudio: stringList(value.environmentAndAudio),
        timingPlan,
        endReport,
    };
}

function readMotionAction(value: unknown): StoryboardMotionAction | null {
    if (!isRecord(value)) return null;
    const order = typeof value.order === "number" && Number.isInteger(value.order) ? value.order : 0;
    const actor = stringValue(value.actor);
    const action = stringValue(value.action);
    const result = stringValue(value.result);
    return order > 0 && actor && action && result ? {
        order,
        actor,
        trigger: optionalString(value.trigger),
        action,
        pathOrContact: optionalString(value.pathOrContact),
        result,
    } : null;
}

function motionSpecActionSummary(spec: StoryboardMotionSpec) {
    return spec.orderedSubjectMotion.map((item) => [
        `${item.order}. ${item.actor}`,
        item.trigger && `触发：${item.trigger}`,
        `动作：${item.action}`,
        item.pathOrContact && `路径/接触：${item.pathOrContact}`,
        `结果：${item.result}`,
    ].filter(Boolean).join("，")).join("；");
}

function formatMotionSpec(spec: StoryboardMotionSpec) {
    return [
        `起点锚定：${spec.startAnchor}`,
        spec.orderedSubjectMotion.length && `主体动作：${motionSpecActionSummary(spec)}`,
        `表演弧线：${spec.performanceArc}`,
        `摄影机：${spec.camera}`,
        spec.environmentAndAudio.length && `环境与声音：${spec.environmentAndAudio.join("；")}`,
        `时间分配：${spec.timingPlan}`,
        `终点核对：${spec.endReport}`,
    ].filter(Boolean).join("\n");
}

function formatBoundary(boundary: StoryboardBoundary) {
    return [
        boundary.positions.length && `位置：${boundary.positions.join("；")}`,
        boundary.facing.length && `朝向：${boundary.facing.join("；")}`,
        boundary.gaze.length && `目光：${boundary.gaze.join("；")}`,
        boundary.hands.length && `双手：${boundary.hands.join("；")}`,
        boundary.heldProps.length && `持物：${boundary.heldProps.join("；")}`,
        boundary.visibleState.length && `可见状态：${boundary.visibleState.join("；")}`,
    ].filter(Boolean).join("\n");
}

function formatSourceRef(source: StoryboardSourceRef) {
    return [source.unitId, source.blockId, source.role, source.unitUpdatedAt].filter(Boolean).join("/");
}

function degraded(reason: string): ProjectShotDefinitionParseResult {
    return { warning: `${reason}，已按非结构化镜头导入；生成前请补齐项目定义。` };
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function stringValue(value: unknown) {
    return typeof value === "string" ? value.trim() : "";
}

function optionalString(value: unknown) {
    return stringValue(value) || undefined;
}

function stringList(value: unknown) {
    return Array.isArray(value) ? value.map(stringValue).filter(Boolean) : [];
}
