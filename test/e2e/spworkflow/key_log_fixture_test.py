#!/usr/bin/env python3
"""Offline fixture: the e2e entrypoints must pass the test key on without printing it.

Usage: python3 test/e2e/spworkflow/key_log_fixture_test.py [repo-root]
Needs python3 with PyYAML. Exits nonzero when any check fails.

Runs the real "set private key" step of each e2e workflow as a script file (with
the ${{ }} expressions rendered from a fake key, as the runner does), then feeds
what it wrote to $GITHUB_ENV into the real e2e_test.sh. Checks the key stays out
of stdout, stderr and a tee'd log, and still reaches $GITHUB_ENV and moca-cmd's
key.txt. Starts no services and needs no network; moca-cmd and the two checkout
helpers are stubbed.

Not covered:
- what the Actions runner prints around a step (the rendered script and the env
  block in the step header), which a shell cannot observe;
- the output of generate-private-key.js;
- e2e_test.sh modes that need the chain: only its top-level code and build_cmd
  run here, and the stub moca-cmd prints nothing.
"""
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parents[3]).resolve()
HARNESS = ROOT / "test/e2e/spworkflow/e2e_test.sh"
WORKFLOWS = ("e2e-test.yml", "e2e-sdk.yml", "sp-exit-e2e-test.yml")
KEY = "f00dfeed" * 8
ADDR = "0x00000000000000000000000000000000000000a1"
OUTPUTS = {"private_key": KEY, "account_address": ADDR}
TIMEOUT = 60
PASS = FAIL = 0


def check(name, cond):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"PASS: {name}")
    else:
        FAIL += 1
        print(f"FAIL: {name}")


def leaks(*texts):
    return any(KEY in t or KEY.upper() in t for t in texts)


def run_script(body, args, env, cwd):
    # how the runner executes a `run:` step: a script file under its default shell
    script = Path(cwd) / "step.sh"
    script.write_text(body)
    return subprocess.run(
        ["bash", "--noprofile", "--norc", "-eo", "pipefail", str(script), *args],
        cwd=cwd, env=env, capture_output=True, text=True, timeout=TIMEOUT,
    )


def base_env(ws):
    return {
        "PATH": os.environ["PATH"],
        "HOME": str(ws),
        "TMPDIR": str(ws),
        "GITHUB_WORKSPACE": str(ws),
        "GIT_CONFIG_GLOBAL": "/dev/null",
        "GIT_CONFIG_NOSYSTEM": "1",
    }


def render(script):
    def sub(m):
        return OUTPUTS[m.group(1)]
    rendered = re.sub(r"\$\{\{\s*steps\.generate-private-key\.outputs\.(\w+)\s*\}\}", sub, script)
    if "${{" in rendered:
        raise ValueError(f"unrendered expression in: {rendered}")
    return rendered


def set_key_step(workflow):
    wf = yaml.safe_load((ROOT / ".github/workflows" / workflow).read_text())
    steps = wf["jobs"][next(iter(wf["jobs"]))]["steps"]
    return next(s for s in steps if s.get("name") == "set private key and account address as job variables")


def workflow_case(workflow):
    with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as tmp:
        ws = Path(tmp)
        github_env = ws / "github_env"
        env = base_env(ws)
        env["GITHUB_ENV"] = str(github_env)
        step = run_script(render(set_key_step(workflow)["run"]), [], env, ws)

        check(f"{workflow}: set-key step exits 0", step.returncode == 0)
        check(f"{workflow}: set-key step keeps the key out of stdout", not leaks(step.stdout))
        check(f"{workflow}: set-key step keeps the key out of stderr", not leaks(step.stderr))
        check(f"{workflow}: set-key step still prints the address", ADDR in step.stdout)
        written = github_env.read_text().splitlines() if github_env.exists() else []
        check(f"{workflow}: $GITHUB_ENV gets exactly the key and the address",
              written == [f"PRIVATE_KEY={KEY}", f"ACCOUNT_ADDR={ADDR}"])

        # the next steps see $GITHUB_ENV in their environment
        next_env = dict(base_env(ws), **dict(line.split("=", 1) for line in written))
        # --noSuchMode runs only the top-level code that every mode runs
        for mode in ("--noSuchMode", "--recordMetadata"):
            res = run_script('bash "$1" "$2"\n', [str(HARNESS), mode], next_env, ws)
            check(f"{workflow}: e2e_test.sh {mode} exits 0 and still prints the address",
                  res.returncode == 0 and ADDR in res.stdout)
            check(f"{workflow}: e2e_test.sh {mode} keeps the key out of stdout", not leaks(res.stdout))
            check(f"{workflow}: e2e_test.sh {mode} keeps the key out of stderr", not leaks(res.stderr))

        # the shape of the workflows' chain, SP, cmd and test steps
        (ws / ".github/action-logs").mkdir(parents=True, exist_ok=True)
        log = ws / ".github/action-logs/start-chain.log"
        tee = run_script(f'set -o pipefail\nbash "{HARNESS}" --noSuchMode 2>&1 | tee "{log}"\n', [], next_env, ws)
        check(f"{workflow}: tee'd e2e_test.sh log has the address and not the key",
              tee.returncode == 0 and ADDR in log.read_text() and not leaks(log.read_text()))


BUILD_CMD = r"""
source "$1"
prepare_moca_go_sdk() { :; }
prepare_moca_cmd() { :; }
build_cmd
"""

MOCA_CMD_STUB = """#!/usr/bin/env bash
# records what moca-cmd would have imported
for a in "$@"; do
  if [ "$a" = key.txt ] || [ "$a" = dev-key.txt ]; then cp "$a" "../imported-$a"; fi
done
"""


def build_cmd_case():
    with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as tmp:
        ws = Path(tmp)
        build = ws / "moca-cmd/build"
        build.mkdir(parents=True)
        stub = build / "moca-cmd"
        stub.write_text(MOCA_CMD_STUB)
        stub.chmod(0o755)
        env = dict(base_env(ws), PRIVATE_KEY=KEY, ACCOUNT_ADDR=ADDR)
        res = run_script(BUILD_CMD, [str(HARNESS)], env, ws)
        check("build_cmd: exits 0", res.returncode == 0)
        check("build_cmd: keeps the key out of stdout and stderr", not leaks(res.stdout, res.stderr))
        imported = ws / "moca-cmd/imported-key.txt"
        check("build_cmd: moca-cmd still imports the key from the environment",
              imported.exists() and imported.read_text() == f"{KEY}\n")
        dev = ws / "moca-cmd/imported-dev-key.txt"
        check("build_cmd: moca-cmd still imports the dev key",
              dev.exists() and re.fullmatch(r"[0-9a-f]{64}\n", dev.read_text()) is not None)


def main():
    for workflow in WORKFLOWS:
        workflow_case(workflow)
    build_cmd_case()
    print(f"TOTAL PASS={PASS} FAIL={FAIL}")
    return 1 if FAIL else 0


if __name__ == "__main__":
    sys.exit(main())
