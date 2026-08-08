[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if (Test-Path Variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}

function Resolve-HttpPort {
    param([string]$Root)

    $value = [Environment]::GetEnvironmentVariable("CANVAS_HTTP_PORT")
    if ([string]::IsNullOrWhiteSpace($value)) {
        $envFile = Join-Path $Root ".env"
        if (Test-Path -LiteralPath $envFile -PathType Leaf) {
            $line = Get-Content -LiteralPath $envFile | Where-Object { $_ -match '^\s*CANVAS_HTTP_PORT\s*=' } | Select-Object -Last 1
            if ($null -ne $line) {
                $value = ($line -replace '^\s*CANVAS_HTTP_PORT\s*=\s*', '').Trim()
                if ($value -match '^"([^\"]+)"\s*(?:#.*)?$' -or $value -match "^'([^']+)'\s*(?:#.*)?$") {
                    $value = $Matches[1]
                } else {
                    # Compose 仅把空白后的 # 当作行内注释；这里保持相同的常用写法。
                    $value = ($value -replace '\s+#.*$', '').Trim()
                }
            }
        }
    }
    if ([string]::IsNullOrWhiteSpace($value)) {
        $value = "3000"
    }
    [int]$parsedPort = 0
    if (-not [int]::TryParse($value, [ref]$parsedPort) -or $parsedPort -lt 1 -or $parsedPort -gt 65535) {
        throw "CANVAS_HTTP_PORT 必须是 1 到 65535 的整数，当前值：$value"
    }
    return $parsedPort
}

function Get-PortListenerDescription {
    param([int]$Port)

    if (Get-Command Get-NetTCPConnection -ErrorAction SilentlyContinue) {
        $connections = @(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)
        if ($connections.Count -eq 0) {
            return $null
        }
        $owners = @(
            foreach ($processIdValue in ($connections | Select-Object -ExpandProperty OwningProcess -Unique)) {
                try {
                    $process = Get-Process -Id $processIdValue -ErrorAction Stop
                    "$($process.ProcessName) (PID $processIdValue)"
                } catch {
                    "PID $processIdValue"
                }
            }
        )
        return ($owners -join "、")
    }

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Any, $Port)
    $started = $false
    try {
        $listener.Start()
        $started = $true
        return $null
    } catch {
        return "其他进程"
    } finally {
        if ($started) {
            $listener.Stop()
        }
    }
}

function Test-ComposeWebOwnsPort {
    param([string]$ComposeFile, [int]$Port)

    $containerIds = @(& docker compose -f $ComposeFile ps -q web 2>$null)
    if ($LASTEXITCODE -ne 0) {
        return $false
    }
    foreach ($containerId in $containerIds) {
        $containerId = $containerId.ToString().Trim()
        if ($containerId -notmatch '^[0-9a-f]{12,64}$') {
            continue
        }
        $publishedPorts = @(& docker port $containerId 3000/tcp 2>$null)
        $portSuffixPattern = ":" + [regex]::Escape($Port.ToString()) + '$'
        if ($LASTEXITCODE -eq 0 -and ($publishedPorts | Where-Object { $_.ToString().Trim() -match $portSuffixPattern })) {
            return $true
        }
    }
    return $false
}

function Show-ComposeDiagnostics {
    param([string]$ComposeFile)

    Write-Host "当前容器状态："
    & docker compose -f $ComposeFile ps
    Write-Host "最近的 Backend/Web 日志："
    & docker compose -f $ComposeFile logs --tail=80 backend web
    Write-Host "可继续查看：docker compose -f docker-compose.local.yml logs --tail=200 backend web"
}

