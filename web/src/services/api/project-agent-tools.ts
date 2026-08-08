import {
    confirmProjectAssetCandidate,
    createProjectUnit,
    createProjectAssetVersion,
    createProjectAssetCandidates,
    getProject,
    getProjectUnit,
    listProjectAssetVersions,
    linkProjectAsset,
    linkShotAsset,
    registerProjectTaskOutput,
    saveProjectShot,
    updateProjectUnit,
    updateWorkflowStep,
    type ProjectDetail,
    type ShotAssetReference,
} from "./projects";

export const projectAgentToolNames = [
    "project_get_context",
    "project_list_units",
    "project_get_unit",
    "project_list_asset_versions",
    "project_create_unit",
    "project_update_unit",
    "project_extract_asset_candidates",
    "project_confirm_asset_candidate",
    "project_create_or_update_shots",
    "project_link_shot_asset",
    "project_start_workflow_step",
    "project_update_workflow_step",
    "project_link_asset",
    "project_upsert_asset_version",
    "project_register_task_output",
] as const;

export type ProjectAgentToolName = (typeof projectAgentToolNames)[number];

export function isProjectAgentToolName(value: string): value is ProjectAgentToolName {
    return projectAgentToolNames.includes(value as ProjectAgentToolName);
}

export function isProjectAgentReadTool(value: string) {
    return value === "project_get_context" || value === "project_list_units" || value === "project_get_unit" || value === "project_list_asset_versions";
}

