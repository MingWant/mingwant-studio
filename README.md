<p align="center">
  <img src="web/public/logo.svg" width="88" alt="明想 MingWant Studio logo">
</p>

<h1 align="center">明想 MingWant Studio</h1>

<p align="center">AI 电商广告与商业短视频生产工作台</p>

<p align="center">
  <a href="VERSION"><img src="https://img.shields.io/badge/version-v0.1.0-7c3aed?style=flat-square" alt="Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-f97316?style=flat-square" alt="License"></a>
</p>

明想 MingWant Studio 用一张可追踪的无限画布组织商业视频生产：导入客户提供的真人授权素材，锁定艺人身份与商品事实，再生成创意矩阵、分镜、首尾帧、视频、质检记录和交付清单。

当前首个垂直工作流面向“5 条约 1 分钟真人素材 → 15 条约 30 秒 AI 带货短视频”。原项目中的自由画布、节点与连线、短剧工作流、角色资产、3D 导演台、任务队列、素材库、多模型渠道、Canvas Agent 和管理后台均完整保留。

> 当前为派生项目首版，尚未完成构建、浏览器和生产部署验证。请先在本地或可信测试环境验收，再用于正式广告交付。

## 核心能力

- **无限画布与工作流**：文本、图片、视频、音频、分镜、配置、绘图、背板、连线、框选、撤销重做、小地图、导入导出与分享。
- **15 条电商模板**：一键创建项目 Brief、授权限制、身份锚点、商品事实、5×3 创意矩阵、15 份五镜头分镜、首尾帧母版、15 个成片槽位、QA 与交付清单。
- **真实感优先**：将人物身份、商品包装、Logo、口型、手部接触、物理连续性和广告宣称列为独立质量门禁。
- **多模型接入**：沿用系统渠道和用户自定义渠道，可接入文本、图片、视频与音频 API；任务通过后端异步队列执行并保留失败原因与用量。
- **素材与项目管理**：支持项目关联、角色与商品参考、私有 OSS 或后端资源存储、跨设备同步和版本记录。
- **Agent 协作**：保留网页助手、本地 Canvas Agent、MCP 与 Codex 插件，可读取当前画布并在确认后执行节点操作。
- **短剧与影视能力**：原结构化分镜、角色卡、画风、镜头批量生成、3D 导演台和视频编辑能力继续可用。

## 电商工作流

1. 确认项目 Brief、平台、受众、目标和交付规格。
2. 校验肖像、品牌、渠道、地区、期限和内容授权。
3. 从 5 条真人视频建立身份、声音、表情与动作锚点。
4. 锁定商品包装、规格、卖点证据、价格权益和禁用宣称。
5. 用 5 种创意形式 × 3 个卖点形成 15 条差异化方案。
6. 每条生成五个 6 秒镜头、合计约 30 秒的结构化分镜。
7. 先生成专属首帧、尾帧与镜头锚点，再生成 5 段视频并按顺序合并。
8. 执行身份、商品、口型、物理、宣称和投放规格质检。
9. 输出成片、封面、字幕、生成参数、成本与审批记录。

进入“画布”页面后点击“15 条带货模板”即可创建整套节点和连线。模板中的商品信息、授权限制、价格权益和口播均为待填写占位，不会替品牌方编造事实。

## 快速开始

零基础本地运行只需要 Docker Desktop。Windows PowerShell 在仓库根目录执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\start-local.ps1
```

脚本会先确认 Docker CLI、Docker Engine、Compose 和 Web 端口可用，再构建 Web 与 Backend、等待容器健康，并检查数据库、运行时协调器和 Worker。端口已被其他程序占用时会在创建容器前停止并显示进程；需要换端口可先执行 `$env:CANVAS_HTTP_PORT=3001`。首次健康启动还会记录 Compose 项目标识，后续目录名或项目名变化会在切换数据卷前停止。脚本不会自动启动 Docker Desktop，也不会删除数据卷。

如果看到 `dockerDesktopLinuxEngine` 或 `//./pipe/dockerDesktopLinuxEngine` 不存在，说明 Docker 命令已经安装，但 Docker Desktop 的 Linux Engine 还没有启动。先打开 Docker Desktop，等待显示 Engine running，再重新执行脚本；反复运行 Compose 不能修复这个状态。如果提示 `Access is denied` / `permission denied`，说明当前 Windows 账号没有访问 Docker Engine 的权限；确认 Docker Desktop 已完全启动，必要时加入 `docker-users` 组并重新打开 PowerShell。启动脚本在这两种情况下都不会创建、修改或删除容器和数据卷。

macOS、Linux 或需要手工执行时使用：

```bash
docker compose -f docker-compose.local.yml up -d --build --wait
docker compose -f docker-compose.local.yml ps
```

