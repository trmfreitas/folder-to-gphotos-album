#!/usr/bin/env bash
set -euo pipefail

go vet ./...
go test -race -cover ./...
go build -o folder-to-gphotos-album ./cmd/folder-to-gphotos-album/
echo "Build and tests passed"
