package role

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadTextWindow(t *testing.T) {
	tests := []struct {
		name                  string
		input                 string
		offset, limit, budget int
		want                  textWindow
	}{
		{name: "empty", want: textWindow{EOF: true}},
		{name: "offset beyond EOF", input: "a\nb\n", offset: 4, want: textWindow{Scanned: 2, EOF: true}},
		{name: "offset beyond unterminated EOF", input: "a\nb", offset: 4, want: textWindow{Scanned: 2, EOF: true}},
		{name: "defaults", input: "a\nb", offset: -2, limit: -1, budget: -1,
			want: textWindow{Body: "     1\ta\n     2\tb\n", Included: 2, Scanned: 2, EOF: true}},
		{name: "trailing newline", input: "a\n", want: textWindow{Body: "     1\ta\n", Included: 1, Scanned: 1, EOF: true}},
		{name: "empty lines", input: "\n\n", want: textWindow{Body: "     1\t\n     2\t\n", Included: 2, Scanned: 2, EOF: true}},
		{name: "window", input: "a\nb\nc\nd\n", offset: 2, limit: 2,
			want: textWindow{Body: "     2\tb\n     3\tc\n", Included: 2, Scanned: 3, More: true}},
		{name: "line limit does not probe EOF", input: "a\n", limit: 1,
			want: textWindow{Body: "     1\ta\n", Included: 1, Scanned: 1, More: true}},
		{name: "line limit known EOF", input: "a", limit: 1,
			want: textWindow{Body: "     1\ta\n", Included: 1, Scanned: 1, EOF: true}},
		{name: "exact byte budget", input: "a\nb\n", budget: 9,
			want: textWindow{Body: "     1\ta\n", Included: 1, Scanned: 1, More: true}},
		{name: "excluded line", input: "a\nbbbbb\nc\n", budget: 20,
			want: textWindow{Body: "     1\ta\n", Included: 1, Scanned: 2, More: true}},
		{name: "first line oversized", input: "abcd\n", budget: 10,
			want: textWindow{Scanned: 1, More: true, OversizeLine: 1}},
		{name: "numbering exceeds budget", input: "\n", budget: 7,
			want: textWindow{Scanned: 1, More: true, OversizeLine: 1}},
		{name: "unicode exact budget", input: "猫\nx\n", budget: 11,
			want: textWindow{Body: "     1\t猫\n", Included: 1, Scanned: 1, More: true}},
		{name: "unicode not split", input: "猫\n", budget: 10,
			want: textWindow{Scanned: 1, More: true, OversizeLine: 1}},
		{name: "unterminated CR preserved", input: "value\r",
			want: textWindow{Body: "     1\tvalue\r\n", Included: 1, Scanned: 1, EOF: true}},
		{name: "CRLF trimmed", input: "value\r\n",
			want: textWindow{Body: "     1\tvalue\n", Included: 1, Scanned: 1, EOF: true}},
		{name: "unterminated CR counts toward budget", input: "value\r", budget: 13,
			want: textWindow{Scanned: 1, EOF: true, More: true, OversizeLine: 1}},
		{name: "CRLF fits display budget", input: "value\r\n", budget: 13,
			want: textWindow{Body: "     1\tvalue\n", Included: 1, Scanned: 1, More: true}},
		{name: "CRLF and BOM", input: "\ufeffa\r\n\ufeffb\r\n",
			want: textWindow{Body: "     1\ta\n     2\t\ufeffb\n", Included: 2, Scanned: 2, EOF: true}},
		{name: "BOM and CR excluded from budget", input: "\ufeffa\r\n", budget: 9,
			want: textWindow{Body: "     1\ta\n", Included: 1, Scanned: 1, More: true}},
		{name: "BOM not stripped at offset", input: "a\n\ufeffb\n", offset: 2,
			want: textWindow{Body: "     2\t\ufeffb\n", Included: 1, Scanned: 2, EOF: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readTextWindow(context.Background(), strings.NewReader(tt.input), tt.offset, tt.limit, tt.budget)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("window = %+v, want %+v", got, tt.want)
			}
			if !utf8.ValidString(got.Body) {
				t.Fatal("output is not valid UTF-8")
			}
		})
	}
}

func TestReadTextWindowContinuation(t *testing.T) {
	const input = "a\nbbbbb\nc\nd\n"
	for _, tt := range []struct {
		name          string
		limit, budget int
	}{
		{name: "line limit", limit: 2, budget: 8000},
		{name: "byte limit", limit: 2000, budget: 20},
	} {
		t.Run(tt.name, func(t *testing.T) {
			offset := 1
			var body strings.Builder
			for i := 0; i < 5; i++ {
				w, err := readTextWindow(context.Background(), strings.NewReader(input), offset, tt.limit, tt.budget)
				if err != nil {
					t.Fatal(err)
				}
				body.WriteString(w.Body)
				if !w.More {
					break
				}
				if w.Included == 0 {
					t.Fatalf("continuation made no progress: %+v", w)
				}
				offset += w.Included
			}
			if want := "     1\ta\n     2\tbbbbb\n     3\tc\n     4\td\n"; body.String() != want {
				t.Fatalf("continued output = %q, want %q", body.String(), want)
			}
		})
	}
}

