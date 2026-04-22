#!/usr/bin/env bash
set -euo pipefail

go vet ./...
go build -o folder-to-gphotos-album ./cmd/folder-to-gphotos-album/
go test -v ./cmd/folder-to-gphotos-album/
echo "Built: folder-to-gphotos-album"
