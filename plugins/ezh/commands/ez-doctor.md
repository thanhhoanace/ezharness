---
description: Check this machine and the ezHarness workspaces (read-only)
allowed-tools: Bash(bash:*)
---
!`bash -c 'bash "$1/hooks/ez-doctor.sh" 2>&1; echo "ez-doctor exit=$?"' ez-doctor "${CLAUDE_PLUGIN_ROOT}"`

Show the ez-doctor output above to the user unchanged, in a code block. Then, for each line that
starts with `ENV PROBLEM` or `PROBLEM`, give the one fix in plain words. For `ENV NOTE workspaces`,
say it is optional. Change nothing on the machine.
