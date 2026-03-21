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

./scripts/update-readme-install.sh "$VERSION"

GOCACHE="$ROOT/.gocache" go test ./...
./scripts/build-release.sh "$VERSION" >/dev/null

if ! git diff --quiet -- README.md; then
  git add README.md
  git commit -m "Prepare ${VERSION} release"
fi

git tag -a "$VERSION" -m "$VERSION"
git push origin "$CURRENT_BRANCH"
git push origin "$VERSION"

echo "pushed $VERSION"

RUN_ID=""
for _ in {1..12}; do
  RUN_ID="$(gh run list --workflow release.yml --limit 20 --json databaseId,headSha --jq ".[] | select(.headSha == \"$(git rev-parse HEAD)\") | .databaseId" | head -n1 || true)"
  if [[ -n "$RUN_ID" ]]; then
    break
  fi
  sleep 5
done

if [[ -n "$RUN_ID" ]]; then
  gh run watch "$RUN_ID" --exit-status
else
  echo "release workflow was not visible yet; watch it manually with: gh run list --workflow release.yml --limit 5"
fi

echo "Release URL: https://github.com/$(./scripts/repo-slug.sh)/releases/tag/${VERSION}"
