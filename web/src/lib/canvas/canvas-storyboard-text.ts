import { CanvasNodeType, type CanvasNodeData, type StoryboardBoundary, type StoryboardMotionSpec, type StoryboardRow } from "@/types/canvas";

const UNICODE_REPLACEMENT_CHARACTER = /\uFFFD+/g;

/**
 * 上游把 emoji 或多字节字符错误解码后只能留下 U+FFFD，原字符已经无法恢复。
 * 分镜交付层移除这类损坏标记，避免把“��”继续展示、编辑或带入下游提示词。
 */
export function sanitizeStoryboardText(value: unknown) {
    return typeof value === "string" ? value.replace(UNICODE_REPLACEMENT_CHARACTER, "") : "";
}

export function sanitizeStoryboardRows(rows: StoryboardRow[] | null | undefined) {
    if (!Array.isArray(rows)) return [];
    let changed = false;
    const sanitized = rows.flatMap((row) => {
        if (!row || typeof row !== "object") {
            changed = true;
            return [];
        }
        const textSanitized = sanitizeStoryboardValue(row) as StoryboardRow;
        const next = normalizeStoryboardRow(textSanitized);
        if (next !== row) changed = true;
        return [next];
    });
    return changed ? sanitized : rows;
}

export function sanitizeCanvasStoryboardNode(node: CanvasNodeData): CanvasNodeData {
    if (node.type !== CanvasNodeType.Script || !node.metadata?.storyboard) return node;
    const rows = sanitizeStoryboardRows(node.metadata.storyboard.rows);
    if (rows === node.metadata.storyboard.rows) return node;
    return {
        ...node,
        metadata: {
            ...node.metadata,
            storyboard: { ...node.metadata.storyboard, rows },
        },
    };
}

function sanitizeStoryboardValue(value: unknown): unknown {
    if (typeof value === "string") {
        const sanitized = sanitizeStoryboardText(value);
        return sanitized === value ? value : sanitized;
    }
    if (Array.isArray(value)) {
        let changed = false;
        const sanitized = value.map((item) => {
            const next = sanitizeStoryboardValue(item);
            if (next !== item) changed = true;
            return next;
        });
        return changed ? sanitized : value;
    }
    if (!value || typeof value !== "object") return value;
    const record = value as Record<string, unknown>;
    let changed = false;
    const sanitized: Record<string, unknown> = {};
    Object.entries(record).forEach(([key, item]) => {
        const next = sanitizeStoryboardValue(item);
        sanitized[key] = next;
        if (next !== item) changed = true;
    });
    return changed ? sanitized : value;
}

function normalizeStoryboardRow(row: StoryboardRow): StoryboardRow {
    const characters = arrayValue(row.characters);
    const referenceNodeIds = arrayValue(row.referenceNodeIds);
    const sourceRefs = row.sourceRefs === undefined ? undefined : arrayValue(row.sourceRefs);
    const assetBindings = row.assetBindings === undefined ? undefined : arrayValue(row.assetBindings);
    const startBoundary = normalizeStoryboardBoundary(row.startBoundary);
    const endBoundary = normalizeStoryboardBoundary(row.endBoundary);
    const motionSpec = normalizeStoryboardMotionSpec(row.motionSpec);
    const plotDescription = sanitizeStoryboardText(row.plotDescription);
    const dialogue = sanitizeStoryboardText(row.dialogue);
    const shotSize = sanitizeStoryboardText(row.shotSize);
    const emotion = sanitizeStoryboardText(row.emotion);
    const lightingAndAtmosphere = sanitizeStoryboardText(row.lightingAndAtmosphere);
    const audioEffects = sanitizeStoryboardText(row.audioEffects);
    const camera = sanitizeStoryboardText(row.camera);
    const motion = sanitizeStoryboardText(row.motion);
    const timeBeats = sanitizeStoryboardText(row.timeBeats);
    const imageGenerationPrompt = sanitizeStoryboardText(row.imageGenerationPrompt);
    const videoMotionPrompt = sanitizeStoryboardText(row.videoMotionPrompt);
    const negativePrompt = sanitizeStoryboardText(row.negativePrompt);
    if (
        characters === row.characters
        && referenceNodeIds === row.referenceNodeIds
        && sourceRefs === row.sourceRefs
        && assetBindings === row.assetBindings
        && startBoundary === row.startBoundary
        && endBoundary === row.endBoundary
        && motionSpec === row.motionSpec
        && plotDescription === row.plotDescription
        && dialogue === row.dialogue
        && shotSize === row.shotSize
        && emotion === row.emotion
        && lightingAndAtmosphere === row.lightingAndAtmosphere
        && audioEffects === row.audioEffects
        && camera === row.camera
        && motion === row.motion
        && timeBeats === row.timeBeats
        && imageGenerationPrompt === row.imageGenerationPrompt
        && videoMotionPrompt === row.videoMotionPrompt
        && negativePrompt === row.negativePrompt
    ) return row;
    return {
        ...row,
        characters,
        referenceNodeIds,
        sourceRefs,
        assetBindings,
        startBoundary,
        endBoundary,
        motionSpec,
        plotDescription,
        dialogue,
        shotSize,
        emotion,
        lightingAndAtmosphere,
        audioEffects,
        camera,
        motion,
        timeBeats,
        imageGenerationPrompt,
        videoMotionPrompt,
        negativePrompt,
    };
}

function normalizeStoryboardBoundary(boundary: StoryboardBoundary | null | undefined) {
    if (!boundary || typeof boundary !== "object") return undefined;
    const positions = arrayValue(boundary.positions);
    const facing = arrayValue(boundary.facing);
    const gaze = arrayValue(boundary.gaze);
    const hands = arrayValue(boundary.hands);
    const heldProps = arrayValue(boundary.heldProps);
    const visibleState = arrayValue(boundary.visibleState);
    if (positions === boundary.positions && facing === boundary.facing && gaze === boundary.gaze && hands === boundary.hands && heldProps === boundary.heldProps && visibleState === boundary.visibleState) return boundary;
    return { ...boundary, positions, facing, gaze, hands, heldProps, visibleState };
}

function normalizeStoryboardMotionSpec(spec: StoryboardMotionSpec | null | undefined) {
    if (!spec || typeof spec !== "object") return undefined;
    const orderedSubjectMotion = arrayValue(spec.orderedSubjectMotion);
    const environmentAndAudio = arrayValue(spec.environmentAndAudio);
    return orderedSubjectMotion === spec.orderedSubjectMotion && environmentAndAudio === spec.environmentAndAudio
        ? spec
        : { ...spec, orderedSubjectMotion, environmentAndAudio };
}

function arrayValue<T>(value: T[] | null | undefined): T[] {
    return Array.isArray(value) ? value : [];
}
