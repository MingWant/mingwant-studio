# 短剧审查契约

## 目录

- 审查独立性
- 规则分级
- 检查链路
- 结论格式

## 审查独立性

审查者必须重新读取项目当前字节和结构，不采用负责人的自检结论。若审查者参与过目标章节、资产、镜头或提示词的创作，使用 fresh context；做不到时记录 `self_check`，结论只能是 `PROVISIONAL`。

审查者只写问题、影响、修订结果和 owner，不直接修改来源。

## 规则分级

- `structural_invariant`：可直接证明的结构错误，例如无效 ID、缺少边界、时长算术错误；阻断。
- `reviewed_invariant`：需要结合原文判断的语义义务，例如改写动机、动作不可执行、连续性跳变；有证据时阻断。
- `craft_default`：通常有帮助的制作建议；说明影响，用户可覆盖。
- `taste_option`：画风、镜头节奏和表演口味；不能单独阻断。

不要把固定镜头数、统一时长、形容词数量或“电影感”当作结构门槛。

## 检查链路

按顺序核对：

```text
章节事实 -> 资产身份/版本 -> 镜头目的与起止边界
-> 冻结关键帧 -> 有序运动/声音 -> 下一镜开始状态
```

重点检查：

- 每个生产相关原文段落是否被落实、明确省略或保留为非视觉背景；
- 同一人物、地点和道具是否绑定准确版本，临时状态是否污染身份；
- 镜头是否有新的注意重点或信息变化，而不是重复剧情摘要；
- 开始/结束位置、目光、双手、持物、伤污、光线和可读文字是否连续；
- 图片提示词是否只有一个冻结瞬间；视频提示词是否按顺序实现边界而不改写它；
- 精确时间段总和是否等于 `durationMs`；
- 网页任务成功是否被误写成内容审查通过。

## 结论格式

把审查结果作为 `project_update_workflow_step.output` 传入；工具负责序列化。

```json
{
  "schemaVersion": "mingwant.short-drama.review/v1",
  "unitId": "<ProjectUnit.id>",
  "requestedMode": "independent_agent",
  "effectiveMode": "fresh_agent | self_check",
  "projectRevision": 1,
  "verdict": "APPROVE | APPROVE_WITH_NOTES | REVISE | PROVISIONAL",
  "reviewedShotIds": [],
  "findings": [
    {
      "id": "FIND-001",
      "classification": "structural_invariant | reviewed_invariant | craft_default | taste_option",
      "severity": "fatal | error | warning | note",
      "target": "章节/资产版本/镜头 ID 与字段",
      "evidence": "必要且有边界的证据",
      "impact": "对观众理解或制作的影响",
      "requiredChange": "负责人必须达到的结果，不代写正文",
      "owner": "story | assets | storyboard | video-prompts",
      "status": "open | resolved"
    }
  ]
}
```

存在未关闭的 `fatal` 或 `error` 时只能 `REVISE`。没有 fresh context 或存在未接受输入时只能 `PROVISIONAL`。
