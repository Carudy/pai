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
	endpoint, limiter := c.arxivEndpoint, c.arxivLimiter
	values := make(url.Values)
	if req.Source == "arxiv" {
		query, err := paperArxivQuery(req)
		if err != nil {
			return nil, err
		}
		values.Set("search_query", query)
		values.Set("start", "0")
		values.Set("max_results", strconv.Itoa(req.Limit))
		values.Set("sortBy", "relevance")
		if req.Sort == "newest" {
			values.Set("sortBy", "submittedDate")
		}
		values.Set("sortOrder", "descending")
	} else {
		endpoint, limiter = c.crossrefEndpoint, c.crossrefLimiter
		values.Set("query.bibliographic", req.Query)
		values.Set("rows", strconv.Itoa(req.Limit))
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
	}
	body, err := c.fetch(ctx, endpoint+"?"+values.Encode(), req.Source, limiter)
	if err != nil {
		return nil, err
	}
	var papers []Paper
	if req.Source == "arxiv" {
		papers, err = decodePaperArxiv(body)
	} else {
		papers, err = decodePaperCrossref(body)
	}
	if err != nil {
		return nil, fmt.Errorf("papersearch %s: %w", req.Source, err)
	}
	if len(papers) > req.Limit {
		papers = papers[:req.Limit]
	}
	return &PaperSearchResult{Source: req.Source, Query: req.Query, RetrievedAt: time.Now().UTC(), Papers: papers}, nil
}

func paperArxivQuery(req PaperSearchRequest) (string, error) {
	// Only letters, combining marks, and digits enter quoted all: terms. Quotes,
	// colons, parentheses, and provider operators can never become query syntax.
	words := strings.FieldsFunc(req.Query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return "", fmt.Errorf("papersearch arxiv query contains no searchable keywords")
	}
	terms := make([]string, 0, len(words))
	for _, word := range words {
		terms = append(terms, `all:"`+word+`"`)
	}
	query := strings.Join(terms, " AND ")
	if req.YearFrom != 0 || req.YearTo != 0 {
		from, to := req.YearFrom, req.YearTo
		if from == 0 {
			from = 1
		}
		if to == 0 {
			to = 9999
		}
		query = fmt.Sprintf("(%s) AND submittedDate:[%04d01010000 TO %04d12312359]", query, from, to)
	}
	return query, nil
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
