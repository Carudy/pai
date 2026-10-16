# role

The single agent loop, parameterized by a data-defined role.
See the [module map](../organization.md) for the loop's dependencies and ports.

## Entry points

- `Run` drives model actions, tools, questions, and completion.
- `Runtime` holds per-run clients, semantic ports, transcript state, and optional
  persistence; configuration remains in `config.UserConfig`.
- Set `Runtime.WorkingDir` before running and keep it immutable during the run.
  It scopes local tools and project instructions; empty preserves process cwd.
  Separate runtimes can use separate workspaces without changing process cwd.
- `Runtime.Interrupt` cancels the current step rather than the entire runtime.
- `tools.go` owns tool dispatch and capability coverage, including `papersearch`
  object validation, provider errors as observations, and complete-record output
  budgeting. The `coder` and `latex` roles enable keyless academic discovery;
  `devops` does not.
  `filetools.go` handles file operations and approvals; `fileread.go`,
  `fileedit.go`, and `filesafety.go` implement bounded reads, batch planning,
  and guarded atomic publication. See [file-tool design](filetools.md) for
  contracts, safety limits, and upstream comparisons. `command.go` declares
  in-session commands and generated help.

## Boundaries

Roles differ by TOML, not separate loops. Tool handlers validate model payloads
and return bad input as tool-error observations so the model can self-correct.
Approval belongs here via `core.Prompter`, not the execution layer.
Use `core` ports instead of importing terminal or session adapters.

Suggested validation from the repository root:

```sh
go test ./internal/role
```

## Built-in roles

A role is data: an intro (its system prompt) plus the set of tools it may use.
There is a single agent loop — roles differ only by prompt and tool set.

### `devops` — DevOps (default)
Autonomous reason→act→observe loop for multi-step sysadmin tasks. Tools:
- **execute** — Run local shell commands (an optional `dir` runs one in another directory, no `cd … && …` needed)
- **remote** — Run commands on remote servers via SSH
- **read** — Read a text file (numbered, bounded) instead of `cat`
- **edit** — Replace exact text in a file, with a diff to approve
- **write** — Create a new file (never overwrites) without a shell here-doc
- **websearch** — Search the web for current information (config `tavily_api_key` or `TAVILY_API_KEY` env)


```bash
pai "deploy my app to staging"
pai -i                        # Interactive: keep loop alive for follow-ups
```

#### Remote Host Management
Configure hosts in `~/.ssh/config` as usual:
```bash
pai "check nginx status on myserver"
# Connections are cached via SSH ControlMaster — no re-auth between commands.
```

ssh runs the command through the remote login shell non-interactively, so
profile files aren't sourced and the remote `PATH`/env may be missing. This is
common with **nix** and asdf, which set `PATH` from `/etc/profile` (a file fish,
for example, never reads — hence `fish: Unknown command: netbird`). Point pai at
a *login* POSIX shell and it wraps each command accordingly:
```bash
pai config set remote_shell bash   # → bash -lc '<cmd>'
```
A bare name gets `-lc` appended; set a value containing a space (e.g.
`"bash -lc"`) to use it verbatim as the prefix.

Leave `remote_shell` empty (the default) to keep plain ssh behaviour. Only set a
shell that exists on the remote host.

#### Web Search
The role automatically searches when it encounters unfamiliar terms or needs current info:
```bash
export TAVILY_API_KEY="your-key"
pai "what's the latest Kubernetes CVE and how do I patch it"
```

### `coder` — Software engineering
Helps read, write, refactor, and test code in the current repository. Tools:
- **execute** — Inspect and modify the repo, run builds and tests
- **read** — Read a file region with line numbers (preferred over `cat`)
- **edit** — Replace exact text in a file, reviewed as a diff before it is written
- **write** — Create a new file from data, not a shell here-doc (never overwrites)
- **websearch** — Look up libraries, APIs, and error messages

