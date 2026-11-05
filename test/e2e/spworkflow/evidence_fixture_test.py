#!/usr/bin/env python3
"""Offline fixture for the e2e diagnostics bundle (e2e_test.sh --redactEvidence).

Usage: python3 test/e2e/spworkflow/evidence_fixture_test.py [repo-root]

For every e2e workflow it builds a throwaway workspace with planted fake keys,
runs each tee'd test step's own `run:` text (the harness call swapped for the
real harness in an unknown no-op mode, plus echoed lines that plant the keys as
tool output) under GitHub's bash -eo pipefail, once passing and once failing, then
the workflow's own Record stage results and Redact steps, and scans the bundle
its Upload step publishes. Further cases drive the collector through races,
budgets, symlinks, unreadable key sources and stale bundles; those hook os
calls inside the real collector body extracted from e2e_test.sh.

Starts no services and needs no network; every key is random and holds
nothing. Needs python3 with PyYAML, bash, git and coreutils. Exits nonzero
when any check fails.
"""
import hashlib
import json
import os
import random
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import yaml

REPO = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parents[3]).resolve()
HARNESS = "test/e2e/spworkflow/e2e_test.sh"
SDK_DIFF = REPO / "test/e2e/spworkflow/testdata/sdk-prepare.offline.diff"
WORKFLOWS = {"e2e-test.yml": "sp-e2e", "e2e-sdk.yml": "sdk-e2e", "sp-exit-e2e-test.yml": "sp-exit",
             "block_syncer-e2e-test.yml": "bs-e2e"}
BUNDLE = ".github/e2e-diagnostics"
DEV_KEY = "2228e392584d902843272c37fd62b8c73c10c81a5ecb901773c9ebe366e937bb"
CALL = re.compile(r"bash \./(test/e2e/spworkflow/e2e_test\.sh|modular/blocksyncer/test/bs_test\.sh) --\w+")
TMP = os.environ.get("TMPDIR") or tempfile.gettempdir()
NODE_ID = secrets.token_hex(20)
PASS = FAIL = 0


def check(name, cond):
    global PASS, FAIL
    print(("PASS: " if cond else "FAIL: ") + name)
    if cond:
        PASS += 1
    else:
        FAIL += 1


def hexkey():
    return secrets.token_hex(32)


WORDS = ["alpha", "bravo", "cargo", "delta", "eagle", "fabric", "garden", "harbor", "island", "jungle",
         "kettle", "lemon", "magnet", "napkin", "orbit", "pencil", "quartz", "rocket", "saddle", "tunnel",
         "umbrella", "velvet", "walnut", "yellow"]


def mnemonic(n=24):
    return " ".join(secrets.choice(WORDS) for _ in range(n))


def new_keys():
    return {"test": hexkey(), "sp_op": hexkey(), "sp_seal": hexkey(), "challenger": hexkey(),
            "other": hexkey(), "mnemonic": mnemonic(), "inline": mnemonic()}


def run(script, cwd, env, timeout=120):
    # GitHub's default `run` shell is bash --noprofile --norc -eo pipefail {0}
    with tempfile.NamedTemporaryFile("w", suffix=".sh", delete=False, dir=TMP) as f:
        f.write(script)
    try:
        return subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", f.name],
                              cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    finally:
        os.unlink(f.name)


def build_ws(root, keys):
    ws = root / "ws"
    (ws / "test/e2e/spworkflow").mkdir(parents=True)
    shutil.copy(REPO / HARNESS, ws / HARNESS)
    (ws / "go.mod").write_text("module fx\n")
    subprocess.run(["git", "init", "-q", "-b", "main", str(ws)], check=True)
    subprocess.run(["git", "-C", str(ws), "add", "."], check=True)
    subprocess.run(["git", "-C", str(ws), "-c", "user.name=fx", "-c", "user.email=fx@example.invalid",
                    "commit", "-qm", "fx"], check=True)
    local = ws / "moca/deployment/localup/.local"
    (local / "validator0/logs").mkdir(parents=True)
    (local / "challenger0").mkdir(parents=True)
    (local / "validator0/info").write_text("address: 0x01\n" + keys["mnemonic"] + "\n")
    (local / "challenger0/challenger_info").write_text(keys["challenger"] + "\n")
    (ws / "moca/sp.json").write_text(json.dumps({"sp0": {"OperatorPrivateKey": keys["sp_op"],
                                                          "SealPrivateKey": keys["sp_seal"],
                                                          "OperatorAddress": "0x02"}}))
    # localup/SP logs that leak keys (simulates a config dump / key output)
    (local / "validator0/logs/node.log").write_text(
        "node started height=1\nloaded seal key " + keys["sp_seal"].upper() + "\n")
    for i in range(8):
        d = ws / f"deployment/localup/local_env/sp{i}"
        (d / "data").mkdir(parents=True)
        (d / "log.txt").write_text(f"sp{i} up\noperator {keys['sp_op']}\n" + keys["mnemonic"] + "\n"
                                   + "private_key: 0x" + keys["other"] + "\n"
                                   + '{"name":"sp%d_fund","mnemonic":"%s"}\n' % (i, keys["inline"])
                                   + "dialing seed " + NODE_ID + "@127.0.0.1:26656\n"
                                   + "secret connection established with " + NODE_ID + "\n")
        # database WAL and config: never part of the bundle, never opened
        (d / "data/000001.log").write_text("wal private_key " + keys["other"] + "\n")
        (d / "config.toml").write_text("SpOperatorAddress = \"0x02\"\nOperatorPrivateKey = \"" + keys["sp_op"] + "\"\n")
    (ws / "modular/blocksyncer").mkdir(parents=True)
    (ws / "modular/blocksyncer/coverage.txt").write_text("mode: set\nfx/a.go:1.1,2.2 1 1\n")
    return ws


def base_env(root, ws, keys):
    return {"PATH": os.environ["PATH"], "HOME": str(root), "TMPDIR": TMP,
            "GITHUB_WORKSPACE": str(ws), "PRIVATE_KEY": "0x" + keys["test"],
            "ACCOUNT_ADDR": "0x0000000000000000000000000000000000000001",
            "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"}


def steps_of(name):
    wf = yaml.safe_load((REPO / ".github/workflows" / name).read_text())
    return wf["jobs"][next(iter(wf["jobs"]))]["steps"]


def tree_digest(ws):
    # every file in the run tree except the bundle and its staging dir
    digest = {}
    for p in sorted(ws.rglob("*")):
        rel = p.relative_to(ws).as_posix()
        if rel.startswith(BUNDLE) or rel.startswith(".git/"):
            continue
        if p.is_file() and not p.is_symlink():
            digest[rel] = hashlib.sha256(p.read_bytes()).hexdigest()
    return digest


def manifest_of(ws):
    path = ws / BUNDLE / "manifest.txt"
    if not path.is_file():
        return {}
    return dict(line.split("=", 1) for line in path.read_text().splitlines() if "=" in line)


