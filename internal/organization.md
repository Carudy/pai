# internal/ — module map

PAI is a ports-and-adapters app. The agent loop depends on **interfaces**
(`core`), and the presentation/storage sides implement them. `cli` is the only
package that knows about all of them.

The shape to keep in mind: there is **one loop** (`role`) and **two frontends**
(`tui` for the terminal, `web` + `runner` for the browser), and what a role *is*
lives in data (`prompts`), not in Go.

## How a turn flows

1. `cli` parses the command line, opens the `session` store, loads `config`, and
   resolves the role through `prompts`.
2. `chat.LoadRolePrompt` composes the prompt **head** (shared preamble + `hostinfo`
   facts + role intro + tool specs + any project context files) and the **tail**
   (the output contract). `RolePrompt.Head()` is built once per run; `Tail()` is
   re-rendered for every request.
3. `role.Run` drives the loop: `chat` builds the request, `provider` streams the
   completion, `chat.ParseResponse` validates the JSON response, and `role`
   dispatches the named tool to `tool` — asking the user first, through a
   `core.Prompter`, when trust does not already cover it.
4. Every turn is recorded through `core.Recorder`, which `session` implements.
5. What is *replayed* is not the whole transcript. Per request the loop selects a
   window (`chat.ReplayTurns`: the last summary checkpoint plus `max_turns`),
   applies the user's surgery (`chat.ApplyOverrides`), and elides old tool output
   (`chat.CompactHistory`). Past a token budget, `chat.ShouldSummarize` asks the
   model to summarize the oldest turns instead.
6. In server mode `runner` hosts one bounded, concurrent runtime per session and
   `web` streams its events to the browser over SSE; in terminal mode `tui`
   renders the same `core.Observer` events inline. `cli` wires both.

## Layers

```
  cmd/pai ─ main: flags and signals only
     │
    cli ───── composition root: parses, wires, runs
     │
     ├── tui ──────────────┐          terminal frontend
     │                     ▼
     └── web ──► runner ─► role ─ the single agent loop
                             │
        ┌────────┬───────────┼──────────┬────────────┐
        ▼        ▼           ▼          ▼            ▼
      chat     tool      prompts    config      session
        │        │           │          │            │
        └────────┴───────────┴──────────┴────────────┘
                             │
                core ────────┴──── paths, provider, hostinfo
```

