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

## Boundaries

Only `cli` imports this adapter and chooses the terminal mode.
Keep ANSI and styling decisions here rather than in `core`, `chat`, or `role`.
Terminal input/output does not own provider transport or session persistence;
those are supplied separately by the composition root.

Suggested validation from the repository root:

```sh
go test ./internal/tui
go test -tags filestore ./internal/tui
```
