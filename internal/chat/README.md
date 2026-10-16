# chat

Conversation protocol between the agent loop and an LLM provider.
See the [module map](../organization.md) for the surrounding layers.

## Entry points

- `LoadRolePrompt` / `LoadRolePromptAt` compose role and tool definitions
  (resolved and parsed by `prompts`), project instructions, and the host block
  (from `hostinfo`); the latter scopes them to a workspace.
- `LoadRolePromptAt` takes the user's extra intro text as a plain string, so the
  override does not widen this package's dependencies. (The settings aggregate
  `config.UserConfig` is still taken directly for streaming, reasoning, and the
  context budget — see the note below.)
- `RolePrompt` keeps a stable prompt head; `OutputGuide` supplies the JSON
  response contract after history on each request.
- `ChatStr` uses `Ports` for provider calls, observation, and logging,
  respecting configured streaming and reporting token usage.
- `ParseResponse` / `ParseResponseWithRetry` validate model actions.
- `Truncate`, `CompactHistory`, and `summarize.go` bound model context;
  shortening replay does not replace durable session history. Both compaction
  layers window by **user turn** (a user instruction plus every tool call it
  takes to answer it), never mid-turn by message count, so the model always keeps
  the whole of the task it is in the middle of.

## Boundaries

This package handles messages and protocol, not tool execution or user approval.
It depends on semantic `core` ports, never terminal or storage adapters. It also
parses no definition files and probes no host: `prompts` owns the TOML format and
`hostinfo` owns environment probing, so nothing here shells out.

Known coupling: the protocol functions take `config.UserConfig` (and
`ContextConfig`) wholesale, when they use only streaming, reasoning, and the
context budget. Narrowing that to a small options struct would drop the `config`
edge, but it touches every caller, so it is a deliberate open item rather than an
oversight.

Suggested validation from the repository root (not a live provider call):

```sh
go test ./internal/chat
```

## Keeping the context bounded

Command output is shortened before it goes back to the model: the `[context]`
section's `exec_limit` / `search_limit` cap the bytes, while `head_lines` /
`tail_lines` decide how much of the head and tail survives. The tail is kept on
purpose — errors, exit codes and summaries land at the end of command output.

Long conversations are compacted in two layers. The first is deterministic:
the last `keep_turns` **turns** are replayed verbatim — a turn is one user
instruction plus everything the model does to answer it (each assistant reply and
tool observation), so a long turn is never cut mid-task. Once the conversation
exceeds `elide_after_turns` turns, older command output larger than
`elide_min_bytes` is reduced to its header (label, command, exit status) with a
visible marker, so the model knows it was omitted and can re-run the command if
it needs the result.

The second layer is optional and costs one extra model call: with
`summarize_after_tokens` set, once the prompt exceeds that token budget the
oldest turns are summarized into a single message and only the last `keep_turns`
turns are kept verbatim. Summarization uses a plain call — no streaming, no
reasoning, no JSON mode. Trigger it on demand in-session with `/compact`.

In both layers user input, assistant replies and answers are never dropped, and
the full transcript stays in the session store — compression only changes what
is *replayed* to the model.

A summary is also written to the session as a **checkpoint** (a `system` turn of
kind `summary`). Attaching later replays that summary and only the turns after
it, so a long session resumes without re-summarizing — while every original turn
is still on disk (visible in `pai session show`).