The bands are a reading aid; `session` is reached only through `cli`, and the
authoritative graph is [Dependency rules](#dependency-rules) below.

## Packages

| Package | Responsibility | Key types |
|---|---|---|
| `core` | The ports the loop needs from its host, plus shared value types. No internal deps. | `Observer`, `Prompter`, `Recorder`, `Sessions`, `Steerer`, `Turn`, `ToolCall`, `ToolResult`, `Usage`, `TrustChoice`, `ErrAborted` |
| `paths` | XDG-aware directories. Leaf. | `ConfigDir`, `DataDir`, `Home` |
| `provider` | LLM transport: OpenAI-compatible chat-completions and Responses protocols, streaming, provider registry. Leaf, stdlib only. | `Provider`, `CreateClient`, `ProviderConfig`, `Message`, `CompletionParams`, `ChatCompletion`, `ReasoningEffort` |
| `hostinfo` | The host facts embedded in the system prompt (OS, platform, shell, user, date, workspace). Leaf, so composition never shells out. | `Describe` |
| `prompts` | Role/tool **definitions as data** (`roles/*.toml`, `tools/*.toml`), embedded, plus user overrides from `~/.config/pai/roles/`. The only package that knows the definition file format. | `RoleNames`, `ToolNames`, `ReadRole`, `ReadTool`, `LoadRole`, `LoadTool`, `Role`, `Tool` |
| `config` | Configuration only: `config.toml` loading, the `provider:model` split, role extras, and the comment-preserving editor. | `UserConfig`, `ContextConfig`, `LoadUserConfig`, `LoadRoleExtra`, `SetScalar`/`UnsetScalar`, `AddTrustedCmds`, `AddTrustedPath` |
| `chat` | The LLM conversation protocol: composes the prompt head and tail, builds the message list, parses/validates the JSON response, retries, streams, truncates tool output, and projects what a request would send. Parses no definition files and probes no host. | `RolePrompt`, `ParseResponse`, `Ports`, `OutputGuide`, `ReplayTurns`, `ApplyOverrides`, `CompactHistory`, `ContextCards`, `Truncate` |
| `tool` | Tool **execution** and trust checks: local shell, SSH, web and academic search, plus command/path allowlists. Never prompts the user. | `ExecuteCommand`, `ExecuteCommandAt`, `RemoteManager`, `Search`, `PaperSearch`, `IsTrusted`, `IsTrustedPath`, `SplitSegments` |
| `role` | The **single agent loop** and the tool-handler registry (`tools.go`) plus the file-edit planner (`filetools.md`). Runs a role to completion. | `Runtime`, `Run` |
| `session` | Session persistence behind one `Store` interface; SQLite only. Keeps the per-turn **override layer** that shapes what a run sends. The cross-process lock uses `golang.org/x/sys`, so that dependency is not SQLite-specific. | `Store`, `Meta`, `Session`, `Override`, `Recorder` |
| `tui` | Terminal adapters implementing `core` ports. Knows about styling; `core` does not. Imports `tool` only for the shared command splitter. | `LineObserver`, `Prompter`, `Logger` |
| `runner` | Transport-independent, bounded concurrent runtime hosting: input routing, prompts, event subscriptions, per-session settings that outlive a runtime. Storage arrives through a backend port. | `Manager`, `Backend`, `Snapshot`, `ContextView`, `ContextBackend`, `ContextSetter` |
| `web` | HTTP/SSE adapter and the embedded, dependency-free browser UI, with cookie authentication and origin validation. | `New`, `Options` |
| `cli` | Composition root: subcommand dispatch, flag parsing, wiring, signal handling; serves per-session persisted workspaces without changing process cwd. Only package that imports `tui`/`session`. | `Run`, `CliFlags` |

## Dependency rules

- Nothing except `cli` may import `tui` or `session`.
- `core` must stay dependency-light — it is the shared vocabulary.
- No cycles. The graph below (`X -> Y` means X imports Y) is checked against the
  code, not aspirational:

  ```
  cmd/pai -> cli
  cli     -> chat config core paths prompts provider role runner session tui web
  web     -> config core provider runner
  runner  -> config core provider role
  role    -> chat config core prompts provider tool
  chat    -> config core hostinfo prompts provider
  session -> core paths
  tui     -> core tool
  tool    -> paths
  config  -> paths provider
  prompts -> paths
  core, paths, provider, hostinfo -> (nothing internal)
  ```

- External dependencies are deliberately few, and each earns its place:

  | Package | Dependency | Why |
  |---|---|---|
  | `prompts`, `config`, `cli` | `github.com/BurntSushi/toml` | The definition and config file format |
  | `session` | `modernc.org/sqlite` | Pure-Go SQLite, so releases stay cgo-free |
  | `session` | `golang.org/x/sys/{unix,windows}` | The cross-process data-directory lock |
  | `tool` | `mvdan.cc/sh/v3/syntax` | The real shell parser behind `SplitSegments` |
  | `tui` | `charm.land/lipgloss/v2`, `charmbracelet/bubbletea`, `bubbles/textinput`, `x/ansi` | Terminal styling and interaction |

## Design decisions worth preserving

- **One loop, data-defined roles.** There is exactly one agent loop (`role`).
  Roles differ only by their TOML intro + tool list, so adding one is a file, not
  Go code. Never hardcode a role or tool list: generate them from
  `prompts.RoleNames()` / `prompts.ToolNames()`.
- **The response contract is system-owned.** `chat.OutputGuide()` is rendered
  *after* the chat history on every request, so the model is reminded of the exact
  JSON shape right before it generates. A user prompt can never break parsing.
- **The prompt head is byte-stable.** Shared preamble + terminal info + role intro
  + tool specs are composed once per run to keep provider prefix caching
  effective; only the tail is re-rendered.
- **Storage is append-only; the projection is computed.** The stored transcript is
  never rewritten. Elision is recalculated per request, and a user's edit or drop
  is a separate layer (`session.Override`) applied while building the request. That
  is why the inspector can show original *and* sent text side by side.
- **Ports stay semantic.** Styling never crosses `core.Observer` — events carry
  meaning, and an adapter decides how they look.
- **Capability boundaries are enforced.** A role's `tools` list is checked against
  the handler registry at startup (`checkToolCoverage`), so a role can never
  declare a tool that cannot run.
- **One parser for command chaining.** `tool.SplitSegments` is the single source of
  truth for "how many commands is this": the trust check and the confirmation
  display both use it, so the count shown cannot disagree with what trust saw. The
  browser's own splitter is presentation only and never feeds execution.
- **One owner per concern at the bottom.** `prompts` is the only package that
  parses a role/tool definition; `hostinfo` is the only place that probes the host,
  so `chat` composes prompts from resolved values and never shells out. Keep those
  seams: they are what let `chat` stay a protocol package.
- **Validate at the boundary; feed failures back as data.** A tool handler does not
  error on bad model output — it returns a `[tool error]` observation so the model
  can self-correct.
