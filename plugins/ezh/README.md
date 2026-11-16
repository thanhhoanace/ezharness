# ezharness — ezHarness plugin for Claude Code

ezharness adds four things to every Claude Code session, in the terminal and in VS Code:

| Piece | What it does |
|---|---|
| Plan gate | When a prompt names a plan (`.harness/plans/<slug>.md`), it runs `ez-evidence plan validate` and tells Claude to refuse a plan that fails. |
| Lesson index | At session start inside a configured workspace, it lists the reviewed lessons (`.harness/lessons/`). |
| Skill `ezh` | How to use the evidence gate, plans and lessons. |
| `/ez-doctor` | A read-only check of this machine and of the workspaces. |

The `ez-evidence` binary for macOS (arm64, amd64) and Linux (amd64, arm64) ships inside the
plugin (`libexec/`). Nothing is downloaded at run time. Windows is not supported yet.

## Source and rebuild

The Go source of `ez-evidence` ships next to the binaries, in `source/ez-evidence/` (no
dependencies outside the Go standard library). `libexec/BUILD` names the Go version and the
sha256 of each binary. To rebuild one and compare, use that Go version and run, from this folder:

```bash
cd source/ez-evidence
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid=" -o /tmp/ez-evidence-linux-amd64 ./cmd/ez-evidence
shasum -a 256 /tmp/ez-evidence-linux-amd64    # equals the line in ../../libexec/BUILD
```

`GOOS`/`GOARCH` pairs: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`.
`source_commit` in `BUILD` is the commit of the development repository the tree was built from.

## Install

```text
/plugin marketplace add thanhhoanace/ezharness
/plugin install ezharness@ezharness
/ez-doctor
```

`/ez-doctor` should end with `RESULT: OK`. Each `ENV PROBLEM` line names its fix.

## Requirements

| OS | Needs |
|---|---|
| macOS | git; python 3 (the Xcode command line tools bring both) |
| Linux | bash, git, python 3 |
| Windows | Not supported yet: the plugin ships no Windows binary, so `/ez-doctor` reports it missing and the plan gate blocks a prompt that names a plan. |

Without python the plan gate still runs; the lesson index is off and session start says so.

## Optional: a workspace

Without a workspace the plan gate checks the plans of the repo a session stands in. To also cover
a folder that holds many repos (and to see its lesson index), list that folder:

```bash
mkdir -p ~/.config/ezh && echo /path/to/folder >> ~/.config/ezh/workspaces
```

## Privacy

ezharness collects nothing and sends nothing over the network. There is no account, no
telemetry and no server. Everything below happens on your machine:

| It reads | Why |
|---|---|
| The text of your prompt | to find the plan name (`.harness/plans/<slug>.md`) it should check |
| Plan and lesson files in your repos | to validate the plan, and to list reviewed lessons |
| The size of the session transcript, never its content | to check a plan again when the session moved on |
| Claude Code `settings.json` (one key: `autoMemoryDirectory`) and the session's memory folder | to find memories that may become lessons |
| `~/.config/ezh/` | your workspace list and switches |

| It writes | Where |
|---|---|
| A runnable copy of `ez-evidence` when the installed one lost its exec bit | the plugin data folder (`${CLAUDE_PLUGIN_DATA}`) |
| A small per-session cache (hashes, so it does not check the same prompt twice) | `~/.cache/ezh/sessions/` |
| Memories moved to the lesson inbox, only when `lesson-route` is `on` (off by default) | `~/.local/share/ezh/inbox/` |

It does not look for, use or send API keys, tokens or passwords; from `settings.json` it uses
only the one key above. The hooks and the `ez-evidence` binary run
only the local commands `git`, `python3` and standard shell tools. The Go source of `ez-evidence`
ships in `source/` (see above).

## Switches

| Variable or file | Effect |
|---|---|
| `EZH_ROUTE=off` | turns the lesson route hooks off |
| `~/.config/ezh/lesson-route` = `on` | moves memories created in a session to the lesson inbox (off by default) |

License: MIT.
