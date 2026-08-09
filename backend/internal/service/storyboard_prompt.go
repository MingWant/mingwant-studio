package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

type StoryboardPromptTemplateRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Enabled *bool  `json:"enabled"`
}

type StoryboardPromptVariable struct {
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
}

var storyboardPromptVariables = []StoryboardPromptVariable{
	{Label: "剧情", Placeholder: "{{剧情}}"},
	{Label: "用户要求", Placeholder: "{{用户要求}}"},
	{Label: "画布资产", Placeholder: "{{画布资产}}"},
}

func (s *Service) EnsureDefaultStoryboardPromptTemplate() error {
	count, err := s.repo.StoryboardPromptTemplateCount()
	if err != nil {
		return err
	}
	if count > 0 {
		// 只升级没有创建者且仍带旧内置签名的模板，管理员新建的版本保持不变。
		templates, err := s.repo.StoryboardPromptTemplates()
		if err != nil {
			return err
		}
		for index := range templates {
			template := &templates[index]
			if template.Name == "默认影视分镜提示词" && template.CreatedBy == "" && legacyStoryboardPromptNeedsUpgrade(template.Content) {
				template.Content = defaultStoryboardPromptTemplate()
				return s.repo.SaveStoryboardPromptTemplate(template)
			}
		}
		return nil
	}
	return s.repo.SaveStoryboardPromptTemplate(&model.StoryboardPromptTemplate{
		ID:      newID(),
		Name:    "默认影视分镜提示词",
		Content: defaultStoryboardPromptTemplate(),
		Enabled: true,
	})
}

func (s *Service) AdminStoryboardPromptTemplates(actor *model.User) ([]model.StoryboardPromptTemplate, []StoryboardPromptVariable, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, nil, err
	}
	templates, err := s.repo.StoryboardPromptTemplates()
	if err != nil {
		return nil, nil, err
	}
	return templates, storyboardPromptVariables, nil
}

func (s *Service) CreateStoryboardPromptTemplate(actor *model.User, req StoryboardPromptTemplateRequest) (*model.StoryboardPromptTemplate, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	template, err := storyboardPromptTemplateFromRequest(req, model.StoryboardPromptTemplate{ID: newID(), CreatedBy: actor.ID})
	if err != nil {
		return nil, err
	}
	if req.Enabled == nil {
		template.Enabled = false
	}
	if err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		if err := txRepo.SaveStoryboardPromptTemplate(&template); err != nil {
			return err
		}
		return appendAdminAuditWithRepository(txRepo, actor, "storyboard_prompt.create", "storyboard_prompt_template", template.ID, "创建分镜提示词模板", map[string]any{
			"name": template.Name, "enabled": template.Enabled, "contentBytes": len([]byte(template.Content)),
		})
	}); err != nil {
		return nil, err
	}
	return &template, nil
}

func (s *Service) UpdateStoryboardPromptTemplate(actor *model.User, id string, req StoryboardPromptTemplateRequest) (*model.StoryboardPromptTemplate, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	var updated *model.StoryboardPromptTemplate
	err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		template, err := txRepo.StoryboardPromptTemplateForUpdate(id)
		if err != nil {
			return err
		}
		if template.Enabled && req.Enabled != nil && !*req.Enabled {
			return BadAuthRequest("至少需要保留一个启用的分镜提示词")
		}
		next, err := storyboardPromptTemplateFromRequest(req, *template)
		if err != nil {
			return err
		}
		if err := txRepo.SaveStoryboardPromptTemplate(&next); err != nil {
			return err
		}
		if err := appendAdminAuditWithRepository(txRepo, actor, "storyboard_prompt.update", "storyboard_prompt_template", next.ID, "更新分镜提示词模板", map[string]any{
			"name": next.Name, "enabled": next.Enabled, "contentBytes": len([]byte(next.Content)),
		}); err != nil {
			return err
		}
		updated = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) DeleteStoryboardPromptTemplate(actor *model.User, id string) error {
	if err := s.RequireAdmin(actor); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	return s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		template, err := txRepo.StoryboardPromptTemplateForUpdate(id)
		if err != nil {
			return err
		}
		if template.Enabled {
			return BadAuthRequest("启用中的分镜提示词不能删除，请先启用其他版本")
		}
		if err := txRepo.DeleteStoryboardPromptTemplate(id); err != nil {
			return err
		}
		return appendAdminAuditWithRepository(txRepo, actor, "storyboard_prompt.delete", "storyboard_prompt_template", id, "删除分镜提示词模板", map[string]any{"name": template.Name})
	})
}

