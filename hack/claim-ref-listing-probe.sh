#!/usr/bin/env bash
# claim-ref-listing-probe.sh — rung-1 reproduction for the claim-ref listing
# process fan-out (specs/in-progress/026-batch-claim-ref-commit-reads.md).
#
# Seeds a temporary bare origin with PROBE_REFS refs/claims/success/... refs,
# puts a counting `git` shim first on the *server's* PATH, boots the git-rest
# binary against a clone of that origin, and GETs the claim-ref listing. The
# printed `cat-file` count comes from the shim's counter file and the ref count
# from the response body — neither is hard-coded. The script deliberately does
# not assert the `cat-file` count: the pre-fix build must still print its real
# count so the reviewer can compare the two builds.
#
# Discrimination (pre-fix vs post-fix). Build a binary from a clean
# origin/master checkout separately:
#     git worktree add /tmp/git-rest-prefix origin/master
#     (cd /tmp/git-rest-prefix && go build -o /tmp/git-rest-prefix-bin .)
# then run:
#     GIT_REST_BIN=/tmp/git-rest-prefix-bin bash hack/claim-ref-listing-probe.sh
# The pre-fix build prints "cat-file invocations: 300" (one `cat-file -p` per
# matching ref) and "refs returned: 300". The fixed build prints
# "cat-file invocations: 1". The mirror run
#     bash hack/claim-ref-listing-probe.sh --mirror
# prints "served_from=mirror" and still "cat-file invocations: 1".
#
# Requires only bash, git, go and curl. No Docker, kubectl or cluster.
set -euo pipefail

die() {
	echo "claim-ref-listing-probe: $*" >&2
	exit 1
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Mode: default is the live-origin path; --mirror exercises the claim mirror.
MODE="live"
case "${1:-}" in
"") ;;
--mirror | mirror) MODE="mirror" ;;
*) die "unknown argument '$1' (expected --mirror)" ;;
esac

PROBE_REFS="${PROBE_REFS:-300}"
printf '%s' "${PROBE_REFS}" | grep -Eq '^[1-9][0-9]*$' ||
	die "PROBE_REFS must be a positive integer (got '${PROBE_REFS}')"

# Allocate a free localhost port unless PORT was supplied. python3 gives the
# kernel's own free port; the RANDOM fallback keeps the script runnable on a
# host without python3 (bash 3.2 has $RANDOM).
if [ -n "${PORT:-}" ]; then
	port="${PORT}"
elif command -v python3 >/dev/null 2>&1; then
	port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
else
	port=$(( (RANDOM % 10000) + 40000 ))
fi
printf '%s' "${port}" | grep -Eq '^[0-9]+$' || die "PORT must be a number (got '${port}')"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/claim-ref-listing-probe.XXXXXX")"
origin="${tmp}/origin.git"
seed="${tmp}/seed"
work="${tmp}/work"
shim_dir="${tmp}/shim"
counter="${tmp}/git-argv.log"
server_log="${tmp}/server.log"
body="${tmp}/body.json"
mkdir -p "${shim_dir}"
: >"${counter}"

server_pid=""
# shellcheck disable=SC2317  # invoked indirectly by the EXIT trap
cleanup() {
	if [ -n "${server_pid}" ]; then
		kill "${server_pid}" 2>/dev/null || true
		wait "${server_pid}" 2>/dev/null || true
	fi
	rm -rf "${tmp}"
}
trap cleanup EXIT

# Binary under test: an explicit GIT_REST_BIN (for the pre-fix comparison) or a
# fresh build from this tree. `make build` builds an image/Job, not a local
# binary, so this probe uses `go build` and stays host-runnable.
if [ -n "${GIT_REST_BIN:-}" ]; then
	bin="${GIT_REST_BIN}"
	[ -x "${bin}" ] || die "GIT_REST_BIN '${bin}' is not executable"
else
	bin="${tmp}/git-rest"
	(cd "${REPO_ROOT}" && go build -o "${bin}" .) || die "go build failed"
fi

# --- seed a temporary bare origin -------------------------------------------
git init --bare -q "${origin}" || die "git init --bare failed"
git init -q "${seed}" || die "git init seed failed"
git -C "${seed}" config user.email probe@example.com
git -C "${seed}" config user.name claim-ref-probe
printf 'probe\n' >"${seed}/README.md"
git -C "${seed}" add README.md
git -C "${seed}" commit -q -m 'seed'
git -C "${seed}" push -q "${origin}" HEAD:main || die "push seed failed"
# Give the clone a branch: without this the bare origin's HEAD points nowhere
# and `git clone` leaves a detached, empty worktree.
git -C "${origin}" symbolic-ref HEAD refs/heads/main
git -C "${origin}" config user.email probe@example.com
git -C "${origin}" config user.name claim-ref-probe

# Claim commits sit on git's empty tree, exactly as pkg/git/claim_refs.go writes
# them. A small set of distinct commits is reused across the refs; the probe
# measures git process count, not commit count.
empty_tree="4b825dc642cb6eb9a060e54bf8d69288fbee4904"
commit_count=5
claim_shas=()
i=1
while [ "${i}" -le "${commit_count}" ]; do
	message="$(printf '{"cluster":"probe","task_id":"task-%d","lease_until":"2026-01-01T00:00:00Z","nonce":"nonce-%d"}' "${i}" "${i}")"
	sha="$(git -C "${origin}" commit-tree "${empty_tree}" -m "${message}")" ||
		die "git commit-tree failed"
	claim_shas+=("${sha}")
	i=$((i + 1))
