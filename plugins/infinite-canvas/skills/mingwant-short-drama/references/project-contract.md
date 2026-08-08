# 项目与镜头契约

## 目录

- 项目事实归属
- 镜头定义
- 资产版本与画布引用
- 修改与失效

## 项目事实归属

| 事实 | MingWant owner |
| --- | --- |
| 项目画幅、画风、制作形态 | `Project` 与项目工作流输出 |
| 分集正文、逐字台词、剧情状态 | `ProjectUnit.sourceText` |
| 人物/地点/道具身份与版本 | `Asset`、`AssetVersion` |
| 镜头目的、时长、绑定、起止边界 | `Shot`、`Shot.definitionJson` |
| 首尾帧与生成结果 | `ShotAssetReference`、`AssetRepresentation`、画布媒体节点 |
| 阶段候选、接受和审查结论 | `WorkflowStepInstance.outputJson` |

画布 Storyboard 行是项目镜头的可编辑投影，不得以画布标题覆盖项目事实。

## 镜头定义

调用 `project_create_or_update_shots` 时，在每个镜头的 `definition` 中使用以下结构。删除不适用的可选字段，不写占位符。

```json
{
  "schemaVersion": "mingwant.short-drama.shot/v1",
  "shotCode": "EP001-SHOT001",
  "sourceRefs": [
    {
      "unitId": "<ProjectUnit.id>",
      "blockId": "SC001-A01",
      "role": "action | dialogue | sound | text | continuity",
      "unitUpdatedAt": "<ProjectUnit.updatedAt>"
    }
  ],
  "purpose": "观众此刻必须注意什么，以及为什么需要这个新镜头",
  "informationChange": "镜头前观众/人物知道什么 -> 镜头后发生什么变化",
  "assetBindings": [
    {
      "assetVersionId": "<AssetVersion.id>",
      "role": "subject | location | prop | wardrobe | reference"
    }
  ],
  "startBoundary": {
    "positions": [],
    "facing": [],
    "gaze": [],
    "hands": [],
    "heldProps": [],
    "visibleState": []
  },
  "endBoundary": {
    "positions": [],
    "facing": [],
    "gaze": [],
    "hands": [],
    "heldProps": [],
    "visibleState": []
  },
  "motionSpec": {
    "startAnchor": "只重述开始运动所需事实",
    "orderedSubjectMotion": [
      {
        "order": 1,
        "actor": "准确资产版本或画面主体",
        "trigger": "动作触发",
        "action": "可见动作",
        "pathOrContact": "方向、路径或接触",
        "result": "阶段结果"
      }
    ],
    "performanceArc": "触发 -> 可见处理 -> 决定 -> 落点",
    "camera": "固定或一次有动机的运动及终点",
    "environmentAndAudio": [],
    "timingPlan": "阶段或精确时间分配；精确时间之和等于镜头时长",
    "endReport": "怎样到达 endBoundary；只用于核对"
  },
  "imagePrompt": "冻结在 startBoundary 的通用图片提示词",
  "videoPrompt": "实现 startBoundary 到 endBoundary 的通用视频提示词",
  "negativePrompt": "只针对本镜具体风险的排除项"
}
```

`durationMs` 是镜头时长唯一权威。`motionSpec` 不得包含 `durationOverride`、`endOverride` 或下一镜写入字段。

## 资产版本与画布引用

- `definition.assetBindings` 和 `project_link_shot_asset` 使用同一个准确 `assetVersionId`。
- 项目引用证明业务绑定；`referenceNodeIds` 只负责让画布生成任务取得实际参考媒体，两者不能互相替代。
- 角色参考节点应保存 `workflowKind: character`、`characterAssetId`、`characterVersionPolicy: pinned` 和准确 `characterVersionId`。
- 地点、道具或服装没有可用表现资源时先生成/选择参考图，不用文字名称假装已经绑定媒体。
- 首帧、尾帧分别使用 `start_frame`、`end_frame`；普通参考使用 `reference`，最终结果使用 `output`。

## 修改与失效

发生以下变化时，将依赖镜头和审查视为待刷新：

- 章节来源的 `updatedAt` 与 `sourceRefs.unitUpdatedAt` 不同；
- 绑定的资产版本被替换；
- 镜头时长、目的、开始或结束边界变化；
- 图片/视频提示词不再由当前定义投影；
- 审查针对的是旧镜头定义或旧项目修订。

只更新受影响镜头。资产库新增无关记录不应使整集全部失效。
