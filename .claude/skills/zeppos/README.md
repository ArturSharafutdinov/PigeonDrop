# zeppos skill

A Claude Code skill for developing Zepp OS Mini Programs (Amazfit / Zepp watches). It is written to be project-independent so that
it can move into its own repository (or a plugin) and be used by every watch app.

**What it does.** Teaches the agent to look things up instead of recalling them: `scripts/setup.sh` fetches the official
documentation (`zepp-health/zeppos-docs`, only `docs/`), the official samples (`zepp-health/zeppos-samples`) and the API
declarations (`@zeppos/device-types`) into a cache directory and builds a one-line-per-export API index from the declarations.
`SKILL.md` says how to use them; `references/docs-map.md` maps questions to documentation pages; `references/gotchas.md`
records where real devices differ from the documentation.

**What it does not contain.** No copy of the documentation, the samples or the declarations: they are fetched on demand, so they
stay current and nothing of theirs is redistributed (the documentation and the samples are Apache-2.0, Zepp Health).

## Files

```
SKILL.md                        entry point: when to use, how to look things up, orientation, rules
references/gotchas.md           documentation vs device differences, with their evidence
references/docs-map.md          question -> documentation page
scripts/setup.sh                fetches sources into $ZEPPOS_SKILL_CACHE (default ~/.cache/zeppos-skill), builds the index
scripts/build-api-index.mjs     index.d.ts -> api-index.md (no dependencies)
scripts/build-api-index.test.mjs   node --test scripts/build-api-index.test.mjs
```

## Using it

In a project: copy this folder to `.claude/skills/zeppos/` (or install it as a plugin); nothing else is needed. The first lookup
runs `scripts/setup.sh`. Projects with their own `node_modules/@zeppos/device-types` are indexed from that copy (the version they
build against).

## Keeping it right

- `gotchas.md` is the part with lasting value. Add an entry whenever a device contradicts the documentation, with what was
  done, what happened, on which device and API level. Mark whether it was checked against the docs or only seen on a device.
- Update the "docs checked" commit in `gotchas.md` when the entries are re-verified against newer documentation.
- `docs-map.md` paths are relative to `docs/` of the documentation repository; if the documentation is reorganized, check them
  with `ls`.
- Not covered yet: Settings App widgets in depth, watchfaces, workout extensions, the custom keyboard (API 4.2). `docs-map.md`
  points at their documentation, but no gotchas have been collected for them.
