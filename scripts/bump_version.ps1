$ErrorActionPreference = "Stop"

# bump_version.ps1 - 交互式记录版本、提交、创建并推送发布 tag
#
# 用法（在仓库根目录 PowerShell 中运行）：
#   .\scripts\bump_version.ps1

$rootDir = Split-Path -LiteralPath $PSScriptRoot -Parent
Set-Location $rootDir

& git diff --quiet
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error: 工作区有未提交改动，请先处理干净再运行。" -ForegroundColor Red
    exit 1
}

& git diff --cached --quiet
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error: 暂存区有未提交改动，请先处理干净再运行。" -ForegroundColor Red
    exit 1
}

$currentBranch = (& git branch --show-current).Trim()
if ($currentBranch -ne "master") {
    Write-Host "Error: 当前分支是 '$currentBranch'，请切换到 master 后再运行。" -ForegroundColor Red
    exit 1
}

$newVersion = $null
while ($true) {
    $raw = Read-Host "请输入新版本号 (格式 X.Y.Z，例如 1.7.8，输入 q 退出)"

    if ($raw -eq "q" -or $raw -eq "Q") {
        Write-Host "已取消"
        exit 0
    }

    $cleaned = $raw -replace '\s+', ''
    $cleaned = $cleaned -replace '。', '.'
    $cleanedChars = $cleaned.ToCharArray() | Where-Object { $_ -match '[0-9.]' }
    $newVersion = -join $cleanedChars

    if ($newVersion -match '^[0-9]+\.[0-9]+\.[0-9]+$') {
        break
    }

    Write-Host "Error: 版本号格式必须类似 1.7.8，请重新输入。" -ForegroundColor Red
}

$tagName = "v$newVersion"

& git rev-parse --verify --quiet "refs/tags/$tagName" *> $null
if ($LASTEXITCODE -eq 0) {
    Write-Host "Error: tag '$tagName' 已存在。" -ForegroundColor Red
    exit 1
}

$timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss zzz"
Add-Content -Path "release.log" -Value "$timestamp $tagName"

& git add release.log
& git commit -m "release: $tagName"
& git tag $tagName
& git push origin $currentBranch
& git push origin $tagName

Write-Host "已记录 release.log 并提交: release: $tagName" -ForegroundColor Green
Write-Host "已创建 tag: $tagName" -ForegroundColor Green
Write-Host "已推送分支: $currentBranch" -ForegroundColor Green
Write-Host "已推送 tag: $tagName" -ForegroundColor Green
