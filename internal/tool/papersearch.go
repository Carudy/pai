package tool

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Match modes for combining several keywords into one provider query.
const (
	PaperMatchAll = "all"
	PaperMatchAny = "any"
)

// paperSearchMaxOffset bounds pagination so a caller cannot walk an unbounded
// result set through repeated requests.
const paperSearchMaxOffset = 10000

// PaperSearchRequest describes a bibliographic search, not an AI-generated answer.
type PaperSearchRequest struct {
	Source   string `json:"source"`
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
	YearFrom int    `json:"year_from"`
	YearTo   int    `json:"year_to"`
	Sort     string `json:"sort"`
	// Match controls how several keywords combine. Empty (the default) tries
	// all-of first and falls back to relevance-ranked any-of only when the
	// stricter query matches nothing; "all" forces all-of and "any" forces any-of.
	Match string `json:"match"`
	// Offset skips that many leading matches, for walking a result set.
	Offset int `json:"offset"`
}

func (r *PaperSearchRequest) Normalize(defaultSource string) error {
	r.Query = strings.TrimSpace(r.Query)
	if r.Query == "" || utf8.RuneCountInString(r.Query) > 1000 {
		return fmt.Errorf("papersearch query must contain 1..1000 runes")
	}
	r.Source = strings.ToLower(strings.TrimSpace(r.Source))
	if r.Source == "" {
		r.Source = strings.ToLower(strings.TrimSpace(defaultSource))
		if r.Source == "" {
			r.Source = "arxiv"
		}
	}
	if r.Source != "arxiv" && r.Source != "crossref" {
		return fmt.Errorf("papersearch source must be arxiv or crossref")
	}
	if r.Limit == 0 {
		r.Limit = 5
	}
	if r.Limit < 1 || r.Limit > 20 {
		return fmt.Errorf("papersearch limit must be 1..20")
	}
	if r.YearFrom < 0 || r.YearFrom > 9999 || r.YearTo < 0 || r.YearTo > 9999 {
		return fmt.Errorf("papersearch years must be 1..9999, or 0 for absent")
	}
	if r.YearFrom != 0 && r.YearTo != 0 && r.YearFrom > r.YearTo {
		return fmt.Errorf("papersearch year_from must not exceed year_to")
	}
	r.Sort = strings.ToLower(strings.TrimSpace(r.Sort))
	if r.Sort == "" {
		r.Sort = "relevance"
	}
	if r.Sort != "relevance" && r.Sort != "newest" {
		return fmt.Errorf("papersearch sort must be relevance or newest")
	}
	r.Match = strings.ToLower(strings.TrimSpace(r.Match))
	switch r.Match {
	case "", PaperMatchAll, PaperMatchAny:
	default:
		return fmt.Errorf("papersearch match must be all or any")
	}
	if r.Offset < 0 || r.Offset > paperSearchMaxOffset {
		return fmt.Errorf("papersearch offset must be 0..%d", paperSearchMaxOffset)
	}
	return nil
}

// Paper contains provider metadata only. Type does not assert peer-review status.
// Date is an ISO date (possibly only a year or year-month); ID is the provider ID.
// Score is the provider's relevance score when it reports one (Crossref), and 0
// otherwise, since it is provider-specific and never comparable across sources.
type Paper struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	Date     string   `json:"date"`
	Venue    string   `json:"venue"`
	DOI      string   `json:"doi"`
	ArxivID  string   `json:"arxiv_id"`
	URL      string   `json:"url"`
	Abstract string   `json:"abstract"`
	Type     string   `json:"type"`
	Score    float64  `json:"score"`
}

// PaperSearchResult carries both the records and the diagnostics needed to tell a
// genuine empty result from an over-narrow or misdirected query.
type PaperSearchResult struct {
	Source string
	Query  string
	// QuerySent is the provider expression actually sent (arXiv), so the caller
	// can see how keywords were combined.
	QuerySent string
	// Match is the mode that produced Papers; Fallback records that the strict
	// all-of attempt returned nothing and Attempted its candidate count.
	Match     string
	Fallback  bool
	Attempted int
	// Candidates is the provider's total match count for the successful query;
	// it can exceed len(Papers).
	Candidates  int
	Offset      int
	YearFrom    int
	YearTo      int
	Sort        string
	RetrievedAt time.Time
	Papers      []Paper
}

// NextOffset returns the offset to continue from, or 0 when the end was reached.
func (r *PaperSearchResult) NextOffset() int {
	next := r.Offset + len(r.Papers)
	if len(r.Papers) == 0 || next >= r.Candidates {
		return 0
	}
	return next
}

