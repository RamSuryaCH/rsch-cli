# Native skills

Project skills live under `.rsch/skills/<name>/` and must contain `SKILL.md`.
Additional directories such as `rules/`, `examples/`, `templates/`, `tools/`,
and `verification/` are copied as ordinary skill assets.

Optional `metadata.json` makes a skill discoverable and composable:

```json
{
  "id": "web",
  "name": "Web",
  "version": "2.0.0",
  "dependencies": ["base@^1.0.0"],
  "conflicts": ["legacy-web"]
}
```

`rsch skill compose web` resolves dependencies in dependency-first order,
rejects cycles, disabled dependencies, incompatible versions, and declared
conflicts, then prints the combined Markdown instructions. Skill tools are
never executed by discovery, audit, resolve, or compose operations.
