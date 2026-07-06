#!/usr/bin/env bash
# One-click local deploy for the dns-edge NS/DNS test environment: edgeapi,
# edgeadmin, dns-edge. Checks dependencies, confirms all repos are on the
# expected branch, then builds+(re)starts each service in dependency order
# via each repo's own `make deploy-local` (no sudo/systemd — PID-file based,
# see each Makefile).
#
# This targets THIS machine (the box you're running it on), not a remote
# host. To ship a fresh install to a different machine, build packages with
# scripts/package-release.sh instead and transfer those tarballs.
set -euo pipefail

# ── configurable paths (override via env if your checkout layout differs) ──
DNS_EDGE_DIR="${DNS_EDGE_DIR:-/home/ivloli/dns_dev}"
EDGEAPI_DIR="${EDGEAPI_DIR:-/home/ivloli/Git_repo/edgeapi}"
EDGEADMIN_DIR="${EDGEADMIN_DIR:-/home/ivloli/Git_repo/edgeadmin}"
EDGECOMMON_DIR="${EDGECOMMON_DIR:-/home/ivloli/Git_repo/edgecommon}"
EXPECTED_BRANCH="${EXPECTED_BRANCH:-feature/ns-dns-edge}"

MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-3306}"
MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-123456}"
MYSQL_DATABASE="${MYSQL_DATABASE:-db_edge}"
IP2REGION_XDB="${IP2REGION_XDB:-/home/ivloli/edge/static/ip2region.xdb}"

DNS_EDGE_HEALTHZ="${DNS_EDGE_HEALTHZ:-http://127.0.0.1:8080/healthz}"
EDGEADMIN_URL="${EDGEADMIN_URL:-http://127.0.0.1:7788/}"
TEST_DOMAIN="${TEST_DOMAIN:-test.local}"

log()  { echo "==> $*"; }
fail() { echo "FAILED: $*" >&2; exit 1; }

# ── 1. dependency checks ────────────────────────────────────────────────────

log "checking Go toolchain"
command -v go >/dev/null 2>&1 || fail "go not found in PATH — install Go first"
go version

log "checking MySQL connectivity ($MYSQL_HOST:$MYSQL_PORT/$MYSQL_DATABASE)"
command -v mysql >/dev/null 2>&1 || fail "mysql client not found in PATH"
mysql -h"$MYSQL_HOST" -P"$MYSQL_PORT" -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" "$MYSQL_DATABASE" \
    -e "SELECT 1" >/dev/null 2>&1 \
    || fail "cannot connect to MySQL at $MYSQL_HOST:$MYSQL_PORT as $MYSQL_USER, database $MYSQL_DATABASE"

log "checking ip2region xdb file"
[ -f "$IP2REGION_XDB" ] || fail "ip2region xdb not found at $IP2REGION_XDB (geo routing needs this — place it manually, this script won't fetch it)"

# ── 2. branch checks ─────────────────────────────────────────────────────────
# Refuses to switch branches for you — if a repo is on the wrong branch that's
# your call to make, not this script's.

check_branch() {
    local dir="$1" name="$2"
    [ -d "$dir" ] || fail "$name checkout not found at $dir"
    local branch
    branch="$(git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")"
    if [ "$branch" != "$EXPECTED_BRANCH" ]; then
        fail "$name is on branch '$branch', expected '$EXPECTED_BRANCH' — checkout the right branch first (git -C $dir checkout $EXPECTED_BRANCH)"
    fi
    log "$name: $dir @ $branch"
}

log "checking all repos are on $EXPECTED_BRANCH"
check_branch "$EDGECOMMON_DIR" "edgecommon"
check_branch "$EDGEAPI_DIR" "edgeapi"
check_branch "$EDGEADMIN_DIR" "edgeadmin"
check_branch "$DNS_EDGE_DIR" "dns-edge"

# ── 3. deploy in dependency order: edgeapi -> edgeadmin -> dns-edge ─────────
# edgeapi has to be up first (both edgeadmin and dns-edge's edgeagent talk to
# its gRPC :8031); edgeadmin and dns-edge don't depend on each other.

log "deploying edgeapi"
make -C "$EDGEAPI_DIR" deploy-local

log "deploying edgeadmin"
make -C "$EDGEADMIN_DIR" deploy-local

log "deploying dns-edge"
make -C "$DNS_EDGE_DIR" deploy-local

# ── 4. smoke test ────────────────────────────────────────────────────────────

log "smoke test: dns-edge healthz"
curl -sf "$DNS_EDGE_HEALTHZ" || fail "dns-edge healthz check failed ($DNS_EDGE_HEALTHZ)"
echo

log "smoke test: dig $TEST_DOMAIN A"
if command -v dig >/dev/null 2>&1; then
    dig @127.0.0.1 -p 5300 "$TEST_DOMAIN" A +short || echo "(no answer — check that $TEST_DOMAIN exists in edgeNSDomains)"
else
    echo "(dig not installed, skipping)"
fi

log "smoke test: edgeadmin reachable"
curl -sf -A "Mozilla/5.0" "$EDGEADMIN_URL" >/dev/null || fail "edgeadmin not reachable at $EDGEADMIN_URL"
echo "edgeadmin OK"

log "all services deployed and healthy"
echo "  edgeapi:   $(cat "$EDGEAPI_DIR/.run/edge-api.pid" 2>/dev/null || echo '?') (gRPC :8031)"
echo "  edgeadmin: $(cat "$EDGEADMIN_DIR/.run/edge-admin.pid" 2>/dev/null || echo '?') (HTTP :7788)"
echo "  dns-edge:  $(cat "$DNS_EDGE_DIR/.run/dns-edge.pid" 2>/dev/null || echo '?') (DNS :5300, API :8080)"
