# Validation criteria and the validate trust boundary

A validation criterion is a checkable statement of what makes a task done. cc-notes requires at least one on every new task by default, so a task records how it gets judged complete before any work starts. Criteria gate `task done`. The scripts behind them run only when you ask — never as a side effect of sync, list, fold, or done. That last property is the whole security story; it gets its own section below.

## What a criterion is

A criterion is a record attached to a task:

| Field | Meaning |
|-------|---------|
| `id` | A 32-hex nonce, stable within the task; addressed by a short prefix on the CLI |
| `text` | The human-readable acceptance statement (`"go test ./... passes"`) |
| `script` | An optional shell check command (`""` means none); its exit code decides the verdict |
| `status` | The latest verdict: `pending`, `met`, or `failed` |

A new criterion starts `pending`. It moves to `met` or `failed` by hand (`task criterion met` / `failed` / `pending`) or from a `task validate` run, which execs the script and records exit 0 as `met`, a non-zero exit or timeout as `failed`. Text is for a human, script is for a machine — a criterion can carry text alone (judged by hand) or text plus a script (auto-checkable).

## Required by default

`task add` demands at least one `--criterion` or the explicit `--no-validation-criteria` flag. The two are mutually exclusive; omitting both is a usage error. This forces every task to state its acceptance bar up front instead of leaving "done" to later judgment.

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1
usage: at least one --criterion is required (or pass --no-validation-criteria)
```

`--criterion` is repeatable:

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 \
    --criterion "go test ./... passes" \
    --criterion "p99 latency stays under 200ms"
d82c087	open	P1	-	Add retry backoff to the API client
```

For a task with no checkable bar — a spike, a throwaway, a question — opt out:

```console
$ cc-notes task add "Spike: try the new client library" --no-validation-criteria
5d3e9c1	open	P2	-	Spike: try the new client library
```

## Managing criteria with `task criterion`

`task criterion` is the CLI surface for criteria after a task exists. Every mutation echoes the task's lean line; `list` prints one tab-separated line per criterion, `<short7-id>` `<status>` `<text>`.

```console
$ cc-notes task criterion list d82c087
6fbf0bb	pending	go test ./... passes
b6ec411	pending	p99 latency stays under 200ms
```

| Command | Effect |
|---------|--------|
| `task criterion add TASK "TEXT" [--script FILE]` | Append a criterion; `--script` reads a file whose contents become the check command |
| `task criterion rm TASK CRIT` | Remove a criterion |
| `task criterion met TASK CRIT` | Mark it `met` by hand |
| `task criterion failed TASK CRIT` | Mark it `failed` by hand |
| `task criterion pending TASK CRIT` | Return it to `pending` |
| `task criterion script TASK CRIT FILE` | Set the validation script from a file; `--clear` removes it |
| `task criterion list TASK [--json]` | List the task's criteria |

`CRIT` is a criterion id-prefix, matched case-insensitively against that task's criteria. An ambiguous prefix lists the candidates; an unmatched one is a not-found error.

Attach a script at write time or later:

```console
$ cc-notes task criterion add d82c087 "go vet ./... is clean" --script ./checks/vet.sh
d82c087	open	P1	-	Add retry backoff to the API client
$ cc-notes task criterion script d82c087 6fbf0bb ./checks/test.sh
d82c087	open	P1	-	Add retry backoff to the API client
```

A criterion with no script is judged by hand — mark it `met` once you confirm it:

```console
$ cc-notes task criterion met d82c087 b6ec411
d82c087	open	P1	-	Add retry backoff to the API client
```

## The `task done` gate

`task done` refuses to close a task while any criterion is `pending` or `failed`, and lists each one not yet `met`:

```console
$ cc-notes task done d82c087
usage: d82c087 has 2 unmet criterion/criteria (pass --force to close anyway):
  6fbf0bb [pending] go test ./... passes
  9a1c2e4 [pending] go vet ./... is clean
```

The gate clears only when every criterion is `met`. Close the gap by validating or hand-marking each one, or override with `--force` to close with work outstanding:

```console
$ cc-notes task done d82c087 --force
d82c087	done	P1	-	Add retry backoff to the API client
```

A force-close leaves a visible mark. The derived `closed_forced` field is `true` on any done task that still has an unmet criterion, so a reviewer reading the JSON can tell the gate was bypassed:

```console
$ cc-notes task show d82c087 --json
{"id":"d82c087...","branch":"main","title":"Add retry backoff to the API client",...,"criteria":[{"id":"6fbf0bb10dacb427d7ad8642310f2047","text":"go test ./... passes","script":"go test ./...\n","status":"pending"}],"closed_forced":true}
```