function Resolve-ComposeProjectName {
    param([string]$ComposeFile)

    $configOutput = @(& docker compose -f $ComposeFile config --format json)
    if ($LASTEXITCODE -ne 0) {
        throw "无法解析 Compose 项目标识，请更新 Docker Compose 后重试。"
    }
    try {
        $config = (($configOutput | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine) | ConvertFrom-Json
    } catch {
        throw "无法读取 Compose 项目标识：$($_.Exception.Message)"
    }
    $projectNameProperty = $config.PSObject.Properties["name"]
    if ($null -eq $projectNameProperty -or [string]::IsNullOrWhiteSpace([string]$projectNameProperty.Value)) {
        throw "Compose 配置没有返回项目名称，请更新 Docker Compose 后重试。"
    }
    return ([string]$projectNameProperty.Value).Trim()
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$composeFile = Join-Path $repoRoot "docker-compose.local.yml"
$composeProjectMarker = Join-Path $repoRoot ".local\compose-project-name"
$httpPort = Resolve-HttpPort -Root $repoRoot
$healthUri = "http://127.0.0.1:$httpPort/api/health"

if (-not (Test-Path -LiteralPath $composeFile -PathType Leaf)) {
    Write-Error "找不到本地 Compose 文件：$composeFile"
    exit 1
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Error "未找到 docker 命令。请先安装并启动 Docker Desktop。"
    exit 1
}

$engineOutput = & docker info --format '{{.OSType}}|{{.ServerVersion}}' 2>&1
if ($LASTEXITCODE -ne 0) {
    $engineDetail = (($engineOutput | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine).Trim()
    if ($engineDetail -match '(?i)(access is denied|permission denied|not authorized)') {
        Write-Host "Docker CLI 已安装，但当前 Windows 终端没有访问 Docker Engine 的权限。"
        Write-Host "请确认 Docker Desktop 已完全启动；若仍提示权限错误，请将当前账号加入 docker-users 组后重新打开 PowerShell。"
        Write-Host "本次未执行 Compose，也没有创建、修改或删除容器和数据卷。"
    } else {
        Write-Host "Docker CLI 已安装，但 Docker Engine 尚未就绪。"
        Write-Host "请启动 Docker Desktop，等待左下角显示 Engine running 后再执行本脚本。"
    }
    if ($engineDetail) {
        Write-Host $engineDetail
    }
    exit 1
}
$engineInfo = (($engineOutput | ForEach-Object { $_.ToString() }) -join "").Trim()
$engineParts = $engineInfo -split '\|', 2
if ($engineParts.Count -ne 2 -or $engineParts[0] -ne "linux") {
    Write-Error "当前 Docker Engine 不是 Linux 容器模式。请在 Docker Desktop 中切换到 Linux containers。"
    exit 1
}
$composeOutput = & docker compose version --short 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Error "Docker Compose v2 不可用，请更新 Docker Desktop。"
    exit 1
}
$composeUpHelp = & docker compose up --help 2>&1
$composeUpHelpText = (($composeUpHelp | ForEach-Object { $_.ToString() }) -join "`n")
if ($LASTEXITCODE -ne 0 -or $composeUpHelpText -notmatch '(?m)^\s+--wait(?:\s|$)') {
    Write-Error "Docker Compose 版本过旧，不支持 --wait。请更新 Docker Desktop 后重试。"
    exit 1
}
$supportsWaitTimeout = $composeUpHelpText -match '(?m)^\s+--wait-timeout(?:\s|$)'

Write-Host "Docker Engine：Linux $($engineParts[1])"
Write-Host "Docker Compose：$($composeOutput -join '')"
Write-Host "Web 端口：$httpPort"

Push-Location $repoRoot
try {
    & docker compose -f $composeFile config --quiet
    if ($LASTEXITCODE -ne 0) {
        Write-Error "docker-compose.local.yml 校验失败，请先修正上方配置错误。"
        exit 1
    }

    $composeProjectName = Resolve-ComposeProjectName -ComposeFile $composeFile
    # Compose 项目名参与命名卷身份，必须在任何 up 操作前阻止静默切换。
    if (Test-Path -LiteralPath $composeProjectMarker -PathType Leaf) {
        $previousProjectName = (Get-Content -LiteralPath $composeProjectMarker -Raw).Trim()
        if (-not [string]::IsNullOrWhiteSpace($previousProjectName) -and $previousProjectName -ne $composeProjectName) {
            Write-Error "Compose 项目标识从 '$previousProjectName' 变成了 '$composeProjectName'。继续启动会挂载另一套数据卷，因此尚未创建或修改容器。请恢复原仓库目录名或设置 `$env:COMPOSE_PROJECT_NAME='$previousProjectName'；只有在备份旧卷并确认要创建独立全新实例后，才可手工移除 .local\compose-project-name。"
            exit 1
        }
    }
    Write-Host "Compose 项目标识：$composeProjectName"

    $portOwner = Get-PortListenerDescription -Port $httpPort
    $existingWebOwnsPort = Test-ComposeWebOwnsPort -ComposeFile $composeFile -Port $httpPort
    if ($null -ne $portOwner -and -not $existingWebOwnsPort) {
        Write-Error "端口 $httpPort 已被 $portOwner 占用，尚未创建或修改本项目容器。请停止占用程序，或先执行 `$env:CANVAS_HTTP_PORT=3001 再重新运行脚本。"
        exit 1
    }

    $upArguments = @("compose", "-f", $composeFile, "up", "-d", "--build", "--wait")
    if ($supportsWaitTimeout) {
        $upArguments += @("--wait-timeout", "600")
    }
    & docker @upArguments
    if ($LASTEXITCODE -ne 0) {
        Write-Host "启动未完成。"
        Show-ComposeDiagnostics -ComposeFile $composeFile
        exit 1
    }

    & docker compose -f $composeFile ps
    $health = $null
    $healthFailure = "未返回健康响应"
    $healthDeadline = [DateTimeOffset]::Now.AddSeconds(30)
    do {
        try {
            $candidate = Invoke-RestMethod -Uri $healthUri -TimeoutSec 10
            if ($candidate.data.status -eq "ok") {
                $health = $candidate
                break
            }
            $healthFailure = "运行状态不是 ok：$($candidate | ConvertTo-Json -Depth 6 -Compress)"
        } catch {
            $healthFailure = $_.Exception.Message
        }
        if ([DateTimeOffset]::Now -lt $healthDeadline) {
            Start-Sleep -Seconds 2
        }
    } while ([DateTimeOffset]::Now -lt $healthDeadline)
    if ($null -eq $health) {
        Show-ComposeDiagnostics -ComposeFile $composeFile
        Write-Error "容器已启动，但 30 秒内无法通过 $healthUri：$healthFailure"
        exit 1
    }
    Write-Host "运行检查通过：$($health.data.checks | ConvertTo-Json -Depth 4 -Compress)"

    $composeProjectMarkerDirectory = Split-Path -Parent $composeProjectMarker
    if (-not (Test-Path -LiteralPath $composeProjectMarkerDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $composeProjectMarkerDirectory -Force | Out-Null
    }
    Set-Content -LiteralPath $composeProjectMarker -Value $composeProjectName -Encoding Ascii -NoNewline

    Write-Host "MingWant Studio 已启动：http://127.0.0.1:$httpPort"
} finally {
    Pop-Location
}
