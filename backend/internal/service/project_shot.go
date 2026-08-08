package service

import (
	"encoding/json"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

type CreateProjectShotRequest struct {
	ID             string  `json:"id"`
	UnitID         *string `json:"unitId"`
	Title          *string `json:"title"`
	Description    *string `json:"description"`
	DefinitionJSON *string `json:"definitionJson"`
	Position       *int    `json:"position"`
	DurationMs     *int64  `json:"durationMs"`
	Status         *string `json:"status"`
}

const projectShotDefinitionSchemaV1 = "mingwant.short-drama.shot/v1"

type projectShotDefinition struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	ShotCode          string                    `json:"shotCode"`
	SourceRefs        []projectShotSourceRef    `json:"sourceRefs"`
	Purpose           string                    `json:"purpose"`
	InformationChange string                    `json:"informationChange"`
	AssetBindings     []projectShotAssetBinding `json:"assetBindings"`
	StartBoundary     *projectShotBoundary      `json:"startBoundary"`
	EndBoundary       *projectShotBoundary      `json:"endBoundary"`
	MotionSpec        *projectShotMotionSpec    `json:"motionSpec"`
	ImagePrompt       string                    `json:"imagePrompt"`
	VideoPrompt       string                    `json:"videoPrompt"`
	NegativePrompt    string                    `json:"negativePrompt"`
}

type projectShotSourceRef struct {
	UnitID        string `json:"unitId"`
	BlockID       string `json:"blockId"`
	Role          string `json:"role"`
	UnitUpdatedAt string `json:"unitUpdatedAt"`
}

type projectShotAssetBinding struct {
	AssetVersionID string `json:"assetVersionId"`
	Role           string `json:"role"`
}

type projectShotBoundary struct {
	Positions    []string `json:"positions"`
	Facing       []string `json:"facing"`
	Gaze         []string `json:"gaze"`
	Hands        []string `json:"hands"`
	HeldProps    []string `json:"heldProps"`
	VisibleState []string `json:"visibleState"`
}

type projectShotMotionSpec struct {
	StartAnchor          string                     `json:"startAnchor"`
	OrderedSubjectMotion *[]projectShotMotionAction `json:"orderedSubjectMotion"`
	PerformanceArc       string                     `json:"performanceArc"`
	Camera               string                     `json:"camera"`
	EnvironmentAndAudio  []string                   `json:"environmentAndAudio"`
	TimingPlan           string                     `json:"timingPlan"`
	EndReport            string                     `json:"endReport"`
}

type projectShotMotionAction struct {
	Order         int    `json:"order"`
	Actor         string `json:"actor"`
	Trigger       string `json:"trigger"`
	Action        string `json:"action"`
	PathOrContact string `json:"pathOrContact"`
	Result        string `json:"result"`
}

type ReplaceProjectUnitShotsRequest struct {
	Shots []CreateProjectShotRequest `json:"shots"`
}

type LinkShotAssetRequest struct {
	AssetVersionID string `json:"assetVersionId"`
	Role           string `json:"role"`
}

type AssetCandidateInput struct {
	UnitID   string         `json:"unitId"`
	ShotID   string         `json:"shotId"`
	Name     string         `json:"name"`
	Category string         `json:"category"`
	Details  map[string]any `json:"details"`
}

type CreateAssetCandidatesRequest struct {
	Candidates []AssetCandidateInput `json:"candidates"`
}

