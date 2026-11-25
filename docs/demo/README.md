# Demo GIF

`ground.gif` is the picture at the top of the main README. It shows `hyctl oracle ground --by-sentence` checking two summaries of one support report. One states a number the report does not have, is marked `NOT SUPPORTED: number 1.9` and exits 1. The other is marked `supported` with the report lines that back it and exits 0. No model is called, so the output is the same every time.

Files here:

- `report.txt`: the short report.
- `prompt.txt`: the report inside a Hydra fence, the shape `oracle ground` reads. The fence carries a digest of the report, so change `report.txt` and this file together.
- `summary-bad.txt`, `summary-good.txt`: the two summaries.
- `ground.tape`: the [vhs](https://github.com/charmbracelet/vhs) script.

## Re-record

From the repo root, with `vhs` installed and `hyctl` on your PATH:

```sh
go build -o /tmp/hyctl-demo/hyctl ./cmd/hydra
PATH=/tmp/hyctl-demo:$PATH vhs docs/demo/ground.tape
```

The tape sets a throwaway `HOME` and `HYDRA_HOME`, so nothing is recorded on your machine.

The current GIF was made with `hyctl` built from the commit that last changed this file (after v1.9.0). `go test ./cmd/hydra -run TestDemo_` runs the same two commands and checks the exit codes, so a CLI change that breaks the demo fails CI before the GIF goes stale.
