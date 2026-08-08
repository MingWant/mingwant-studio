# 明想 MingWant Studio Codex 插件

这个插件把明想 MingWant Studio 的本地 Canvas Agent MCP 打包给 Codex app 使用，让 Codex 能打开本地画布、读取当前节点、创建内容并触发生成流程。

## 安装

> 明想 MingWant Studio 尚未上架 Codex 公共插件目录，直接搜索不会显示。请从本仓库自带的 marketplace 安装。

### AI 自动安装

把下面这段发给 Codex：

```text
请从你发布的 MingWant Studio 仓库安装 Codex 插件；如果尚未发布，可直接使用当前本地仓库。
请 clone 仓库到 ~/plugins/mingwant-studio，确认 .agents/plugins/marketplace.json 和
plugins/infinite-canvas/.codex-plugin/plugin.json 都存在。然后运行
codex plugin marketplace add ~/plugins/mingwant-studio，
再运行 codex plugin add infinite-canvas@infinite-canvas-local。
安装后请校验插件，并告诉我是否需要开启一个新对话来加载新技能和 MCP 工具。
```

### 手动安装

如果本机还没有仓库，先 clone：

```bash
mkdir -p ~/plugins
git clone <你的 MingWant Studio 仓库地址> ~/plugins/mingwant-studio
```

注册仓库 marketplace 并安装插件；如果使用已有仓库，请把路径替换为仓库的绝对路径：

```bash
codex plugin marketplace add ~/plugins/mingwant-studio
codex plugin add infinite-canvas@infinite-canvas-local
```

安装后建议开启一个新的 Codex 对话，让新的 skill 和 MCP 工具完整加载。

### 本仓库开发调试

如果你就在 MingWant Studio 仓库中调试插件，可以直接添加当前仓库。建议使用仓库绝对路径，避免 Codex 从其他工作目录解析失败：

```bash
cd /path/to/mingwant-studio
codex plugin marketplace add "$(pwd)"
codex plugin add infinite-canvas@infinite-canvas-local
```

## 使用

1. 新建 Codex 任务后说“打开明想 MingWant Studio”。
2. 插件会确认当前仓库的本地画布服务是否已运行；端口被占用时会检查进程归属，不会把其他项目的 `3000` 当作 MingWant Studio。
3. 确认或启动后，插件会直接打开新建画布 URL，并自动尝试连接本地 Agent。
4. 画布打开后，让 Codex 读取或操作当前画布。

常用提示：

```text
打开明想 MingWant Studio
读取当前画布并总结节点结构
根据选中节点创建一组生图提示词
使用明想短剧制作继续当前项目
```

### 短剧项目

插件内置 `mingwant-short-drama` Skill。它以 MingWant 后端项目为唯一事实源，按“分集正文 → 待确认资产 → 精确资产版本 → 结构化镜头 → 首尾边界 → 视频生成任务 → 独立审查”推进，不创建另一套 JSON/JSONL 项目目录。

结构化镜头会保留原文章节、观看目的、信息变化、资产版本、开始/结束边界和视频运动说明。分镜导入画布后，图片任务只接收开始冻结帧，视频任务只实现开始边界到结束边界，并统一通过画布已配置的模型渠道和后端任务队列生成。若在画布顶部选择“手动交付”，流程会停在分镜图和视频提示词，复制可读提示词后由用户到网页工作台逐镜提交，不创建视频任务。

可直接说：

```text
使用 $mingwant-short-drama 读取当前短剧项目，从下一可执行阶段继续。
把当前分集拆成待确认资产和结构化镜头，不要开始生成。
把已确认镜头通过画布当前配置的视频模型提交生成，完成后进入独立审查。
```

## 工作机制

插件默认通过以下命令启动 MCP，并会在 MCP 启动时自动尝试拉起本地 Agent：

```bash
npx -y @ddcat666/open-ai-canvas-agent mcp
```

插件配置当前仍调用已发布的上游 Agent 包。仓库源码已经包含短剧项目工具；在对应 Agent 新版本发布并同步更新 `.mcp.json` 前，本地开发必须让 HTTP Agent 与 Codex MCP 入口都运行本仓库 `canvas-agent` 源码或构建产物，仅替换网页端 Agent 不会扩展旧 MCP 的工具清单。旧 npm 包缺少项目工具时 Skill 会明确停止写入，不能用画布节点伪装项目保存成功。

## 手动排查

优先本地启动画布：

```bash
cd web
bun install
bun run dev
```

然后启动本地 Agent。端口不是 `3000` 时，把 `CANVAS_URL` 换成真实本地画布地址：

```bash
CANVAS_URL=http://localhost:3000 npx -y @ddcat666/open-ai-canvas-agent
```

手动排查时从 Agent 启动输出读取本地地址和 token，然后直接打开 `<画布网页地址>/canvas?mode=new#agentUrl=<URL 编码后的 Local URL>&agentToken=<URL 编码后的 Connect token>`。公开的 `/config` 只返回地址和是否已配置 Token，不返回 Token 正文；`#` 后的凭据也不会发送给画布服务器。不要通过页面点击来新建画布；`mode=new` 会让网页自动创建具体画布并连接本地 Agent。
