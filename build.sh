#!/bin/bash
set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
    echo "Error: docker is not available in this shell." >&2
    echo "Install Docker or enable Docker Desktop WSL integration for this distro." >&2
    exit 1
fi

echo "# Building container..."
docker build . -t pocketbook --build-arg CACHEBUST=$(date +%s) -f Dockerfile
CID=$(docker create pocketbook)
mkdir -p build
# copy build artifacts from the container
echo "# Copying built artifacts from the container..."
docker cp ${CID}:/home/app/pocketframe.app build/pocketframe.app
# Remove the container..."
docker rm ${CID}