export async function runProjectAgentTool(name: ProjectAgentToolName, rawInput: Record<string, unknown>, fallbackProjectId?: string) {
    const projectId = String(rawInput.projectId || fallbackProjectId || "").trim();
    if (!projectId) throw new Error("当前画布没有关联制作项目");
    if (name === "project_get_context") return getProject(projectId);
    if (name === "project_list_units") {
        const detail = await getProject(projectId);
        const kind = String(rawInput.kind || "").trim();
        const status = String(rawInput.status || "").trim();
        return { units: detail.units.filter((unit) => (!kind || unit.kind === kind) && (!status || unit.status === status)) };
    }
    if (name === "project_get_unit") {
        return getProjectUnit(projectId, String(rawInput.unitId || ""));
    }
    if (name === "project_list_asset_versions") {
        return listProjectAssetVersions(projectId, String(rawInput.assetId || ""));
    }
    if (name === "project_create_unit") {
        return createProjectUnit(projectId, {
            kind: String(rawInput.kind || "episode"),
            title: String(rawInput.title || ""),
            sourceText: typeof rawInput.sourceText === "string" ? rawInput.sourceText : undefined,
            position: typeof rawInput.position === "number" ? rawInput.position : undefined,
        });
    }
    if (name === "project_update_unit") {
        return updateProjectUnit(projectId, String(rawInput.unitId || ""), {
            title: typeof rawInput.title === "string" ? rawInput.title : undefined,
            sourceText: String(rawInput.sourceText || ""),
            status: typeof rawInput.status === "string" ? rawInput.status : undefined,
        });
    }
    if (name === "project_extract_asset_candidates") {
        const candidates = Array.isArray(rawInput.candidates) ? rawInput.candidates : [];
        if (!candidates.length) throw new Error("至少需要提供一个资产候选");
        const validCandidates = candidates.map((candidate, index) => {
            if (!isCandidateInput(candidate)) throw new Error(`第 ${index + 1} 个资产候选格式无效`);
            return candidate;
        });
        return createProjectAssetCandidates(projectId, validCandidates);
    }
    if (name === "project_confirm_asset_candidate") {
        return confirmProjectAssetCandidate(projectId, String(rawInput.candidateId || ""), String(rawInput.assetId || "") || undefined);
    }
    if (name === "project_create_or_update_shots") {
        const shots = Array.isArray(rawInput.shots) ? rawInput.shots : [];
        if (!shots.length) throw new Error("至少需要提供一个项目镜头");
        const result = [];
        for (const [index, shot] of shots.entries()) {
            if (!isShotInput(shot)) throw new Error(`第 ${index + 1} 个项目镜头格式无效`);
            const { definition, ...input } = shot;
            try {
                result.push((await saveProjectShot(projectId, {
                    ...input,
                    definitionJson: definition ? JSON.stringify(definition) : undefined,
                })).shot);
            } catch (error) {
                const reason = error instanceof Error ? error.message : "保存失败";
                const saved = result.length ? `；此前 ${result.length} 个镜头已经保存，请先重新读取项目上下文再继续` : "";
                throw new Error(`第 ${index + 1} 个项目镜头保存失败：${reason}${saved}`);
            }
        }
        return { shots: result };
    }
    if (name === "project_link_shot_asset") {
        return linkShotAsset(projectId, String(rawInput.shotId || ""), { assetVersionId: String(rawInput.assetVersionId || ""), role: String(rawInput.role || "reference") as ShotAssetReference["role"] });
    }
    if (name === "project_start_workflow_step") {
        return updateWorkflowStep(projectId, String(rawInput.stepId || ""), { status: "running" });
    }
    if (name === "project_update_workflow_step") {
        return updateWorkflowStep(projectId, String(rawInput.stepId || ""), {
            status: String(rawInput.status || "review"),
            outputJson: JSON.stringify(rawInput.output || {}),
            error: String(rawInput.error || ""),
        });
    }
    if (name === "project_link_asset") {
        return linkProjectAsset(projectId, { assetId: String(rawInput.assetId || ""), category: String(rawInput.category || "other") });
    }
    if (name === "project_upsert_asset_version") {
        if (rawInput.definition !== undefined && !isRecord(rawInput.definition)) throw new Error("资产版本 definition 必须是对象");
        const definitionJson = isRecord(rawInput.definition) ? JSON.stringify(rawInput.definition) : typeof rawInput.definitionJson === "string" ? rawInput.definitionJson : undefined;
        const status = rawInput.status === "draft" || rawInput.status === "review" || rawInput.status === "confirmed" ? rawInput.status : undefined;
        return createProjectAssetVersion(projectId, String(rawInput.assetId || ""), { prompt: String(rawInput.prompt || ""), definitionJson, note: String(rawInput.note || ""), status });
    }
    if (name === "project_register_task_output") {
        return registerProjectTaskOutput(projectId, String(rawInput.stepId || ""), { taskId: String(rawInput.taskId || ""), assetVersionId: String(rawInput.assetVersionId || "") || undefined, resourceId: String(rawInput.resourceId || "") || undefined, mediaType: String(rawInput.mediaType || "") || undefined, role: String(rawInput.role || "output"), metadataJson: typeof rawInput.metadataJson === "string" ? rawInput.metadataJson : undefined, outputJson: typeof rawInput.outputJson === "string" ? rawInput.outputJson : undefined });
    }
    throw new Error(`未知项目工具：${name}`);
}

function isCandidateInput(value: unknown): value is { unitId?: string; shotId?: string; name: string; category: string; details?: Record<string, unknown> } {
    if (!value || typeof value !== "object") return false;
    const item = value as Record<string, unknown>;
    return typeof item.name === "string"
        && typeof item.category === "string"
        && (item.unitId === undefined || typeof item.unitId === "string")
        && (item.shotId === undefined || typeof item.shotId === "string")
        && (item.details === undefined || isRecord(item.details));
}

function isShotInput(value: unknown): value is { id?: string; unitId?: string; title: string; description?: string; definition?: Record<string, unknown>; position?: number; durationMs?: number; status?: string } {
    if (!value || typeof value !== "object") return false;
    const item = value as Record<string, unknown>;
    return typeof item.title === "string" && (item.definition === undefined || isRecord(item.definition));
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

export type ProjectAgentContext = ProjectDetail;
