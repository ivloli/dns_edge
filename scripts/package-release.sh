#!/usr/bin/env bash
# Builds edgeapi/edgeadmin/dns-edge on THIS machine (where the private
# gitlab.gainetics.io edgecommon dependency is already resolvable) and
# collects the three self-contained tarballs into one release directory,
# ready to copy to a fresh machine.
#
# The fresh machine needs none of this box's setup — no Go toolchain, no
# private-repo SSH access, no `../edgecommon` sibling-checkout layout —
# because each tarball already contains a fully-linked binary. See each repo's
# Makefile `package` target for exactly what goes in.
#
# What this script does NOT do: it does not touch the target machine. Copy
# the output directory over (scp/rsync) and, on the target, per service:
#   1. extract the tarball
#   2. fill in configs/*.yaml from the shipped *.template.yaml (real
#      credentials/nodeId/secret — these are never in the tarball)
#   3. run the binary (see README.txt written into the release dir)
set -euo pipefail

EDGEAPI_DIR="${EDGEAPI_DIR:-/home/ivloli/Git_repo/edgeapi}"
EDGEADMIN_DIR="${EDGEADMIN_DIR:-/home/ivloli/Git_repo/edgeadmin}"
DNS_EDGE_DIR="${DNS_EDGE_DIR:-/home/ivloli/dns_dev}"
EXPECTED_BRANCH="${EXPECTED_BRANCH:-feature/ns-dns-edge}"

RELEASE_DIR="${RELEASE_DIR:-$DNS_EDGE_DIR/release-artifacts/$(date +%Y%m%d-%H%M%S 2>/dev/null || echo latest)}"

log()  { echo "==> $*"; }
fail() { echo "FAILED: $*" >&2; exit 1; }

check_branch() {
    local dir="$1" name="$2"
    local branch
    branch="$(git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")"
    [ "$branch" = "$EXPECTED_BRANCH" ] || fail "$name is on branch '$branch', expected '$EXPECTED_BRANCH'"
}

log "checking branches"
check_branch "$EDGEAPI_DIR" "edgeapi"
check_branch "$EDGEADMIN_DIR" "edgeadmin"
check_branch "$DNS_EDGE_DIR" "dns-edge"

install -d -m 755 "$RELEASE_DIR"

log "packaging edgeapi"
make -C "$EDGEAPI_DIR" package
cp "$EDGEAPI_DIR"/edge-api-test-env.tar.gz "$RELEASE_DIR/"

log "packaging edgeadmin"
make -C "$EDGEADMIN_DIR" package
cp "$EDGEADMIN_DIR"/edge-admin-test-env.tar.gz "$RELEASE_DIR/"

log "packaging dns-edge"
make -C "$DNS_EDGE_DIR" release-package
# Compute the exact tarball name (matches the Makefile's own RELEASE_TAG
# logic) instead of globbing dns-edge-*.tar.gz — old builds left stray
# tarballs of the same shape sitting in the repo root, and a glob would
# have swept those up too.
dns_edge_tag="$(git -C "$DNS_EDGE_DIR" describe --always --dirty --tags 2>/dev/null || date +%Y%m%d%H%M%S)"
dns_edge_tarball="dns-edge-linux-amd64-${dns_edge_tag}.tar.gz"
[ -f "$DNS_EDGE_DIR/$dns_edge_tarball" ] || fail "expected dns-edge tarball not found: $DNS_EDGE_DIR/$dns_edge_tarball"
cp "$DNS_EDGE_DIR/$dns_edge_tarball" "$RELEASE_DIR/"

cat > "$RELEASE_DIR/README.txt" <<'EOF'
dns-edge NS/DNS test-env release
=================================

Three tarballs, one per service. On the target machine, for each one:

  1. tar -xzf <name>.tar.gz -C /opt/<service>
  2. cd /opt/<service>/configs && cp X.template.yaml X.yaml, then fill in
     real values (DB credentials, nodeId/secret, listen ports) — the
     template files ship with placeholders only, never real credentials.
  3. Run the binary directly (nohup ./<binary> > run.log 2>&1 &) or wire it
     into your own service manager — these tarballs don't assume systemd.

Start order matters: edgeapi first (edgeadmin and dns-edge's edgeagent both
talk to its gRPC :8031), then edgeadmin and dns-edge in either order.

MySQL (db_edge) and the ip2region.xdb file (for dns-edge's geo routing) are
NOT part of this package — provision those separately on the target.
EOF

log "release artifacts in $RELEASE_DIR"
ls -la "$RELEASE_DIR"
