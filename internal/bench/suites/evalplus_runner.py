# SPDX-License-Identifier: MIT
# Grades one EvalPlus job the way evalplus.eval.unsafe_execute does (Apache-2.0).
# Prints one line and exits 0 pass, 1 fail, 3 timeout. Usage: python3 -I -B runner.py job.json
import contextlib
import copy
import io
import json
import math
import os
import signal
import sys
import time

FAIL, TIMEOUT = 1, 3
LOAD_LIMIT = 10.0
ALARM = {"fired": False}
DENIED = ("socket.", "subprocess.", "os.system", "os.exec", "os.posix_spawn", "os.spawn",
          "os.fork", "os.kill", "os.killpg", "pty.", "ctypes.", "webbrowser.")
WRITES = ("os.remove", "os.rename", "os.rmdir", "os.mkdir", "os.chmod", "os.truncate",
          "os.symlink", "os.link", "shutil.")


class Timeout(BaseException):
    pass


def finish(code, line):
    sys.__stdout__.write(line + "\n")
    sys.__stdout__.flush()
    os._exit(code)


def guard(event, args):
    # No network, no child process, no write outside the working directory.
    if event.startswith(DENIED) or (event == "import" and args[0] in ("ctypes", "_ctypes")):
        raise PermissionError("sandbox: " + event + " is not allowed")
    if event == "open" and writes(args[1], args[2]):
        inside(event, [args[0]])
    if event.startswith(WRITES):
        inside(event, args)


def writes(mode, flags):
    if mode:
        return any(c in mode for c in "wax+")
    return bool(flags & (os.O_WRONLY | os.O_RDWR | os.O_APPEND | os.O_CREAT | os.O_TRUNC))


def inside(event, args):
    paths = [os.fsdecode(a) for a in args if isinstance(a, (str, bytes))]
    if not paths:
        raise PermissionError("sandbox: " + event + " on a descriptor")
    for p in paths:
        real = os.path.realpath(p)
        if real != CWD and not real.startswith(CWD + os.sep):
            raise PermissionError("sandbox: " + event + " outside the working directory")


def on_alarm(*_):
    # A second alarm means the solution swallowed the first: stop the process.
    if ALARM["fired"]:
        finish(TIMEOUT, "timeout: the solution ignored its time limit")
    ALARM["fired"] = True
    raise Timeout()


def limited(limit, f, *args):
    ALARM["fired"] = False
    signal.setitimer(signal.ITIMER_REAL, limit, 0.5)
    try:
        with contextlib.redirect_stdout(io.StringIO()):
            return f(*args)
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)


def call(fn, inp):
    return fn(*copy.deepcopy(inp))


def is_floats(x):
    if isinstance(x, float):
        return True
    return isinstance(x, (list, tuple)) and bool(x) and all(isinstance(i, float) for i in x)


def close(out, exp, atol):
    # np.allclose(out, exp, rtol=1e-07, atol=atol) for a scalar or a flat sequence.
    if type(out) is not type(exp):
        return False
    pairs = list(zip(out, exp)) if isinstance(exp, (list, tuple)) else [(out, exp)]
    if isinstance(exp, (list, tuple)) and len(out) != len(exp):
        return False
    try:
        return all(abs(o - e) <= atol + 1e-07 * abs(e) for o, e in pairs)
    except TypeError:
        return False


def poly(xs, x):
    return sum(c * math.pow(x, i) for i, c in enumerate(xs))


def matches(job, inp, out, exp):
    atol = job["atol"]
    if job["entry_point"] == "find_zero":  # HumanEval/32 checks a root, not a value
        return abs(poly(*inp, out)) <= atol
    if out == exp:
        return True
    if atol == 0 and is_floats(exp):
        atol = 1e-6
    return atol != 0 and close(out, exp, atol)


def load(code, entry_point):
    env = {}
    exec(compile(code, "solution.py", "exec"), env)
    return env[entry_point]


def main():
    with open(sys.argv[1]) as f:
        job = json.load(f)
    inputs = job["base"] + (job["plus"] if job["use_plus"] else [])
    signal.signal(signal.SIGALRM, on_alarm)
    sys.addaudithook(guard)
    ref = limited(0, load, job["reference"], job["entry_point"])
    expected, limits = [], []
    for inp in inputs:
        start = time.perf_counter()
        expected.append(limited(0, call, ref, inp))
        limits.append(max(1.0, 4 * (time.perf_counter() - start)))
    try:
        fn = limited(LOAD_LIMIT, load, job["code"], job["entry_point"])
    except Timeout:
        finish(TIMEOUT, "timeout: loading the solution took over %.0fs" % LOAD_LIMIT)
    except BaseException as e:
        finish(FAIL, "does not load: %s: %s" % (type(e).__name__, str(e)[:200]))
    n = len(inputs)
    for i, inp in enumerate(inputs):
        try:
            ok = limited(limits[i], lambda: matches(job, inp, call(fn, inp), expected[i]))
        except Timeout:
            finish(TIMEOUT, "timeout: input %d of %d over %.1fs" % (i + 1, n, limits[i]))
        except BaseException as e:
            finish(FAIL, "input %d of %d raised %s: %s" % (i + 1, n, type(e).__name__, str(e)[:200]))
        if not ok:
            finish(FAIL, "input %d of %d: wrong output" % (i + 1, n))
    finish(0, "passed %d inputs" % len(inputs))


CWD = os.path.realpath(os.getcwd())
main()
