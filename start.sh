#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

export GOMODCACHE="$PWD/.cache/gomod"
export GOCACHE="$PWD/.cache/gobuild"
export GOSUMDB=off

go run -buildvcs=false ./cmd/tideftp
