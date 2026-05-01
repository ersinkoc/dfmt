#!/usr/bin/env bash
# coverage-gate.sh — package-level coverage threshold check.
#
# Reads a Go coverage profile (default: cover.out), aggregates raw statement
# counts per package, and compares each tracked package against the threshold
# documented in CLAUDE.md / AGENTS.md.
#
# Usage:
#   scripts/coverage-gate.sh [profile] [--strict]
#
#   profile    Path to the coverage profile (default: cover.out).
#   --strict   Exit 1 when any tracked package is below target. Without this
#              flag the script is informational — it prints the table and
#              exits 0. The CI step starts informational; flip to --strict
#              once the suite reaches the documented thresholds.
#
# Why parse the raw profile instead of `go tool cover -func`:
#   The -func report rounds per-function percentages and aggregating those
#   loses precision. The raw profile gives integer statement counts, which
#   sum cleanly into a per-package ratio.
set -euo pipefail

profile="${1:-cover.out}"
strict=0
if [[ "${2:-}" == "--strict" ]]; then
    strict=1
fi

if [[ ! -f "$profile" ]]; then
    echo "coverage-gate: profile not found: $profile" >&2
    exit 2
fi

# Tracked packages and their thresholds, sourced from CLAUDE.md / AGENTS.md.
# Format: "<repo-relative-package>:<min-percent>"
TARGETS=(
    "internal/core:90"
    "internal/transport:85"
    "internal/daemon:80"
    "internal/cli:75"
)

# Aggregate covered / total statements per tracked package in a single awk
# pass. Sub-packages (e.g. internal/transport/sub) roll up into their tracked
# parent.
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

awk -v pkgs="$(IFS=,; echo "${TARGETS[*]}")" '
BEGIN {
    n = split(pkgs, arr, ",")
    for (i = 1; i <= n; i++) {
        split(arr[i], kv, ":")
        target_pkg[i] = kv[1]   # ordered list to keep output deterministic
        target_min[kv[1]] = kv[2]
        total[kv[1]] = 0
        covered[kv[1]] = 0
    }
    target_n = n
}
NR == 1 { next }  # mode: <atomic|set|count>
{
    # field layout: <import-path>/<file>:<range> <numStmts> <hitCount>
    file = $1
    sub(/:.*/, "", file)
    # Strip module prefix (github.com/<org>/<repo>/) so paths become repo-
    # relative. Three slashes deep covers the standard Go module layout.
    sub(/^[^\/]+\/[^\/]+\/[^\/]+\//, "", file)

    pkg = file
    sub(/\/[^\/]+$/, "", pkg)

    stmts = $2 + 0
    hits  = $3 + 0

    # Match against tracked packages — exact match or sub-package.
    for (i = 1; i <= target_n; i++) {
        p = target_pkg[i]
        if (pkg == p || index(pkg, p "/") == 1) {
            total[p] += stmts
            if (hits > 0) covered[p] += stmts
            break
        }
    }
}
END {
    for (i = 1; i <= target_n; i++) {
        p = target_pkg[i]
        t = total[p] + 0
        c = covered[p] + 0
        pct = (t > 0) ? (c * 100.0 / t) : 0
        printf "%s\t%d\t%d\t%.2f\t%d\n", p, c, t, pct, target_min[p]
    }
}
' "$profile" > "$tmp"

fail=0
printf "%-22s %10s %10s %9s %8s  %s\n" "PACKAGE" "COVERED" "TOTAL" "ACTUAL" "TARGET" "STATUS"
printf "%-22s %10s %10s %9s %8s  %s\n" "-------" "-------" "-----" "------" "------" "------"
while IFS=$'\t' read -r pkg covered total actual target; do
    if [[ "$total" == "0" ]]; then
        status="NO DATA"
    else
        below=$(awk -v a="$actual" -v t="$target" 'BEGIN{print (a+0 < t+0) ? 1 : 0}')
        if [[ "$below" == "1" ]]; then
            status="BELOW"
            fail=1
        else
            status="OK"
        fi
    fi
    printf "%-22s %10s %10s %8s%% %7s%%  %s\n" "$pkg" "$covered" "$total" "$actual" "$target" "$status"
done < "$tmp"

# Optional: emit a Markdown summary into GitHub Actions' step-summary if the
# environment exposes one. This shows up rendered in the run page sidebar.
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
        echo "### Coverage gate"
        echo
        echo "| Package | Covered | Total | Actual | Target | Status |"
        echo "|---|---:|---:|---:|---:|---|"
        while IFS=$'\t' read -r pkg covered total actual target; do
            if [[ "$total" == "0" ]]; then
                st="NO DATA"
            else
                below=$(awk -v a="$actual" -v t="$target" 'BEGIN{print (a+0 < t+0) ? 1 : 0}')
                [[ "$below" == "1" ]] && st="❌ below" || st="✅ ok"
            fi
            echo "| \`$pkg\` | $covered | $total | ${actual}% | ${target}% | $st |"
        done < "$tmp"
    } >> "$GITHUB_STEP_SUMMARY"
fi

if [[ "$fail" == "1" ]]; then
    if [[ "$strict" == "1" ]]; then
        echo
        echo "coverage-gate: one or more packages below target (strict mode)" >&2
        exit 1
    fi
    echo
    echo "coverage-gate: informational — not failing the build."
    echo "Flip to --strict once the targets are met."
fi
