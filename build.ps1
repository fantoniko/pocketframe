if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "Docker is not available in this shell. Start Docker Desktop and try again."
}

function Invoke-Docker {
    param(
        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    & docker @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

Write-Host "# Building container..."
$cacheBust = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
Invoke-Docker @("build", ".", "-t", "pocketbook", "--build-arg", "CACHEBUST=$cacheBust", "-f", "Dockerfile", "--progress=plain")

$containerId = $null
try {
    $containerId = docker create pocketbook
    if ($LASTEXITCODE -ne 0) {
        throw "docker create pocketbook failed with exit code $LASTEXITCODE"
    }
    $containerId = $containerId.Trim()

    New-Item -ItemType Directory -Force build -ErrorAction Stop | Out-Null

    Write-Host "# Copying built artifacts from the container..."
    Invoke-Docker @("cp", "${containerId}:/home/app/pocketframe.app", "build/pocketframe.app")
} finally {
    if ($containerId) {
        docker rm $containerId | Out-Null
    }
}
