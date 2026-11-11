# config

Loads user configuration and edits TOML without discarding surrounding comments.
See the [module map](../organization.md) for its near-leaf position.

## Entry points

- `UserConfig` holds settings; `LoadUserConfig` loads them from `Path()`.
- `types.go` defines TOML shapes, built-in defaults, and context budgets.
  Defaults include streaming enabled, role `devops`, model
  `deepseek:deepseek-v4-flash`, and three recap exchanges. `max_output_tokens`
  is opt-in (0 = the provider default); the provider layer maps it to
  `max_tokens` for chat completions and `max_output_tokens` for the Responses
  API, which is the lever when long outputs end with `finish_reason=length`.
- `SetModel` resolves the `provider:model` selection; `LoadRoleExtra` reads the
  selected role's extra intro text, and `LoadRoleExtraEntry` also returns tools
  the user added under that role. `PresetModels` are the cleaned `provider:model`
  suggestions frontends offer (never an allowlist); when `default_model` is
  unset, the first preset becomes the default.
- `[tool] trusted_cmds`, `trusted_tools`, `trusted_projects`, and
  `trusted_paths` are the hand-edited grants. `AddTrustedCmds`,
  `AddTrustedTools`, and `AddTrustedPath` append without creating duplicates;
  `ResetTemplate` preserves all four. Project tools never reach
  `trusted_tools`; `trusted_projects` only names the workspaces whose
  `.pai/tools/` definitions may load, and the gate itself is applied by
  `prompts`.
- `[conn]` holds the defaults for `pai conn` (`url`, `token`, `origin`). The
  token is a credential: `Redacted` masks it and `ResetTemplate` keeps it, while
  the URL and origin reset with the rest. Flag/environment precedence is resolved
  in `cli`, not here.
- `[serve]` holds the two runtime bounds for `pai serve`: `max_active_sessions`
  (live chat runtimes; an idle one is evicted) and `max_resident` (prepared
  service runtimes, 0 = unbounded). Both are optional pointers in the TOML so an
  omitted key keeps the built-in default — 0 is a meaningful `max_resident`. The
  command's flags override them.
- `SetScalar` / `UnsetScalar` perform comment-preserving scalar edits.
- `ParseServiceManifest` parses and validates a resident-session manifest
  (`[service]` plus `[[watcher]]`), normalizes durations, and produces the
  canonical JSON stored in `sessions.spec`; `ParseStoredServiceSpec` reads it
  back. Interval floors, watcher limits, and command rules are structural rails,
  not cost budgets. The watcher's decision is the probe's own 1/0 output, so the
  manifest carries no query language.
- `LoadRoleExtraEntry` is the role-extra seam for custom tools; the tool
  definitions themselves are parsed by `prompts`.
- `Template`, `WriteTemplate`, `MergeTemplate`, and `ResetTemplate` support
  configuration initialization and maintenance.
- `UserConfig.Redacted` masks credentials for diagnostic output.
- Paper search defaults to arXiv (`[tool] papersearch_source`); optional
  `papersearch_email` identifies Crossref polite-pool requests. No key is needed.
  `[context] paper_limit` defaults to 16384 bytes, separate from `search_limit`.

## Boundaries

Configuration is not per-run state: clients, ports, and cwd belong to
`role.Runtime`. New keys also need CLI key registration and template coverage;
optional template keys should remain commented out.

Suggested validation from the repository root:

```sh
go test ./internal/config
```
