#!/usr/bin/env python3
"""PR contract, immutable release plans, and resumable draft publication.

Only publish mutates GitHub or git refs. All subprocess inputs are arguments,
never shell programs. Python's standard library is the only dependency.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

TITLE = re.compile(r"^(feat|fix|perf|refactor|revert|security|docs|test|ci|chore|build|style)(\([a-z0-9_-]+\))?(!)?: (?!.*\.$)[a-z0-9].*$")
VERSION = re.compile(r"v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
BREAKING = re.compile(r"^\s*BREAKING[ -]CHANGE:", re.M)
HEADINGS = ("Release note", "Compatibility", "Documentation", "Verification")
ASSETS = ("stackmon-linux-amd64", "stackmon-linux-arm64", "stackmon-darwin-arm64", "checksums.txt")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def run(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.PIPE).strip()


def git(*args):
    return run("git", *args)


def ancestor(sha, ref):
    return subprocess.run(["git", "merge-base", "--is-ancestor", sha, ref],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def repo_name():
    name = os.environ.get("GITHUB_REPOSITORY") or run("gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", name), "invalid repository")
    return name


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def sections(body):
    # Headings inside comments or fenced examples are not contract sections.
    body = re.sub(r"<!--.*?-->", "", body or "", flags=re.S)
    result = {}
    current = None
    fence = None
    for line in body.splitlines():
        marker = re.match(r"^\s*(`{3,}|~{3,})", line)
        if marker:
            token = marker[1]
            if fence is None:
                fence = token
            elif token[0] == fence[0] and len(token) >= len(fence):
                fence = None
            continue
        if fence:
            continue
        heading = re.match(r"^##\s+(.+?)\s*#*\s*$", line)
        if heading:
            current = heading[1]
            require(current not in result, f"duplicate section: {current}")
            result[current] = []
        elif current:
            result[current].append(line)
    return {k: "\n".join(v).strip() for k, v in result.items()}


def omitted(note):
    return bool(re.match(r"^None:\s*\S", note, re.I))


def validate_pr(event):
    pr = json.loads(Path(event).read_text())["pull_request"]
    title, body = pr["title"], pr.get("body") or ""
    require(TITLE.fullmatch(title), "title must be type(scope)!: lowercase summary without a trailing period")
    parts = sections(body)
    for name in HEADINGS:
        require(parts.get(name), f"missing or empty section: {name}")
        require(not re.search(r"<[^>]+>|\bTODO\b|\bTBD\b|^\s*\.\.\.\s*$", parts[name], re.M),
                f"replace placeholders in {name}")
    require(not re.search(r"^\s*(MSG|BODY|EOF)\s*$", body, re.M), "remove shell heredoc delimiter")
    note = parts["Release note"]
    require(not re.match(r"^None\b", note, re.I) or omitted(note), "use None: followed by an omission reason")
    breaking = bool(TITLE.fullmatch(title)[3])
    require(not BREAKING.search(body) or breaking, "BREAKING CHANGE requires ! in the title")
    compatibility = parts["Compatibility"]
    if breaking:
        require(BREAKING.search(compatibility) and not omitted(note),
                "breaking title requires a release note and BREAKING CHANGE: migration guidance in Compatibility")
    else:
        require(compatibility == "None.", "nonbreaking Compatibility must be None.; use ! for breaking changes")
    print("PR documentation contract passed")


def stable(tag):
    # v0.4 is the repository's historical two-component version.
    if tag == "v0.4":
        return (0, 4, 0)
    match = VERSION.fullmatch(tag)
    return tuple(map(int, match.groups())) if match else None


def tags_at(sha):
    return [(stable(t), t) for t in git("tag", "--merged", sha).splitlines() if stable(t) is not None]


def infrastructure(paths):
    return bool(paths) and all(p.startswith((".github/", "scripts/", "docs/agents/", "docs/research/"))
                               or p in ("AGENTS.md", "CONTEXT.md", ".workmux.yaml", ".gitignore") for p in paths)


def bump_for(subject, body, paths):
    if infrastructure(paths) or all(p.startswith(("docs/", "e2e/")) or p == "README.md" for p in paths):
        return "none"
    if re.match(r"^[a-zA-Z]+(?:\([^)]*\))?!:", subject) or BREAKING.search(body):
        return "breaking"
    kind = subject.split(":", 1)[0].split("(", 1)[0].lower()
    if kind in ("feat", "feature"):
        return "minor"
    if kind in ("doc", "docs", "test", "tests", "e2e", "ci", "chore", "build", "style"):
        return "none"
    return "patch"


def plan_release(notes_file, explicit):
    notes_file = Path(notes_file)
    sha = git("rev-parse", os.environ.get("GITHUB_SHA", "HEAD") + "^{commit}")
    require(ancestor(sha, "origin/main"), "release SHA must be on fetched origin/main (historical commits are supported)")
    run("gh", "auth", "status")
    repo = repo_name()
    tags = tags_at(sha)
    previous = max(tags)[1] if tags else ""
    core = stable(previous) if previous else (0, 0, 0)
    commits = git("rev-list", "--reverse", "--no-merges", f"{previous}..{sha}" if previous else sha).splitlines()
    changes = []
    for commit in commits:
        subject = git("show", "-s", "--format=%s", commit)
        body = git("show", "-s", "--format=%b", commit)
        paths = git("diff-tree", "--root", "--no-commit-id", "--name-only", "-r", commit).splitlines()
        level = bump_for(subject, body, paths)
        pages = json.loads(run("gh", "api", "--paginate", "--slurp",
                               f"repos/{repo}/commits/{commit}/pulls?per_page=100"))
        prs = {p["number"]: p for page in pages for p in page
               if p.get("merged_at") and p.get("merge_commit_sha") == commit and p.get("base", {}).get("ref") == "main"}
        require(len(prs) <= 1, f"ambiguous PR attribution for {commit}")
        pr = next(iter(prs.values()), None)
        parts = sections(pr.get("body", "") if pr else body)
        note = parts.get("Release note", "")
        legacy = not note
        if legacy:
            note = re.sub(r"^[a-zA-Z]+(?:\([^)]*\))?!?:\s*", "", subject)
            if pr:
                note = re.sub(rf"\s+\(#{pr['number']}\)$", "", note)
            print(f"release-plan: legacy title fallback for {commit[:8]}", file=sys.stderr)
        reason = "infrastructure only" if infrastructure(paths) else note if omitted(note) else ""
        changes.append(dict(sha=commit, subject=subject, body=body, paths=paths,
                            bump=level, included=not reason, reason=reason, legacy=legacy,
                            note=note, compatibility=parts.get("Compatibility", ""), pr=pr))
    rank = {"none": 0, "patch": 1, "minor": 2, "breaking": 3}
    bump = max((c["bump"] for c in changes), key=rank.get, default="none")
    if explicit:
        match = VERSION.fullmatch(explicit)
        require(match, "only stable versions vMAJOR.MINOR.PATCH are supported")
        version_core = tuple(map(int, match.groups()))
        require(version_core > core, "explicit version must be newer than the previous stable release")
    else:
        require(bump != "none", "nothing releasable; pass an explicit version to release anyway")
        major, minor, patch = core
        version_core = (major + 1, 0, 0) if bump == "breaking" else (major, minor + 1, 0) if bump == "minor" else (major, minor, patch + 1)
    version = "v" + ".".join(map(str, version_core))
    require(version not in git("tag", "--list").splitlines(), f"tag {version} already exists; reuse its saved plan to resume")
    lines = [f"# stackmon {version}", ""]
    if previous:
        lines += [f"Changes since {previous}.", ""]
    headings = {"breaking": "Breaking Changes", "minor": "Features", "patch": "Fixes and Improvements", "none": "Documentation and Other Changes"}
    for level, heading in headings.items():
        entries = [c for c in changes if c["included"] and c["bump"] == level]
        if not entries:
            continue
        lines += [f"## {heading}", ""]
        for c in entries:
            pr = c["pr"]
            link = f"[#{pr['number']}](https://github.com/{repo}/pull/{pr['number']})" if pr else f"[{c['sha'][:8]}](https://github.com/{repo}/commit/{c['sha']})"
            author = (pr.get("user") or {}).get("login") if pr else None
            attribution = f" ({link}" + (f", @{author}" if author else "") + ")"
            lines += ["- " + " ".join(c["note"].split()) + attribution]
            if level == "breaking" and c["compatibility"] and c["compatibility"] != "None.":
                lines += ["  " + " ".join(c["compatibility"].split())]
        lines.append("")
    if previous:
        lines += [f"[Full changelog](https://github.com/{repo}/compare/{previous}...{version})", ""]
    notes = "\n".join(lines)
    notes_file.write_text(notes)
    plan = dict(schema=1, repository=repo, sha=sha, previous_tag=previous, version=version,
                bump=bump, changes=changes, notes_sha256=digest(notes.encode()))
    plan["plan_sha256"] = digest(canonical(plan))
    notes_file.with_suffix(".json").write_text(json.dumps(plan, indent=2) + "\n")
    outputs(plan)


def outputs(plan):
    print(f"version={plan['version']}\nprevious_tag={plan['previous_tag']}\nsha={plan['sha']}")
    print(f"commits={len(plan['changes'])}\nprs={sum(c['pr'] is not None for c in plan['changes'])}")
    print(f"noted={sum(c['included'] for c in plan['changes'])}")


def verify(plan_file, notes_file, checkout=True):
    plan = json.loads(Path(plan_file).read_text())
    signature = plan.pop("plan_sha256")
    require(signature == digest(canonical(plan)), "plan content hash mismatch")
    require(plan["schema"] == 1 and plan["repository"] == repo_name(), "plan repository/schema mismatch")
    require(VERSION.fullmatch(plan["version"]) and plan["version"].startswith("v"), "invalid plan version")
    require(re.fullmatch(r"[0-9a-f]{40}", plan["sha"]), "invalid plan SHA")
    require(digest(Path(notes_file).read_bytes()) == plan["notes_sha256"], "release notes hash mismatch")
    require(ancestor(plan["sha"], "origin/main"), "planned SHA is no longer on origin/main")
    if checkout:
        require(git("rev-parse", "HEAD") == plan["sha"], "checkout does not match planned SHA")
    current_tags = [(v, t) for v, t in tags_at("origin/main") if t != plan["version"]]
    latest = max(current_tags)[1] if current_tags else ""
    require(latest == plan["previous_tag"], "release baseline changed; review a new plan")
    return plan


def read_release(repo, version):
    # GitHub's tag endpoint excludes drafts. The authenticated list includes
    # them, including a draft left behind by an interrupted publication.
    pages = json.loads(run("gh", "api", "--paginate", "--slurp", f"repos/{repo}/releases"))
    return next((release for page in pages for release in page
                 if release["tag_name"] == version), None)


def restore_plan(run_id):
    require(re.fullmatch(r"[1-9][0-9]*", run_id), "plan run ID must be a positive integer")
    repo = repo_name()
    source = json.loads(run("gh", "api", f"repos/{repo}/actions/runs/{run_id}"))
    workflow = json.loads(run("gh", "api", f"repos/{repo}/actions/workflows/release.yml"))
    require(source["workflow_id"] == workflow["id"] and source["event"] == "workflow_dispatch"
            and source["status"] == "completed", "plan must come from a completed Release workflow dispatch")
    with tempfile.TemporaryDirectory(prefix="stackmon-release-plan-") as directory:
        run("gh", "run", "download", run_id, "--repo", repo, "--name", "release-plan", "--dir", directory)
        plan_file = Path(directory) / "release-notes.json"
        notes_file = Path(directory) / "release-notes.md"
        plan = verify(plan_file, notes_file, checkout=False)
        require(plan["sha"] == source["head_sha"], "use the original planning run; artifact SHA must match its run")
        Path("release-notes.json").write_bytes(plan_file.read_bytes())
        Path("release-notes.md").write_bytes(notes_file.read_bytes())
    outputs(plan)


def publish(plan_file, notes_file, dist):
    plan = verify(plan_file, notes_file)
    version, sha, repo = plan["version"], plan["sha"], plan["repository"]
    dist = Path(dist)
    require({p.name for p in dist.iterdir()} == set(ASSETS), "dist must contain exactly the three binaries and checksums.txt")
    hashes = {name: digest((dist / name).read_bytes()) for name in ASSETS}
    expected_checksums = {f"{hashes[name]}  {name}" for name in ASSETS if name != "checksums.txt"}
    require(set((dist / "checksums.txt").read_text().splitlines()) == expected_checksums, "asset checksums mismatch")
    refs = git("ls-remote", "origin", f"refs/tags/{version}", f"refs/tags/{version}^{{}}").splitlines()
    remote = {line.split()[1]: line.split()[0] for line in refs}
    tag_sha = remote.get(f"refs/tags/{version}^{{}}", remote.get(f"refs/tags/{version}"))
    require(not tag_sha or tag_sha == sha, "remote tag points at a different SHA; never move it")
    release = read_release(repo, version)
    if release:
        require(tag_sha == sha and release["body"] == Path(notes_file).read_text()
                and not release["prerelease"] and release["tag_name"] == version,
                "existing release conflicts with the saved plan")
    if not tag_sha:
        local = git("tag", "--list", version)
        if local:
            require(git("rev-parse", version + "^{commit}") == sha, "local tag points at a different SHA")
        else:
            name = git("show", "-s", "--format=%an", sha)
            email = git("show", "-s", "--format=%ae", sha)
            git("-c", f"user.name={name}", "-c", f"user.email={email}", "tag", "-a", version, "-m", f"stackmon {version}", sha)
        git("push", "origin", f"refs/tags/{version}")
    if not release:
        run("gh", "release", "create", version, "--repo", repo, "--verify-tag", "--draft", "--title", version, "--notes-file", notes_file)
        release = read_release(repo, version)
    require(release is not None, "created draft could not be read; reuse the plan to retry")
    if release["draft"]:
        run("gh", "release", "upload", version, *(str(dist / n) for n in ASSETS), "--repo", repo, "--clobber")
        release = read_release(repo, version)
    require(release is not None, "release disappeared during upload")
    assets = {a["name"]: a for a in release["assets"]}
    require(set(assets) == set(ASSETS), "release asset set mismatch")
    for name in ASSETS:
        require(assets[name].get("digest") == "sha256:" + hashes[name]
                and assets[name]["size"] == (dist / name).stat().st_size,
                f"uploaded asset digest/size mismatch: {name}")
    if release["draft"]:
        run("gh", "release", "edit", version, "--repo", repo, "--draft=false", "--latest")
    print(f"Published and verified {version} at {sha}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    p = commands.add_parser("plan")
    p.add_argument("notes")
    p.add_argument("version", nargs="?", default=os.environ.get("INPUT_VERSION", ""))
    p = commands.add_parser("validate-pr")
    p.add_argument("event")
    p = commands.add_parser("restore-plan")
    p.add_argument("run_id")
    for command in ("verify", "publish"):
        p = commands.add_parser(command)
        p.add_argument("plan")
        p.add_argument("notes")
        if command == "publish":
            p.add_argument("dist")
        else:
            p.add_argument("--before-checkout", action="store_true")
    args = parser.parse_args()
    if args.command == "plan":
        plan_release(args.notes, args.version)
    elif args.command == "validate-pr":
        validate_pr(args.event)
    elif args.command == "restore-plan":
        restore_plan(args.run_id)
    elif args.command == "verify":
        outputs(verify(args.plan, args.notes, checkout=not args.before_checkout))
    else:
        publish(args.plan, args.notes, args.dist)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else str(error)
        print(f"release: {detail}", file=sys.stderr)
        sys.exit(1)