func TestReadTextWindowDefaultLimits(t *testing.T) {
	w, err := readTextWindow(context.Background(), strings.NewReader(strings.Repeat("\n", 2001)), 0, 0, 100000)
	if err != nil || w.Included != 2000 || !w.More || w.EOF {
		t.Fatalf("default line limit: window = %+v, err = %v", w, err)
	}
	w, err = readTextWindow(context.Background(), strings.NewReader(strings.Repeat("a\n", 2000)), 0, 0, 0)
	if err != nil || len(w.Body) > 8000 || w.Included != 888 || !w.More {
		t.Fatalf("default byte limit: included = %d, bytes = %d, more = %v, err = %v", w.Included, len(w.Body), w.More, err)
	}
}

func TestReadTextWindowLongLines(t *testing.T) {
	long := strings.Repeat("x", 2*1024*1024)
	t.Run("oversized first selected line stops early", func(t *testing.T) {
		r := &textWindowCountingReader{r: strings.NewReader(long + "\nsmall\n")}
		w, err := readTextWindow(context.Background(), r, 1, 10, 8000)
		if err != nil || w.Body != "" || w.Included != 0 || w.OversizeLine != 1 || !w.More || w.EOF {
			t.Fatalf("window = %+v, err = %v", w, err)
		}
		if r.bytes > 8192 {
			t.Fatalf("oversized line consumed %d bytes", r.bytes)
		}
	})
	t.Run("oversized later line preserves complete output", func(t *testing.T) {
		w, err := readTextWindow(context.Background(), strings.NewReader("ok\n"+long+"\n"), 1, 10, 8000)
		if err != nil || w.Body != "     1\tok\n" || w.Included != 1 || w.Scanned != 2 || !w.More || w.OversizeLine != 0 || w.EOF {
			t.Fatalf("window = %+v, err = %v", w, err)
		}
	})
	t.Run("large skipped line", func(t *testing.T) {
		w, err := readTextWindow(context.Background(), strings.NewReader(long+"\nsmall\n"), 2, 10, 20)
		if err != nil || w.Body != "     2\tsmall\n" || w.Included != 1 || w.Scanned != 2 || !w.EOF || w.More {
			t.Fatalf("window = %+v, err = %v", w, err)
		}
	})
	t.Run("unicode crosses chunk boundary", func(t *testing.T) {
		line := strings.Repeat("x", 4095) + "猫" + strings.Repeat("é", 2500)
		w, err := readTextWindow(context.Background(), strings.NewReader(line+"\n"), 1, 10, 20000)
		if err != nil || w.Body != fmt.Sprintf("%6d\t%s\n", 1, line) || !w.EOF {
			t.Fatalf("included = %d, EOF = %v, err = %v", w.Included, w.EOF, err)
		}
	})
}

func TestReadTextWindowRejectsBinaryAndInvalidUTF8(t *testing.T) {
	for _, tt := range []struct {
		name, input, errorText string
		offset                 int
	}{
		{name: "selected NUL after sniff boundary", input: strings.Repeat("x", 9000) + "\x00\n", errorText: "NUL", offset: 1},
		{name: "skipped NUL after sniff boundary", input: strings.Repeat("x", 9000) + "\x00\nok\n", errorText: "NUL", offset: 2},
		{name: "invalid selected UTF8", input: "ok\n\xff\n", errorText: "invalid UTF-8", offset: 2},
		{name: "incomplete UTF8 at EOF", input: "\xe7\x8c", errorText: "invalid UTF-8", offset: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readTextWindow(context.Background(), strings.NewReader(tt.input), tt.offset, 10, 20000)
			if err == nil || !strings.Contains(err.Error(), tt.errorText) {
				t.Fatalf("want %q error, got %v", tt.errorText, err)
			}
		})
	}
	_, err := readTextWindow(context.Background(), strings.NewReader("too long\xff\n"), 1, 10, 9)
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("invalid UTF-8 in completed overbudget line: %v", err)
	}
	w, err := readTextWindow(context.Background(), strings.NewReader("\xff\nok\n"), 2, 10, 100)
	if err != nil || w.Body != "     2\tok\n" {
		t.Fatalf("invalid UTF-8 in skipped line: window = %+v, err = %v", w, err)
	}
}

func TestReadTextWindowCancellation(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &textWindowCountingReader{r: strings.NewReader("a\n")}
		_, err := readTextWindow(ctx, r, 1, 10, 100)
		if !errors.Is(err, context.Canceled) || r.bytes != 0 {
			t.Fatalf("err = %v, bytes read = %d", err, r.bytes)
		}
	})
	for _, offset := range []int{1, 2} {
		t.Run(fmt.Sprintf("between chunks offset %d", offset), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &textWindowCountingReader{r: strings.NewReader(strings.Repeat("x", 20000) + "\n"), cancel: cancel}
			_, err := readTextWindow(ctx, r, offset, 10, 30000)
			if !errors.Is(err, context.Canceled) || r.bytes > 4096 {
				t.Fatalf("err = %v, bytes read = %d", err, r.bytes)
			}
		})
	}
}

func TestReadTextWindowReadError(t *testing.T) {
	want := errors.New("broken reader")
	_, err := readTextWindow(context.Background(), textWindowErrorReader{err: want}, 1, 10, 100)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrapped %v", err, want)
	}
}

type textWindowCountingReader struct {
	r      io.Reader
	bytes  int
	cancel context.CancelFunc
}

func (r *textWindowCountingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.bytes += n
	if r.cancel != nil {
		r.cancel()
	}
	return n, err
}

type textWindowErrorReader struct{ err error }

func (r textWindowErrorReader) Read([]byte) (int, error) { return 0, r.err }