def one_workflow(name, fail_rc):
    steps = steps_of(name)
    profile = WORKFLOWS[name]
    tag = f"{name}[{'fail rc=' + str(fail_rc) if fail_rc else 'pass'}]"
    keys = new_keys()
    root = Path(tempfile.mkdtemp(prefix="redact-", dir=TMP))
    ws = build_ws(root, keys)
    env = base_env(root, ws, keys)

    ids = [s.get("id") for s in steps]
    redact_i = ids.index("redact") if "redact" in ids else -1
    upload_i = next((i for i, s in enumerate(steps) if str(s.get("uses", "")).startswith("actions/upload-artifact")), -1)
    stage_i = next((i for i, s in enumerate(steps) if s.get("name") == "Record stage results"), -1)
    tee_steps = [i for i, s in enumerate(steps) if "| tee" in str(s.get("run", ""))]
    check(f"{tag}: redact step present, always(), after every tee step and stage record, before upload",
          redact_i >= 0 and steps[redact_i].get("if") == "always()" and stage_i < redact_i
          and all(i < redact_i for i in tee_steps) and redact_i < upload_i)
    check(f"{tag}: redact step names its fixed profile {profile}",
          steps[redact_i]["run"].split() == ["bash", f"./{HARNESS}", "--redactEvidence", profile])
    check(f"{tag}: upload gated on redact success",
          upload_i >= 0 and steps[upload_i].get("if") == "always() && steps.redact.outcome == 'success'")
    upload = steps[upload_i]["with"]
    check(f"{tag}: upload path is only the published bundle, missing bundle is an error",
          upload["path"].strip() == BUNDLE + "/" and upload.get("if-no-files-found") == "error")

    meta_step = next(s for s in steps if s.get("id") == "metadata")
    r = run(meta_step["run"], ws, env)
    check(f"{tag}: metadata step exit 0 and prints no key", r.returncode == 0 and keys["test"] not in r.stdout + r.stderr)
    shutil.copy(SDK_DIFF, ws / ".github/action-logs/sdk-prepare.diff")
    # run tee'd test steps; the first one fails in the failing variant
    outcomes = {"metadata": "success", "mysql": "success"}
    for s in steps:
        if s.get("id") and s.get("id") not in outcomes and "| tee" in str(s.get("run", "")):
            outcomes[s["id"]] = "skipped"
        elif s.get("id") in ("make-config", "reset"):
            outcomes[s["id"]] = "success"
    for n, i in enumerate(tee_steps):
        rc = fail_rc if (fail_rc and n == 0) else 0
        # the harness no longer prints the test key; a tool's output still could,
        # and the scrub must catch it
        body = CALL.sub(f"( bash ./{HARNESS} --fixtureNoop; echo 'tool: test key 0x{keys['test']}'; "
                        f"echo 'tail: private key {keys['other']}'; exit {rc} )",
                        steps[i]["run"])
        r = run(body, ws, env)
        check(f"{tag}: step '{steps[i]['name']}' exit {r.returncode} == {rc} through tee/pipefail", r.returncode == rc)
        outcomes[steps[i]["id"]] = "failure" if rc else "success"
        if fail_rc:
            break  # later steps are skipped by GitHub after a failure
    logs = list((ws / ".github/action-logs").glob("*.log"))
    check(f"{tag}: raw tee log carries the planted test key before redaction (the scrub has work to do)",
          any(keys["test"] in p.read_text() for p in logs))

    stage_env = dict(env)
    for key in steps[stage_i]["env"]:
        step_id = key[len("STAGE_"):].lower().replace("_", "-")
        stage_env[key] = outcomes.get(step_id, "skipped")
    r = run(steps[stage_i]["run"], ws, stage_env)
    check(f"{tag}: record stage results exit 0", r.returncode == 0)

    before = tree_digest(ws)
    r = run(steps[redact_i]["run"], ws, env)
    check(f"{tag}: redact step exit 0 (business outcome does not change it)", r.returncode == 0)
    check(f"{tag}: redact step output prints no planted secret (test key included)",
          not any(v in r.stdout + r.stderr for v in keys.values()))
    check(f"{tag}: run tree byte-identical after redact (nothing modified in place, WAL untouched)",
          tree_digest(ws) == before)
    bundle = ws / BUNDLE
    present = sorted(p for p in bundle.rglob("*") if p.is_file())
    rels = {p.relative_to(bundle).as_posix() for p in present}
    if profile != "bs-e2e":
        check(f"{tag}: bundled sdk-prepare.diff equals the source (its recorded hash stays valid)",
              (bundle / ".github/action-logs/sdk-prepare.diff").read_bytes() == SDK_DIFF.read_bytes())
    check(f"{tag}: bundle holds only allowlisted files plus manifest (no data/, config, keys)",
          "manifest.txt" in rels and not any("/data/" in x or x.endswith(".toml") or x.endswith(".json") for x in rels))
    blob = "\n".join(p.read_bytes().decode("latin-1") for p in present)
    needles = [keys["test"], keys["sp_op"], keys["sp_seal"], keys["challenger"], keys["other"], keys["mnemonic"],
               keys["inline"], DEV_KEY]
    leaked = [n[:8] for n in needles if n.lower() in blob.lower()]
    check(f"{tag}: {len(present)} bundled file(s) contain no planted secret (any case, 0x or not) {leaked}", not leaked)
    tee_logs = [p for p in present if p.suffix == ".log" and p.parent.name == "action-logs"]
    check(f"{tag}: bundled tee logs are present and redacted", bool(tee_logs)
          and all("[REDACTED]" in p.read_text() for p in tee_logs))
    meta = (bundle / ".github/action-logs/e2e-metadata.txt").read_text()
    check(f"{tag}: diagnostics kept (address line, metadata sha256 values, commit sha)",
          all("TEST_ACCOUNT_ADDRESS is 0x0000000000000000000000000000000000000001" in p.read_text() for p in tee_logs)
          and re.search(r"^sp\.go_mod_sha256=[0-9a-f]{64}$", meta, re.M)
          and re.search(r"^sp\.checkout_sha=[0-9a-f]{40}$", meta, re.M))
    sp_logs = [p for p in present if p.name == "log.txt"]
    check(f"{tag}: p2p seed/secret-connection node ids kept in SP logs",
          all(p.read_text().count(NODE_ID) == 2 for p in sp_logs))
    m = manifest_of(ws)
    first = steps[tee_steps[0]]["id"]
    check(f"{tag}: manifest result=success, profile, stage outcome of the first step recorded",
          m.get("result") == "success" and m.get("profile") == profile
          and m.get(f"stage.{first}") == ("failure" if fail_rc else "success"))
    captured = {k[len("file."):-len(".status")] for k, v in m.items() if k.endswith(".status") and v == "captured"}
    check(f"{tag}: manifest captured set equals the bundled files",
          captured == rels - {"manifest.txt"} and all(
              m.get(f"file.{x}.sha256") == hashlib.sha256((bundle / x).read_bytes()).hexdigest() for x in captured))
    if fail_rc:
        later = [steps[i]["id"] for i in tee_steps[1:]]
        later_logs = [k for k, v in m.items() if k.endswith(".stage") and v == "skipped"]
        check(f"{tag}: logs of skipped steps recorded missing with evidence UNKNOWN, not required",
              (not later) or (later_logs and all(
                  m.get(k[:-len('.stage')] + ".status") in ("missing", "captured")
                  and m.get(k[:-len('.stage')] + ".required") == "no" for k in later_logs)
                  and any(m.get(k[:-len('.stage')] + ".evidence") == "UNKNOWN" for k in later_logs)))
    shutil.rmtree(root)