It deliberately has no **remote** tool: a role's tool list is its capability
boundary, not just a prompt hint.

It also folds the repository's own instructions into its system prompt: if
`AGENTS.md` exists in the working directory or any parent, its
contents are appended as *project instructions* — subordinate to the role rules
and to the response format. See `context_files` under Custom roles.

```bash
pai -r coder "why does the build fail, and fix it"
```

### `latex` — Scientific writing and LaTeX

Drafts, polishes, restructures, and debugs manuscripts while preserving scientific
meaning, equations, labels, citation keys, and existing project conventions.
Tools: **read**, **edit**, **write**, **execute**, **websearch**, **papersearch**;
no remote execution. It reads project instructions from `AGENTS.md`.

```bash
pai -r latex "polish the introduction without changing the scientific claims"
pai -r latex "fix undefined citations using the project's existing build"
```

The role distinguishes verified bibliography metadata from missing information,
never invents evidence, and does not equate abstracts with full-text reading.
Builds follow the project's engine/tooling and run non-interactively. Missing TeX
tools and pre-existing warnings are reported, not hidden; successful compilation
is not a visual PDF check. Shell escape requires explicit user authorization.

A local `~/.config/pai/roles/latex.toml` **overrides** this built-in role, including
its tool list. Keep it for customization; to use the built-in role instead, move
it out of the roles directory or rename it without the `.toml` extension.

### Paper Search

The `coder` and `latex` roles can discover academic papers without a search API
key; `devops` deliberately does not include this tool:

```bash
pai -r latex "find recent arXiv papers about retrieval augmented generation evaluation"
pai -r coder "find Crossref papers on distributed consensus and cite their DOIs"
pai config set papersearch_source crossref  # default when the model omits source
pai config set papersearch_email you@example.org  # optional Crossref polite pool
pai config set context.paper_limit 16384   # bytes of formatted paper results
```

`papersearch` takes an object, unlike `websearch`'s string query:

```json
{"source":"arxiv","query":"\"decision tree\" homomorphic","limit":5,"year_from":2023,"sort":"relevance","match":"any","offset":0}
```

- **Sources:** arXiv is strongest for CS/math/physics preprints; Crossref searches
  DOI/bibliographic metadata across disciplines, but abstracts are often absent.
  One provider is queried per call; neither is a comprehensive scholarly index.
- **Options:** `source` is optional (default `arxiv`, configurable); `query` is
  required plain-text keywords, not provider syntax. `limit` defaults to 5
  (maximum 20), `year_from`/`year_to` are inclusive, and `sort` is `relevance`
  or `newest`. arXiv dates refer to first submission; Crossref dates refer to
  publication. `offset` (0-based) walks a result set.
- **Matching:** searching is lexical, so include your own variants and wrap exact
  phrases in `"double quotes"`. Any provider operators or field prefixes you
  write (`AND`, `ti:`) are treated as literal words. `match` defaults to all-of,
  which **falls back to relevance-ranked any-of only when the strict query matches
  nothing**; set `match:"any"` to search any-of keywords directly, or
  `match:"all"` to require every keyword and accept an empty result. Crossref
  always relevance-ranks any-of keywords, so `match:"all"` is refused there.
- **Diagnostics:** every result header reports the query actually sent, the match
  mode used, the provider's candidate count, the year filter, the sort, and the
  next offset. That separates a genuinely empty result from a too-narrow or
  misdirected query, so a repeat search can change something real. Crossref
  records also show that provider's relevance score (not comparable across sources).
- **Output:** original titles, authors, dates, identifiers, URLs, and available
  abstracts—not an extra AI summary. Abstracts are capped at 2,000 characters
  with a truncation marker. The separate 16,384-byte default budget omits whole
  records with a notice rather than severing their identifiers. Full-text/PDF
  reading, file downloads, cross-source merge, and citation-graph traversal are
  not implemented.