func (s *Service) CreateProjectShot(userID string, projectID string, req CreateProjectShotRequest) (model.Shot, error) {
	if _, err := s.repo.ProjectForUser(userID, projectID); err != nil {
		return model.Shot{}, err
	}
	shotID := strings.TrimSpace(req.ID)
	create := shotID == ""
	now := time.Now()
	shot := model.Shot{ID: shotID, ProjectID: projectID, Status: "draft", CreatedAt: now}
	if create {
		shot.ID = newID()
	} else {
		existing, err := s.repo.ShotForProject(projectID, shotID)
		if err != nil {
			return model.Shot{}, err
		}
		shot = *existing
	}
	// 更新接口按字段存在性覆盖，避免 Agent 只改定义时把章节、时长或顺序清零。
	if req.UnitID != nil {
		shot.UnitID = strings.TrimSpace(*req.UnitID)
	}
	if req.Title != nil {
		shot.Title = strings.TrimSpace(*req.Title)
	}
	if req.Description != nil {
		shot.Description = strings.TrimSpace(*req.Description)
	}
	if req.DefinitionJSON != nil {
		shot.DefinitionJSON = strings.TrimSpace(*req.DefinitionJSON)
	}
	if req.Position != nil {
		shot.Position = *req.Position
	}
	if req.DurationMs != nil {
		shot.DurationMs = *req.DurationMs
	}
	if req.Status != nil {
		shot.Status = strings.TrimSpace(*req.Status)
	}
	if shot.DefinitionJSON == "" {
		shot.DefinitionJSON = "{}"
	}
	if shot.Title == "" {
		return model.Shot{}, BadAuthRequest("镜头标题不能为空")
	}
	if shot.Position < 0 || shot.DurationMs < 0 {
		return model.Shot{}, BadAuthRequest("镜头顺序和时长不能为负数")
	}
	if shot.UnitID != "" {
		if _, err := s.repo.ProjectUnit(projectID, shot.UnitID); err != nil {
			return model.Shot{}, err
		}
	}
	if !validShotStatus(shot.Status) {
		return model.Shot{}, BadAuthRequest("不支持的镜头状态")
	}
	definitionJSON, err := s.validateProjectShotDefinition(projectID, shot.UnitID, shot.DefinitionJSON, shot.DurationMs)
	if err != nil {
		return model.Shot{}, err
	}
	shot.DefinitionJSON = definitionJSON
	shot.UpdatedAt = time.Now()
	if err := s.repo.SaveShot(&shot, create); err != nil {
		return model.Shot{}, err
	}
	if err := s.repo.BumpProjectRevision(projectID); err != nil {
		return model.Shot{}, err
	}
	return shot, nil
}

func (s *Service) ReplaceProjectUnitShots(userID string, projectID string, unitID string, req ReplaceProjectUnitShotsRequest) ([]model.Shot, error) {
	if _, err := s.repo.ProjectForUser(userID, projectID); err != nil {
		return nil, err
	}
	unitID = strings.TrimSpace(unitID)
	if _, err := s.repo.ProjectUnit(projectID, unitID); err != nil {
		return nil, err
	}
	if len(req.Shots) == 0 || len(req.Shots) > 200 {
		return nil, BadAuthRequest("章节分镜数量必须在 1 到 200 之间")
	}
	now := time.Now()
	shots := make([]model.Shot, 0, len(req.Shots))
	for position, input := range req.Shots {
		title := projectShotRequestText(input.Title)
		description := projectShotRequestText(input.Description)
		if title == "" || description == "" || input.DurationMs == nil || *input.DurationMs < 0 {
			return nil, BadAuthRequest("分镜标题、描述或时长无效")
		}
		definitionJSON, err := s.validateProjectShotDefinition(projectID, unitID, projectShotRequestText(input.DefinitionJSON), *input.DurationMs)
		if err != nil {
			return nil, err
		}
		shots = append(shots, model.Shot{ID: newID(), ProjectID: projectID, UnitID: unitID, Title: title, Description: description, DefinitionJSON: definitionJSON, Position: position, DurationMs: *input.DurationMs, Status: "draft", CreatedAt: now, UpdatedAt: now})
	}
	// 章节级重生成是一个整体写操作，旧镜头与引用必须和新镜头在同一事务中替换。
	if err := s.repo.ReplaceProjectUnitShots(projectID, unitID, shots); err != nil {
		return nil, err
	}
	return shots, nil
}