func (s *Service) buildAgentStoryboardPlannerPrompt(brief string, requirements string, assets []storyboardAsset, shotDuration int, shotCount int) string {
	template := defaultStoryboardPromptTemplate()
	if active, err := s.repo.ActiveStoryboardPromptTemplate(); err == nil && strings.TrimSpace(active.Content) != "" {
		template = active.Content
	}
	return renderStoryboardPromptTemplate(template, brief, requirements, assets) + "\n\n" + storyboardCinematicQualityContract(shotDuration, shotCount)
}

func storyboardCinematicQualityContract(shotDuration int, shotCount int) string {
	durationRule := "单个镜头时长由剧情节奏决定，必须是 1 到 60 秒的整数。"
	if shotDuration == 5 || shotDuration == 10 || shotDuration == 15 || shotDuration == 30 {
		durationRule = fmt.Sprintf("本次生成单个镜头时长必须严格等于 %d 秒；模型只决定镜头数量和总时长，不得修改单镜头时长。", shotDuration)
	}
	countRule := "镜头数量由模型按剧情节奏自动决定。"
	if shotCount >= 1 && shotCount <= 10 {
		countRule = fmt.Sprintf("shots 数组必须严格输出 %d 个镜头，不得多于或少于 %d 个；该规则覆盖模板中的默认镜头数量范围。", shotCount, shotCount)
	}
	contract := `分镜制作契约（优先级高于用户 brief 中的泛化叙述）：
- ` + durationRule + `
- ` + countRule + `
- 先遵循项目已经指定的画风、媒介和制作形态；真人、二维、三维、定格、绘本或混合媒介都可以，不得擅自替换成另一种表现形态。
- 不要把剧情段落直接改写成镜头摘要。每一行必须写明 purpose、informationChange、可见主体、空间关系与结果。
- startBoundary 和 endBoundary 是制作事实，至少在 positions 中写明主体位置，并按需要填写朝向、目光、双手、持物和可见状态；相邻镜头必须能连续承接。
- description 只写可见画面与动作；不可见心理必须转译为表情、停顿、手部动作、走位、道具或环境反馈。
- visualPrompt 只描述冻结在 startBoundary 的单一画面，可写构图、光线、材质和媒介特征，不得混入动作过程、时间段或 endBoundary。
- videoPrompt 只描述怎样从 startBoundary 到达 endBoundary，按因果顺序写主体动作、环境/声音反馈与结束核对，不得重设人物身份、服装、持物或空间。
- camera、motion 和 timeBeats 要具体且可执行；固定视点与运动视点都有效，是否运动、使用何种技法及持续多久由叙事和项目媒介决定。
- timeBeats 覆盖完整 durationSeconds，并明确开始、变化与结束落点；使用精确时间段时，各段总和必须等于镜头时长。
- styleGuide、visualPrompt 和 videoPrompt 禁止写入 2.39:1、16:9 等具体画幅比例，也不要讨论画幅配置。
- negativePrompt 只写本镜真实存在的风险；没有具体风险时返回空字符串，不复制通用禁词清单。
- 所有可见文本只使用正常文字和标点，不要添加 emoji、图标或装饰符号，避免兼容网关错误解码后污染分镜内容。
- assetTags 只能引用输入中存在的画布资产标签；业务项目中的精确资产版本仍由项目镜头绑定负责。
- 未指定镜头数量时优先生成 3 个镜头，剧情确有必要时最多 5 个；每个字段使用能执行的短句，避免重复解释和长篇 prose，确保结果能在输出上限内完整收束。
- 为了兼容慢推理模型和五分钟级上游网关，默认严格生成 3 个镜头；只有用户明确指定镜头数量时才改变数量。每个镜头的 description、purpose、informationChange、camera、motion、timeBeats 控制在短句范围内，visualPrompt 和 videoPrompt 各不超过约 160 个中文字符；不要在每个镜头重复 styleGuide、角色设定或制作说明。
- startBoundary 和 endBoundary 各保留最少一项 positions，其他数组只在确实改变镜头连续性时填写；characters、locations 和 assetTags 只保留本镜真正需要的内容。优先保证完整 JSON、首帧图片提示词和视频动作提示词，不要为了扩写细节牺牲最后几个镜头的闭合括号。
- 最终响应必须是单个完整 JSON 对象，以 { 开始、以 } 结束；键和字符串使用双引号，不要输出 Markdown、思考过程、注释、尾随逗号或顶层数组。
- 只返回 JSON。shots 中每项必须包含：title、description、purpose、informationChange、startBoundary、endBoundary、durationSeconds、dialogue、shotSize、emotion、lightingAndAtmosphere、audioEffects、visualPrompt、videoPrompt、camera、motion、timeBeats、negativePrompt、assetTags。`

	return contract + "\n\n" + storyboardCameraLanguageGuide()
}

