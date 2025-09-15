$Binaries = @("conveyor-server", "conveyor-agent")
$OutputDir = "dist"

Write-Host "Starting build process..." -ForegroundColor Green

Write-Host "Cleaning up old builds..."
if (Test-Path $OutputDir) {
    Remove-Item -Recurse -Force $OutputDir
}
New-Item -ItemType Directory -Path $OutputDir | Out-Null

$targets = @(
    @{ OS = "linux"; Arch = "amd64"; GoArm = $null; Suffix = "linux-amd64"; Description = "Linux AMD64 (x64)" },
    @{ OS = "linux"; Arch = "arm64"; GoArm = $null; Suffix = "linux-arm64"; Description = "Linux ARM64" },
    @{ OS = "linux"; Arch = "arm";   GoArm = "7";    Suffix = "linux-armhf"; Description = "Linux ARMv7 (armhf)" }
)

$env:CGO_ENABLED = "0"

foreach ($binaryName in $Binaries) {
    Write-Host "--- Building $($binaryName) ---" -ForegroundColor Yellow
    $buildPath = "./cmd/$($binaryName)/"

    foreach ($target in $targets) {
        Write-Host "Building for $($target.Description)..." -ForegroundColor Cyan

        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        if ($null -ne $target.GoArm) {
            $env:GOARM = $target.GoArm
        }

        $outputPath = Join-Path $OutputDir "$($binaryName)-$($target.Suffix)"

        go build -ldflags="-s -w" -o $outputPath $buildPath

        if ($null -ne $target.GoArm) {
            Remove-Item env:GOARM
        }
    }
    Write-Host "--- Finished building $($binaryName) ---" -ForegroundColor Yellow
    Write-Host ""
}

Remove-Item env:CGO_ENABLED
Remove-Item env:GOOS
Remove-Item env:GOARCH

Write-Host "Build complete! All binaries are located in the '$OutputDir' directory." -ForegroundColor Green