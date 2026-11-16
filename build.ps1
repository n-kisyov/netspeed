$ErrorActionPreference = "Stop"

Write-Host "Running unit tests..." -ForegroundColor Cyan
go test -v ./internal/... ./cmd/...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

Write-Host "Building bin\netspeed-cli.exe..." -ForegroundColor Cyan
go build -ldflags="-s -w" -o bin\netspeed-cli.exe .\cmd\netspeed-cli
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Building bin\netspeed-web.exe..." -ForegroundColor Cyan
go build -ldflags="-s -w" -o bin\netspeed-web.exe .\cmd\netspeed-web
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Building bin\netspeed.exe..." -ForegroundColor Cyan
go build -ldflags="-s -w -H=windowsgui" -o bin\netspeed.exe .\cmd\netspeed
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "`nBuild succeeded:" -ForegroundColor Green
Get-Item bin\netspeed.exe, bin\netspeed-cli.exe, bin\netspeed-web.exe | Format-Table Name, Length, LastWriteTime