// 手动交付只需要可读、可复制的短分镜，不把基础文本模型强行套进长 JSON 契约。
// 后端仍会把完整标签文本解析并校验成同一份结构化分镜结果。
func buildManualStoryboardPlannerPrompt(brief string, requirements string, shotDuration int, shotCount int) string {
	countRule := "默认生成 3 个镜头；只有用户明确指定数量时才调整，最多 8 个。"
	if shotCount >= 1 && shotCount <= 8 {
		countRule = fmt.Sprintf("严格生成 %d 个镜头，不得多于或少于 %d 个。", shotCount, shotCount)
	}
	durationRule := "每个镜头时长 1 到 60 秒，按剧情节奏决定。"
	if shotDuration >= 1 && shotDuration <= 60 {
		durationRule = fmt.Sprintf("每个镜头严格使用 %d 秒。", shotDuration)
	}
	return fmt.Sprintf(`你是短剧分镜助手。请把用户需求整理成可直接编辑和复制的短分镜。

用户需求：
%s

用户要求：
%s

规则：
- %s
- %s
- 每个镜头都要有可见的画面描述、图片生成提示词和视频动作提示词；没有台词时写“无”。
- 提示词使用短句，保持人物、场景、道具和光线连续，不要重复整段风格说明。
- 不要添加 emoji、图标或装饰符号，只使用正常文字和标点。
- 不要解释过程，不要写 Markdown 表格；按下面的标签格式逐镜输出纯文本即可：

镜头 1：
画面描述：……
图片提示词：……
视频动作提示词：……
时长：6 秒
台词：……

只输出镜头内容。`, strings.TrimSpace(brief), strings.TrimSpace(requirements), countRule, durationRule)
}

func storyboardCameraLanguageGuide() string {
	return `镜头设计参考（服务信息变化，不作为固定配额）：
- shotSize 写明观众能看见的空间范围和注意重点；景别变化应由新信息、动作可读性或情绪距离驱动。
- camera 写明视点、角度、构图与空间关系。真人项目可以使用摄影机术语，动画或其他媒介使用对应的虚拟视点与构图语言。
- motion 先说明固定或移动，再写移动的主体、方向、起点、终点和叙事动机；没有移动同样是完整设计。
- 构图应明确主体、视觉焦点及必要的空间层级，但不要为了套用术语强加无关前景、焦段或景深。
- 对话、反应、插入、主观、匹配或交叉剪辑都按原文义务与信息变化选择，不设固定数量。
- 镜头技法、持续时间和使用次数没有通用配额；只检查是否可执行、是否连续，以及是否到达 endBoundary。`
}

func legacyStoryboardPromptNeedsUpgrade(content string) bool {
	return strings.Contains(content, "\n....\n") ||
		(strings.Contains(content, "优先使用真实电影机语言") && strings.Contains(content, "不要在 visualPrompt 或 videoPrompt 中使用 3D动漫"))
}

func storyboardPromptTemplateFromRequest(req StoryboardPromptTemplateRequest, template model.StoryboardPromptTemplate) (model.StoryboardPromptTemplate, error) {
	name := strings.TrimSpace(req.Name)
	content := strings.TrimSpace(req.Content)
	if name == "" {
		return template, BadAuthRequest("请填写提示词名称")
	}
	if content == "" {
		return template, BadAuthRequest("请填写提示词内容")
	}
	template.Name = name
	template.Content = content
	if req.Enabled != nil {
		template.Enabled = *req.Enabled
	}
	return template, nil
}

