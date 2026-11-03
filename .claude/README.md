# `.claude/`

Project configuration for [Claude Code](https://claude.com/claude-code), the AI coding assistant this repository is developed with. Nothing here is needed to build, run or deploy PigeonDrop; it is published because it documents how the assistant is kept on a short leash.

| Path | What it is |
|---|---|
| `settings.json` | Registers the `PreToolUse` hook below. |
| `hooks/protect-files.sh` | Refuses edits to `.env` files, key material, lock files, `.git/` and `node_modules/`. Needs `jq`; without it the hook allows everything. Exit code 2 blocks the edit and shows the reason. |
| `skills/zeppos/` | A skill for Zepp OS development: it makes the assistant look things up in the official documentation and API declarations instead of recalling them. `references/gotchas.md` lists the places where real watches differ from the documentation, with the evidence. |

Personal overrides go into `settings.local.json`, which is ignored by git.
