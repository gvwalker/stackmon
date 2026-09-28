#!/usr/bin/env bash
# Plans a stackmon release: picks the next version tag and writes the release
# notes for it. Creates nothing -- the caller tags, builds, and publishes.
#
# Usage: release-plan.sh <notes-file> [explicit-version]
#
#   release-plan.sh release-notes.md            # derive the version from commits
#   release-plan.sh release-notes.md v0.6.0     # release exactly this version
#
# Prints `key=value` lines on stdout for the caller to append to $GITHUB_OUTPUT.
# Progress and the plan itself go to stderr, so stdout stays machine-readable.
#
# Environment:
#   GITHUB_SHA    commit to release (default: HEAD)
#   GITHUB_REPOSITORY  owner/repo to read merged PRs from (default: gh's view of
#                      the current checkout)
set -euo pipefail

notes_file=${1:?usage: release-plan.sh <notes-file> [explicit-version]}
explicit=${2:-${INPUT_VERSION:-}}
sha=${GITHUB_SHA:-$(git rev-parse HEAD)}
repo=${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}

# A release must come from a commit main can serve. Refuse to tag a commit that
# only exists on a branch, or one main has moved past.
if git rev-parse --verify --quiet origin/main >/dev/null; then
  if ! git merge-base --is-ancestor "$sha" origin/main; then
    echo "release-plan: $sha is not on main; refusing to release it" >&2
    exit 1
  fi
fi

# The previous release is the nearest tag reachable from the commit being
# released. A repository with no tags releases from its entire history.
previous_tag=$(git describe --tags --abbrev=0 "$sha" 2>/dev/null || true)
if [ -n "$previous_tag" ]; then
  range="$previous_tag..$sha"
  since=$(git log -1 --format=%cI "$previous_tag" | cut -dT -f1)
else
  range="$sha"
  since=1970-01-01
  echo "release-plan: no previous tag found; planning the first release" >&2
fi

# ---------------------------------------------------------------------------
# Version
# ---------------------------------------------------------------------------

# Classify a conventional-commit subject. Prints "breaking", "minor", "patch",
# or "none" for the bump it implies, or nothing when the subject is not a
# conventional commit at all.
classify() {
  local subject=$1 body=$2 spec type
  spec=$(printf '%s' "$subject" | sed -nE 's/^([A-Za-z]+)(\([^)]*\))?(!)?:.*/\1|\3/p')
  type=${spec%%|*}
  local bang=${spec#*|}

  if [ "$bang" = '!' ] || printf '%s' "$body" | grep -qE '^[[:space:]]*BREAKING[ -]CHANGE:'; then
    printf 'breaking'
    return
  fi
  case $(printf '%s' "$type" | tr '[:upper:]' '[:lower:]') in
    feat|feature) printf 'minor' ;;
    fix|bugfix|hotfix|perf|revert|security|refactor) printf 'patch' ;;
    doc|docs|test|tests|e2e|ci|chore|build|style) printf 'none' ;;
    # An unrecognised subject is not evidence that nothing shipped. Treat it as
    # a patch and say so, rather than silently swallowing the change.
    *) printf 'patch' ;;
  esac
}

if [ -n "$explicit" ]; then
  if ! printf '%s' "$explicit" | grep -qE '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
    echo "release-plan: '$explicit' is not a version like v1.2.3" >&2
    exit 1
  fi
  version=${explicit#v}
  version=v$version
else
  bump=''
  while read -r commit; do
    [ -n "$commit" ] || continue
    level=$(classify "$(git log -1 --format=%s "$commit")" "$(git log -1 --format=%b "$commit")")
    case "$level" in
      breaking) bump=major ;;
      minor) [ "$bump" = major ] || bump=minor ;;
      patch) [ -n "$bump" ] || bump=patch ;;
    esac
  done < <(git log "$range" --format=%H)

  if [ -z "$bump" ]; then
    echo "release-plan: nothing releasable since ${previous_tag:-the beginning};" >&2
    echo "release-plan: pass an explicit version to release anyway" >&2
    exit 1
  fi

  # Tags are normalised to full semver from here on: vMAJOR.MINOR.PATCH.
  core=${previous_tag#v}
  core=${core%%-*} # a prerelease tag bumps from its base version
  major=$(printf '%s' "$core" | cut -d. -f1); major=${major:-0}
  minor=$(printf '%s' "$core" | cut -s -d. -f2); minor=${minor:-0}
  patch=$(printf '%s' "$core" | cut -s -d. -f3); patch=${patch:-0}
  case $bump in
    major) version=v$((major + 1)).0.0 ;;
    minor) version=v$major.$((minor + 1)).0 ;;
    patch) version=v$major.$minor.$((patch + 1)) ;;
  esac
