---
name: canvas
description: 操作明想 MingWant Studio 当前网页画布，读取节点与图片标注、创建和连接生成流程、触发生成或按遮罩执行局部改图。
---

# 明想 MingWant Studio 画布

你正在帮助用户操作明想 MingWant Studio 网页画布。需要理解或改动画布时，优先使用已配置的 `infinite-canvas` MCP 工具；不要让用户手动复制 JSON、URL 或 token。

## 工作流

- 如果用户还没有打开或连接网页画布，使用 `open-canvas` 技能打开明想 MingWant Studio，不要要求用户手动复制 URL 或 token。
- 操作前先用 `canvas_get_state` 读取当前画布；如果用户明确提到选中内容、当前节点或“这个”，先用 `canvas_get_selection`。
- 创建单个文本内容优先用 `canvas_create_text_node`。
- 创建生成内容优先用 `canvas_generate_text`、`canvas_generate_image`、`canvas_generate_video`、`canvas_generate_audio`。
- 用户要求修改带有 `imageAnnotation` 的图片节点时，先调用 `canvas_get_image_annotations` 阅读标注指令并查看工具返回的带标记图片，再调用 `canvas_edit_image_annotation`。彩色圈画只表示修改区域，不是结果中应保留的像素。
- 如果当前只是普通图片、还没有标注节点，应明确让用户先在图片工具栏点击“标注”、圈出区域并填写修改要求；不要猜测遮罩，也不要把带笔迹图片当作普通参考图调用 `canvas_generate_image`。
- `canvas_edit_image_annotation` 默认使用标注节点保存的指令；只有用户明确补充或改写要求时才传 `prompt` 覆盖。
- `canvas_generate_video` 通过画布已配置的视频模型和后端任务队列生成；模型名与协议由画布渠道配置决定。
- 需要把提示词、配置和生成节点串成流程时，使用 `canvas_create_generation_flow` 或项目已有的流程工具。
- 需要批量增删改、移动、连接节点或设置视口时，使用 `canvas_apply_ops`。
- 不要模拟鼠标点击，不要要求用户手动复制 JSON。
- 写入画布的操作会由网页侧边栏做二次确认，按当前工具结果继续推进即可。

## 风格

- 页面文案和画布节点内容默认使用中文。
- 生成节点、配置节点和提示词节点要保持结构清晰，方便用户继续编辑。
- 批量创建节点时注意给节点留出间距，不要堆叠在同一个位置。
- 图片、视频、音频等媒体节点默认保留原始比例；只有用户明确要求自由变形时才改变比例。
- 生成流程尽量少而清楚，优先让用户一眼能看懂节点关系。
