# Builds fynequery and all database plugins into dist/.
#
#   ./build.ps1            # build everything into dist/
#   ./build.ps1 -Clean     # remove dist/ first
#
# dist/
#   fynequery.exe          main app (windowsgui, no console)
#   plugins/
#     fq-plugin-postgres.exe
#     fq-plugin-mssql.exe
#     fq-plugin-oracle.exe
#     fq-plugin-sqlite.exe
#
# Copy dist/ anywhere; the app discovers plugins relative to its own exe.
# In dev you can also keep plugins in ./plugins next to the repo checkout.

param([switch]$Clean)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

if ($Clean -and (Test-Path dist)) { Remove-Item -Recurse -Force dist }
New-Item -ItemType Directory -Force -Path dist\plugins | Out-Null

$ldflags = '-s -w -H=windowsgui'

Write-Host '== fynequery.exe ==' -ForegroundColor Cyan
go build -trimpath -ldflags $ldflags -o dist\fynequery.exe .
if ($LASTEXITCODE -ne 0) { throw 'main build failed' }

$plugins = @(
    @{ Name = 'fq-plugin-postgres.exe'; Dir = 'plugins\postgres' },
    @{ Name = 'fq-plugin-mssql.exe';    Dir = 'plugins\mssql' },
    @{ Name = 'fq-plugin-oracle.exe';   Dir = 'plugins\oracle' },
    @{ Name = 'fq-plugin-sqlite.exe';   Dir = 'plugins\sqlite' }
)
foreach ($p in $plugins) {
    Write-Host "== $($p.Name) ==" -ForegroundColor Cyan
    go build -trimpath -ldflags '-s -w' -o "dist\plugins\$($p.Name)" ".\$($p.Dir -replace '\\','/')"
    if ($LASTEXITCODE -ne 0) { throw "$($p.Name) build failed" }
}

Write-Host ''
Write-Host 'Build complete:' -ForegroundColor Green
Get-ChildItem -Recurse dist | ForEach-Object {
    if (-not $_.PSIsContainer) {
        '{0,10:N1} MB  {1}' -f ($_.Length / 1MB), $_.FullName.Substring((Get-Location).Path.Length + 1)
    }
}
