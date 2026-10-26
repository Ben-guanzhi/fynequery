# Runs the full verification gate: vet, build, tests with the race detector.
#
#   ./check.ps1              # everything
#   ./check.ps1 -Fast        # skip the race detector (quicker local loop)
#
# CI runs the same three steps (see .github/workflows/ci.yml).
#
# vet note: -unsafeptr=false skips the uintptr→unsafe.Pointer analyzer, which
# flags chrome_windows.go's Win32 message-lParam casts. Those conversions are
# mandated by the window-procedure ABI (the OS hands us a raw pointer as a
# uintptr); the file documents it inline.

param([switch]$Fast)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

Write-Host '== go vet ==' -ForegroundColor Cyan
go vet -unsafeptr=false ./...
if ($LASTEXITCODE -ne 0) { throw 'vet failed' }

Write-Host '== go build ==' -ForegroundColor Cyan
go build ./...
if ($LASTEXITCODE -ne 0) { throw 'build failed' }

Write-Host '== go test ==' -ForegroundColor Cyan
if ($Fast) {
    go test ./...
} else {
    go test -race ./...
}
if ($LASTEXITCODE -ne 0) { throw 'tests failed' }

Write-Host 'check: all green' -ForegroundColor Green