func renderStoryboardPromptTemplate(template string, brief string, requirements string, assets []storyboardAsset) string {
	assetJSON, _ := json.MarshalIndent(assets, "", "  ")
	replacer := strings.NewReplacer(
		"{{剧情}}", strings.TrimSpace(brief),
		"{{用户brief}}", strings.TrimSpace(brief),
		"{{用户要求}}", strings.TrimSpace(requirements),
		"{{画布资产}}", string(assetJSON),
		"{{获取当前画布资产}}", string(assetJSON),
	)
	return replacer.Replace(template)
}

func defaultStoryboardPromptTemplate() string {
	codeFence := "```"
	return `你是分镜导演和 AI 媒体制作规划师。
请根据用户剧情 brief、用户要求和当前画布资产，生成可编辑并可分别用于图片与视频生成的分镜 JSON。

制作方法：
- 先识别项目已经指定的画风、媒介与制作形态，再分析故事目标、人物动机、冲突、信息变化和结尾；不要擅自改换媒介。
- 每个镜头先写 purpose 和 informationChange，再决定可见动作、空间、景别、构图、视点、时长和声音。
- startBoundary 与 endBoundary 分别记录主体位置、朝向、目光、双手、持物和可见状态；positions 至少包含一项。
- visualPrompt 只描述冻结在 startBoundary 的一个瞬间，不写动作过程、时间推进或 endBoundary。
- videoPrompt 只描述从 startBoundary 到 endBoundary 的有序变化，不重新发明人物身份、服装、道具或地点。
- camera 与 motion 使用符合项目媒介的表达；固定视点是有效设计，不为追求术语强行移动。
- timeBeats 覆盖完整 durationSeconds；durationSeconds 必须是 1 到 60 的整数。
- 没有台词、旁白、音效或本镜专属排除项时，对应字段返回空字符串。
- 需要保持角色、服装、道具、空间和风格连续；能复用画布资产时，在 assetTags 中引用现有标签。
- characters 必须逐角色输出，记录剧情定位、稳定识别点、当前服装/状态、持物与跨镜一致性，不要把多个角色合并成一项。
- assetTags 只能引用当前画布资产已有的标签或标签值；没有可复用资产时返回空数组。

用户 brief：
` + codeFence + `
{{剧情}}
` + codeFence + `

用户要求：
` + codeFence + `
{{用户要求}}
` + codeFence + `

当前画布资产：{{画布资产}}

格式：
{"title":"项目标题","logline":"一句话故事","styleGuide":"沿用项目画风、媒介、材质、光线、色彩和一致性规则","characters":["张三：男主角，稳定身份、当前服装与持物、剧情动机和跨镜一致性"],"locations":["场景身份、空间关系与当前状态"],"shots":[{"title":"镜头标题","description":"可见主体、空间关系、动作和结果","purpose":"观众此刻必须注意什么及为什么需要新镜头","informationChange":"镜头前已知状态 -> 镜头后新增信息或可见结果","startBoundary":{"positions":["张三站在门内左侧"],"facing":["面向走廊"],"gaze":["看向门外"],"hands":["右手扶门框"],"heldProps":["左手握旧怀表"],"visibleState":["门外尚无人出现"]},"endBoundary":{"positions":["张三仍在门内左侧"],"facing":["面向走廊"],"gaze":["视线落到来人身上"],"hands":["右手离开门框"],"heldProps":["左手仍握旧怀表"],"visibleState":["来人停在走廊尽头"]},"durationSeconds":8,"dialogue":"台词或旁白","shotSize":"能读清人物与门口空间关系的景别","emotion":"由等待转为警觉","lightingAndAtmosphere":"沿用项目设定的光线与氛围","audioEffects":"走廊脚步声","visualPrompt":"冻结在开始边界：张三位于门内左侧，右手扶门框，左手握旧怀表，门外尚无人出现；构图、材质与光线沿用项目画风","videoPrompt":"从开始边界出发，脚步声先出现，张三右手离开门框并抬眼，来人进入走廊尽头后停下，最终严格到达结束边界","camera":"符合项目媒介的稳定视点，保持门内与走廊方向可读","motion":"固定视点；主体和来人完成画内动作","timeBeats":"0-2秒：保持开始边界并出现脚步声；2-5秒：张三抬眼、来人进入；5-8秒：来人停下并保持结束边界","negativePrompt":"本镜专属风险；没有则为空字符串","assetTags":["角色:张三"]}]}

特别注意：
- 只返回 JSON，不要 markdown，不要解释。
- shots 输出 3 到 5 个；如果后续分镜制作契约指定了镜头数量，以契约为准。`
}
