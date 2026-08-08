---
name: mingwant-short-drama
description: 在明想 MingWant Studio 中继续或完成短剧、漫剧项目，把章节正文转成可确认的资产、结构化分镜、首尾帧、视频运动说明、视频生成任务和独立审查记录。用户提出开发或续写短剧、拆角色/场景/道具、生成资产参考图提示词、制作分镜或关键帧、编写视频提示词、调用已配置的视频模型、检查连续性或审查交付时使用；项目事实必须通过 infinite-canvas MCP 保存到当前制作项目，不建立第二套文件项目。
---

# 明想短剧制作

把创作判断写入当前 MingWant 项目，把可视化和媒体任务放到当前画布。项目数据库是事实来源，画布是编辑与执行界面。

## 每次请求的起点

1. 调用 `project_get_context`，确认当前画布关联的项目、项目类型、章节摘要、资产、镜头、引用和工作流。
2. 项目类型不是 `short-drama` 时停止写入并说明；不要把电商或自由画布伪装成短剧项目。
3. 用户指明章节时使用该章节；否则优先使用当前画布已关联的章节，再使用唯一未完成章节。存在多个候选且会改变产物归属时再询问。
4. 选定章节后调用 `project_get_unit` 读取完整 `sourceText` 和修订信息；章节摘要、画布文本和对话记忆都不能代替原文。
5. 比较项目现有事实和工作流状态，只推进用户所求环节的最小闭环；不要为了流程完整补造上游内容。
6. 写镜头前读取 [项目与镜头契约](references/project-contract.md)；做审查时读取 [审查契约](references/review-contract.md)。

不要创建 `short-drama.json`、`.short-drama/`、JSONL 项目目录或本地 WAL。不要让用户复制 JSON、URL 或 Token。

## 阶段路由

| 用户目标 | 负责阶段 | 主要工具 |
| --- | --- | --- |
| 读取、新建或修改章节正文 | 故事/剧本 | `project_get_unit`、`project_create_unit`、`project_update_unit` |
| 拆人物、地点、服装、道具 | 资产 | `project_extract_asset_candidates` |
| 检查、接受候选并建立可复用版本 | 资产 | `project_list_asset_versions`、`project_confirm_asset_candidate`、`project_upsert_asset_version` |
| 资产参考图或定点改图 | 图片 | `canvas_generate_image`、`canvas_get_image_annotations`、`canvas_edit_image_annotation` |
| 镜头表、首帧、尾帧、连续性 | 分镜 | `project_create_or_update_shots`、`project_link_shot_asset`、画布节点工具 |
| 动作、表演、运镜、声音 | 视频提示词 | 更新结构化镜头后使用画布节点工具 |
| 实际视频生成 | 已配置模型 API | `canvas_generate_video`；手动交付时停止在视频提示词 |
| 质量检查与交付结论 | 独立审查 | fresh reviewer；`project_update_workflow_step` 保存结论 |

工具在当前 Agent 版本中不存在时，不用近似工具伪造成功。保留候选内容，明确指出需要更新仓库版 Canvas Agent。

## 所有阶段共同约束

- 每类事实只有一个 owner：章节拥有剧情与逐字台词；资产版本拥有身份和可变状态；镜头拥有时长和起止边界；运动说明只实现边界；审查只拥有问题与结论。
- 用稳定的项目 ID、章节 ID、镜头 ID 和资产版本 ID 关联，不靠名称或模糊标签猜身份。
- 复用或新建资产版本前调用 `project_list_asset_versions` 读取既有定义；项目摘要里的标题、主版本 ID 和版本数不能代替版本事实。
- `pending_confirmation`、含糊代词、冲突版本和未知状态不能进入正式镜头绑定。
- 候选、创作者接受、独立审查和媒体生成成功是四件不同的事。Agent 写入候选后不得自行声称用户已经接受。
- 修改已存在事实前，先说明语义变化和受影响的下游镜头、提示词或任务；局部修改保留不相关内容。
- 写路径失败必须明确报告。不得以默认值、旧结果或画布节点状态掩盖项目 API 失败。
- 不调用未获用户授权的模型任务。涉及生图、视频或音频时，沿用画布的费用确认与二次批准。

## 从章节到资产

