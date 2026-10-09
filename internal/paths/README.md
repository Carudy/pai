# paths

Centralizes resolution of PAI's user directories.
See the [module map](../organization.md) for this leaf package's consumers.

## Entry points

- `Home` returns the OS-reported home directory, or an empty string on failure.
- `ConfigDir` returns `XDG_CONFIG_HOME/pai`, falling back to `~/.config/pai`.
- `DataDir` returns `XDG_DATA_HOME/pai`, falling back to `~/.local/share/pai`.
- Directory resolution lives in `paths.go`; callers share these rules rather
  than constructing their own platform-specific locations.

## Boundaries

This package has no internal dependencies and only resolves names.
It does not create directories, load configuration, or open session storage.
Callers must handle an empty result when a directory cannot be determined.

Suggested API inspection from the repository root (no package tests):

```sh
go doc ./internal/paths
```
