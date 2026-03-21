#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "usage: $0 <version>" >&2
  exit 1
fi

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]]; then
  echo "version must look like v0.1.0" >&2
  exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
  echo "working tree must be clean before releasing" >&2
  exit 1
fi

if git rev-parse "$VERSION" >/dev/null 2>&1; then
  echo "tag $VERSION already exists locally" >&2
  exit 1
fi

CURRENT_BRANCH="$(git branch --show-current)"
if [[ -z "$CURRENT_BRANCH" ]]; then
  echo "release must be run from a local branch, not a detached HEAD" >&2
  exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "gh is not authenticated" >&2
  exit 1
fi

if git ls-remote --tags --exit-code origin "refs/tags/$VERSION" >/dev/null 2>&1; then
  echo "tag $VERSION already exists on origin" >&2
  exit 1
fi

if gh release view "$VERSION" >/dev/null 2>&1; then
  echo "GitHub release $VERSION already exists" >&2
  exit 1
fi

./scripts/update-readme-install.sh "$VERSION"

GOCACHE="$ROOT/.gocache" go test ./...
./scripts/build-release.sh "$VERSION" >/dev/null

DIST_DIR="$ROOT/dist/$VERSION"

if ! git diff --quiet -- README.md; then
  git add README.md
  git commit -m "Prepare ${VERSION} release"
fi

git tag -a "$VERSION" -m "$VERSION"
git push origin "$CURRENT_BRANCH"
git push origin "$VERSION"

gh release create "$VERSION" "$DIST_DIR"/* --title "$VERSION" --verify-tag --generate-notes

echo "Release URL: https://github.com/$(./scripts/repo-slug.sh)/releases/tag/${VERSION}"
