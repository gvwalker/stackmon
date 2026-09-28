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

# gh exits 4 with a generic message when it has no token, which reads like a
# script bug rather than a missing credential. Say which one, before the first
# gh call can fail with it.
if ! gh auth status >/dev/null 2>&1; then
  echo 'release-plan: gh is not authenticated; set GH_TOKEN to read merged PRs' >&2
  exit 1
fi

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
# What counts as a change a user could notice
# ---------------------------------------------------------------------------

# The paths a commit changed, one per line.
commit_paths() {
  git show --name-only --format= "$1" 2>/dev/null | sed '/^$/d'
}

# True when every path is repository infrastructure rather than something a
# stackmon user runs or reads: the CI and release workflows, the release script
# itself, and the agent-facing docs. A change to how the project is built says
# nothing about the program it builds, so it has no place in the notes of a
# release, and does not by itself warrant one.
#
# Paths are checked rather than the conventional-commit type, because a title
# like "fix(release): ..." describes the wrong half of the story -- it is a fix
# to something that is not the program. It also means a PR touching a workflow
# and the code together still ships, and is kept.
#
# An empty list is not infrastructure: not knowing what a change touched is a
# reason to report it, not a reason to hide it.
is_infra() {
  [ $# -gt 0 ] || return 1
  local path
  for path in "$@"; do
    case $path in
      .github/* | scripts/release-plan.sh | AGENTS.md | CONTEXT.md | docs/*) ;;
      *) return 1 ;;
    esac
  done
  return 0
}

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
    # A commit that only rebuilds the project, whatever its type, ships nothing
    # new to a user. It must not turn a `fix(release)` into a patch release with
    # no release notes behind it. An explicit version overrides this.
    mapfile -t bump_files < <(commit_paths "$commit")
    if is_infra "${bump_files[@]}"; then
      continue
    fi
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
noted=0
covered=$(mktemp)
pr_json=$(gh pr list --repo "$repo" --state merged --limit 200 \
  --search "merged:>=$since" \
  --json number,title,url,author,mergedAt,mergeCommit,files |
  jq -c 'sort_by(.mergedAt)[]')
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
  # Changed how the project is built, not what it does. Counted as a PR, left
  # out of the notes, and reported so the omission is deliberate and visible.
  mapfile -t pr_paths < <(printf '%s' "$pr" | jq -r '.files[]?.path')
  pr_count=$((pr_count + 1))
  if is_infra "${pr_paths[@]}"; then
    printf 'release-plan: omitting #%s (%s): infrastructure only\n' \
      "$(printf '%s' "$pr" | jq -r '.number')" \
      "$(printf '%s' "$pr" | jq -r '.title')" >&2
    continue
  fi
  section=$(pr_section "$(printf '%s' "$pr" | jq -r '.title')")
  number=$(printf '%s' "$pr" | jq -r '.number')
  title=$(printf '%s' "$pr" | jq -r '.title')
  url=$(printf '%s' "$pr" | jq -r '.url')
  author=$(printf '%s' "$pr" | jq -r '.author.login // ""')
  section_body[$section]+="- [#$number]($url) $title (@$author)"$'\n'
  noted=$((noted + 1))
done <<<"$pr_json"

# Commits that no merged PR accounts for were pushed straight to main. They are
# still part of the release, so they are listed rather than dropped -- a release
# whose notes omit a shipped change is a worse changelog than an untidy one.
while read -r commit; do
  [ -n "$commit" ] || continue
  grep -qxF "$commit" "$covered" 2>/dev/null && continue
  subject=$(git log -1 --format=%s "$commit")
  [ -n "$subject" ] || continue
  mapfile -t commit_files < <(commit_paths "$commit")
  if is_infra "${commit_files[@]}"; then
    printf 'release-plan: omitting %s: infrastructure only\n' \
      "${commit:0:8} $subject" >&2
    continue
  fi
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

echo "release-plan: ${previous_tag:-no tag} -> $version ($commits_since commit(s), $noted of $pr_count merged PR(s) in the notes)" >&2
echo "release-plan: notes written to $notes_file" >&2

printf 'version=%s\n' "$version"
printf 'previous_tag=%s\n' "$previous_tag"
printf 'commits=%s\n' "$commits_since"
printf 'prs=%s\n' "$pr_count"
printf 'noted=%s\n' "$noted"
