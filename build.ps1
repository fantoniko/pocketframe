$ErrorActionPreference = "Stop"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Error "Docker is not available in this shell. Start Docker Desktop and try again."
}

Write-Host "# Building container..."
$cacheBust = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
docker build . -t pocketbook --build-arg "CACHEBUST=$cacheBust" -f Dockerfile

$containerId = $null
try {
    $containerId = docker create pocketbook
    New-Item -ItemType Directory -Force build | Out-Null

    Write-Host "# Copying built artifacts from the container..."
    docker cp "${containerId}:/home/app/pocketframe.app" "build/pocketframe.app"
} finally {
    if ($containerId) {
        docker rm $containerId | Out-Null
    }
}