fi

if git rev-parse --verify --quiet "refs/tags/$version" >/dev/null; then
  echo "release-plan: tag $version already exists" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Release notes
# ---------------------------------------------------------------------------

# One section per kind of change, in the order a reader wants them.
sections=(breaking minor patch other)
declare -A section_body
declare -A section_heading=(
  [breaking]='Breaking Changes'
  [minor]='Features'
  [patch]='Fixes and Improvements'
  [other]='Other Changes'
)

# A merged PR belongs in the section its title's conventional prefix implies.
pr_section() {
  case $(classify "$1" '') in
    breaking) printf 'breaking' ;;
    minor) printf 'minor' ;;
    none) printf 'other' ;;
    patch) printf 'patch' ;;
  esac
}

# Merged PRs since the previous tag, oldest first. A PR counts only when its
# merge commit landed in the range being released, so a PR merged into another
# branch after the tag is not reported as part of this release.
pr_count=0
covered=$(mktemp)
pr_json=$(gh pr list --repo "$repo" --state merged --limit 200 \
  --search "merged:>=$since" \
  --json number,title,url,author,mergedAt,mergeCommit | jq -c 'sort_by(.mergedAt)[]')
while read -r pr; do
  [ -n "$pr" ] || continue
  merge_commit=$(printf '%s' "$pr" | jq -r '.mergeCommit.oid // ""')
  if [ -n "$merge_commit" ]; then
    # A shallow or unrelated history means "not in this release" rather than an
    # error, so ancestry checks stay quiet about a commit they cannot place.
    if ! git merge-base --is-ancestor "$merge_commit" "$sha" 2>/dev/null; then
      continue
    fi
    if [ -n "$previous_tag" ] &&
      git merge-base --is-ancestor "$merge_commit" "$previous_tag" 2>/dev/null; then
      continue
    fi
    printf '%s\n' "$merge_commit" >>"$covered"
  fi
  section=$(pr_section "$(printf '%s' "$pr" | jq -r '.title')")
  number=$(printf '%s' "$pr" | jq -r '.number')
  title=$(printf '%s' "$pr" | jq -r '.title')
  url=$(printf '%s' "$pr" | jq -r '.url')
  author=$(printf '%s' "$pr" | jq -r '.author.login // ""')
  section_body[$section]+="- [#$number]($url) $title (@$author)"$'\n'
  pr_count=$((pr_count + 1))
done <<<"$pr_json"

# Commits that no merged PR accounts for were pushed straight to main. They are
# still part of the release, so they are listed rather than dropped -- a release
# whose notes omit a shipped change is a worse changelog than an untidy one.
while read -r commit; do
  [ -n "$commit" ] || continue
  grep -qxF "$commit" "$covered" 2>/dev/null && continue
  subject=$(git log -1 --format=%s "$commit")
  [ -n "$subject" ] || continue
  section=$(classify "$subject" "$(git log -1 --format=%b "$commit")")
  # A docs- or ci-only commit is not releasable on its own, but it is still
  # part of the release, so it belongs under Other Changes.
  [ "$section" = none ] && section=other
  section_body[$section]+="- $subject"$'\n'
done < <(git log --no-merges --reverse "$range" --format=%H)
rm -f "$covered"

{
  printf '# stackmon %s\n\n' "$version"
  if [ -n "$previous_tag" ]; then
    printf 'Changes since %s.\n\n' "$previous_tag"
  fi
  for section in "${sections[@]}"; do
    [ -n "${section_body[$section]:-}" ] || continue
    printf '## %s\n\n' "${section_heading[$section]}"
    printf '%s\n' "${section_body[$section]}"
  done
} >"$notes_file"

commits_since=$(git rev-list --count "$range")

echo "release-plan: ${previous_tag:-no tag} -> $version ($commits_since commit(s), $pr_count merged PR(s))" >&2
echo "release-plan: notes written to $notes_file" >&2

printf 'version=%s\n' "$version"
printf 'previous_tag=%s\n' "$previous_tag"
printf 'commits=%s\n' "$commits_since"
printf 'prs=%s\n' "$pr_count"
