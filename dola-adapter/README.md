# Dola New API Adapter

这个服务把已授权的 Dola 网页工作台包装成 MingWant Studio 可直接使用的 New API Channel 1 视频接口。

当前 MVP 支持：

- `POST /v1/videos`
- `GET /v1/videos/{task_id}`
- `GET /v1/models`
- 文本生视频：Seedance 2.5、比例、分辨率和时长
- 每个账号一个独立浏览器分区；支持 Adapter 管理浏览器或接管普通 Chrome 工作台
- 任务、`conversation_id`、`vid`、结果文件和账号租约持久化
- Dola 明确在提交前返回额度不足时自动切换下一个账号
- 账号明确耗尽额度后按每日刷新时间自动恢复，不会在刷新前重复尝试

当前不支持参考图片、参考视频或参考音频；这需要单独抓取并实现 Dola 的素材上传流程。

## 启动

需要 Node.js 20+，首次运行前在本目录安装 Playwright：

```powershell
npm install
```

建议显式配置以下环境变量：

```powershell
$env:DOLA_ADAPTER_API_KEY = "为 MingWant Studio 使用的随机密钥"
$env:DOLA_ADAPTER_ADMIN_KEY = "仅用于账号管理的另一把随机密钥"
$env:DOLA_ADAPTER_DATA_DIR = "F:\Documents\GitHub\mingwant-studio\.local\dola-adapter"
$env:DOLA_ADAPTER_PROFILE_ROOT = "F:\Documents\GitHub\mingwant-studio\.local\dola-adapter\profiles"
$env:DOLA_ADAPTER_RESULT_ROOT = "F:\Documents\GitHub\mingwant-studio\.local\dola-adapter\results"
$env:DOLA_ADAPTER_PUBLIC_BASE_URL = "http://127.0.0.1:8787"
$env:DOLA_QUOTA_TIME_ZONE = "Asia/Hong_Kong"
$env:DOLA_QUOTA_RESET_HOUR = "0"
$env:DOLA_HEADLESS = "false"
npm start
```

也可以复制 `dola-adapter/.env.example` 到启动脚本或进程管理器使用；Adapter 本身不自动读取 `.env` 文件。

Windows `cmd.exe` 使用 `.env` 启动：

```bat
cd /d F:\Documents\GitHub\mingwant-studio\dola-adapter
node --env-file=.env src/index.mjs
```

`state.json` 只保存账号和任务索引，不保存 `msToken`、`a_bogus` 或 Cookie 原文；登录态保存在每个账号自己的浏览器 Profile 中。不要把 `profiles/`、`state.json` 或 `results/` 提交到 Git，也不要把浏览器分区复制给不受信任的机器。

## 添加并登录账号

```powershell
$h = @{ Authorization = "Bearer $env:DOLA_ADAPTER_ADMIN_KEY"; "Content-Type" = "application/json" }
Invoke-RestMethod http://127.0.0.1:8787/internal/accounts -Method Post -Headers $h -Body '{"name":"Dola 账号 A","profileKey":"account-a"}'
```

网页工作台模式下，也可以为账号填写独立的本机 CDP 地址，例如 `http://127.0.0.1:9222`。不填写时使用 `.env` 的 `DOLA_BROWSER_CDP_URL`。

打开登录页：

```powershell
Invoke-RestMethod http://127.0.0.1:8787/internal/accounts/<account_id>/login -Method Post -Headers @{ Authorization = "Bearer $env:DOLA_ADAPTER_ADMIN_KEY" }
```

在弹出的可见浏览器中完成正常登录。登录成功后，把账号标记为可用：

```powershell
Invoke-RestMethod http://127.0.0.1:8787/internal/accounts/<account_id> -Method Patch -Headers $h -Body '{"state":"healthy"}'
```

Adapter 不自动处理验证码，也不通过伪造设备指纹、签名参数或批量注册账号来绕过 Dola 风控。只使用自己拥有或明确获授权的账号。

## 管理 UI

启动 Adapter 后，在本机浏览器打开 `http://127.0.0.1:8787/admin/`。输入 `DOLA_ADAPTER_ADMIN_KEY` 后，可以在页面中：

- 查看浏览器 Worker、账号状态、当前额度和每日刷新时间；
- 添加账号并为每个账号分配独立的 Profile Key；
- 点击“打开登录”启动或接管该账号的可见浏览器工作台，完成 Dola 正常登录；
- 将已登录账号标记为可用，或停用不参与调度的账号。

管理 Key 只在当前浏览器标签页的 `sessionStorage` 中使用。当前 UI 不提供删除账号，避免误删持久化登录分区；如需重新登录，直接打开对应账号的登录浏览器即可。若使用 `.env` 启动 Adapter，请执行 `node --env-file=.env src/index.mjs`，因为 `npm start` 默认不会自动加载 `.env`。

