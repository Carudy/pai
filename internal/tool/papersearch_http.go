package tool

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	paperResponseLimit = 4 << 20
	paperSearchTimeout = 45 * time.Second
	paperMaxRetryDelay = 10 * time.Second
)

// The gate protects start times without sleeping under a mutex. For arXiv it
// remains held through response reading, so even slow responses never overlap.
type paperLimiter struct {
	gate     chan struct{}
	interval time.Duration
	serial   bool
	next     time.Time
}

func newPaperLimiter(interval time.Duration, serial bool) *paperLimiter {
	return &paperLimiter{gate: make(chan struct{}, 1), interval: interval, serial: serial}
}

func (l *paperLimiter) acquire(ctx context.Context) (func(), error) {
	select {
	case l.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-l.gate }
	if err := paperWait(ctx, time.Until(l.next)); err != nil {
		release()
		return nil, err
	}
	l.next = time.Now().Add(l.interval)
	if !l.serial {
		release()
		return func() {}, nil
	}
	return release, nil
}

func paperWait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Tests construct isolated clients and gates; they never replace shared globals.
type paperClient struct {
	httpClient       *http.Client
	arxivEndpoint    string
	crossrefEndpoint string
	arxivLimiter     *paperLimiter
	crossrefLimiter  *paperLimiter
}

var defaultPaperClient = paperClient{
	httpClient: &http.Client{
		Timeout: paperSearchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("papersearch provider redirect refused")
		},
	},
	arxivEndpoint:    "https://export.arxiv.org/api/query",
	crossrefEndpoint: "https://api.crossref.org/works",
	arxivLimiter:     newPaperLimiter(3*time.Second, true),
	crossrefLimiter:  newPaperLimiter(time.Second, false),
}

func (c *paperClient) search(ctx context.Context, req PaperSearchRequest, contactEmail string) (*PaperSearchResult, error) {
	if err := req.Normalize("arxiv"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, paperSearchTimeout)
	defer cancel()
	if req.Source == "arxiv" {
		return c.searchArxiv(ctx, req)
	}
	return c.searchCrossref(ctx, req, contactEmail)
}

// searchArxiv runs the strict all-of query first and, when it matches nothing,
// falls back once to a relevance-ranked any-of query. Quoting every keyword
// keeps arXiv's own analysis (and any operator in the text) out of the query.
func (c *paperClient) searchArxiv(ctx context.Context, req PaperSearchRequest) (*PaperSearchResult, error) {
	terms := paperArxivTerms(req.Query)
	if len(terms) == 0 {
		return nil, fmt.Errorf("papersearch arxiv query contains no searchable keywords")
	}
	modes := []string{PaperMatchAll, PaperMatchAny}
	if req.Match != "" {
		modes = []string{req.Match}
	}
	var first *PaperSearchResult
	for i, mode := range modes {
		result, err := c.arxivAttempt(ctx, req, terms, mode)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			first = result
		}
		if len(result.Papers) > 0 || result.Candidates > 0 || i == len(modes)-1 {
			if i > 0 {
				result.Fallback = true
				result.Attempted = first.Candidates
			}
			return result, nil
		}
	}
	return first, nil
}

func (c *paperClient) arxivAttempt(ctx context.Context, req PaperSearchRequest, terms []string, match string) (*PaperSearchResult, error) {
	expression := paperArxivExpression(terms, match, req.YearFrom, req.YearTo)
	values := make(url.Values)
	values.Set("search_query", expression)
	values.Set("start", strconv.Itoa(req.Offset))
	values.Set("max_results", strconv.Itoa(req.Limit))
	values.Set("sortBy", "relevance")
	if req.Sort == "newest" {
		values.Set("sortBy", "submittedDate")
	}
	values.Set("sortOrder", "descending")

	body, err := c.fetch(ctx, c.arxivEndpoint+"?"+values.Encode(), "arxiv", c.arxivLimiter)
	if err != nil {
		return nil, err
	}
	papers, candidates, err := decodePaperArxiv(body)
	if err != nil {
		return nil, fmt.Errorf("papersearch arxiv: %w", err)
	}
	if len(papers) > req.Limit {
		papers = papers[:req.Limit]
	}
	return &PaperSearchResult{
		Source: "arxiv", Query: req.Query, QuerySent: expression, Match: match,
		Candidates: candidates, Offset: req.Offset, YearFrom: req.YearFrom, YearTo: req.YearTo,
		Sort: req.Sort, RetrievedAt: time.Now().UTC(), Papers: papers,
	}, nil
}

func (c *paperClient) searchCrossref(ctx context.Context, req PaperSearchRequest, contactEmail string) (*PaperSearchResult, error) {
	// Crossref ranks a loose any-of match; claiming to honor all-of would be a
	// silent lie, so an explicit match=all is refused instead.
	if req.Match == PaperMatchAll {
		return nil, fmt.Errorf("papersearch match=all is arxiv-only; crossref always relevance-ranks any-of keywords")
	}
	values := make(url.Values)
	values.Set("query.bibliographic", req.Query)
	values.Set("rows", strconv.Itoa(req.Limit))
	values.Set("offset", strconv.Itoa(req.Offset))
	values.Set("sort", "score")
	if req.Sort == "newest" {
		values.Set("sort", "published")
	}
	values.Set("order", "desc")
	var filters []string
	if req.YearFrom != 0 {
		filters = append(filters, fmt.Sprintf("from-pub-date:%04d-01-01", req.YearFrom))
	}
	if req.YearTo != 0 {
		filters = append(filters, fmt.Sprintf("until-pub-date:%04d-12-31", req.YearTo))
	}
	if len(filters) != 0 {
		values.Set("filter", strings.Join(filters, ","))
	}
	if email := strings.TrimSpace(contactEmail); email != "" {
		values.Set("mailto", email)
	}

	body, err := c.fetch(ctx, c.crossrefEndpoint+"?"+values.Encode(), "crossref", c.crossrefLimiter)
	if err != nil {
		return nil, err
	}
	papers, candidates, err := decodePaperCrossref(body)
	if err != nil {
		return nil, fmt.Errorf("papersearch crossref: %w", err)
	}
	if len(papers) > req.Limit {
		papers = papers[:req.Limit]
	}
	return &PaperSearchResult{
		Source: "crossref", Query: req.Query, QuerySent: req.Query, Match: PaperMatchAny,
		Candidates: candidates, Offset: req.Offset, YearFrom: req.YearFrom, YearTo: req.YearTo,
		Sort: req.Sort, RetrievedAt: time.Now().UTC(), Papers: papers,
	}, nil
}

