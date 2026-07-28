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

需要 Bun、Go 1.25，以及可用的 OpenAI 兼容或项目已支持的模型渠道。

```bash
cd backend
go run ./cmd/server

# 另开终端
cd web
bun install
bun run dev
```

打开 `http://localhost:3000`，注册首个管理员账号，在“系统渠道”或个人模型设置中配置 API。后端默认监听 `:8080`；本地调试时请按 [AGENTS.md](AGENTS.md) 的数据目录约束启动，避免切换到另一套账号数据。

Docker 本地运行：

```bash
docker compose -f docker-compose.local.yml up -d --build
```

服务器源码部署脚本不再默认拉取上游仓库。发布到你自己的 Git 仓库后显式传入地址：

```bash
curl -fsSL https://你的仓库/raw/main/scripts/install-server.sh | sudo REPOSITORY_URL=https://你的仓库/mingwant-studio.git bash
```

镜像部署还需设置 `COMPOSE_URL`、`MINGWANT_BACKEND_IMAGE` 和 `MINGWANT_WEB_IMAGE`，防止误拉取不包含本项目改动的上游镜像。

## 数据与安全

- 用户自定义 AI API Key 保存在浏览器本地；异步任务可能将密钥加密后提交到自部署后端。仅使用可信部署，生产环境必须启用 HTTPS。
- 画布和素材登录后同步到后端，本地 `localForage` 继续承担缓存及后端不可用时的降级存储。
- 启用 OSS 时媒体保存到私有 OSS，否则保存到后端数据目录；删除业务记录不会自动清理 OSS 远端对象。
- 电商模板中的合规清单不能替代品牌法务、平台规则审核或广告上线审批。
- 肖像素材、商品参考、合同边界与生成结果应按客户项目隔离存储，并设置访问、留存和删除策略。

## 项目结构

- `web/`：React、TypeScript、tldraw、Zustand、Ant Design 前端。
- `backend/`：Go、Gin、GORM、SQLite/PostgreSQL、Redis 后端。
- `canvas-agent/`：本地 Canvas Agent 与 MCP 服务。
- `plugins/infinite-canvas/`：Codex 插件；内部标识暂保留以兼容现有 MCP 工具名。
- `docs/`：功能索引、待验证项与派生说明。

## 许可证与上游归属

本项目是 [ddcat-ai/open-ai-canvas](https://github.com/ddcat-ai/open-ai-canvas) `v1.0.9` 的派生作品；该项目又基于 [basketikun/infinite-canvas](https://github.com/basketikun/infinite-canvas) `v0.5.0`（提交 `568f0f1838df8de31fe885a4e130e2f346dd14ab`）开发。`ddcat`、`basketikun`、`HouYunFei` 及其他上游作者对各自贡献保留权利与署名，详见 [NOTICE](NOTICE)。

本项目继续采用 [GNU Affero General Public License v3.0](LICENSE)。如果通过网络向用户提供修改后的软件功能，必须按 AGPL-3.0 向这些用户提供对应版本的完整源代码；分发时也必须保留许可证、版权和归属声明。

安全问题请按 [SECURITY.md](SECURITY.md) 的方式处理。功能状态见 [docs/index.md](docs/index.md)。
