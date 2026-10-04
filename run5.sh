#!/bin/zsh
S=/tmp/claude-501/-Users-ankitjha-IdeaProjects-hydra/babf4603-0a32-4bb3-98f7-8513528c9aa5/scratchpad/audit
cd $S/proj
for i in 1 2 3 4 5; do
  out=$($S/hy.sh edit --file $S/proj/calc.go --enum GRUNT --prompt "add a Sub function that returns a - b" 2>/dev/null)
  st=$(echo "$out" | python3 -c "import json,sys;d=json.load(sys.stdin);print(d['status'], d.get('error',''), 'head=' + str(d.get('head','<absent>')))")
  echo "run $i: $st"
done
