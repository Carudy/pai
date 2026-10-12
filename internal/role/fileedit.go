package role

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type fileEdit struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type editPlan struct {
	Content            string
	Diff               string
	Replacements       int
	FirstLine          int
	LineEndingAdjusted bool
}

const (
	fileEditMaxEdits        = 50
	fileEditMaxReplacements = 10000
	fileEditMaxBytes        = 16 << 20
	fileEditDiffBytes       = 16 << 10
	fileEditDiffLines       = 80
	fileEditDiffHunks       = 6
)

type fileEditSpan struct {
	start, end int
	edit       int
	text       string
}

// planFileEdits resolves every query against the same snapshot. Replacement text
// is never searched, so a batch cannot accidentally cascade into its own output.
func planFileEdits(content string, edits []fileEdit) (editPlan, error) {
	var plan editPlan
	if len(edits) == 0 || len(edits) > fileEditMaxEdits {
		return plan, fmt.Errorf("edit batch requires 1–%d edits", fileEditMaxEdits)
	}
	if err := fileEditTextError(content); err != nil {
		return plan, fmt.Errorf("file content: %w", err)
	}
	lines := fileEditLineStarts(content)
	uniformCRLF := fileEditEOLStyle(content) == 2
	var normalized string
	var removedCR []int
	normalizedReady := false
	var spans []fileEditSpan
	for i, edit := range edits {
		for _, field := range []struct{ name, value string }{{"old_string", edit.OldString}, {"new_string", edit.NewString}} {
			if err := fileEditTextError(field.value); err != nil {
				return editPlan{}, fmt.Errorf("edit %d %s: %w", i+1, field.name, err)
			}
		}
		if edit.OldString == "" {
			return editPlan{}, fmt.Errorf("edit %d requires a non-empty old_string", i+1)
		}
		if edit.OldString == edit.NewString {
			return editPlan{}, fmt.Errorf("edit %d old_string and new_string are identical", i+1)
		}

		haystack, query := content, edit.OldString
		fallback := strings.Index(content, query) < 0
		if fallback {
			if !normalizedReady {
				normalized, removedCR = fileEditNormalize(content)
				normalizedReady = true
			}
			haystack = normalized
			query = strings.ReplaceAll(query, "\r\n", "\n")
		}
		count, lastEnd := 0, -1
		var locations []string
		var matches []fileEditSpan
		overlaps := false
		for offset := 0; offset <= len(haystack)-len(query); {
			found := strings.Index(haystack[offset:], query)
			if found < 0 {
				break
			}
			start := offset + found
			end := start + len(query)
			offset = start + 1 // Count self-overlap, unlike strings.Count/ReplaceAll.
			if fallback {
				start += sort.SearchInts(removedCR, start)
				end += sort.SearchInts(removedCR, end)
			} else if start > 0 && content[start] == '\n' && content[start-1] == '\r' {
				// An LF-only query can match the second byte of CRLF exactly.
				// Keep that line ending atomic, just as normalized matching does,
				// rather than leaving a stray CR or doubling it on replacement.
				start--
			}
			count++
			if len(locations) < 8 {
				locations = append(locations, fmt.Sprint(fileEditLineAt(lines, start)+1))
			}
			if start < lastEnd {
				overlaps = true
			}
			lastEnd = end
			if count == 1 || edit.ReplaceAll {
				// Bound allocations during discovery, not after accumulating all
				// matches. Previous edits consume the same batch-wide budget.
				if len(spans)+len(matches) >= fileEditMaxReplacements {
					return editPlan{}, fmt.Errorf("edit %d at line %d exceeds the batch limit of %d planned replacements; split the edits into smaller batches or refine old_string with more surrounding context", i+1, fileEditLineAt(lines, start)+1, fileEditMaxReplacements)
				}
				matches = append(matches, fileEditSpan{start: start, end: end, edit: i + 1})
			}
		}
		if count == 0 {
			return editPlan{}, fmt.Errorf("edit %d old_string not found; read the file and copy the exact text", i+1)
		}
		where := "lines " + strings.Join(locations, ", ")
		if count > len(locations) {
			where += ", …"
		}
		if count > 1 && !edit.ReplaceAll {
			return editPlan{}, fmt.Errorf("edit %d old_string appears %d times (%s); include more surrounding context to make it unique, or set replace_all for non-overlapping occurrences", i+1, count, where)
		}
		if overlaps {
			return editPlan{}, fmt.Errorf("edit %d old_string appears %d times with overlapping occurrences (%s); include more surrounding context; replace_all requires non-overlapping matches", i+1, count, where)
		}
		for _, match := range matches {
			text := edit.NewString
			style := fileEditEOLStyle(content[match.start:match.end])
			if uniformCRLF {
				style = 2
			}
			// A mixed range (or a range without newlines in a mixed file) gives
			// no reliable convention; preserve the supplied replacement there.
			switch style {
			case 1:
				text = strings.ReplaceAll(text, "\r\n", "\n")
			case 2:
				text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
			}
			if content[match.start:match.end] == text {
				return editPlan{}, fmt.Errorf("edit %d is identical after line-ending adjustment at line %d", i+1, fileEditLineAt(lines, match.start)+1)
			}
			plan.LineEndingAdjusted = plan.LineEndingAdjusted || text != edit.NewString || content[match.start:match.end] != edit.OldString
			match.text = text
			spans = append(spans, match)
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end < spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return editPlan{}, fmt.Errorf("edit %d at line %d overlaps edit %d at line %d; use disjoint original-file ranges", spans[i].edit, fileEditLineAt(lines, spans[i].start)+1, spans[i-1].edit, fileEditLineAt(lines, spans[i-1].start)+1)
		}
	}
	// Check the final size, not intermediate batch sizes: a later deletion may
	// offset an earlier insertion, and neither is applied until planning ends.
	size := int64(len(content))
	for _, span := range spans {
		size += int64(len(span.text)) - int64(span.end-span.start)
	}
	if size > fileEditMaxBytes {
		return editPlan{}, fmt.Errorf("planned content exceeds 16 MiB (%d bytes)", size)
	}
	var out strings.Builder
	out.Grow(int(size))
	cursor := 0
	for _, span := range spans {
		out.WriteString(content[cursor:span.start])
		out.WriteString(span.text)
		cursor = span.end
	}
	out.WriteString(content[cursor:])
	plan.Content = out.String()
	plan.Replacements = len(spans)
	plan.FirstLine = fileEditLineAt(lines, spans[0].start) + 1
	plan.Diff = fileEditPreview(content, lines, spans)
	return plan, nil
}

