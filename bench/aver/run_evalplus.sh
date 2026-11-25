#!/usr/bin/env bash
# Score the fetched EvalPlus samples. Usage: run_evalplus.sh DATA_DIR [head ...]
# Model code runs only under sandbox.sb (macOS sandbox-exec), with the network denied.
set -euo pipefail

if [ "$(uname -s)" != Darwin ] || ! command -v sandbox-exec > /dev/null; then
  echo "run_evalplus.sh needs macOS sandbox-exec; it will not run generated code without it" >&2
  exit 1
fi

here=$(cd "$(dirname "$0")" && pwd -P)
data=$(cd "${1:?usage: run_evalplus.sh DATA_DIR [head ...]}" && pwd -P)
shift
only=" $* "
export HOME="$data/home" TMPDIR="$data/tmp" EVALPLUS_MAX_MEMORY_BYTES=-1
mkdir -p "$HOME" "$TMPDIR"

if [ ! -x "$data/venv/bin/evalplus.evaluate" ]; then
  python3 -m venv "$data/venv"
  "$data/venv/bin/python" -m pip install --quiet "evalplus==0.3.1"
fi
# Problem files are read here, with the network on. The scoring below has none.
"$data/venv/bin/python" -I -c \
  "from evalplus.data import get_human_eval_plus as h, get_mbpp_plus as m; h(); m()"

for suite in humaneval mbpp; do
  for dir in "$data/samples/$suite"/*/; do
    head=$(basename "$dir")
    [ "$only" = "  " ] || [[ "$only" == *" $head "* ]] || continue
    out="$data/results/$suite/$head.json"
    [ -f "$out" ] && continue
    rm -f "$dir/eval_results.json"
    echo "scoring $suite/$head"
    sandbox-exec -D WORK="$data" -f "$here/sandbox.sb" \
      "$data/venv/bin/evalplus.evaluate" "$suite" --samples "$dir"
    mkdir -p "$(dirname "$out")"
    mv "$dir/eval_results.json" "$out"
  done
done
