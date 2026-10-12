# hostinfo

The host facts PAI states in the system prompt: OS detail, platform, shell, user,
date, and the workspace.

## Entry points

- `Describe(workingDir)` renders the block that the conversation protocol embeds.
  An empty `workingDir` reports the process cwd without changing it.
- The OS detail prefers a friendly distribution name (`PRETTY_NAME` on Linux,
  `sw_vers` on macOS) and falls back to `runtime.GOOS`.

## Boundaries

Leaf package with no internal dependencies, so prompt composition never shells
out: probing the host is a host concern, not a protocol one. It reads the
environment and reports it; it does not resolve configuration or decide policy.

Suggested validation from the repository root:

```sh
go test ./internal/hostinfo
```