- **Network/privacy:** the explicit query goes to the selected public API;
  an optional contact email goes only to Crossref. No local files are included.
  Tool observations may be retained in persisted sessions. Keyless does not
  mean unlimited: requests share process-wide throttles (arXiv: one connection,
  starts at least three seconds apart; Crossref: one start per second), a
  45-second deadline, and one bounded retry for HTTP 429/503. An arXiv all-of
  query that matches nothing costs one extra request (the any-of fallback), and
  both count against the same throttle. Separate PAI processes do not share these
  throttles; coordinate them when using arXiv.
- **Evidence:** metadata/abstracts are untrusted source material, not instructions;
  missing abstracts are explicit. Preprints, DOIs, and publication types do not
  establish peer review or paper quality. The tool does not claim full-text access.

User role overrides must add `"papersearch"` to their `tools` list to enable it.
The existing Tavily-backed `websearch` remains unchanged.


### Custom roles

Roles are data, so you can add your own without rebuilding. Drop a file at
`~/.config/pai/roles/<name>.toml`:

```toml
name        = "writer"
description = "Technical writing assistant"

tools = ["execute", "websearch"]

intro = '''
You are a technical writer. Help draft, edit, and tighten prose.
Read whichever files you need with the execute tool before rewriting anything.
'''
```

Then run it with `pai -r writer`. A user role with the same name as a built-in
one **overrides** it — handy for retuning `devops` without editing the source.

`tools` may only reference built-in tools (`execute`, `remote`, `websearch`,
`papersearch`, `read`, `edit`, `write`): tool *implementations* live in Go, so new tools require
code — new roles do not.

`context_files` folds project instruction files into the system prompt, searched
upwards from the working directory (nearest match wins) and capped at 8 KiB:

```toml
context_files = ["AGENTS.md", "CLAUDE.md"]
```

PAI prints a one-line notice whenever it uses one, so repository content
influencing the model is never silent.

See the [Trust and approvals](../../README.md#-trust-and-approvals) section for how
tool calls are confirmed.

## Precise file operations

`edit` supports several disjoint changes to **one file** in one reviewed call:

```json
{"path":"main.go","edits":[{"old_string":"first original block","new_string":"first replacement"},{"old_string":"second original block","new_string":"second replacement"}]}
```

- Each entry matches the **original file**, not earlier replacements; all entries
  must validate before anything is written, and overlapping ranges are rejected.
  The original top-level `old_string`/`new_string`/`replace_all` shape still works,
  but cannot be mixed with `edits` (1–50 edits, up to 10000 replacement sites),
  and a missing replacement field is an error, not an accidental deletion.
- Matching is exact except for CRLF/LF representation. Untouched bytes and a UTF-8
  BOM are preserved; indentation and Unicode punctuation are not fuzzy-matched.
  Previews show full affected lines with context, mark truncation, and are bounded.
- After approval, edits recheck file/parent identity, contents, and permissions.
  If changed, the model must read again and request a fresh edit. `write` remains
  create-only, with atomic no-clobber publication even if a path appears during
  approval. Failed operations do not grant directory trust.
- `read` returns complete numbered lines up to 2000 lines or `context.exec_limit`
  bytes (default 8000). Its continuation offset follows what was actually shown;
  oversized first lines receive an actionable notice instead of partial text.
  Total line counts appear only when known. Strip only the line number and tab
  when copying text for an edit.
- File tools handle regular UTF-8 text, not devices/directories or binary data.
  Mutations are limited to 16 MiB and refuse final-path symlinks; explicitly edit
  the real target instead. Parent directories must exist. Create-only publication
  requires filesystem hard-link support and fails safely if unavailable.

These checks protect against stale approvals and serialize PAI's own commits;
they are not a filesystem sandbox or a portable lock against external writers.
Atomic edits preserve permission bits, not inode identity, ownership, or extended
attributes. See [file-tool design](filetools.md) for the upstream comparison,
exact contracts, and remaining limitations.