# ---- collector-level cases -------------------------------------------------

def collector_body():
    text = (REPO / HARNESS).read_text()
    start = text.index("python3 -I - \"${profile}\" <<'PY'\n") + len("python3 -I - \"${profile}\" <<'PY'\n")
    return text[start:text.index("\nPY\n", start)] + "\n"


def run_collector(ws, profile, keys, hook="", extra_env=None, timeout=200):
    """Run the real collector body with an optional os-level hook prepended."""
    env = {"PATH": os.environ["PATH"], "TMPDIR": TMP,
           "E2E_REDACT_KEY_1": "0x" + keys["test"], "E2E_REDACT_KEY_2": DEV_KEY}
    env.update(extra_env or {})
    script = Path(tempfile.mkstemp(suffix=".py", dir=TMP)[1])
    script.write_text(hook + "\n" + collector_body())
    try:
        return subprocess.run([sys.executable, "-I", str(script), profile], cwd=ws, env=env,
                              capture_output=True, text=True, timeout=timeout)
    finally:
        script.unlink()


def fresh(profile="sp-e2e", stages=None):
    keys = new_keys()
    root = Path(tempfile.mkdtemp(prefix="collect-", dir=TMP))
    ws = build_ws(root, keys)
    logs = ws / ".github/action-logs"
    logs.mkdir(parents=True)
    stages = stages or {"start-chain": "success", "start-sp": "success", "build-cmd": "success", "run-test": "success"}
    (logs / "e2e-metadata.txt").write_text("sp.checkout_sha=" + "a" * 40 + "\n"
                                           + "".join(f"stage.{k}={v}\n" for k, v in stages.items()))
    prefix = {"sp-e2e": "sp-e2e", "sp-exit": "sp-exit", "sdk-e2e": "sdk-e2e", "bs-e2e": "bs-e2e"}[profile]
    for stage, outcome in stages.items():
        if outcome in ("success", "failure"):
            (logs / f"{prefix}-{stage}.log").write_text(
                f"{stage} ok\nTEST_ACCOUNT_PRIVATE_KEY is {keys['test']}\n")
    shutil.copy(SDK_DIFF, logs / "sdk-prepare.diff")
    return root, ws, keys


def no_bundle(ws):
    return not (ws / BUNDLE).exists() and not (ws / (BUNDLE + ".staging")).exists()


# deterministic RED of #237: the original redactor walks local_env, selects
# the WAL and fails when it vanishes between selection and read
RACE_HOOK = r'''
import os as _os, pathlib as _pl
_opened = []
_real_open = _os.open
def _open(path, *a, **k):
    p = str(path)
    _opened.append(p)
    if "/data/" in p:
        try:
            _os.unlink(p)
        except FileNotFoundError:
            pass
    return _real_open(path, *a, **k)
_os.open = _open
_real_rb = _pl.Path.read_bytes
def _rb(self):
    if "/data/" in str(self):
        try:
            _os.unlink(self)
        except FileNotFoundError:
            pass
    return _real_rb(self)
_pl.Path.read_bytes = _rb
import atexit as _ax
_ax.register(lambda: print("OPENED_DATA=%d" % sum("/data/" in p for p in _opened), file=__import__("sys").stderr))
'''

# act on an allowlisted file right after the collector opened and fstat'ed it
AFTER_FSTAT = r'''
import os as _os
_target = %r
_action = %r
_real_open, _real_fstat = _os.open, _os.fstat
_fds = {}
def _open(path, *a, **k):
    fd = _real_open(path, *a, **k)
    _fds[fd] = str(path)
    return fd
def _fstat(fd):
    st = _real_fstat(fd)
    path = _fds.get(fd, "")
    if path.endswith(_target):
        if _action == "grow":
            with open(path, "a") as f:
                f.write("LATE-MARKER private key 0x" + "ab" * 32 + "\n")
        elif _action == "delete":
            _os.unlink(path)
        elif _action == "rotate":
            _os.rename(path, path + ".1")
            with open(path, "w") as f:
                f.write("LATE-MARKER rotated\n")
        elif _action == "shrink":
            _os.truncate(path, 3)
        elif _action == "sleep":
            import time
            time.sleep(5)
    return st
_os.open, _os.fstat = _open, _fstat
'''


