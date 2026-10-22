# config

Loads user configuration and edits TOML without discarding surrounding comments.
See the [module map](../organization.md) for its near-leaf position.

## Entry points

- `UserConfig` holds settings; `LoadUserConfig` loads them from `Path()`.
- `types.go` defines TOML shapes, built-in defaults, and context budgets.
  Defaults include streaming enabled, role `devops`, model
  `deepseek:deepseek-v4-flash`, and three recap exchanges.
- `SetModel` resolves the `provider:model` selection; `LoadRoleExtra`
  reads the selected role's extra intro text. `PresetModels` are the cleaned
  `provider:model` suggestions frontends offer (never an allowlist); when
  `default_model` is unset, the first preset becomes the default.
- `[conn]` holds the defaults for `pai conn` (`url`, `token`, `origin`). The
  token is a credential: `Redacted` masks it and `ResetTemplate` keeps it, while
  the URL and origin reset with the rest. Flag/environment precedence is resolved
  in `cli`, not here.
- `SetScalar` / `UnsetScalar` perform comment-preserving scalar edits.
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
