package tool

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// PaperSearchRequest describes a bibliographic search, not an AI-generated answer.
type PaperSearchRequest struct {
	Source   string `json:"source"`
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
	YearFrom int    `json:"year_from"`
	YearTo   int    `json:"year_to"`
	Sort     string `json:"sort"`
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
	return nil
}

// Paper contains provider metadata only. Type does not assert peer-review status.
// Date is an ISO date (possibly only a year or year-month); ID is the provider ID.
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
}

type PaperSearchResult struct {
	Source      string    `json:"source"`
	Query       string    `json:"query"`
	RetrievedAt time.Time `json:"retrieved_at"`
	Papers      []Paper   `json:"papers"`
}

// PaperSearch uses fixed public endpoints and process-wide provider throttles.
// contactEmail is optional Crossref polite-pool identification, not a credential.
func PaperSearch(ctx context.Context, req PaperSearchRequest, contactEmail string) (*PaperSearchResult, error) {
	return defaultPaperClient.search(ctx, req, contactEmail)
}

// Format includes only complete records. It reserves room for an omission notice
// before adding each record, rather than cutting off identifiers or UTF-8 text.
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
	header := fmt.Sprintf("Paper search: %s\nQuery: %s\nRetrieved: %s\n\n", r.Source, r.Query, r.RetrievedAt.UTC().Format(time.RFC3339))
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
	} else if len(r.Papers) == 0 && b.Len()+len("No papers found.\n") <= maxBytes {
		b.WriteString("No papers found.\n")
	}
	return b.String()
}

func formatPaper(index int, p Paper) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. %s\n", index, paperText(p.Title, "[title unavailable]"))
	for _, field := range []struct{ name, value string }{
		{"ID", p.ID}, {"DOI", p.DOI}, {"arXiv ID", p.ArxivID}, {"URL", p.URL},
		{"Authors", strings.Join(p.Authors, "; ")}, {"Date", p.Date}, {"Venue", p.Venue}, {"Type", p.Type},
	} {
		if field.value != "" {
			fmt.Fprintf(&b, "%s: %s\n", field.name, paperText(field.value, ""))
		}
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