def collector_cases():
    tee = "deployment/localup/local_env/sp1/data/000001.log"

    # RED: the redactor as frozen at the #237 head, read from git history
    orig = subprocess.run(["git", "show", "43261e3550e1177d65a1dd3067f3da9fe84fe408:" + HARNESS],
                          cwd=REPO, capture_output=True, text=True)
    if orig.returncode == 0:
        text = orig.stdout
        start = text.index("python3 - \"${E2E_REDACT_PATHS[@]}\" <<'PY'\n") + len("python3 - \"${E2E_REDACT_PATHS[@]}\" <<'PY'\n")
        body = text[start:text.index("\nPY\n", start)] + "\n"
        root, ws, keys = fresh()
        script = Path(tempfile.mkstemp(suffix=".py", dir=TMP)[1])
        script.write_text(RACE_HOOK + body)
        r = subprocess.run([sys.executable, "-I", str(script), ".github/action-logs", "deployment/localup/local_env",
                            "moca/deployment/localup/.local", "modular/blocksyncer/coverage.txt"], cwd=ws,
                           env={"PATH": os.environ["PATH"], "E2E_REDACT_KEY_1": keys["test"]},
                           capture_output=True, text=True, timeout=120)
        script.unlink()
        check("RED (frozen #237 redactor): WAL deleted after selection -> FileNotFoundError, exit 1",
              r.returncode == 1 and "FileNotFoundError" in r.stderr)
        shutil.rmtree(root)
    else:
        check("RED: frozen #237 harness readable from git", False)

    # GREEN: same hook, the collector never opens anything under data/
    root, ws, keys = fresh()
    wal = ws / tee
    wal_hash = hashlib.sha256(wal.read_bytes()).hexdigest()
    r = run_collector(ws, "sp-e2e", keys, RACE_HOOK)
    check("GREEN: same race hook -> exit 0, no data/ path opened, WAL bytes unchanged",
          r.returncode == 0 and "OPENED_DATA=0" in r.stderr and wal.exists()
          and hashlib.sha256(wal.read_bytes()).hexdigest() == wal_hash)
    shutil.rmtree(root)

    for action, expect_ok in (("grow", True), ("delete", True), ("rotate", True), ("shrink", False)):
        root, ws, keys = fresh()
        target = "deployment/localup/local_env/sp0/log.txt"
        original = (ws / target).read_bytes()
        r = run_collector(ws, "sp-e2e", keys, AFTER_FSTAT % (target, action))
        if expect_ok:
            copy = ws / BUNDLE / target
            check(f"{action} after open: exit 0, bundle holds the bytes from open time (no LATE-MARKER)",
                  r.returncode == 0 and copy.is_file() and b"LATE-MARKER" not in copy.read_bytes()
                  and manifest_of(ws).get(f"file.{target}.bytes") == str(len(copy.read_bytes()))
                  and copy.read_bytes().count(b"\n") == original.count(b"\n"))
        else:
            check(f"{action} while copying: exit 1, no bundle published, reason named",
                  r.returncode == 1 and no_bundle(ws) and "shrank" in r.stdout + r.stderr)
        shutil.rmtree(root)

    # secret appended after the snapshot never reaches the bundle; source log untouched
    root, ws, keys = fresh()
    target = ".github/action-logs/sp-e2e-run-test.log"
    r = run_collector(ws, "sp-e2e", keys, AFTER_FSTAT % (target, "grow"))
    check("secret appended to a tee log after open is absent from bundle and source keeps it (no in-place edit)",
          r.returncode == 0 and b"LATE-MARKER" not in (ws / BUNDLE / target).read_bytes()
          and b"LATE-MARKER" in (ws / target).read_bytes())
    shutil.rmtree(root)

    # stale bundle and staging are removed, both on success and on failure
    for ok in (True, False):
        root, ws, keys = fresh()
        for d in (BUNDLE, BUNDLE + ".staging"):
            (ws / d / ".github/action-logs").mkdir(parents=True)
            (ws / d / "stale.txt").write_text("stale\n")
        if not ok:
            (ws / "moca/sp.json").write_text("{not json")
        r = run_collector(ws, "sp-e2e", keys)
        if ok:
            check("stale bundle/staging removed before a successful publish",
                  r.returncode == 0 and not (ws / BUNDLE / "stale.txt").exists()
                  and not (ws / (BUNDLE + ".staging")).exists())
        else:
            check("stale bundle removed and nothing published when the step fails", r.returncode == 1 and no_bundle(ws))
        shutil.rmtree(root)

    # key sources: unreadable, unparseable, symlinked -> fail; never generated -> unavailable
    for case in ("sp.json unparseable", "info unreadable", "challenger_info symlink", "sources absent"):
        root, ws, keys = fresh()
        local = ws / "moca/deployment/localup/.local"
        if case == "sp.json unparseable":
            (ws / "moca/sp.json").write_text("{not json")
        elif case == "info unreadable":
            (local / "validator0/info").chmod(0)
        elif case == "challenger_info symlink":
            (local / "challenger0/challenger_info").unlink()
            (local / "challenger0/challenger_info").symlink_to(ws / "go.mod")
        else:
            (ws / "moca/sp.json").unlink()
            (local / "validator0/info").unlink()
            (local / "challenger0/challenger_info").unlink()
        r = run_collector(ws, "sp-e2e", keys)
        if case == "info unreadable":
            (local / "validator0/info").chmod(stat.S_IRUSR | stat.S_IWUSR)
        if case == "sources absent":
            m = manifest_of(ws)
            check("key sources never generated: recorded unavailable, bundle still published",
                  r.returncode == 0 and m.get("key_source.moca/sp.json") == "unavailable"
                  and m.get("key_source.moca/deployment/localup/.local/sp_export.json") == "unavailable"
                  and m.get("key_source.moca/deployment/localup/.local/validator0/info") == "unavailable"
                  and m.get("key_source.moca/deployment/localup/.local/challenger0/challenger_info") == "unavailable")
        else:
            check(f"key source {case}: exit 1, no bundle, no secret printed",
                  r.returncode == 1 and no_bundle(ws) and not any(v in r.stdout + r.stderr for v in keys.values()))
        shutil.rmtree(root)

    # unreadable / symlinked / escaping / non-regular evidence files fail closed
    for case in ("unreadable tee log", "tee log symlink", "symlinked sp dir", "local_env escapes", "tee log fifo"):
        root, ws, keys = fresh()
        target = ws / ".github/action-logs/sp-e2e-run-test.log"
        outside = root / "outside"
        outside.mkdir()
        (outside / "log.txt").write_text("outside private key " + hexkey() + "\n")
        if case == "unreadable tee log":
            target.chmod(0)
        elif case == "tee log symlink":
            target.unlink()
            target.symlink_to(outside / "log.txt")
        elif case == "symlinked sp dir":
            shutil.rmtree(ws / "deployment/localup/local_env/sp3")
            (ws / "deployment/localup/local_env/sp3").symlink_to(outside)
        elif case == "local_env escapes":
            shutil.rmtree(ws / "deployment/localup/local_env")
            (ws / "deployment/localup/local_env").symlink_to(outside)
        else:
            target.unlink()
            os.mkfifo(target)
        r = run_collector(ws, "sp-e2e", keys, timeout=30)
        if case == "unreadable tee log":
            target.chmod(stat.S_IRUSR | stat.S_IWUSR)
        check(f"{case}: exit 1, no bundle, outside file not copied", r.returncode == 1 and no_bundle(ws)
              and "outside private key" not in r.stdout + r.stderr)
        shutil.rmtree(root)

    # required vs optional
    root, ws, keys = fresh(stages={"start-chain": "success", "start-sp": "failure", "build-cmd": "skipped",
                                   "run-test": "skipped"})
    for i in range(8):
        (ws / f"deployment/localup/local_env/sp{i}/log.txt").unlink()
    (ws / ".github/action-logs/sdk-prepare.diff").unlink()
    r = run_collector(ws, "sp-e2e", keys)
    m = manifest_of(ws)
    sp0 = "file.deployment/localup/local_env/sp0/log.txt"
    check("early failure: optional SP logs/diffs missing -> evidence UNKNOWN, skipped tee logs not required, published",
          r.returncode == 0 and m.get(sp0 + ".status") == "missing" and m.get(sp0 + ".evidence") == "UNKNOWN"
          and m.get("file..github/action-logs/sp-e2e-build-cmd.log.required") == "no"
          and m.get("file..github/action-logs/sp-e2e-build-cmd.log.reason") == "step did not run"
          and m.get("file..github/action-logs/sp-e2e-start-sp.log.status") == "captured")
    shutil.rmtree(root)

    root, ws, keys = fresh()
    (ws / ".github/action-logs/sp-e2e-build-cmd.log").unlink()
    r = run_collector(ws, "sp-e2e", keys)
    check("tee log of a step that ran is missing -> exit 1, no bundle",
          r.returncode == 1 and no_bundle(ws) and "sp-e2e-build-cmd.log: required but missing" in r.stderr)
    shutil.rmtree(root)

    root, ws, keys = fresh()
    shutil.rmtree(ws / ".github/action-logs")
    r = run_collector(ws, "sp-e2e", keys)
    check("empty evidence (no metadata, no logs) -> exit 1, no bundle", r.returncode == 1 and no_bundle(ws))
    shutil.rmtree(root)

    root, ws, keys = fresh()
    r = run_collector(ws, "../etc", keys)
    r2 = run_collector(ws, "", keys)
    check("unknown or empty profile -> exit 1, no bundle", r.returncode == 1 and r2.returncode == 1 and no_bundle(ws))
    shutil.rmtree(root)

    # budgets: per file, total, time; the environment cannot raise them
    root, ws, keys = fresh()
    big = ws / "deployment/localup/local_env/sp5/log.txt"
    big.write_bytes((b"x" * 1023 + b"\n") * (20 * 1024 + 1))
    r = run_collector(ws, "sp-e2e", keys, extra_env={"E2E_BUNDLE_FILE_LIMIT": str(10 ** 12)})
    check("file over 20 MiB (raise attempt ignored) -> exit 1, no bundle, nothing truncated",
          r.returncode == 1 and no_bundle(ws) and "over the 20971520 byte budget" in r.stderr)
    shutil.rmtree(root)

    root, ws, keys = fresh()
    for i in range(6):
        (ws / f"deployment/localup/local_env/sp{i}/log.txt").write_bytes((b"y" * 1023 + b"\n") * (17 * 1024))
    r = run_collector(ws, "sp-e2e", keys, extra_env={"E2E_BUNDLE_TOTAL_LIMIT": str(10 ** 12)})
    check("total over 100 MiB (6 x 17 MiB, raise attempt ignored) -> exit 1, no bundle",
          r.returncode == 1 and no_bundle(ws) and "byte total budget" in r.stderr)
    shutil.rmtree(root)

    root, ws, keys = fresh()
    r = run_collector(ws, "sp-e2e", keys, AFTER_FSTAT % ("sp-e2e-run-test.log", "sleep"),
                      extra_env={"E2E_BUNDLE_TIME_LIMIT": "1"})
    check("over the time budget -> exit 1, no bundle, staging removed",
          r.returncode == 1 and no_bundle(ws) and "time budget" in r.stderr)
    r = run_collector(ws, "sp-e2e", keys, extra_env={"E2E_BUNDLE_TIME_LIMIT": "100000"})
    check("time budget cannot be raised from the environment", r.returncode == 0
          and manifest_of(ws).get("limits.seconds") == "120")
    shutil.rmtree(root)

    # a budget can only be tightened: zero, negative or non-numeric values are
    # rejected (setitimer(0) would switch the time budget off), and the stale
    # bundle is still removed first
    for name in ("E2E_BUNDLE_TIME_LIMIT", "E2E_BUNDLE_FILE_LIMIT", "E2E_BUNDLE_TOTAL_LIMIT"):
        for value in ("0", "-1", "abc", "nan"):
            root, ws, keys = fresh()
            (ws / BUNDLE).mkdir(parents=True)
            (ws / BUNDLE / "stale.txt").write_text("stale\n")
            r = run_collector(ws, "sp-e2e", keys, extra_env={name: value})
            check(f"{name}={value} -> exit 1, stale bundle removed, nothing published",
                  r.returncode == 1 and no_bundle(ws) and name in r.stderr)
            shutil.rmtree(root)

    root, ws, keys = fresh()
    (ws / BUNDLE).mkdir(parents=True)
    (ws / BUNDLE / "stale.txt").write_text("stale\n")
    r = run_collector(ws, "../etc", keys)
    check("unknown profile still removes the stale bundle", r.returncode == 1 and no_bundle(ws))
    shutil.rmtree(root)

    # the bundle location is fixed: an E2E_BUNDLE_DIR from the environment is
    # neither deleted nor written
    root, ws, keys = fresh()
    outside = Path(root) / "outside"
    outside.mkdir()
    (outside / "keep.txt").write_text("keep\n")
    r = run_collector(ws, "sp-e2e", keys, extra_env={"E2E_BUNDLE_DIR": str(outside)})
    check("E2E_BUNDLE_DIR is ignored: outside dir untouched, bundle at the fixed path",
          r.returncode == 0 and (outside / "keep.txt").read_text() == "keep\n"
          and sorted(x.name for x in outside.iterdir()) == ["keep.txt"] and (ws / BUNDLE / "manifest.txt").is_file())
    shutil.rmtree(root)

    # a total-budget overflow names the total budget, not the per-file one
    root, ws, keys = fresh()
    for i in range(6):
        (ws / f"deployment/localup/local_env/sp{i}/log.txt").write_bytes((b"y" * 1023 + b"\n") * (17 * 1024))
    r = run_collector(ws, "sp-e2e", keys)
    check("total overflow message names the 104857600 byte total budget",
          r.returncode == 1 and "104857600 byte total budget" in r.stderr)
    shutil.rmtree(root)

    # redaction rules kept: 12-word mnemonic line and named key redacted, unnamed sha kept
    root, ws, keys = fresh()
    target = ws / ".github/action-logs/sp-e2e-run-test.log"
    target.write_text("x\n" + mnemonic(12) + "\nSecret_Key hex 0X" + hexkey().upper() + "\nplain sha " + hexkey() + "\n")
    r = run_collector(ws, "sp-e2e", keys)
    out = (ws / BUNDLE / ".github/action-logs/sp-e2e-run-test.log").read_text()
    check("12-word mnemonic and named key redacted in the copy, unnamed sha kept",
          r.returncode == 0 and out.count("[REDACTED]") == 2 and re.search(r"plain sha [0-9a-f]{64}", out))
    shutil.rmtree(root)

    output_parent_cases()
    corrupt_key_source_cases()


