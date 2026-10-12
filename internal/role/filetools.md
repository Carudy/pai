# File-tool contracts and safety

The `read`, `edit`, and `write` handlers stay in `role`: approval is part of the
operation, not an execution-layer concern. Their helpers are deliberately small:
`fileread.go` streams bounded text, `fileedit.go` plans replacements and previews,
and `filesafety.go` checks snapshots and publishes staged files.

## Upstream investigation

The following current implementations informed this design; these are architecture
comparisons, not claims that every harness provides the same guarantees:

- [Pi edit](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/edit.ts)
  supports disjoint edits matched against the original file, preserves BOM/line
  endings, and serializes file mutations. Its
  [read tool](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/read.ts)
  combines line/byte budgets with actionable continuation hints.
- [Zed edit_file](https://github.com/zed-industries/zed/blob/main/crates/agent/src/tools/edit_file_tool.rs)
  accepts batches, explains how to strip numbered-read prefixes, and tests
  changed-buffer handling. Its edits are sequential and can stream into editor
  buffers; PAI deliberately plans a whole batch against an original snapshot
  before requesting approval and makes no changes until committing.
- [Codex patch matching](https://github.com/openai/codex/blob/main/codex-rs/apply-patch/src/seek_sequence.rs)
  progressively relaxes whitespace and Unicode-punctuation matching. PAI does
  **not** adopt those relaxations: indentation can be syntax, and punctuation,
  Unicode minus signs, and compatibility characters can carry scientific meaning.
  Only CRLF/LF representation differences are tolerated.

No upstream code or new dependency is imported. PAI retains its existing
create-only `write` policy rather than adopting whole-file overwrite tools.

## Read

Payload: `{"path":"file","offset":1,"limit":2000}`. Offset is one-based; omitted
or zero values use defaults. Negative values are refused. Limit is capped at 2000.
The existing `[context] exec_limit` bounds the numbered body (default 8000 bytes).

Only complete lines are returned. Headers and continuation hints are outside the
body budget. A byte-limited read resumes at the first undisplayed line, not at the
originally requested window's end. If the first line cannot fit, no partial text
is returned: use a bounded, targeted shell extraction or increase the budget.
Total line count is reported only when EOF was reached without an omitted line;
stopping at a limit does not require scanning the rest of the file.

The streaming reader retains at most the budget plus a fixed-size chunk, including
when skipping oversized lines before an offset. It checks cancellation between
chunks, rejects NUL in scanned chunks and invalid UTF-8 in selected complete lines,
hides an initial UTF-8 BOM, and displays CRLF as LF without discarding a bare CR.
Remove only the six-character line-number prefix and tab when copying for edits.

Reads follow symlinks, but resolve the target before deciding path trust, and
recheck the resolved name, regular-file type, and identity after approval. Empty
files are explicit; out-of-range offsets fail. Directories and special files are
not read. `confirm_read` remains off by default.

## Edit

The legacy payload remains supported:

```json
{"path":"file","old_string":"before","new_string":"after","replace_all":false}
```

Several disjoint changes to one file use a batch:

```json
{
  "path":"file",
  "edits":[
    {"old_string":"first original block","new_string":"first replacement"},
    {"old_string":"second original block","new_string":"second replacement"}
  ]
}
```

Never mix these shapes. Each array entry matches the **original file**, independent
of array order. A batch allows 1–50 edits and at most 10000 replacement sites.
Missing, ambiguous, self-overlapping, or mutually overlapping ranges fail before
approval or publication. `replace_all` is explicit and requires non-overlapping
matches. Ambiguity errors provide bounded line locations for adding context.

`new_string` is required: an explicit empty string deletes; a missing/misspelled
field does not. Unknown fields are refused by all three tools. Files, payload text,
and planned results must be UTF-8 without NUL; editable files and results are
limited to 16 MiB. Normalized matching maps CRLF/LF queries back to original byte
ranges, preserving untouched bytes and BOM. Replacement newline style follows a
uniform file or the matched range; uncertain mixed endings are not globally
normalized. Success reports the first changed line and line-ending adjustments.

The preview shows actual changed full lines, including a substring match's
prefix/suffix, with nearby context. Previews are bounded to 16 KiB, 80 lines,
and six hunks; omissions are explicit. A truncated preview still approves the
whole planned operation, not only the visible portion.

## Guarded publication

Before preview, mutations capture a canonical parent identity. Edits also capture
file identity, contents, and mode. Final-path symlinks, dangling links, directories,
and special files are refused; edit a link's real target explicitly instead.
Parent symlinks are allowed only while their resolved target and identity remain
unchanged. Parent directories must already exist; tools never create them.

After approval, commits are serialized within the PAI process, and the snapshot is
rechecked before staging and just before publication. A stale edit is rejected
rather than silently replanned, because the user approved a particular diff.
Cancellation before publication leaves no destination change and cleans up staging.
Directory trust is granted only after a successful commit and another parent check;
a failed operation does not persist approval for a substituted directory.

Both operations stage, set permissions, sync, and close a temporary file in the
same directory. Edit renames it over the verified target and preserves permission
bits. Write publishes it with an atomic **no-clobber hard link**; an existing file
or dangling symlink cannot be overwritten even if it appears after the last check.
Filesystems without hard-link support fail safely rather than falling back to an
overwriting rename. New files retain the existing 0644 policy.

These are cooperative concurrency safeguards, **not a filesystem sandbox**:

- Separate processes do not share the commit gate. Portable filesystems offer no
  compare-and-swap rename; an external writer can still race the final edit check.
- Path checks do not eliminate hostile directory/file replacement races, including
  the small type-check/open window. Regular-file disk I/O is not universally
  interruptible by context cancellation.
- Atomic replacement does not preserve inode identity, hard-link relationships,
  ownership, ACLs, or extended attributes. Only permission bits are preserved.
- Staged content is synced, but parent-directory durability across power loss is
  not guaranteed. Shell tools remain governed separately and do not use this gate.

## Validation

Use fixture files and deterministic approval callbacks to exercise stale edits,
raced creates, parent substitution, trust decisions, cancellation, batch rollback,
CRLF/BOM behavior, Unicode budgets, and oversize lines. Targeted race tests cover
concurrent guarded commits; Unix tests cover FIFO replacement before opening.
Cross-compilation checks platform compatibility, not filesystem behavior on that
platform. No live model credentials are required for these tests.
