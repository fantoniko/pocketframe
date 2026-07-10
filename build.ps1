if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "Docker is not available in this shell. Start Docker Desktop and try again."
}

function Invoke-Docker {
    param(
        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    & docker @Arguments 2>&1 | ForEach-Object {
        if ($_ -is [System.Management.Automation.ErrorRecord]) {
            $_.Exception.Message
        } else {
            $_.ToString()
        }
    }

    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

Write-Host "# Building container..."
Invoke-Docker @("build", ".", "-t", "pocketbook", "-f", "Dockerfile", "--progress=plain")

$containerId = $null
try {
    $containerId = Invoke-Docker @("create", "pocketbook")
    $containerId = $containerId.Trim()

    New-Item -ItemType Directory -Force build -ErrorAction Stop | Out-Null

    Write-Host "# Copying built artifacts from the container..."
    Invoke-Docker @("cp", "${containerId}:/home/app/pocketframe.app", "build/pocketframe.app")
    Invoke-Docker @("cp", "${containerId}:/home/app/sleep-probe.app", "build/sleep-probe.app")
} finally {
    if ($containerId) {
        Invoke-Docker @("rm", $containerId) | Out-Null
    }
}