def real_redact(ws, profile, keys):
    """The real entry point the workflows call: bash e2e_test.sh --redactEvidence."""
    env = base_env(ws.parent, ws, keys)
    return subprocess.run(["bash", HARNESS, "--redactEvidence", profile], cwd=ws, env=env,
                          capture_output=True, text=True, timeout=200)


def output_parent_cases():
    # .github resolving outside the workspace: refuse before any cleanup, so a
    # bundle/staging dir at the symlink target survives; for valid and invalid
    # profiles, through the collector body and through the real entry point
    for how in ("collector", "entry point"):
        for profile in ("sp-e2e", "bs-e2e", "invalid"):
            for kind in ("symlink", "file"):
                root, ws, keys = fresh("bs-e2e" if profile == "bs-e2e" else "sp-e2e")
                outside = root / "outside"
                if kind == "symlink":
                    shutil.move(str(ws / ".github"), str(outside))
                    (ws / ".github").symlink_to(outside, target_is_directory=True)
                    for d in ("e2e-diagnostics", "e2e-diagnostics.staging"):
                        (outside / d).mkdir()
                        (outside / d / "sentinel").write_text("keep\n")
                else:
                    shutil.rmtree(ws / ".github")
                    (ws / ".github").write_text("not a dir\n")
                r = (run_collector(ws, profile, keys) if how == "collector" else real_redact(ws, profile, keys))
                if kind == "symlink":
                    kept = all((outside / d / "sentinel").is_file()
                               and sorted(x.name for x in (outside / d).iterdir()) == ["sentinel"]
                               for d in ("e2e-diagnostics", "e2e-diagnostics.staging"))
                else:
                    kept = (ws / ".github").is_file() and (ws / ".github").read_text() == "not a dir\n"
                check(f"{how} {profile}: .github is a {kind} -> exit 1, nothing outside removed or written",
                      r.returncode == 1 and kept and ".github is" in r.stderr)
                shutil.rmtree(root)

    # a symlinked bundle dir itself is unlinked, never followed
    root, ws, keys = fresh()
    outside = root / "outside"
    outside.mkdir()
    (outside / "sentinel").write_text("keep\n")
    (ws / BUNDLE).symlink_to(outside, target_is_directory=True)
    (ws / (BUNDLE + ".staging")).symlink_to(outside, target_is_directory=True)
    r = run_collector(ws, "sp-e2e", keys)
    check("symlinked bundle/staging unlinked, target untouched, real bundle published",
          r.returncode == 0 and sorted(x.name for x in outside.iterdir()) == ["sentinel"]
          and not (ws / BUNDLE).is_symlink() and (ws / BUNDLE / "manifest.txt").is_file())
    shutil.rmtree(root)


