# ezh — ezHarness plugin for Claude Code

ezh adds four things to every Claude Code session, in the terminal and in VS Code:

| Piece | What it does |
|---|---|
| Plan gate | When a prompt names a plan (`.harness/plans/<slug>.md`), it runs `ez-evidence plan validate` and tells Claude to refuse a plan that fails. |
| Lesson index | At session start inside a configured workspace, it lists the reviewed lessons (`.harness/lessons/`). |
| Skill `ezh` | How to use the evidence gate, plans and lessons. |
| `/ez-doctor` | A read-only check of this machine and of the workspaces. |

The `ez-evidence` binary for macOS (arm64, amd64), Linux (amd64, arm64) and Windows (amd64)
ships inside the plugin (`libexec/`). Nothing is downloaded at run time.

## Install

```text
/plugin marketplace add thanhhoanace/ezharness
/plugin install ezh@ezharness
/ez-doctor
```

`/ez-doctor` should end with `RESULT: OK`. Each `ENV PROBLEM` line names its fix.

## Requirements

| OS | Needs |
|---|---|
| macOS | git; python 3 (the Xcode command line tools bring both) |
| Linux | bash, git, python 3 |
| Windows | **Git for Windows** (Claude Code runs the hooks in its Git Bash) and Python 3 from python.org or `winget install Python.Python.3.12`. Status: **not yet verified on a real Windows machine.** |

Without python the plan gate still runs; the lesson index is off and session start says so.

## Optional: a workspace

Without a workspace the plan gate checks the plans of the repo a session stands in. To also cover
a folder that holds many repos (and to see its lesson index), list that folder:

```bash
mkdir -p ~/.config/ezh && echo /path/to/folder >> ~/.config/ezh/workspaces
```

## Switches

| Variable or file | Effect |
|---|---|
| `EZH_ROUTE=off` | turns the lesson route hooks off |
| `~/.config/ezh/lesson-route` = `on` | moves memories created in a session to the lesson inbox (off by default) |

License: MIT.