func projectShotRequestText(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (s *Service) validateProjectShotDefinition(projectID string, unitID string, raw string, durationMs int64) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return "{}", nil
	}
	if !json.Valid([]byte(raw)) {
		return "", BadAuthRequest("镜头定义必须是有效 JSON")
	}
	var definition projectShotDefinition
	if err := json.Unmarshal([]byte(raw), &definition); err != nil {
		return "", BadAuthRequest("镜头定义格式无效")
	}
	if strings.TrimSpace(definition.SchemaVersion) != projectShotDefinitionSchemaV1 {
		return "", BadAuthRequest("镜头定义版本不受支持")
	}
	if strings.TrimSpace(definition.ShotCode) == "" || strings.TrimSpace(definition.Purpose) == "" || strings.TrimSpace(definition.InformationChange) == "" {
		return "", BadAuthRequest("镜头定义缺少编号、观看目的或信息变化")
	}
	if durationMs <= 0 {
		return "", BadAuthRequest("结构化镜头必须提供大于零的时长")
	}
	if len(definition.SourceRefs) == 0 || definition.StartBoundary == nil || definition.EndBoundary == nil {
		return "", BadAuthRequest("镜头定义缺少来源或起止边界")
	}
	if !projectShotBoundaryHasPosition(definition.StartBoundary) || !projectShotBoundaryHasPosition(definition.EndBoundary) {
		return "", BadAuthRequest("镜头起止边界必须明确主体位置")
	}
	hasOwningUnit := unitID == ""
	for _, source := range definition.SourceRefs {
		sourceUnitID := strings.TrimSpace(source.UnitID)
		if sourceUnitID == "" || strings.TrimSpace(source.Role) == "" {
			return "", BadAuthRequest("镜头来源必须包含章节和用途")
		}
		if _, err := s.repo.ProjectUnit(projectID, sourceUnitID); err != nil {
			return "", err
		}
		if sourceUnitID == unitID {
			hasOwningUnit = true
		}
		if stamp := strings.TrimSpace(source.UnitUpdatedAt); stamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				return "", BadAuthRequest("镜头来源版本时间格式无效")
			}
		}
	}
	if !hasOwningUnit {
		return "", BadAuthRequest("镜头定义没有引用所属章节")
	}
	for _, binding := range definition.AssetBindings {
		versionID := strings.TrimSpace(binding.AssetVersionID)
		if versionID == "" || strings.TrimSpace(binding.Role) == "" {
			return "", BadAuthRequest("镜头资产绑定缺少版本或用途")
		}
		if _, err := s.repo.AssetVersionForProject(projectID, versionID); err != nil {
			return "", err
		}
	}
	if motion := definition.MotionSpec; motion != nil {
		if strings.TrimSpace(motion.StartAnchor) == "" || strings.TrimSpace(motion.PerformanceArc) == "" || strings.TrimSpace(motion.Camera) == "" || strings.TrimSpace(motion.TimingPlan) == "" || strings.TrimSpace(motion.EndReport) == "" {
			return "", BadAuthRequest("视频运动说明缺少起点、表演、摄影、时间或终点核对")
		}
		if motion.OrderedSubjectMotion == nil {
			return "", BadAuthRequest("视频运动说明必须明确有序主体动作")
		}
		for index, action := range *motion.OrderedSubjectMotion {
			if action.Order != index+1 || strings.TrimSpace(action.Actor) == "" || strings.TrimSpace(action.Action) == "" || strings.TrimSpace(action.Result) == "" {
				return "", BadAuthRequest("视频运动动作必须按顺序填写主体、动作和结果")
			}
		}
	}
	return raw, nil
}

func projectShotBoundaryHasPosition(boundary *projectShotBoundary) bool {
	for _, position := range boundary.Positions {
		if strings.TrimSpace(position) != "" {
			return true
		}
	}
	return false
}

func validShotStatus(status string) bool {
	switch status {
	case "draft", "ready", "running", "review", "completed", "failed":
		return true
	default:
		return false
	}
}

