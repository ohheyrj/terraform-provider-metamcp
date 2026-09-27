#!/usr/bin/env bash
# Build the MetaMCP provider and install it where dev_overrides expects it.
#
# Run this after any source change: Terraform reads the binary at the override
# path, so a stale binary silently means a stale schema.
#
#   ./scripts/install-local.sh [provider-source-dir]
#
# Then point Terraform at the directory it writes to, via a CLI config file:
#
#   provider_installation {
#     dev_overrides { "ohheyrj/metamcp" = "<INSTALL_DIR>" }
#     direct {}
#   }
#
set -euo pipefail

SRC="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
INSTALL_DIR="${METAMCP_PROVIDER_INSTALL_DIR:-$HOME/.local/share/terraform-provider-metamcp}"

cd "$SRC"

# mise supplies the pinned toolchain; fall back to whatever go is on PATH so the
# script still works outside a mise-managed shell.
if command -v mise >/dev/null 2>&1 && [ -f mise.toml ]; then
	GO=(mise exec -- go)
else
	GO=(go)
fi

mkdir -p "$INSTALL_DIR"

# -trimpath keeps the build reproducible. Without it the build directory is
# embedded, so two builds of identical source differ and "is my installed binary
# current?" cannot be answered by comparing the files. The version string is
# reported to the server's user agent, so a dev build says so rather than
# impersonating a release.
"${GO[@]}" build -trimpath -ldflags "-X main.version=dev" \
	-o "$INSTALL_DIR/terraform-provider-metamcp" .

echo "installed: $INSTALL_DIR/terraform-provider-metamcp"
echo
echo "Terraform CLI config for dev_overrides:"
echo
cat <<EOF
provider_installation {
  dev_overrides {
    "ohheyrj/metamcp" = "$INSTALL_DIR"
  }
  direct {}
}
EOF