def corrupt_key_source_cases():
    local_rel = "moca/deployment/localup/.local"
    bad = {"empty": b"", "blank lines": b"\n \n", "broken-content": b"broken-content\n",
           "not utf-8": b"\xff\xfe" + mnemonic().encode() + b"\n"}
    cases = [(f, name, data) for f in ("validator0/info", "challenger0/challenger_info") for name, data in bad.items()]
    cases += [("validator0/info", "truncated mnemonic", mnemonic(5).encode() + b"\n"),
              ("validator0/info", "hex key instead of mnemonic", hexkey().encode() + b"\n"),
              ("validator0/info", "mnemonic then trailing garbage", mnemonic().encode() + b"\nbroken\n"),
              ("challenger0/challenger_info", "63 hex digits", hexkey()[:63].encode() + b"\n")]
    skipped = {"start-chain": "skipped", "start-sp": "skipped", "build-cmd": "skipped", "run-test": "skipped"}
    for how in ("collector", "entry point"):
        for rel, name, data in cases:
            for profile, stages in (("sp-e2e", None), ("bs-e2e", {"run-test": "skipped"}), ("sdk-e2e", skipped)):
                root, ws, keys = fresh(profile, stages)
                (ws / local_rel / rel).write_bytes(data)
                r = run_collector(ws, profile, keys) if how == "collector" else real_redact(ws, profile, keys)
                check(f"{how} {profile}: {rel} {name} -> exit 1, nothing published",
                      r.returncode == 1 and no_bundle(ws) and f"key source {local_rel}/{rel}" in r.stderr
                      and not any(v in r.stdout + r.stderr for v in keys.values()))
                shutil.rmtree(root)
                if how == "entry point":
                    break  # one profile through the real entry point is enough per case

    # valid forms still publish: mnemonic challenger_info, 0x hex, CRLF lines
    for rel, data in (("challenger0/challenger_info", mnemonic(12).encode() + b"\n"),
                      ("challenger0/challenger_info", ("0x" + hexkey()).encode() + b"\r\n"),
                      ("validator0/info", mnemonic().encode() + b"\r\n\r\n")):
        root, ws, keys = fresh()
        (ws / local_rel / rel).write_bytes(data)
        r = run_collector(ws, "sp-e2e", keys)
        m = manifest_of(ws)
        check(f"valid {rel} {data[:6]!r}... -> published, source read",
              r.returncode == 0 and m.get(f"key_source.{local_rel}/{rel}") == "read")
        shutil.rmtree(root)

    # SP JSON that parses but holds no private key field
    for text, why in (("{}", "no private key fields"), ("[]", "no private key fields"),
                      ('{"sp0": {"OperatorAddress": "0x02"}}', "no private key fields"),
                      ('{"sp0": {"OperatorPrivateKey": ""}}', "OperatorPrivateKey is not 64 hex digits"),
                      ('{"sp0": {"OperatorPrivateKey": "%s", "FundingPrivateKey": "", "SealPrivateKey": "x"}}' % hexkey(),
                       "FundingPrivateKey is not 1 to 64 hex digits")):
        root, ws, keys = fresh()
        (ws / "moca/sp.json").write_text(text)
        r = run_collector(ws, "sp-e2e", keys)
        check(f"sp.json {text[:60]} -> exit 1 ({why}), nothing published", r.returncode == 1 and no_bundle(ws)
              and why in r.stderr and not any(v in r.stdout + r.stderr for v in keys.values()))
        shutil.rmtree(root)

    sp_key_field_cases()


SP_EXPORT = "moca/deployment/localup/.local/sp_export.json"


def sp_key_field_cases():
    # one valid key field next to a corrupt one: the corrupt field's key would
    # be missing from the redaction set, and the seal key in node.log ("loaded
    # seal key ...") matches no generic rule, so publishing would leak it
    skipped = {"start-chain": "skipped", "start-sp": "skipped", "build-cmd": "skipped", "run-test": "skipped"}
    bad = {"null": None, "number": 123, "object": {}, "list": [hexkey()],
           "long non-hex string": "broken-content-not-a-key",
           "0x prefix": "0x" + hexkey(), "64 chars with a non-hex digit": hexkey()[:63] + "g"}
    for rel in ("moca/sp.json", SP_EXPORT):
        extra = {"moca/sp.json": {"63 hex digits": hexkey()[:63]},
                 SP_EXPORT: {"65 hex digits": hexkey() + "a", "too short to redact": "ab12"}}[rel]
        for name, value in {**bad, **extra}.items():
            for how in ("collector", "entry point"):
                root, ws, keys = fresh("sp-e2e", skipped)
                (ws / "moca/sp.json").unlink()
                (ws / rel).write_text(json.dumps({"sp0": {"OperatorPrivateKey": keys["sp_op"],
                                                          "SealPrivateKey": value,
                                                          "OperatorAddress": "0x02"}}))
                r = run_collector(ws, "sp-e2e", keys) if how == "collector" else real_redact(ws, "sp-e2e", keys)
                leaked = any(keys["sp_seal"].upper() in p.read_text(errors="replace")
                             for p in (ws / BUNDLE).rglob("*") if p.is_file()) if (ws / BUNDLE).exists() else False
                check(f"{how}: {rel} valid OperatorPrivateKey + SealPrivateKey {name} -> exit 1, nothing published",
                      r.returncode == 1 and no_bundle(ws) and not leaked
                      and f"cannot read SP keys from {rel}: SealPrivateKey" in r.stderr
                      and not any(v in r.stdout + r.stderr for v in keys.values()))
                shutil.rmtree(root)

    # FundingPrivateKey, the one sp.json field left unpadded, is checked too
    for value in (None, "broken-content-not-a-key", hexkey() + "a"):
        root, ws, keys = fresh("sp-e2e", skipped)
        (ws / "moca/sp.json").write_text(json.dumps({"sp0": {"OperatorPrivateKey": keys["sp_op"],
                                                              "FundingPrivateKey": value}}))
        r = real_redact(ws, "sp-e2e", keys)
        check(f"entry point: moca/sp.json FundingPrivateKey {str(value)[:24]} -> exit 1, nothing published",
              r.returncode == 1 and no_bundle(ws)
              and "cannot read SP keys from moca/sp.json: FundingPrivateKey" in r.stderr)
        shutil.rmtree(root)

    # what each file really holds still publishes, and the seal key is redacted:
    # export_sps output may drop leading zeros, sp.json is padded to 64 digits
    for rel, seal in (("moca/sp.json", "00" + hexkey()[2:]), ("moca/sp.json", hexkey().upper()),
                      (SP_EXPORT, hexkey()[2:]), (SP_EXPORT, hexkey())):
        for how in ("collector", "entry point"):
            root, ws, keys = fresh("sp-e2e", skipped)
            (ws / "moca/sp.json").unlink()
            # normalize_sp_private_keys does not pad FundingPrivateKey
            (ws / rel).write_text(json.dumps({"sp0": {"OperatorPrivateKey": keys["sp_op"],
                                                      "FundingPrivateKey": hexkey()[3:],
                                                      "SealPrivateKey": seal, "BlsPrivateKey": hexkey()}}))
            log = ws / "moca/deployment/localup/.local/validator0/logs/node.log"
            log.write_text("node started height=1\nloaded seal key " + seal.upper() + "\n")
            r = run_collector(ws, "sp-e2e", keys) if how == "collector" else real_redact(ws, "sp-e2e", keys)
            m = manifest_of(ws)
            texts = [p.read_text(errors="replace") for p in (ws / BUNDLE).rglob("*") if p.is_file()]
            check(f"{how}: {rel} SealPrivateKey of {len(seal)} hex digits -> published, source read, key redacted",
                  r.returncode == 0 and m.get(f"key_source.{rel}") == "read"
                  and texts and not any(seal.upper() in t.upper() for t in texts))
            shutil.rmtree(root)


