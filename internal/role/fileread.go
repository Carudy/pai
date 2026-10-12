package role

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type textWindow struct {
	Body         string
	Included     int
	Scanned      int // Lines encountered, including a line stopped partway through.
	EOF          bool
	More         bool
	OversizeLine int
}

// readTextWindow bounds both the numbered output and retained line data. More
// means reading stopped at a limit, not that EOF was probed; continuation starts
// at offset + Included, even if an excluded line has already been scanned.
func readTextWindow(ctx context.Context, r io.Reader, offset, limit, maxBytes int) (window textWindow, err error) {
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	if maxBytes <= 0 {
		maxBytes = 8000
	}

	br := bufio.NewReaderSize(r, 4096)
	var body strings.Builder
	defer func() { window.Body = body.String() }()
	stopForBudget := func(lineNo int) {
		window.More = true
		if window.Included == 0 {
			window.OversizeLine = lineNo
		}
	}

	for {
		lineNo := window.Scanned + 1
		selected := lineNo >= offset
		prefix := ""
		contentBudget := 0
		if selected {
			prefix = fmt.Sprintf("%6d\t", lineNo)
			contentBudget = maxBytes - body.Len() - len(prefix) - 1
		}
		// Only a first-line BOM and a CR before LF can disappear on display.
		allowance := 1
		if lineNo == 1 {
			allowance += 3
		}
		var line []byte
		started := false
		hasLF := false
		for {
			if err := ctx.Err(); err != nil {
				return window, err
			}
			chunk, readErr := br.ReadSlice('\n')
			if err := ctx.Err(); err != nil {
				return window, err
			}
			if len(chunk) > 0 && !started {
				window.Scanned++
				started = true
			}
			if bytes.IndexByte(chunk, 0) >= 0 {
				return window, fmt.Errorf("line %d contains NUL; read handles text only", lineNo)
			}
			if readErr != nil && readErr != io.EOF && readErr != bufio.ErrBufferFull {
				return window, fmt.Errorf("read text: %w", readErr)
			}
			if readErr == io.EOF {
				window.EOF = true
			}
			if !started {
				return window, nil
			}
			if selected {
				hasLF = bytes.HasSuffix(chunk, []byte{'\n'})
				chunk = bytes.TrimSuffix(chunk, []byte{'\n'})
				if contentBudget < 0 || len(chunk) > contentBudget-len(line)+allowance {
					// A completed excluded line still needs validation. This
					// temporary buffer adds at most one fixed-size chunk.
					if readErr != bufio.ErrBufferFull && !utf8.Valid(append(line, chunk...)) {
						return window, fmt.Errorf("line %d contains invalid UTF-8; read handles text only", lineNo)
					}
					stopForBudget(lineNo)
					return window, nil
				}
				line = append(line, chunk...)
			}
			if readErr == bufio.ErrBufferFull {
				continue
			}
			break
		}

		if selected {
			if !utf8.Valid(line) {
				return window, fmt.Errorf("line %d contains invalid UTF-8; read handles text only", lineNo)
			}
			if lineNo == 1 {
				line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
			}
			if hasLF {
				line = bytes.TrimSuffix(line, []byte{'\r'})
			}
			if len(line) > contentBudget {
				stopForBudget(lineNo)
				return window, nil
			}
			body.WriteString(prefix)
			body.Write(line)
			body.WriteByte('\n')
			window.Included++
		}
		if window.EOF {
			return window, nil
		}
		if window.Included == limit || body.Len() == maxBytes {
			window.More = true
			return window, nil
		}
	}
}