done

# One update-ref --stdin invocation, not one per ref.
refs_input="${tmp}/update-ref.txt"
: >"${refs_input}"
i=1
while [ "${i}" -le "${PROBE_REFS}" ]; do
	idx=$(((i - 1) % commit_count))
	printf 'create refs/claims/success/task%d/phase%d/%d %s\n' \
		"${i}" "${i}" "${i}" "${claim_shas[${idx}]}" >>"${refs_input}"
	i=$((i + 1))
done
git -C "${origin}" update-ref --stdin <"${refs_input}" ||
	die "git update-ref --stdin failed"

git clone -q "${origin}" "${work}" || die "git clone failed"

# Live mode must stay on the live-origin path. The server performs a boot-time
# pull (syncOnStartup) which would populate and mark the claim mirror fresh even
# with a long pull interval, so park the upstream before boot: the startup pull
# then fails fast and the 1h puller never runs inside the probe window. The live
# listing itself (ls-remote/fetch by sha) needs no upstream, so it is unaffected.
# Mirror mode keeps the upstream so the 2s puller syncs the mirror.
if [ "${MODE}" = "live" ]; then
	branch="$(git -C "${work}" symbolic-ref --short HEAD)"
	git -C "${work}" config --unset "branch.${branch}.remote" 2>/dev/null || true
	git -C "${work}" config --unset "branch.${branch}.merge" 2>/dev/null || true
fi

# --- counting git shim -------------------------------------------------------
# Capture the real binary by absolute path BEFORE the shim directory reaches
# PATH. The shim must never call `command -v git` — that would resolve to
# itself and recurse.
real_git="$(command -v git)" || die "git not found on PATH"
[ -n "${real_git}" ] || die "git not found on PATH"
cat >"${shim_dir}/git" <<SHIM
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "${counter}"
exec "${real_git}" "\$@"
SHIM
chmod 0755 "${shim_dir}/git"

# --- boot the server ---------------------------------------------------------
pull_interval="1h"
pull_seconds=3600
if [ "${MODE}" = "mirror" ]; then
	pull_interval="2s"
	pull_seconds=2
fi
# The shim directory is prepended for the server process only, so this script's
# own git commands keep using the real binary.
PATH="${shim_dir}:${PATH}" "${bin}" \
	--repo="${work}" \
	--listen="127.0.0.1:${port}" \
	--pull-interval="${pull_interval}" \
	--pull-timeout=60s \
	>"${server_log}" 2>&1 &
server_pid=$!

wait_for_healthz() {
	local attempt=0
	while [ "${attempt}" -lt 150 ]; do
		if curl -fsS -o /dev/null "http://127.0.0.1:${port}/healthz" 2>/dev/null; then
			return 0
		fi
		if ! kill -0 "${server_pid}" 2>/dev/null; then
			echo "--- server log ---" >&2
			cat "${server_log}" >&2 || true
			die "server exited before /healthz became ready"
		fi
		attempt=$((attempt + 1))
		sleep 0.2
	done
	echo "--- server log ---" >&2
	cat "${server_log}" >&2 || true
	die "/healthz did not become ready"
}
wait_for_healthz

# --- GET the listing ---------------------------------------------------------
url="http://127.0.0.1:${port}/api/v1/refs?prefix=claims%2Fsuccess%2F"

if [ "${MODE}" = "live" ]; then
	# Truncate immediately before the single GET so the count belongs to this
	# request, not to boot or to a pull.
	: >"${counter}"
	curl -fsS "${url}" -o "${body}" || {
		echo "--- server log ---" >&2
		cat "${server_log}" >&2 || true
		die "GET ${url} failed"
	}
else
	# Give the 2s puller time to land at least one pull, then retry until the
	# listing is actually served from the mirror.
	sleep $((pull_seconds + 2))
	attempt=0
	while [ "${attempt}" -lt 15 ]; do
		attempt=$((attempt + 1))
		: >"${counter}"
		curl -fsS "${url}" -o "${body}" || {
			echo "--- server log ---" >&2
			cat "${server_log}" >&2 || true
			die "GET ${url} failed"
		}
		if grep -q 'served_from=mirror' "${server_log}" ||
			grep -q '"served_from":"mirror"' "${server_log}"; then
			break
		fi
		sleep 1
	done
fi

# --- derive the numbers from live data --------------------------------------
# grep -c exits 1 when nothing matches; `|| true` keeps that from aborting the
# script while still leaving "0" as the output.
catfile_invocations="$(grep -c '^cat-file' "${counter}" 2>/dev/null || true)"
[ -n "${catfile_invocations}" ] || catfile_invocations=0
refs_returned="$(grep -o '"ref":' "${body}" 2>/dev/null | wc -l | tr -d ' ' || true)"
[ -n "${refs_returned}" ] || refs_returned=0
served_from="$(grep -o 'served_from=[A-Za-z]*' "${server_log}" | tail -n 1 || true)"
if [ -z "${served_from}" ]; then
	served_from="$(grep -o '"served_from":"[A-Za-z]*"' "${server_log}" |
		tail -n 1 | sed -e 's/^"served_from":"//' -e 's/"$//' || true)"
fi
[ -n "${served_from}" ] || served_from="served_from=unknown"

echo "${served_from}"
echo "cat-file invocations: ${catfile_invocations}"
echo "refs returned: ${refs_returned}"

if [ "${refs_returned}" -eq "${PROBE_REFS}" ]; then
	exit 0
fi
die "expected ${PROBE_REFS} refs, got ${refs_returned}"