func (s *Service) LinkShotAsset(userID string, projectID string, shotID string, req LinkShotAssetRequest) (model.ShotAssetReference, error) {
	if _, err := s.repo.ProjectForUser(userID, projectID); err != nil {
		return model.ShotAssetReference{}, err
	}
	if _, err := s.repo.ShotForProject(projectID, shotID); err != nil {
		return model.ShotAssetReference{}, err
	}
	versionID := strings.TrimSpace(req.AssetVersionID)
	if _, err := s.repo.AssetVersionForProject(projectID, versionID); err != nil {
		return model.ShotAssetReference{}, err
	}
	role := strings.TrimSpace(req.Role)
	if !validShotAssetRole(role) {
		return model.ShotAssetReference{}, BadAuthRequest("不支持的镜头素材用途")
	}
	reference := model.ShotAssetReference{ID: newID(), ShotID: shotID, AssetVersionID: versionID, Role: role, Status: "linked", CreatedAt: time.Now()}
	if err := s.repo.UpsertShotAssetReference(&reference); err != nil {
		return model.ShotAssetReference{}, err
	}
	if err := s.repo.BumpProjectRevision(projectID); err != nil {
		return model.ShotAssetReference{}, err
	}
	return reference, nil
}

func (s *Service) CreateProjectAssetCandidates(userID string, projectID string, req CreateAssetCandidatesRequest) ([]model.ProjectAssetCandidate, error) {
	if _, err := s.repo.ProjectForUser(userID, projectID); err != nil {
		return nil, err
	}
	if len(req.Candidates) == 0 || len(req.Candidates) > 100 {
		return nil, BadAuthRequest("资产候选数量必须在 1 到 100 之间")
	}
	now := time.Now()
	candidates := make([]model.ProjectAssetCandidate, 0, len(req.Candidates))
	for _, input := range req.Candidates {
		name := strings.TrimSpace(input.Name)
		category := model.AssetCategory(strings.TrimSpace(input.Category))
		if name == "" || !validAssetCategory(category) {
			return nil, BadAuthRequest("资产候选名称或分类无效")
		}
		if input.UnitID != "" {
			if _, err := s.repo.ProjectUnit(projectID, input.UnitID); err != nil {
				return nil, err
			}
		}
		if input.ShotID != "" {
			if _, err := s.repo.ShotForProject(projectID, input.ShotID); err != nil {
				return nil, err
			}
		}
		detailsJSON, err := marshalProjectDetails(input.Details)
		if err != nil {
			return nil, BadAuthRequest("资产候选详情格式无效")
		}
		if category == model.AssetCategoryCharacter {
			if err := validateCharacterCandidateDetails(input.Details); err != nil {
				return nil, err
			}
		}
		candidates = append(candidates, model.ProjectAssetCandidate{ID: newID(), ProjectID: projectID, UnitID: strings.TrimSpace(input.UnitID), ShotID: strings.TrimSpace(input.ShotID), Name: name, Category: category, Status: "pending_confirmation", DetailsJSON: detailsJSON, CreatedAt: now, UpdatedAt: now})
	}
	if err := s.repo.CreateProjectAssetCandidates(candidates); err != nil {
		return nil, err
	}
	if err := s.repo.BumpProjectRevision(projectID); err != nil {
		return nil, err
	}
	return candidates, nil
}

func validShotAssetRole(role string) bool {
	switch role {
	case "reference", "start_frame", "end_frame", "keyframe", "storyboard", "output":
		return true
	default:
		return false
	}
}

func marshalProjectDetails(value map[string]any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func validateCharacterCandidateDetails(details map[string]any) error {
	text := func(key string) string {
		value, _ := details[key].(string)
		return strings.TrimSpace(value)
	}
	descriptiveCount := 0
	for _, key := range []string{"appearance", "clothing", "physique", "personality", "consistencyPrompt", "multiViewPrompt"} {
		if text(key) != "" {
			descriptiveCount++
		}
	}
	if text("role") == "" || descriptiveCount < 3 || text("voiceLanguage") == "" || text("voiceAge") == "" || text("voiceTimbre") == "" {
		return BadAuthRequest("角色候选必须包含剧情定位、稳定设定和声音画像")
	}
	return nil
}
