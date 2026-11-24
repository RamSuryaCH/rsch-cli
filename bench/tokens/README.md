# Token benchmark (`bench/tokens`)

Measures how many prompt tokens the optimizer (`internal/optimizer`) saves, optimizer on versus off.

```bash
go run ./bench/tokens                          # o200k_base, 16k context window, table
go run ./bench/tokens -encoding cl100k_base
go run ./bench/tokens -context 128000 -json
BENCH_DEBUG=1 go run ./bench/tokens            # print each tool output's size to stderr
```

## Method

- **Task set:** 8 fixed agent trajectories in `main.go`. Five run on this repository and three on the small
  sample repository in `testdata/sample`. Each one is a sequence of tool calls an agent makes for a task:
  explain how background jobs stop, review the optimizer, summarise the repo, read recent changes, list the
  providers, explain the sample package, run the sample's tests, and summarise the sample. Repeats are kept to
  the ones agents typically make: re-reading a file before editing it, and re-running tests.
- **Tool outputs are real.** Each step runs RSCH's own `view`, `grep`, `ls`, `glob` and `bash` tools on the
  files present at measurement time.
- **What is counted.** For every model request in a task (one before each tool call, plus the final answer),
  the benchmark assembles the system prompt plus the history and counts tokens with
  [tiktoken-go](https://github.com/tiktoken-go/tokenizer) (MIT). It does this with the optimizer on and off,
  then sums over the task's requests. Tool definitions are the same in both modes and are not counted.
- **No model is called.** The trajectories are scripted, so the numbers are deterministic for a given commit and
  say nothing about answer quality. Live runs against Ollama's `prompt_eval_count` were not used. The local
  3B model loops (see the README's limits), and a CPU run of these prompt sizes is slow.

## Results (2026-11-24 IST)

Measured on the Linux x86-64 box (8-vCPU Intel Xeon, Go 1.27.3) at commit `3f0ee44`. o200k_base, 16,384-token
context window:

| task | repo | requests | off tokens | on tokens | reduction |
| --- | --- | ---: | ---: | ---: | ---: |
| explain-background-stop | rsch | 6 | 69,011 | 53,567 | 22.4% |
| review-optimizer | rsch | 6 | 65,253 | 56,788 | 13.0% |
| summarize-repo | rsch | 6 | 91,014 | 70,159 | 22.9% |
| recent-changes | rsch | 4 | 33,223 | 31,405 | 5.5% |
| list-providers | rsch | 5 | 54,810 | 49,376 | 9.9% |
| sample-explain | sample | 4 | 8,940 | 8,940 | 0.0% |
| sample-test-and-fix | sample | 5 | 11,563 | 11,563 | 0.0% |
| sample-summarize | sample | 3 | 6,078 | 6,078 | 0.0% |

- **Median reduction: 7.7%** across the 8 tasks.
- **rsch-repo tasks:** median 13.0%, range 5.5–22.9%.
- **Sample-repo tasks:** 0%. Every output is below the trimming caps, and the repeated test output (80 bytes) is
  below the 256-byte deduplication threshold.
- **Other settings give the same picture.**
  - cl100k_base: the same percentages to within 0.1 point.
  - A 128k window: the same numbers. Compaction never triggered, because no history passed 60% of either window.
- **Where the savings come from:** trimming long file and command outputs once they are older than the two most
  recent tool results, and deduplicating re-read files.

**Reading the numbers.** Savings are mixed. They are real on a mid-sized repository with large files and close to
zero on a tiny one. They also depend on the trajectories chosen. Trimming older outputs saves tokens, but this
benchmark does not measure whether the model then needs to re-read something.

**Observed while building this.** Under the Linux sandbox, the bash tool's `go test` in this repository tried to
download modules and failed, because there was no network inside the sandbox. The rsch-repo tasks therefore use
`git` and `grep` commands instead of `go test`. The sample repository has no dependencies, so its `go test`
step works.
