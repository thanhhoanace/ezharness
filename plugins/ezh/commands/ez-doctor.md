---
description: Check this machine and the ezHarness workspaces (read-only)
allowed-tools: Bash(bash:*)
---
Run this one command with the Bash tool, exactly as written (it only reads; it changes nothing):

```bash
bash "${CLAUDE_PLUGIN_ROOT}/hooks/ez-doctor.sh" 2>&1
```

Show its output to the user unchanged, in a code block, and say its exit code (0 OK, 3 problems
found). Then, for each line that starts with `ENV PROBLEM` or `PROBLEM`, give the one fix in plain
words. For `ENV NOTE workspaces`, say it is optional. Change nothing on the machine.
