$ErrorActionPreference = "Stop"

# bump_version.ps1 - 交互式创建发布 tag（不 push）
#
# 用法（在仓库根目录 PowerShell 中运行）：
#   .\scripts\bump_version.ps1

$rootDir = Split-Path -LiteralPath $PSScriptRoot -Parent
Set-Location $rootDir

$newVersion = $null
while ($true) {
    $raw = Read-Host "请输入新版本号 (格式 X.Y.Z.W，例如 1.7.8.0，输入 q 退出)"

    if ($raw -eq "q" -or $raw -eq "Q") {
        Write-Host "已取消"
        exit 0
    }

    $cleaned = $raw -replace '\s+', ''
    $cleaned = $cleaned -replace '。', '.'
    $cleanedChars = $cleaned.ToCharArray() | Where-Object { $_ -match '[0-9.]' }
    $newVersion = -join $cleanedChars

    if ($newVersion -match '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$') {
        break
    }

    Write-Host "Error: 版本号格式必须类似 1.7.8.0，请重新输入。" -ForegroundColor Red
}

$tagName = "v$newVersion"

& git rev-parse --verify --quiet "refs/tags/$tagName" *> $null
if ($LASTEXITCODE -eq 0) {
    Write-Host "Error: tag '$tagName' 已存在。" -ForegroundColor Red
    exit 1
}

& git tag $tagName

Write-Host "已创建 tag: $tagName" -ForegroundColor Green
Write-Host "请手动执行 push，例如：" -ForegroundColor Yellow
Write-Host "  git push origin $tagName" -ForegroundColor Yellow
