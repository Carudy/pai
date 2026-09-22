# internal/ — module map

PAI is a ports-and-adapters app. The agent loop depends on **interfaces**
(`core`), and the presentation/storage/logging side implements them. `cli` is
the only package that knows about all of them.

## Layers

```
                 cmd/pai (main: signals only)
                        │
                    cli  ← composition root: parses, wires, runs
                        │
        ┌───────────────┼────────────────────────┐
        │               │                        │
      role            session                   tui
   (the loop)      (persistence)          (terminal adapters)
        │                                        │
   ┌────┴────┬─────────┬─────────┐               │
 chat     tool     config    prompts             │
   └────┬────┴─────────┴─────────┘               │
        │                                        │
      core  ◄────────────────────────────────────┘
   (ports + shared types)

   paths, provider  — leaf packages, no internal deps
```

## Packages

| Package | Responsibility | Key types |
|---|---|---|
| `core` | The ports the loop needs from its host, plus shared value types. No internal deps. | `Observer`, `Prompter`, `Logger`, `Recorder`, `ErrAborted`, `Turn`, `ToolCall`, `ToolResult`, `Usage` |
| `paths` | XDG-aware directories (`ConfigDir`, `DataDir`, `Home`). Leaf. | — |
| `provider` | LLM transport. OpenAI-compatible HTTP + streaming; provider registry. Leaf. | `Provider`, `Message`, `CompletionParams`, `ChatCompletion`, `ReasoningEffort` |
| `prompts` | Role/tool **definitions as data** (`roles/*.toml`, `tools/*.toml`), embedded, plus user overrides from `~/.config/pai/roles/`. | `RoleNames`, `ReadRole`, `ReadTool`, `ToolNames` |
| `config` | Configuration only: `config.toml` loading, the `provider:model` split, and the comment-preserving editor. | `UserConfig`, `LoadUserConfig`, `Path`, `SetModel`, `SetScalar`/`UnsetScalar` |
| `chat` | The LLM conversation protocol: builds the message list, parses/validates the JSON response, retries, streams, truncates tool output. | `RolePrompt`, `Ports`, `ChatStr`, `OutputGuide`, `ActionType` |
| `tool` | Tool **execution** only: local shell, SSH, web search. Never prompts the user. | `ExecuteCommand`, `RemoteManager`, `Search` |
| `role` | The **single agent loop** and the tool-handler registry. Runs a role to completion. | `Runtime`, `Run` |
| `session` | Session persistence behind one `Store` interface; SQLite by default, JSONL with `-tags filestore`. | `Store`, `Meta`, `Session`, `Recorder` |
| `tui` | Terminal adapters implementing `core` ports. Knows about styling; core does not. | `LineObserver`, `Prompter`, `Logger` |
| `cli` | Composition root: subcommand dispatch, flag parsing, wiring, signal handling. Only package that imports `tui`/`session`. | `Run`, `CliFlags` |

## Design decisions worth preserving

- **One loop, data-defined roles.** There is exactly one agent loop (`role`).
  Roles differ only by their TOML intro + tool list. Adding a role = adding a
  file, not Go code.
- **The response contract is system-owned.** `chat.OutputGuide()` is rendered
  *after* the chat history on every request, so the model is reminded of the
  exact JSON shape right before it generates. A user prompt can never break
  parsing.
- **The prompt head is byte-stable.** Shared preamble + terminal info + role
  intro + tool specs are composed once per run (`RolePrompt.head`) to keep
  provider prefix caching effective; only the tail (output guide) is re-rendered.
- **Ports stay semantic.** Styling never crosses `core.Observer` — events carry
  meaning, and an adapter decides how they look.
- **Capability boundaries are enforced.** A role's `tools` list is checked
  against the handler registry at startup (`checkToolCoverage`), so a role can
  never declare a tool that cannot run.

## Dependency rules

- Nothing except `cli` may import `tui` or `session`.
- `core` must stay dependency-light — it is the shared vocabulary.
- No cycles; the graph above (`X -> Y` means X imports Y) is:

  ```
  cli     -> config core paths prompts provider role session tui
  role    -> chat config core prompts provider tool
  chat    -> config core prompts provider
  session -> core paths
  tui     -> core
  tool    -> paths
  config  -> paths provider
  prompts -> paths
  core, paths, provider -> (nothing internal)
  ```