// PaperSearch uses fixed public endpoints and process-wide provider throttles.
// contactEmail is optional Crossref polite-pool identification, not a credential.
func PaperSearch(ctx context.Context, req PaperSearchRequest, contactEmail string) (*PaperSearchResult, error) {
	return defaultPaperClient.search(ctx, req, contactEmail)
}

// header renders the diagnostic block shown before the records.
func (r *PaperSearchResult) header() string {
	var b strings.Builder
	status := "ok"
	if len(r.Papers) == 0 {
		status = "empty"
	}
	fmt.Fprintf(&b, "Paper search: %s\nStatus: %s\nQuery: %s\n", r.Source, status, r.Query)
	if r.QuerySent != "" {
		fmt.Fprintf(&b, "Sent: %s\n", r.QuerySent)
	}
	if r.Fallback {
		fmt.Fprintf(&b, "Match: all→any (all returned %d candidates; any returned %d)\n", r.Attempted, r.Candidates)
	} else {
		fmt.Fprintf(&b, "Match: %s (%d candidates)\n", r.Match, r.Candidates)
	}
	if r.YearFrom != 0 || r.YearTo != 0 {
		from, to := r.YearFrom, r.YearTo
		if from == 0 {
			from = 1
		}
		if to == 0 {
			to = 9999
		}
		fmt.Fprintf(&b, "Years: %d–%d\n", from, to)
	}
	fmt.Fprintf(&b, "Sort: %s\n", r.sortLabel())
	if next := r.NextOffset(); next > 0 {
		fmt.Fprintf(&b, "Next offset: %d\n", next)
	}
	fmt.Fprintf(&b, "Retrieved: %s\n", r.RetrievedAt.UTC().Format(time.RFC3339))
	return b.String()
}

// sortLabel spells out that "newest" means different dates per source.
func (r *PaperSearchResult) sortLabel() string {
	if r.Sort != "newest" {
		return "relevance"
	}
	if r.Source == "arxiv" {
		return "newest (arXiv first-submitted date)"
	}
	return "newest (Crossref publication date)"
}

// Format includes only complete records. It reserves room for the diagnostic
// header and an omission notice before adding each record, rather than cutting
// off identifiers or UTF-8 text.
func (r *PaperSearchResult) Format(maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 16384
	}
	notice := func(n int) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf("%d papers omitted (output budget).\n", n)
	}
	header := r.header() + "\n"
	if len(header)+len(notice(len(r.Papers))) > maxBytes {
		header = ""
	}
	var b strings.Builder
	b.WriteString(header)
	included := 0
	for i, p := range r.Papers {
		record := formatPaper(i+1, p)
		if b.Len()+len(record)+len(notice(len(r.Papers)-included-1)) > maxBytes {
			break
		}
		b.WriteString(record)
		included++
	}
	if omitted := len(r.Papers) - included; omitted > 0 {
		n := notice(omitted)
		if b.Len()+len(n) <= maxBytes {
			b.WriteString(n)
		} else {
			// Tiny budgets cannot hold a full record or a detailed notice.
			b.Reset()
			n = fmt.Sprintf("%d omitted", omitted)
			if len(n) <= maxBytes {
				b.WriteString(n)
			}
		}
	} else if len(r.Papers) == 0 {
		// A zero result is ambiguous on its own, so say what was tried and what
		// to change rather than leaving the model to guess.
		hint := "No papers found. The query was accepted as sent above; try broader keywords, quoted phrases, match:\"any\", a different source, or a wider year range.\n"
		if b.Len()+len(hint) <= maxBytes {
			b.WriteString(hint)
		}
	}
	return b.String()
}

func formatPaper(index int, p Paper) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. %s\n", index, paperText(p.Title, "[title unavailable]"))
	fields := []struct{ name, value string }{
		{"ID", p.ID}, {"DOI", p.DOI}, {"arXiv ID", p.ArxivID}, {"URL", p.URL},
		{"Authors", strings.Join(p.Authors, "; ")}, {"Date", p.Date}, {"Venue", p.Venue}, {"Type", p.Type},
	}
	for _, field := range fields {
		if field.value != "" {
			fmt.Fprintf(&b, "%s: %s\n", field.name, paperText(field.value, ""))
		}
	}
	if p.Score > 0 {
		fmt.Fprintf(&b, "Score: %.1f\n", p.Score)
	}
	abstract := paperText(p.Abstract, "[unavailable]")
	if runes := []rune(abstract); len(runes) > 2000 {
		abstract = string(runes[:2000]) + " [truncated]"
	}
	fmt.Fprintf(&b, "Abstract: %s\n\n", abstract)
	return b.String()
}

func paperText(s, fallback string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "�")), " ")
	if s == "" {
		return fallback
	}
	return s
}
