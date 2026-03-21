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

REPO_SLUG="$(./scripts/repo-slug.sh)"
TMP_FILE="$(mktemp)"
BLOCK_FILE="$(mktemp)"

cat >"$BLOCK_FILE" <<'EOF'
Current release: `__VERSION__`

Linux (`amd64`):

```bash
VERSION=__VERSION__; mkdir -p ~/.local/bin && curl -L https://github.com/__REPO__/releases/download/${VERSION}/yap_${VERSION#v}_linux_amd64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

Linux (`arm64`):

```bash
VERSION=__VERSION__; mkdir -p ~/.local/bin && curl -L https://github.com/__REPO__/releases/download/${VERSION}/yap_${VERSION#v}_linux_arm64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

macOS (Apple Silicon):

```bash
VERSION=__VERSION__; mkdir -p ~/.local/bin && curl -L https://github.com/__REPO__/releases/download/${VERSION}/yap_${VERSION#v}_darwin_arm64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

macOS (Intel):

```bash
VERSION=__VERSION__; mkdir -p ~/.local/bin && curl -L https://github.com/__REPO__/releases/download/${VERSION}/yap_${VERSION#v}_darwin_amd64.tar.gz | tar -xz -C /tmp && install -m 755 /tmp/yap ~/.local/bin/yap
```

Windows (`PowerShell`, `amd64`):

```powershell
$version='__VERSION__'; $dir="$HOME\AppData\Local\yap"; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Invoke-WebRequest "https://github.com/__REPO__/releases/download/$version/yap_$($version.TrimStart('v'))_windows_amd64.zip" -OutFile "$dir\yap.zip"; Expand-Archive -Path "$dir\yap.zip" -DestinationPath $dir -Force
```

Windows (`PowerShell`, `arm64`):

```powershell
$version='__VERSION__'; $dir="$HOME\AppData\Local\yap"; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Invoke-WebRequest "https://github.com/__REPO__/releases/download/$version/yap_$($version.TrimStart('v'))_windows_arm64.zip" -OutFile "$dir\yap.zip"; Expand-Archive -Path "$dir\yap.zip" -DestinationPath $dir -Force
```

After install, make sure the install directory is on your `PATH`:

- Linux/macOS: `~/.local/bin`
- Windows: `%USERPROFILE%\AppData\Local\yap`
EOF

sed "s|__VERSION__|$VERSION|g; s|__REPO__|$REPO_SLUG|g" "$BLOCK_FILE" > "$TMP_FILE"

awk -v block_file="$TMP_FILE" '
  /<!-- BEGIN GENERATED INSTALL -->/ {
    print
    while ((getline line < block_file) > 0) {
      print line
    }
    close(block_file)
    skip = 1
    next
  }
  /<!-- END GENERATED INSTALL -->/ {
    skip = 0
    print
    next
  }
  !skip { print }
' README.md > "${TMP_FILE}.out"

mv "${TMP_FILE}.out" README.md
rm -f "$TMP_FILE" "$BLOCK_FILE"