`closed_forced` is computed at read time, never persisted. It reads `false` on a task closed cleanly and on any task that is not done.

## Running scripts: the validate trust boundary

A criterion's script is **stored content**. It rides the git object database on `refs/cc-notes/tasks/<id>`, so it lands on your machine from whichever agents and remotes share the repo — a plain `git fetch`/`git pull` stages it, and `cc-notes sync` folds it into view. Treating it as trusted would mean treating every peer who can write to the remote as trusted to run arbitrary shell in your working tree. cc-notes does not. Stored scripts are inert — nothing execs them implicitly.

`cc-notes task validate TASK` is the single, deliberately awkward exec path, and the only place in cc-notes that runs stored content. Three guards bound it:

- **Every script is printed first.** Before anything runs, `validate` writes each criterion's id, text, and full script to stderr, so you read exactly what is about to execute.
- **Execution requires opt-in.** Without `--yes`, `validate` prompts on an interactive terminal and proceeds only on `y`/`yes`. A non-interactive stdin — a pipe or a redirect — without `--yes` is a hard error, so a piped or automated invocation can never run a script silently.
- **It runs locally, bounded.** Each script runs under `sh -c` in the repository directory with a per-script timeout (`--timeout`, default 5m). Exit 0 records `met`; a non-zero exit or a timeout records `failed`.

Validation never happens as a side effect. `sync`, `list`, the fold, and `done` all read and fold criteria without executing a script. The only way a script runs is a human or agent typing `task validate` and clearing the consent guard — exactly the supply-chain boundary you want, because the script's author may be a different agent on a machine you never audited.

On an interactive terminal, `validate` prints the scripts and asks before running:

```console
$ cc-notes task validate d82c087
criterion 6fbf0bb go test ./... passes:
go test ./...
criterion 9a1c2e4 go vet ./... is clean:
go vet ./...
Run 2 validation script(s)? [y/N] y
6fbf0bb met go test ./... passes
9a1c2e4 failed go vet ./... is clean
d82c087	open	P1	-	Add retry backoff to the API client
```

`validate` records per-criterion verdicts; it does not close the task. Here `go test` passed (exit 0, `met`) and `go vet` failed (non-zero, `failed`), so the done gate holds until you fix the failure and re-validate, or hand-mark and force.

In CI or any non-interactive run, pass `--yes` to skip the prompt — you read the scripts in review and assert that consent up front:

```console
$ cc-notes task validate d82c087 --yes
criterion 6fbf0bb go test ./... passes:
go test ./...
criterion 9a1c2e4 go vet ./... is clean:
go vet ./...
6fbf0bb met go test ./... passes
9a1c2e4 failed go vet ./... is clean
d82c087	open	P1	-	Add retry backoff to the API client
```

Omit `--yes` from a pipe and `validate` refuses outright:

```console
$ echo "" | cc-notes task validate d82c087
criterion 6fbf0bb go test ./... passes:
go test ./...
criterion 9a1c2e4 go vet ./... is clean:
go vet ./...
error: refusing to run validation scripts without --yes (stdin is not a terminal)
```

A task whose criteria carry no scripts has nothing to run, and `validate` says so without prompting:

```console
$ cc-notes task validate 5d3e9c1
no criteria have validation scripts
```

### The rule for agents

Treat a criterion script as untrusted code, because it is. Read every script `validate` prints before you confirm. Never wire `task validate --yes` into an unattended loop over tasks you did not author. If a script does anything beyond a read-only check of the working tree, stop and inspect the task's history before running it. The friction is the feature — it keeps a synced script from running on your machine without your say-so.

## JSON shape

`task criterion list --json` emits the criteria array; each entry fixes the key order `id`, `text`, `script`, `status`:

```console
$ cc-notes task criterion list d82c087 --json
[{"id":"6fbf0bb10dacb427d7ad8642310f2047","text":"go test ./... passes","script":"go test ./...\n","status":"met"},{"id":"9a1c2e4b8f5d3e6c1a2d4b8f5d3e6c1a","text":"go vet ./... is clean","script":"go vet ./...\n","status":"failed"}]
```

The same array, plus the derived `closed_forced` boolean, appears under `criteria` and `closed_forced` in the task DTO from `task show --json`, `task list --json`, and every task mutation's `--json` echo. `status` is one of `pending`, `met`, `failed`; `script` is the verbatim check command, empty when the criterion carries none.