# ---- known-secret redaction: exact output, overlaps, budgets --------------

RUN_LOG = ".github/action-logs/sp-e2e-run-test.log"
BLOCK_BEGIN = "# ---- known secrets: cover every occurrence, then replace (begin) ----\n"
BLOCK_END = "# ---- known secrets (end) ----\n"


def redact_text(content, env1="", env2="", export=None, hook="", extra_env=None, timeout=200):
    """Collector run with RUN_LOG holding content; (result, published copy or None)."""
    root, ws, keys = fresh()
    if export is not None:
        (ws / SP_EXPORT).write_text(json.dumps({"sp0": export}))
    (ws / RUN_LOG).write_bytes(content)
    env = {"E2E_REDACT_KEY_1": env1, "E2E_REDACT_KEY_2": env2, **(extra_env or {})}
    r = run_collector(ws, "sp-e2e", keys, hook, env, timeout)
    copy = ws / BUNDLE / RUN_LOG
    staged = (ws / (BUNDLE + ".staging")).exists()
    out = copy.read_bytes() if r.returncode == 0 and copy.is_file() and not staged else None
    shutil.rmtree(root)
    return r, out


OX_EXCEPTION = "            if s[0] == 0x78 and j >= 1 and canon[j - 1] == 0x30"


def cover_block(without_0x_exception=False):
    """The collector's cover/replace code, run on its own; None if absent.

    without_0x_exception drops the 0x check done before the skips, to show
    the random comparison below reaches it."""
    text = (REPO / HARNESS).read_text()
    if BLOCK_BEGIN not in text or BLOCK_END not in text:
        return None
    block = text[text.index(BLOCK_BEGIN):text.index(BLOCK_END)]
    if without_0x_exception:
        if OX_EXCEPTION not in block:
            return None
        block = block.replace(OX_EXCEPTION, "            if False")
    ns = {"re": re, "MARK": "[REDACTED]"}
    exec(block, ns)
    return ns


def reference_edge(canon, keys):
    """Every occurrence, checked at every offset (with a 0x right before it)."""
    edge = bytearray(len(canon) + 1)
    for s in keys:
        for j in range(len(canon) - len(s) + 1):
            if canon.startswith(s, j):
                a = j - 2 if j >= 2 and canon[j - 2:j] == b"0x" else j
                edge[a + 1:j + len(s)] = b"\x01" * (j + len(s) - a - 1)
    return edge


def cover_cases():
    ns = cover_block()
    check("collector has a separable cover/replace block", ns is not None)
    if ns is None:
        return
    hits = {"subsumed": 0}
    real_subsumed = ns["subsumed"]

    def counted(*a):
        found = real_subsumed(*a)
        hits["subsumed"] += found
        return found
    ns["subsumed"] = counted

    def same_as_reference(text, keys, block=ns):
        canon = text.translate(block["FOLD"])
        keys = sorted({k.translate(block["FOLD"]) for k in keys}, key=len, reverse=True)
        edge = bytearray(len(canon) + 1)
        block["cover"](canon, keys, edge)
        return edge == reference_edge(canon, keys)

    # C1: the longer key covers the short one, whose occurrence at offset 2
    # carries a 0x that reaches byte 0
    check("C1 cover table equals the reference", same_as_reference(b"0" + b"x" * 17 + b"y", [b"x" * 17 + b"y", b"x" * 16]))
    # C2: containment skip must not fire in any of these
    before = hits["subsumed"]
    for name, text, keys in (("inner key overlaps itself", b"xabaxabay ababa", [b"xabay", b"aba"]),
                             ("outer key overlaps itself", b"abxabxab", [b"abxab", b"bx"]),
                             ("inner key also outside the outer", b"abcd bc", [b"abcd", b"bc"]),
                             ("outer starts with x, inner at offset 1 behind 0", b"0xab", [b"xab", b"ab"])):
        check(f"C2 {name}: cover table equals the reference", same_as_reference(text, keys))
    check("C2 containment skip did not fire on the counter-examples", hits["subsumed"] == before)
    check("C2 containment skip fires when it holds and matches the reference",
          same_as_reference(b"abcd abcd", [b"abcd", b"bc"]) and hits["subsumed"] == before + 1)

    # bounded random comparison, fixed seed; the same inputs against the
    # block without the 0x check must disagree somewhere, or the inputs never
    # reach it
    mutant = cover_block(without_0x_exception=True)
    check("cover block has the 0x check done before the skips", mutant is not None)
    rnd = random.Random(237)
    alphabet = b"abx0X"
    cases = bad = mutant_bad = ox_inputs = 0
    hits["subsumed"] = 0
    # a fixed number of cases, so the result does not depend on runner speed
    start = time.monotonic()
    for cases in range(1, 20001):
        keys = [bytes(rnd.choice(alphabet) for _ in range(rnd.randint(2, 6))) for _ in range(rnd.randint(1, 4))]
        text = bytes(rnd.choice(alphabet) for _ in range(rnd.randint(0, 80)))
        bad += not same_as_reference(text, keys)
        if mutant is not None:
            mutant_bad += not same_as_reference(text, keys, mutant)
        # inputs where the 0x check applies: a key starting with x occurs
        # right after a 0 and once more one byte later
        canon = text.translate(ns["FOLD"])
        ox_inputs += any(s[:1] == b"x" and canon.startswith(s, j) and canon.startswith(s, j + 1)
                         for s in {k.translate(ns["FOLD"]) for k in keys}
                         for j in range(1, len(canon)) if canon[j - 1] == 0x30)
    seconds = time.monotonic() - start
    print(f"INFO: random cover comparison seed=237 cases={cases} seconds={seconds:.1f} mismatches={bad} "
          f"containment_skips={hits['subsumed']} inputs_reaching_0x_check={ox_inputs} "
          f"mismatches_without_0x_check={mutant_bad}")
    check("random cover tables equal the reference (containment skip and 0x check both reached)",
          seconds <= 10 and bad == 0 and hits["subsumed"] > 0 and ox_inputs > 0 and mutant_bad > 0)


