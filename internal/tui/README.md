# tui

Terminal adapters for the semantic ports exposed by `core`.
See the [module map](../organization.md) for presentation boundaries.

## Entry points

- `App` / `NewApp` provide the interactive terminal application.
- `LineObserver` / `NewLineObserver` render loop events in line-oriented mode.
- `Prompter` / `NewPrompter` collect questions and confirmations from input.
- `Logger` / `NewLogger` route diagnostics to a writer with debug control.
- `SessionLabel` supplies the displayed session label.
- `style.go` and `highlight.go` own styling and highlighting; observer and
  prompt files translate semantic events into terminal presentation.

## Live region invariants

The interactive UI is an inline (non-alt-screen) bubbletea view: committed output
is printed above it into ordinary scrollback, and the live region is repainted in
place. Its rendered **line count must equal its screen rows**. bubbletea moves the
cursor up by the number of newlines it last wrote, so a line the terminal wraps
into extra rows makes the next frame start too low; fragments of the previous
frame (typically the tail of the status hint) then stay behind in scrollback.
`View` therefore wraps every line to the terminal width (`fitLines`, ANSI-aware)
before returning, and the streaming tail is truncated to a single row so it keeps
its newest end. Any new view state — a prompt, a banner, a wider status bar — must
respect that boundary rather than assume the terminal will wrap harmlessly.

## Boundaries

Only `cli` imports this adapter and chooses the terminal mode.
Keep ANSI and styling decisions here rather than in `core`, `chat`, or `role`.
Terminal input/output does not own provider transport or session persistence;
those are supplied separately by the composition root.

Suggested validation from the repository root:

```sh
go test ./internal/tui
```
