#!/bin/sh

set -e

[ -z "$1" ] && { echo "no version supplied"; exit 1; }

ORG=jwijenbergh
PROJECT_NAME=$(basename "$(pwd)")
PROJECT_VERSION="$1"

# check if there are unstaged changes
[ -z "$(git diff --quiet)" ] && { echo "there are unstaged changes, commit them first"; exit 1; }

# lint
make lint

# run gofumpt
make fmt

# commit
[ -z "$(git diff --quiet)" ] && { git add -u; git commit -m "Format: Run gofumpt"; }

# update version
sed -i "s/const version = \".*\"/const version = \"$1\"/" version.go
sed -i 's/const versionReleased = false/const versionReleased = true/' version.go
git add -u
git commit -m "Version: Update to $1"