func fileEditTextError(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("invalid UTF-8; edit handles text only")
	}
	if strings.IndexByte(text, 0) >= 0 {
		return fmt.Errorf("contains NUL; edit handles text only")
	}
	return nil
}

// Removed CR positions are recorded in normalized coordinates. A boundary at
// the LF maps to the preceding CR; a boundary after it includes both bytes.
func fileEditNormalize(content string) (string, []int) {
	var out strings.Builder
	out.Grow(len(content))
	var removed []int
	for i := 0; i < len(content); i++ {
		if content[i] == '\r' && i+1 < len(content) && content[i+1] == '\n' {
			removed = append(removed, out.Len())
			continue
		}
		out.WriteByte(content[i])
	}
	return out.String(), removed
}

// 0 means no convention or mixed endings, 1 uniform LF, 2 uniform CRLF.
// Bare CR is data, not a newline, and is never normalized.
func fileEditEOLStyle(text string) int {
	style := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '\n' {
			continue
		}
		current := 1
		if i > 0 && text[i-1] == '\r' {
			current = 2
		}
		if style != 0 && style != current {
			return 0
		}
		style = current
	}
	return style
}

func fileEditLineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' && i+1 < len(text) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func fileEditLineAt(starts []int, offset int) int {
	return sort.Search(len(starts), func(i int) bool { return starts[i] > offset }) - 1
}

func fileEditLineEnd(content string, starts []int, line int) int {
	if line+1 < len(starts) {
		return starts[line+1]
	}
	return len(content)
}

type fileEditPreviewWriter struct {
	body      strings.Builder
	lines     int
	truncated bool
}

const fileEditPreviewNotice = "… preview truncated; additional changes or lines omitted\n"

func (w *fileEditPreviewWriter) line(prefix, text string) bool {
	if w.truncated {
		return false
	}
	if w.lines >= fileEditDiffLines-1 || w.body.Len()+len(prefix)+len(text)+1 > fileEditDiffBytes-len(fileEditPreviewNotice) {
		w.truncated = true
		return false
	}
	w.body.WriteString(prefix)
	w.body.WriteString(text)
	w.body.WriteByte('\n')
	w.lines++
	return true
}

func (w *fileEditPreviewWriter) block(prefix, text string) {
	for text != "" && !w.truncated {
		end := strings.IndexByte(text, '\n')
		if end < 0 {
			if w.line(prefix, text) {
				w.line("", "\\ No newline at end of file")
			}
			return
		}
		w.line(prefix, strings.TrimSuffix(text[:end], "\r"))
		text = text[end+1:]
	}
}

func fileEditPreviewLineCount(text string) int {
	if text == "" {
		return 0
	}
	count := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		count++
	}
	return count
}

// Group nearby affected full-line ranges rather than diffing entire files. This
// is linear in the input and replacement sizes, without an LCS/quadratic diff.
func fileEditPreview(content string, lines []int, spans []fileEditSpan) string {
	var w fileEditPreviewWriter
	lineDelta, hunks := 0, 0
	for first := 0; first < len(spans) && !w.truncated; {
		if hunks == fileEditDiffHunks {
			w.truncated = true
			break
		}
		startLine := fileEditLineAt(lines, spans[first].start)
		endLine := fileEditLineAt(lines, spans[first].end)
		last := first + 1
		for last < len(spans) {
			nextLine := fileEditLineAt(lines, spans[last].start)
			if nextLine > endLine+5 {
				break
			}
			endLine = fileEditLineAt(lines, spans[last].end)
			last++
		}
		from, to := max(0, startLine-2), min(len(lines)-1, endLine+2)
		begin, end := lines[startLine], fileEditLineEnd(content, lines, endLine)
		var changed strings.Builder
		cursor, delta := begin, 0
		for _, span := range spans[first:last] {
			changed.WriteString(content[cursor:span.start])
			changed.WriteString(span.text)
			cursor = span.end
			delta += strings.Count(span.text, "\n") - strings.Count(content[span.start:span.end], "\n")
		}
		changed.WriteString(content[cursor:end])
		newCount := startLine - from + to - endLine + fileEditPreviewLineCount(changed.String())
		newStart := from + 1 + lineDelta
		if newCount == 0 {
			newStart--
		}
		w.line("", fmt.Sprintf("@@ -%d,%d +%d,%d @@", from+1, to-from+1, newStart, newCount))
		w.block("  ", content[lines[from]:begin])
		w.block("- ", content[begin:end])
		w.block("+ ", changed.String())
		w.block("  ", content[end:fileEditLineEnd(content, lines, to)])
		lineDelta += delta
		hunks++
		first = last
	}
	if w.truncated {
		w.body.WriteString(fileEditPreviewNotice)
	}
	return strings.TrimSuffix(w.body.String(), "\n")
}