def exact_output_cases():
    key = hexkey()
    mixed = "".join(c.upper() if i % 2 else c for i, c in enumerate(key))
    inner = key[8:40]
    for name, text, want in (
            ("lower", f"a {key} b\n", "a [REDACTED] b\n"),
            ("upper", f"a {key.upper()} b\n", "a [REDACTED] b\n"),
            ("mixed case", f"a {mixed} b\n", "a [REDACTED] b\n"),
            ("0x", f"a 0x{key} b\n", "a [REDACTED] b\n"),
            ("0X", f"a 0X{key} b\n", "a [REDACTED] b\n"),
            ("0x0x", f"a 0x0x{key} b\n", "a 0x[REDACTED] b\n"),
            ("adjacent", f"a {key}{key} b\n", "a [REDACTED][REDACTED] b\n"),
            ("nested key alone", f"a {inner} b\n", "a [REDACTED] b\n"),
            ("nested inside the outer", f"a {key} b\n", "a [REDACTED] b\n"),
            ("line start, CRLF, end of file", f"{key}\r\nx\r\n{key}", "[REDACTED]\r\nx\r\n[REDACTED]")):
        r, out = redact_text(text.encode(), key, inner)
        check(f"exact output: {name}", r.returncode == 0 and out == want.encode())

    # R2: two SP keys overlap (A's tail is B's head); the old per-pattern
    # replacement left B's tail behind and the full-key check let it through
    a, b = "0123456789abcdef0123", "0123fedcba987654"
    r, out = redact_text(b"x 0X0123456789ABcdef0123fedcba987654 y\n",
                         export={"OperatorPrivateKey": a, "SealPrivateKey": b})
    check("R2 overlapping keys: the union is one MARK, no fragment published",
          r.returncode == 0 and out == b"x [REDACTED] y\n")

    # C1 through the collector: the old output kept the 0 in front
    r, out = redact_text(b"0" + b"x" * 17 + b"y", "x" * 17 + "y", "x" * 16)
    check("C1 collector: short key's 0x reaches byte 0 -> exactly one MARK", r.returncode == 0 and out == b"[REDACTED]")

    # environment keys are taken as they are: brackets, newlines, non-ASCII
    hex32 = "0123456789abcdef0123456789abcdef"
    r, out = redact_text(f"foo{hex32}bar\n".encode(), hex32, "foo[REDACTED]bar")
    check("E1 key holding a MARK: replaced until nothing is left", r.returncode == 0 and out == b"[REDACTED]\n")
    r, out = redact_text(f"a{hex32}{'z' * 16}\n".encode(), hex32, "ED]" + "z" * 16)
    check("E2 key meeting a MARK's tail", r.returncode == 0 and out == b"a[REDACT[REDACTED]\n")
    nl = "abcdefgh\nijklmnopq"
    r, out = redact_text(f"x {nl} y\n".encode(), nl)
    check("E3 key with a newline across two lines: covered and published",
          r.returncode == 0 and out == b"x [REDACTED] y\n")
    latin = "café-secret-Kvalue-0001"
    r, out = redact_text("A CAFÉ-SECRET-kVALUE-0001 B\n".encode("latin-1"), latin)
    check("E4 latin-1 and Kelvin sign key in another case", r.returncode == 0 and out == b"A [REDACTED] B\n")
    han = "测试密钥-abcdefghijklmnop"
    text = f"x {han} y\n".encode()
    r, out = redact_text(text, han)
    check("E5 key that cannot occur in latin-1 text: copy unchanged, published", r.returncode == 0 and out == text)


def resource_cases():
    # D1-D4 scaled to 1 MiB; every key goes through the unchanged sp_export.json reader
    mib = 1024 * 1024
    k = hexkey()
    unit = ("0x" + k + " ").encode()
    for name, export, text, want in (
            ("D1 self-overlapping key", {"P64PrivateKey": "a" * 64}, b"a" * mib, b"[REDACTED]"),
            ("D2 49 keys all hitting", {f"P{n}PrivateKey": "a" * n for n in range(16, 65)}, b"A" * mib, b"[REDACTED]"),
            ("D3 16 rotations", {f"P{n}PrivateKey": ("0123456789abcdef" * 2)[n:n + 16] for n in range(16)},
             b"0123456789abcdef" * (mib // 16), b"[REDACTED]"),
            ("D4 49 nested prefixes", {f"P{n}PrivateKey": k[:n] for n in range(16, 65)},
             unit * (mib // len(unit)), b"[REDACTED] " * (mib // len(unit)))):
        start = time.monotonic()
        r, out = redact_text(text, export=export)
        print(f"INFO: {name} 1 MiB {time.monotonic() - start:.1f}s rc={r.returncode}")
        check(f"{name}: published with the exact expected copy", r.returncode == 0 and out == want)

    # R1: the bundle the e2e jobs could not publish in time: every SP log and
    # node.log near the per-file and total budgets (11.4 MiB each so the
    # metadata, step logs and diff still fit in 100 MiB), ~60 known keys
    root, ws, keys = fresh()
    planted = [hexkey() for _ in range(58)]
    (ws / SP_EXPORT).write_text(json.dumps({"sp0": {f"K{i}PrivateKey": v for i, v in enumerate(planted)}}))
    planted += [keys["test"], DEV_KEY]
    rnd = random.Random(1)
    line = ("2026-11-04T00:00:00.000Z INFO gfspapp/app.go:123 handled request object=bucket/obj%06d "
            "size=1048576 duration=12ms peer=12D3KooWAbCdEfGh\n")

    def log(size):
        out, n = [], 0
        while sum(map(len, out)) < size:
            chunk = [line % (n + i) for i in range(5000)]
            v = rnd.choice(planted)
            chunk[rnd.randrange(5000)] = rnd.choice(["key 0X" + v.upper() + "\n", "seen " + v + " ok\n",
                                                     "m " + "".join(c.upper() if rnd.random() < .5 else c for c in v) + "\n"])
            out.append("".join(chunk))
            n += 5000
        return "".join(out).encode()[:size]
    sp = log(int(11.4 * mib))
    for i in range(8):
        (ws / f"deployment/localup/local_env/sp{i}/log.txt").write_bytes(sp)
    (ws / "moca/deployment/localup/.local/validator0/logs/node.log").write_bytes(log(int(7.5 * mib)))
    r = run_collector(ws, "sp-e2e", keys, timeout=200)
    m = manifest_of(ws)
    blobs = [p.read_bytes().lower() for p in (ws / BUNDLE).rglob("*") if p.is_file()] if (ws / BUNDLE).is_dir() else []
    print(f"INFO: R1 rc={r.returncode} elapsed_seconds={m.get('elapsed_seconds')} source_bytes={m.get('files.source_bytes')}")
    check("R1 near-budget bundle with ~60 keys: published well inside the time budget",
          r.returncode == 0 and m.get("result") == "success" and float(m.get("elapsed_seconds", "999")) < 60
          and blobs and not any(v.lower().encode() in b for v in planted for b in blobs))
    shutil.rmtree(root)

    # R3: a timeout says how far it got, and never a key value
    root, ws, keys = fresh()
    r = run_collector(ws, "sp-e2e", keys, AFTER_FSTAT % ("sp-e2e-run-test.log", "sleep"),
                      extra_env={"E2E_BUNDLE_TIME_LIMIT": "1"})
    check("R3 timeout prints progress and result=timeout, no bundle, no key value",
          r.returncode == 1 and no_bundle(ws) and "result=timeout" in r.stdout
          and "progress.phase=" in r.stdout and "progress.files_done=" in r.stdout
          and not any(v in r.stdout + r.stderr for v in keys.values()) and DEV_KEY not in r.stdout + r.stderr)
    shutil.rmtree(root)


for wf in WORKFLOWS:
    one_workflow(wf, 0)
    one_workflow(wf, 2)
    one_workflow(wf, 3)
collector_cases()
cover_cases()
exact_output_cases()
resource_cases()
print(f"TOTAL PASS={PASS} FAIL={FAIL}")
sys.exit(1 if FAIL else 0)