// paperArxivTerms turns free text into safe quoted all:-field terms. A quoted
// span becomes a phrase; a bare span is split on punctuation into single
// keywords. Either way the text is quoted, so quotes, colons, parentheses, and
// provider operators in the model's query can never become query syntax.
func paperArxivTerms(query string) []string {
	var terms []string
	var bare, phrase strings.Builder
	flushBare := func() {
		words := strings.FieldsFunc(bare.String(), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r)
		})
		for _, word := range words {
			terms = append(terms, `all:"`+word+`"`)
		}
		bare.Reset()
	}
	// A phrase must carry a letter or digit, otherwise a span like "*" would be
	// sent as a meaningless term rather than dropped.
	appendPhrase := func() {
		if p := paperPhrase(phrase.String()); paperSearchable(p) {
			terms = append(terms, `all:"`+p+`"`)
		}
		phrase.Reset()
	}
	inQuote := false
	for _, r := range query {
		if r == '"' {
			if inQuote {
				appendPhrase()
			} else {
				flushBare()
			}
			inQuote = !inQuote
			continue
		}
		if inQuote {
			phrase.WriteRune(r)
		} else {
			bare.WriteRune(r)
		}
	}
	// An unterminated quote keeps the remaining text as one phrase.
	if inQuote {
		appendPhrase()
	}
	flushBare()
	return terms
}

// paperSearchable reports whether a term has a letter or digit, so a phrase made
// only of punctuation is dropped instead of being sent as a meaningless term.
func paperSearchable(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// paperPhrase keeps a phrase searchable: quotes and backslashes are removed so
// they cannot end the phrase early, printable characters stay, and whitespace is
// collapsed. A phrase that is a single word is just a term.
func paperPhrase(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// paperArxivExpression joins the terms and, when years are bounded, restricts by
// first-submission date. The term group is parenthesized there because AND binds
// tighter than OR, so an any-of group must be grouped before adding AND.
func paperArxivExpression(terms []string, match string, yearFrom, yearTo int) string {
	sep := " AND "
	if match == PaperMatchAny {
		sep = " OR "
	}
	query := strings.Join(terms, sep)
	if yearFrom != 0 || yearTo != 0 {
		from, to := yearFrom, yearTo
		if from == 0 {
			from = 1
		}
		if to == 0 {
			to = 9999
		}
		query = fmt.Sprintf("(%s) AND submittedDate:[%04d01010000 TO %04d12312359]", query, from, to)
	}
	return query
}

func (c *paperClient) fetch(ctx context.Context, endpoint, source string, limiter *paperLimiter) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		body, status, retryAfter, err := c.fetchOnce(ctx, endpoint, limiter)
		if err != nil {
			return nil, fmt.Errorf("papersearch %s request: %w", source, err)
		}
		if status == http.StatusOK {
			return body, nil
		}
		if attempt == 0 && (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) {
			delay, err := paperRetryDelay(retryAfter, time.Now())
			if err != nil {
				return nil, fmt.Errorf("papersearch %s HTTP %d: %w", source, status, err)
			}
			if err := paperWait(ctx, delay); err != nil {
				return nil, fmt.Errorf("papersearch %s retry wait: %w", source, err)
			}
			continue
		}
		if len(body) > 4096 {
			body = body[:4096]
		}
		return nil, fmt.Errorf("papersearch %s HTTP %d: %s", source, status, paperText(string(body), "empty response"))
	}
	return nil, fmt.Errorf("papersearch %s retry exhausted", source)
}

func (c *paperClient) fetchOnce(ctx context.Context, endpoint string, limiter *paperLimiter) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("User-Agent", "pai-papersearch/1.0")
	req.Header.Set("Accept", "application/atom+xml, application/json")
	release, err := limiter.acquire(ctx)
	if err != nil {
		return nil, 0, "", err
	}
	defer release()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, paperResponseLimit+1))
	if err != nil {
		return nil, 0, "", fmt.Errorf("read response: %w", err)
	}
	if len(body) > paperResponseLimit {
		return nil, 0, "", fmt.Errorf("response exceeds %d byte limit", paperResponseLimit)
	}
	return body, resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

func paperRetryDelay(value string, now time.Time) (time.Duration, error) {
	delay := time.Second
	value = strings.TrimSpace(value)
	if value != "" {
		if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
			if seconds > uint64(paperMaxRetryDelay/time.Second) {
				return 0, fmt.Errorf("Retry-After %q exceeds maximum retry wait of %s", value, paperMaxRetryDelay)
			}
			delay = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(value); err == nil {
			delay = date.Sub(now)
		} else {
			return 0, fmt.Errorf("invalid Retry-After %q", value)
		}
	}
	if delay > paperMaxRetryDelay {
		return 0, fmt.Errorf("Retry-After %q exceeds maximum retry wait of %s", value, paperMaxRetryDelay)
	}
	if delay < 0 {
		delay = 0
	}
	return delay, nil
}