默认启动后访问 `http://localhost:3000`；使用 `CANVAS_HTTP_PORT` 时按脚本最终显示的地址访问。对应 `/api/health` 中的 `database`、`coordination` 和 `worker` 应全部为 `ok`。

首次使用在 `http://localhost:3000` 注册管理员，并在“系统渠道”或个人模型设置中配置 Base URL、API Key、视频模型和对应视频协议。视频生成统一通过已配置渠道进入后端任务队列。

Compose 继续复用原有 `backend-data` 后端卷。停止容器不会清除数据；不要使用 `docker compose down -v`，除非明确要同时删除后端数据。需要 Bun、Go 1.25 的源码开发方式请阅读[零基础快速开始](docs/content/docs/getting-started.mdx)；脱离 Docker 调试后端时必须复用 `.local/project-workbench-debug`，不能直接生成另一套 `backend/data` 账号数据。

服务器源码部署脚本不再默认拉取上游仓库。发布到你自己的 GitHub 仓库后，将示例中的“你的账号”替换为实际账号，先下载并审阅脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/你的账号/mingwant-studio/main/scripts/install-server.sh -o /tmp/mingwant-install.sh
less /tmp/mingwant-install.sh
sudo REPOSITORY_URL=https://github.com/你的账号/mingwant-studio.git bash /tmp/mingwant-install.sh
```

镜像部署还需设置同一发布版本的 `COMPOSE_URL`、`BACKUP_SCRIPT_URL`、`MINGWANT_BACKEND_IMAGE` 和 `MINGWANT_WEB_IMAGE`，防止误拉取不包含本项目改动的上游镜像或部署出缺少恢复入口的服务器。

服务器 Compose 默认只在 `127.0.0.1` 发布 Web 上游端口；公网访问应由同机 HTTPS 反向代理转发，不要把安装脚本输出的本机地址直接作为公网入口。

## 完整教程

第一次使用建议按顺序阅读：

1. [零基础快速开始](docs/content/docs/getting-started.mdx)：启动、创建管理员、配置模型、测活并完成第一条任务。
2. [完整案例：从 5 条真人素材到 15 条带货视频](docs/content/docs/commerce-video-complete-example.mdx)：从授权素材、创意矩阵和分镜一直做到 QA 与交付。
3. [管理部署手册](docs/content/docs/administration-deployment.mdx)：系统渠道、计费、存储、HTTPS、备份恢复、升级和故障处理。

全部功能、开发参考和待验证状态统一从[文档索引](docs/index.md)进入。

## 数据与安全

- 用户自定义 AI API Key 默认只保留在当前标签页会话；只有明确开启“在此设备记住”才写入独立的账号级浏览器密钥区。异步任务可能将所选渠道密钥加密后提交到自部署后端。仅使用可信部署，生产环境必须启用 HTTPS，并把原密钥另存于密码管理器。
- 画布和素材登录后同步到后端，本地 `localForage` 继续承担缓存及后端不可用时的降级存储。
- 启用 OSS 时媒体保存到私有 OSS，否则保存到后端数据目录；删除业务记录不会自动清理 OSS 远端对象。
- 电商模板中的合规清单不能替代品牌法务、平台规则审核或广告上线审批。
- 肖像素材、商品参考、合同边界与生成结果应按客户项目隔离存储，并设置访问、留存和删除策略。

## 项目结构

- `web/`：React、TypeScript、tldraw、Zustand、Ant Design 前端。
- `backend/`：Go、Gin、GORM、SQLite/PostgreSQL、Redis 后端。
- `canvas-agent/`：本地 Canvas Agent 与 MCP 服务；`canvas_generate_video` 调用画布已配置的视频模型。
- `plugins/infinite-canvas/`：Codex 插件；内部标识暂保留以兼容现有 MCP 工具名。
- `docs/`：功能索引、待验证项与派生说明。

## 许可证与上游归属

本项目是 [ddcat-ai/open-ai-canvas](https://github.com/ddcat-ai/open-ai-canvas) `v1.0.9` 的派生作品；该项目又基于 [basketikun/infinite-canvas](https://github.com/basketikun/infinite-canvas) `v0.5.0`（提交 `568f0f1838df8de31fe885a4e130e2f346dd14ab`）开发。`ddcat`、`basketikun`、`HouYunFei` 及其他上游作者对各自贡献保留权利与署名，详见 [NOTICE](NOTICE)。

本项目继续采用 [GNU Affero General Public License v3.0](LICENSE)。如果通过网络向用户提供修改后的软件功能，必须按 AGPL-3.0 向这些用户提供对应版本的完整源代码；分发时也必须保留许可证、版权和归属声明。

安全问题请按 [SECURITY.md](SECURITY.md) 的方式处理。功能状态见 [docs/index.md](docs/index.md)。