## 普通浏览器工作台模式

如果 Dola 使用 Google 登录时出现“目前无法登入帳戶 / 這個瀏覽器或應用程式可能不安全”，建议使用普通 Chrome 工作台，让你手动登录，再由 Adapter 通过 CDP 接管该页面。这样不需要手工维护 `msToken`、`a_bogus`、`fp` 或 Cookie，动态参数仍由 Dola 网页自身生成。

在 `.env` 中设置：

```env
DOLA_BROWSER_MODE=cdp
DOLA_BROWSER_CDP_URL=http://127.0.0.1:9222
```

关闭 Adapter 之外可能占用该端口的 Chrome，然后用 `cmd.exe` 启动一个专用 Profile。不要把日常 Chrome Profile 直接交给 Adapter：

```bat
"C:\Program Files\Google\Chrome\Application\chrome.exe" --remote-debugging-port=9222 --user-data-dir="%LOCALAPPDATA%\DolaAdapter\account-a" --no-first-run --no-default-browser-check "https://www.dola.com/"
```

Chrome 打开后，在该窗口手动完成 Dola 登录；Adapter 启动后进入 `http://127.0.0.1:8787/admin/`，点击账号的“打开登录”，再确认页面已经是已登录的 Dola 工作台，最后点击“标记可用”。

多个账号必须使用不同的 Profile 和端口，例如账号 B 使用 `9223` 与 `account-b`，然后在管理台给账号 B 设置 `http://127.0.0.1:9223`。同一个 CDP 地址不能绑定多个账号。

`DOLA_BROWSER_CHANNEL=chrome` 仍可用于 Adapter 自己启动浏览器的 `managed` 模式，但它不能保证 Google 接受自动化启动的登录流程；`cdp` 模式更接近人工网页工作台。Adapter 不注入隐藏自动化特征，也不绕过第三方登录安全检查。

## 调用示例

`newapi-channel-1` 使用 JSON 请求体：

```powershell
$headers = @{ Authorization = "Bearer $env:DOLA_ADAPTER_API_KEY"; "Content-Type" = "application/json"; "Idempotency-Key" = [guid]::NewGuid().ToString() }
$body = @{
    model = "dola-seedance-2.5"
    input = @{ prompt = "一只小猫在雨后的霓虹街道慢慢走过" }
    parameters = @{ ratio = "9:16"; resolution = "720P"; duration = 10 }
} | ConvertTo-Json -Depth 5
Invoke-RestMethod http://127.0.0.1:8787/v1/videos -Method Post -Headers $headers -Body $body
```

响应中的 `id` 通过 `GET /v1/videos/{id}` 轮询；状态为 `SUCCEEDED` 时，`object` 是可下载的 MP4 地址。

## MingWant Studio 配置

- 协议：`newapi-channel-1`
- Base URL：`http://127.0.0.1:8787`
- API Key：`DOLA_ADAPTER_API_KEY`
- 模型：`dola-seedance-2.5`

Base URL 填根地址，不要填 `/v1/videos`；MingWant Studio 会按协议补充路径。

如果 MingWant Studio 的 Backend 在 Docker 容器内运行，Base URL 和 `DOLA_ADAPTER_PUBLIC_BASE_URL` 必须填写 Backend 容器可访问的 Adapter 地址（例如宿主机网关地址或同一 Compose 网络服务名），不能照搬容器内的 `127.0.0.1`。Adapter 默认只监听回环地址；只有在确认网络边界和防火墙后，才把 `DOLA_ADAPTER_HOST` 改为可被 Backend 访问的地址。

## 状态和换号边界

Dola 返回 `SSE_ACK` 后，当前任务已经可能被接受，任务会固定绑定当前账号并继续用该账号查询会话历史。若聊天回复显示本次消耗后剩余 0 点，只会把账号标记为后续任务不可用，不会重新生成当前任务；视频完成或失败后账号仍保持额度耗尽状态，直到每日刷新点。

只有明确的“提交前额度不足”才会切换账号。超时、断网、5xx、验证码、登录失效或已收到 `SSE_ACK` 时不会静默换号，避免重复扣除额度。默认每日刷新点是 `Asia/Hong_Kong` 的 00:00，可用 `DOLA_QUOTA_TIME_ZONE` 与 `DOLA_QUOTA_RESET_HOUR` 调整；刷新后账号池会重新选入该账号。

Adapter 由真实浏览器生成 Dola 所需的动态请求参数，`msToken`、`a_bogus`、Cookie 和完整签名地址不写入 `state.json`。网页请求只在当前登录的浏览器 Profile 内发出；不要复制 Profile 或把管理 Key 暴露到公网。
