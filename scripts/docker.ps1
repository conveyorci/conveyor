$Timestamp = Get-Date -Format "yyyyMMdd-HHmmss"

try {
    $CommitHash = (git rev-parse --short HEAD).Trim()
}
catch {
    Write-Host "Warning: Could not determine git commit hash. Using 'dev'." -ForegroundColor Yellow
    $CommitHash = "dev"
}

$env:IMAGE_TAG = "$($Timestamp)-$($CommitHash)"
$env:DOCKER_REPO = "conveyor"

Write-Host "Building and tagging images as:" -ForegroundColor Green
Write-Host "  - $($env:DOCKER_REPO)/conveyor-server:$($env:IMAGE_TAG)"
Write-Host "  - $($env:DOCKER_REPO)/conveyor-agent:$($env:IMAGE_TAG)"
Write-Host ""

& docker-compose up --build -d

if ($LASTEXITCODE -ne 0) {
    Write-Host "Docker Compose failed to start." -ForegroundColor Red
} else {
    Write-Host ""
    Write-Host "Conveyor services are starting up!" -ForegroundColor Green
    Write-Host "You can view logs with: docker-compose logs -f"
    Write-Host "To stop the services, run: docker-compose down"
}

Remove-Item env:IMAGE_TAG
Remove-Item env:DOCKER_REPO