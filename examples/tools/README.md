# Example custom tools

Copyable definitions, not runtime config: nothing in this directory is loaded by
PAI. There are two layers, and they differ in where the file goes and whether a
role has to list it.

## Project tools (`.pai/tools/`)

A project tool lives in `<workspace>/.pai/tools/<name>.toml` and loads only when
the workspace is inside a `[tool] trusted_projects` root:

```toml
# ~/.config/pai/config.toml
[tool]
trusted_projects = ["~/work/myproject"]
```

Copy a file into the workspace's `.pai/tools/`, using the same filename:

```bash
mkdir -p /path/to/project/.pai/tools
cp examples/tools/project/repo_status.toml /path/to/project/.pai/tools/
```

| Example | Payload to try | Shows |
|---|---|---|
| `project/repo_status.toml` | `{}` | no parameters, `dir = "."`, read-only |
| `project/go_test.toml` | `{}` or `{"package":"./internal/cli"}` | a string parameter with a default and a pattern |
| `project/github_rate_limit.toml` | `{}` | HTTP GET with a fixed header (needs network) |
| `project/httpbin_echo.toml` | `{"note":"hello","count":2}` | HTTP POST with body parameters (needs network) |

Project tools are appended to **every** role used in that workspace; no role
edit is needed. The trust gate is the boundary. Their grants are session-only:
**Always** is not offered, and the session trust-all switch does not
auto-approve them.

## User tools (`~/.config/pai/tools/`)

A user tool lives in `~/.config/pai/tools/<name>.toml` and must still be listed
by the role that may call it. Add it without copying the role:

```bash
mkdir -p ~/.config/pai/tools
cp examples/tools/user/lines.toml ~/.config/pai/tools/lines.toml
```

```toml
# ~/.config/pai/role-extra.toml
[coder]
tools = ["lines"]
```

`user/lines.toml` wraps `wc -l -w` for one of a few fixed files and shows a
required `enum` parameter. Try the payload `{"file":"README.md"}`.

## Inspect without running

```bash
pai tool ls
pai tool show repo_status
pai tool check repo_status --json '{}'
pai tool check httpbin_echo --json '{"note":"hello","count":2}'
```

`tool check` validates the payload and prints the exact argv or HTTP request. It
executes nothing and never prints header values.

## Writing your own

The filename is the tool name (`^[a-z][a-z0-9_]{0,40}$`); a `name` field must
match it or be omitted. Built-in names are reserved, and a collision between
layers is a load error.

- A CLI tool runs one argv with **no shell**: `args = ["--x", "{param}"]`.
  Spaces, `;`, and `$(...)` inside a value stay inside that argument.
- An HTTP tool pins its scheme and host; placeholders are allowed only in the
  path, query, and body. `auth = "bearer ENV"` (or `header NAME ENV`) reads the
  secret from the environment at run time. Redirects are refused and responses
  are capped by `max_bytes` (default 32 KiB).
- Parameters support `string`, `integer`, `number`, `boolean`, and `enum`, with
  `required`, `default`, `min`, `max`, `max_length`, and `pattern`.
- The payload is always one JSON object. Defaults are applied and validation
  runs before approval, so the card shows exactly what will run.