1. 用 `project_get_unit` 读取章节原文，不凭摘要或对话记忆重写。
2. 逐项提取实际出镜或制作必需的人物、地点、服装和道具；记录原文依据、稳定身份、临时状态、建议处理和未决项。
3. 调用 `project_extract_asset_candidates` 写入候选。不要在同一步自动确认。
4. 对可能复用的资产调用 `project_list_asset_versions`，比较稳定身份、临时状态和有效范围；再向用户展示建议复用、新版本、新资产和未决问题。只有用户接受后才调用 `project_confirm_asset_candidate`。
5. `project_confirm_asset_candidate` 创建新资产时已经建立首个已确认版本；先用 `project_list_asset_versions` 读回并复用，不要无条件再建重复版本。只有关联已有资产需要新状态，或初始版本确实缺少用户已接受的设定时，才调用 `project_upsert_asset_version`。
6. 新版本的 `definition` 对象保存身份锚点、状态差异、有效范围、连续性和通用图片提示词；用户已明确接受时传 `status: confirmed`，仍待讨论时使用 `draft` 或 `review`。后端保存的 `definitionJson` 是事实，`prompt` 是其可编辑投影；不要让用户手工序列化 JSON。

同一人物换装、伤污或天气状态通常是新版本，不是新人；镜头角度和瞬时姿势不是资产版本。

## 从资产到分镜

结构化分镜由当前 Agent 直接构造 `project_create_or_update_shots` 的工具参数，不要先调用 `canvas_generate_text` 或其他文本模型生成一大段 JSON 再解析。不要把 `definition` 序列化成字符串，也不要让用户复制 JSON。超过 5 个镜头时每批提交 3 到 5 镜；当前批次参数失败时按工具返回的字段路径修正，若错误说明此前已有镜头保存，先重新调用 `project_get_context` 核对项目事实，再从未保存镜头继续。

1. 先逐段确认原文怎样被镜头落实，遗漏对白、动作、画面文字和关键声音时不要先追求漂亮画面。
2. 每个镜头先写观众目的与信息变化，再决定构图、摄影机和时长。
3. 使用 [项目与镜头契约](references/project-contract.md) 构造 `definition`，调用 `project_create_or_update_shots`。镜头必须有来源、目的、起止边界；运动说明不得覆盖边界。
4. 对每个已接受资产版本调用 `project_link_shot_asset`。角色、地点、道具必须绑定准确版本；不要只写名称或 `assetTags`。
5. 需要画布编辑时，把项目镜头导入 Script 节点；关键帧只描述一个可冻结瞬间，视频提示词描述从起点到终点的变化。

图片提示词与视频提示词不得承担同一职责：

- 图片提示词：主体、稳定识别点、当前状态、冻结构图、材质、光线、文字政策和排除项。
- 视频提示词：起点、按顺序发生的动作、可见表演变化、摄影机、环境/声音、时间分配和终点核对。

## 提交视频生成

### 手动交付边界

用户选择手动交付、逐镜网页工作台或明确表示暂不调用视频模型时，阶段在“分镜图 + 视频提示词”结束。此时：

- 继续保存结构化镜头、已确认首帧和视频提示词；
- 可以调用画布的复制/导出提示词能力，生成可读文本供用户粘贴到外部网页工作台；
- 不调用 `canvas_generate_video`，不创建视频任务，也不把“已复制提示词”写成视频生成成功；
- 用户从外部工作台带回结果后，再由用户明确要求登记或审查。

手动交付模式不是费用确认的替代品。只要用户切换回自动/专业生成并明确要求提交，仍需走画布的模型、任务幂等和费用确认边界。

1. 只有用户要求生成时才调用 `canvas_generate_video`。
2. 使用画布已配置的视频模型和协议，不添加项目之外的供应商路由参数。
3. `prompt` 使用镜头已接受的通用视频提示词；`referenceNodeIds` 只包含本镜精确绑定的参考节点。
4. API 路由使用画布已配置的视频模型，不在工具参数中传 Base URL 或密钥；网页路由不选择账号、不绕过登录/验证码、不自动操作远端网页。
5. 任务成功和创作审查通过仍是两件事；生成结果回填后继续保留其镜头与资产来源。

## 独立审查

若当前上下文参与过目标内容创作，优先使用 fresh reviewer context，只传目标事实、已接受限制和 [审查契约](references/review-contract.md)。无法取得独立上下文时可以列问题，但结论只能是 `PROVISIONAL`。

审查者不修改章节、资产或镜头来源。把问题按 owner 分派；负责人修改后，使用新项目修订重新审查。用户接受结论后，才通过 `project_update_workflow_step` 将对应步骤标为 `completed`；未接受时使用 `review`。

## 结束回复

说明已写入的项目事实、仍待确认的候选、已创建或提交的媒体任务、受影响的下游内容和下一步最小动作。不要打印内部 Token、Cookie、绝对资料目录或代理凭据。

方法与规则改编自 `worldwonderer/drama-skills`（MIT），许可证见 [LICENSE](LICENSE)。
