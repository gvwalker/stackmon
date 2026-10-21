#!/usr/bin/env python3
"""Hermetic command-level release scenarios; see RELEASE-TESTING.md."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
TRANSCRIPT = []
BODY = """Fix the observed failure.

## Release note
Concurrent enrollments now retain both successful changes.

## Compatibility
None.

## Documentation
README explains inventory locking.

## Verification
Both entries survive. Command: go test ./e2e -count=1.
Artifact: e2e/testdata/transcript.golden; regenerate with the same command.
"""
ASSETS = ["stackmon-linux-amd64", "stackmon-linux-arm64", "stackmon-darwin-arm64"]
FAKE_GH = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
p = pathlib.Path(os.environ['FAKE_STATE'])
s = json.loads(p.read_text())
a = sys.argv[1:]
s['calls'].append(a)
def save(): p.write_text(json.dumps(s))
def output(v): print(json.dumps(v)); save(); sys.exit(0)
if a[:2] == ['auth', 'status']: save(); sys.exit(0)
if a[0] == 'api':
    endpoint = next(x for x in a[1:] if x.startswith('repos/'))
    if '/commits/' in endpoint:
        sha = endpoint.split('/commits/')[1].split('/')[0]
        values = s['prs'].get(sha, [])
        output([values[:100], values[100:]])
    if '/actions/runs/' in endpoint: output(s['run'])
    if '/actions/workflows/' in endpoint: output({'id': 7})
    if '/releases/tags/' in endpoint:
        if s['release'] is None:
            save(); print('HTTP 404: Not Found', file=sys.stderr); sys.exit(1)
        output(s['release'])
if a[:2] == ['release', 'create']:
    s['release'] = {'draft': True, 'prerelease': False, 'tag_name': a[2],
                    'body': pathlib.Path(a[a.index('--notes-file')+1]).read_text(), 'assets': []}
    save(); sys.exit(0)
if a[:2] == ['release', 'upload']:
    if s.get('fail_upload'):
        s['fail_upload'] = False; save(); sys.exit(1)
    names = a[3:a.index('--repo')] if '--repo' in a else a[3:]
    for name in names:
        if name.startswith('--'): continue
        f = pathlib.Path(name)
        s['release']['assets'] = [x for x in s['release']['assets'] if x['name'] != f.name]
        s['release']['assets'].append({'name': f.name, 'size': f.stat().st_size,
          'digest': 'sha256:' + hashlib.sha256(f.read_bytes()).hexdigest()})
    save(); sys.exit(0)
if a[:2] == ['release', 'edit']:
    s['release']['draft'] = False; save(); sys.exit(0)
if a[:2] == ['run', 'download']:
    dest = pathlib.Path(a[a.index('--dir')+1])
    dest.mkdir(parents=True, exist_ok=True)
    for name, content in s['artifact'].items(): (dest / name).write_text(content)
    save(); sys.exit(0)
save(); print('unexpected gh call: ' + repr(a), file=sys.stderr); sys.exit(2)
'''


class ReleaseScenarios(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.repo = self.dir / "repo"
        self.repo.mkdir()
        self.env = dict(os.environ)
        for key in ("GITHUB_SHA", "INPUT_VERSION", "GITHUB_REPOSITORY"):
            self.env.pop(key, None)
        self.env.update(GITHUB_REPOSITORY="example/stackmon", PYTHONDONTWRITEBYTECODE="1")
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "Scenario")
        self.git("config", "user.email", "scenario@example.invalid")
        self.git("init", "--bare", "-q", str(self.dir / "remote"))
        self.git("remote", "add", "origin", str(self.dir / "remote"))
        self.base = self.commit("feat: initial", "cmd/app", "initial")
        self.git("tag", "v0.4")
        self.git("push", "-q", "origin", "main", "--tags")
        self.bin = self.dir / "bin"
        self.bin.mkdir()
        gh = self.bin / "gh"
        gh.write_text(FAKE_GH)
        gh.chmod(0o755)
        self.env["PATH"] = str(self.bin) + os.pathsep + self.env["PATH"]
        self.statefile = self.dir / "state.json"
        self.env["FAKE_STATE"] = str(self.statefile)
        self.save({"calls": [], "prs": {}, "release": None})

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, env=self.env,
                                       stderr=subprocess.PIPE, text=True).strip()

    def commit(self, title, path="cmd/app", content="changed", body=""):
        f = self.repo / path
        f.parent.mkdir(parents=True, exist_ok=True)
        f.write_text(content)
        self.git("add", ".")
        self.git("commit", "-q", "-m", title, "-m", body)
        sha = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/remotes/origin/main", sha)
        return sha

    def state(self):
        return json.loads(self.statefile.read_text())

    def save(self, state):
        self.statefile.write_text(json.dumps(state))

    def pr(self, sha, title="fix(inventory): retain changes", body=BODY, number=1):
        state = self.state()
        state["prs"][sha] = [{"number": number, "title": title, "body": body,
            "html_url": f"https://github.com/example/stackmon/pull/{number}",
            "user": {"login": "writer"}, "merged_at": "2026-10-21T12:00:00Z",
            "merge_commit_sha": sha, "base": {"ref": "main"}}]
        self.save(state)

    def command(self, *args, ok=True):
        p = subprocess.run(["python3", str(ROOT / "release.py"), *args], cwd=self.repo,
                           env=self.env, capture_output=True, text=True)
        if ok:
            self.assertEqual(p.returncode, 0, p.stderr)
        else:
            self.assertNotEqual(p.returncode, 0, p.stdout)
        return p

    def plan(self, *args, ok=True):
        p = self.command("plan", "notes.md", *args, ok=ok)
        return json.loads((self.repo / "notes.json").read_text()) if ok else p

    def dist(self):
        d = self.repo / "dist"
        d.mkdir()
        for name in ASSETS:
            (d / name).write_bytes(name.encode())
        (d / "checksums.txt").write_text("".join(
            f"{hashlib.sha256((d / n).read_bytes()).hexdigest()}  {n}\n" for n in ASSETS))

    def test_curated_plan_and_snapshot(self):
        sha = self.commit("fix(inventory): retain changes", body=BODY)
        self.pr(sha)
        plan = self.plan()
        self.assertEqual((plan["version"], plan["previous_tag"], plan["sha"]), ("v0.4.1", "v0.4", sha))
        notes = (self.repo / "notes.md").read_text()
        self.assertIn("Concurrent enrollments now retain both", notes)
        self.assertIn("[#1]", notes)
        self.assertIn("@writer", notes)
        self.assertIn("/compare/v0.4...v0.4.1", notes)
        self.assertNotIn("fix(inventory):", notes)
        self.assertEqual(len(self.state()["calls"]), 2)
        self.command("verify", "notes.json", "notes.md")
        (self.repo / "notes.md").write_text(notes + "edited")
        self.assertIn("notes", self.command("verify", "notes.json", "notes.md", ok=False).stderr)
        TRANSCRIPT.append("curated plan: v0.4 -> v0.4.1; linked prose, attribution, snapshot; edited notes refused")

    def test_immutable_breaking_classification_and_legacy(self):
        sha = self.commit("fix(api): change format (#1)", body="BREAKING CHANGE: migrate to the new format")
        self.pr(sha, title="docs: edited later", body="Old PR without release sections.")
        self.assertEqual(self.plan()["version"], "v1.0.0")
        self.assertIn("## Breaking Changes", (self.repo / "notes.md").read_text())
        self.assertNotIn("(#1) (", (self.repo / "notes.md").read_text())
        self.assertIn("legacy", (self.repo / "notes.json").read_text())
        TRANSCRIPT.append("body-only legacy breaking change: major bump and Breaking Changes agree despite edited PR title")

    def test_docs_inclusion_and_infra_omission(self):
        sha = self.commit("docs: explain usage", "docs/usage.md", "usage")
        self.pr(sha, title="docs: explain usage")
        self.plan(ok=False)
        self.commit("fix(release): process only", "scripts/release.py", "script")
        sha = self.commit("fix: runtime", content="fixed")
        self.pr(sha, body=BODY.replace("Concurrent enrollments now retain both successful changes.", "None: covered by another note."))
        plan = self.plan()
        self.assertEqual(plan["version"], "v0.4.1")
        self.assertIn("## Documentation", (self.repo / "notes.md").read_text())
        self.assertEqual(sum(x["included"] for x in plan["changes"]), 1)
        self.assertTrue(all(x.get("reason") for x in plan["changes"] if not x["included"]))
        TRANSCRIPT.append("docs notes do not bump; runtime omission keeps patch impact; infrastructure omitted with reasons")

    def test_range_attribution_and_pagination(self):
        sha = self.commit("fix: real change")
        self.pr(sha, body="legacy")
        state = self.state()
        wrong = dict(state["prs"][sha][0], merge_commit_sha=self.base, number=99)
        state["prs"][sha] = [wrong] * 205 + state["prs"][sha]
        self.save(state)
        self.plan()
        self.assertIn("[#1]", (self.repo / "notes.md").read_text())
        calls = self.state()["calls"]
        self.assertTrue(any("--paginate" in c and "--slurp" in c for c in calls))
        state = self.state()
        state["prs"][sha] = [dict(wrong, merge_commit_sha=None)]
        self.save(state)
        self.plan()
        self.assertIn(sha[:8], (self.repo / "notes.md").read_text())
        TRANSCRIPT.append("paginated commit attribution: mismatched/missing merge SHA excluded; direct commit remains visible")

    def test_target_and_version_policy(self):
        sha = self.commit("fix: runtime")
        for version in ("v0.3.9", "v0.4.0", "v0.5.0-rc.1", "v01.2.3"):
            self.plan(version, ok=False)
        self.git("tag", "unrelated-tag")
        self.assertEqual(self.plan()["previous_tag"], "v0.4")
        self.env["GITHUB_SHA"] = self.base
        self.plan(ok=False)
        self.env["GITHUB_SHA"] = sha
        self.git("update-ref", "refs/remotes/origin/main", self.base)
        self.plan(ok=False)
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        self.plan(ok=False)
        TRANSCRIPT.append("release target policy: unrelated tags ignored; older, existing-core, prerelease, malformed, branch-only and missing-main refused")

    def test_first_release_and_historical_main(self):
        self.git("tag", "-d", "v0.4")
        self.assertEqual(self.plan()["version"], "v0.1.0")
        sha = self.commit("fix: next")
        self.env["GITHUB_SHA"] = self.base
        self.assertEqual(self.plan()["sha"], self.base)
        self.assertNotEqual(sha, self.base)
        TRANSCRIPT.append("first release: v0.1.0; historical main commit supported with fixed SHA")

    def test_pr_contract(self):
        def validate(title, body, ok=True):
            event = self.repo / "event.json"
            event.write_text(json.dumps({"pull_request": {"title": title, "body": body}}))
            return self.command("validate-pr", str(event), ok=ok)
        validate("fix(inventory): retain changes", BODY)
        validate("docs: explain process", BODY.replace("Concurrent enrollments now retain both successful changes.", "None: agent guidance only."))
        for title, body in [
            ("Add things", BODY), ("fix: summary.", BODY),
            ("fix: summary", BODY.replace("## Documentation", "## Missing")),
            ("fix: summary", BODY + "\n## Release note\nDuplicate"),
            ("fix: summary", BODY.replace("None.", "BREAKING CHANGE: new format")),
            ("feat!: new format", BODY),
            ("fix: summary", BODY.replace("README explains inventory locking.", "<!-- TODO -->")),
            ("fix: summary", BODY + "\nMSG\n"),
            ("fix: summary", BODY.replace("Concurrent enrollments now retain both successful changes.", "None:")),
            ("fix: summary", "```markdown\n" + BODY + "\n```"),
        ]:
            validate(title, body, ok=False)
        validate("feat!: new format", BODY.replace("None.", "BREAKING CHANGE: use the new flag; migrate configuration before upgrading."))
        TRANSCRIPT.append("PR edits: valid contract accepted; malformed title, missing/duplicate/fenced sections, empty omission, comment-only docs, delimiter and breaking mismatch refused")

    def test_publish_resume_and_repeat(self):
        sha = self.commit("fix: runtime")
        self.plan()
        self.dist()
        state = self.state()
        state["fail_upload"] = True
        self.save(state)
        self.command("publish", "notes.json", "notes.md", "dist", ok=False)
        self.assertEqual(self.git("ls-remote", "origin", "refs/tags/v0.4.1^{}").split()[0], sha)
        self.assertTrue(self.state()["release"]["draft"])
        self.command("publish", "notes.json", "notes.md", "dist")
        self.assertFalse(self.state()["release"]["draft"])
        before = len([c for c in self.state()["calls"] if c[0] == "release"])
        self.command("publish", "notes.json", "notes.md", "dist")
        self.assertEqual(before, len([c for c in self.state()["calls"] if c[0] == "release"]))
        TRANSCRIPT.append("publication: failed upload leaves draft and fixed tag; retry verifies assets then publishes; successful repeat performs no mutation")

    def test_publish_refusals_and_plan_reuse(self):
        self.commit("fix: runtime")
        self.plan()
        self.dist()
        (self.repo / "dist" / ASSETS[0]).write_text("tampered")
        self.command("publish", "notes.json", "notes.md", "dist", ok=False)
        self.assertEqual(self.git("ls-remote", "origin", "refs/tags/v0.4.1"), "")
        (self.repo / "dist" / ASSETS[0]).write_bytes(ASSETS[0].encode())
        self.git("tag", "v0.4.1", self.base)
        self.git("push", "-q", "origin", "refs/tags/v0.4.1")
        self.command("publish", "notes.json", "notes.md", "dist", ok=False)
        self.git("push", "-q", "origin", ":refs/tags/v0.4.1")
        self.git("tag", "-d", "v0.4.1")
        self.commit("fix: newer", content="new")
        self.command("verify", "notes.json", "notes.md", ok=False)
        TRANSCRIPT.append("publication refusals: checksum mismatch before tagging; wrong remote tag never moved; wrong checkout refused")

    def test_restore_reviewed_plan(self):
        sha = self.commit("fix: runtime")
        self.plan()
        state = self.state()
        state['run'] = dict(workflow_id=7, event='workflow_dispatch', status='completed', head_sha=sha)
        state['artifact'] = {'release-notes.json': (self.repo / 'notes.json').read_text(),
                             'release-notes.md': (self.repo / 'notes.md').read_text()}
        self.save(state)
        self.commit("fix: newer", content="new")
        self.command('restore-plan', '123')
        self.assertEqual(json.loads((self.repo / 'release-notes.json').read_text())['sha'], sha)
        state = self.state()
        state['run']['workflow_id'] = 99
        self.save(state)
        self.command('restore-plan', '123', ok=False)
        state['run']['workflow_id'] = 7
        state['artifact']['release-notes.json'] = state['artifact']['release-notes.json'].replace('"bump": "patch"', '"bump": "minor"')
        self.save(state)
        self.command('restore-plan', '123', ok=False)
        TRANSCRIPT.append('reviewed plan reuse: fixed source SHA retained after main advances; unrelated workflow and tampered plan refused')


if __name__ == "__main__":
    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(ReleaseScenarios))
    text = "\n".join(sorted(TRANSCRIPT)) + f"\nscenarios={result.testsRun}; failures={len(result.failures)}; errors={len(result.errors)}\n"
    artifact = ROOT / "out/release-transcript.txt"
    artifact.parent.mkdir(exist_ok=True)
    artifact.write_text(text)
    golden = ROOT / "testdata/release-transcript.golden"
    if result.wasSuccessful() and os.environ.get("UPDATE_GOLDEN"):
        golden.parent.mkdir(exist_ok=True)
        golden.write_text(text)
    matches = golden.exists() and golden.read_text() == text
    print(f"Artifact: {artifact}; golden matches: {matches}")
    raise SystemExit(0 if result.wasSuccessful() and matches else 1)
