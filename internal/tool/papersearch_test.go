package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const paperAtomFixture = `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">
<opensearch:totalResults>2</opensearch:totalResults>
<entry><id>http://arxiv.org/abs/2401.12345v3</id><title>  Quantum
  café 宇宙 </title><summary> Résumé &amp; 日本語. </summary>
<published>2024-01-12T14:00:00Z</published><author><name>Ada Lovelace</name></author><author><name> 李 明 </name></author>
<arxiv:doi>10.1234/example</arxiv:doi><arxiv:journal_ref>Example 12 (2024)</arxiv:journal_ref>
<link rel="alternate" type="text/html" href="https://arxiv.org/abs/2401.12345v3"/>
<link rel="related" type="application/pdf" href="https://arxiv.org/pdf/2401.12345v3"/></entry>
<entry><id>http://arxiv.org/abs/hep-th/9901001v2</id></entry></feed>`

const paperCrossrefFixture = `{"status":"ok","message-type":"work-list","message":{"total-results":3,"items":[
{"DOI":"10.1234/é","title":["A &amp; B"],"author":[{"given":"Ada","family":"Lovelace"},{"name":"研究 Group"},{}],
"published":{"date-parts":[[2023,6,2]]},"container-title":["Example Journal"],"type":"journal-article",
"abstract":"<jats:p>Résumé &amp; 日本語 <jats:italic>café</jats:italic>.</jats:p><script>secret()</script><style>hidden</style>"},
{"title":["Partial"],"issued":{"date-parts":[[1999]]},"type":"posted-content"},{}
]}}`

// paperTestFeed builds an arXiv-style Atom feed whose total match count is
// explicit, which is what lets the client tell "no matches" from "narrow query".
func paperTestFeed(totalResults int, entries string) string {
	return fmt.Sprintf(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"><opensearch:totalResults>%d</opensearch:totalResults>%s</feed>`, totalResults, entries)
}

func paperTestEntry(id, title string) string {
	return fmt.Sprintf(`<entry><id>%s</id><title>%s</title><summary>abstract</summary><published>2024-01-02T00:00:00Z</published></entry>`, id, title)
}

func newPaperTestClient(t *testing.T, handler http.HandlerFunc) *paperClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &paperClient{
		httpClient: server.Client(), arxivEndpoint: server.URL + "/arxiv", crossrefEndpoint: server.URL + "/crossref",
		arxivLimiter: newPaperLimiter(0, true), crossrefLimiter: newPaperLimiter(0, false),
	}
}

func TestPaperNormalize(t *testing.T) {
	var request PaperSearchRequest
	if err := json.Unmarshal([]byte(`{"query":"  宇宙  ","year_from":1,"year_to":9999}`), &request); err != nil {
		t.Fatal(err)
	}
	if err := request.Normalize(""); err != nil {
		t.Fatal(err)
	}
	want := PaperSearchRequest{Source: "arxiv", Query: "宇宙", Limit: 5, YearFrom: 1, YearTo: 9999, Sort: "relevance"}
	if request != want {
		t.Fatalf("got %+v, want %+v", request, want)
	}
	request = PaperSearchRequest{Query: strings.Repeat("宇", 1000), Limit: 20, Sort: " NEWEST "}
	if err := request.Normalize(" CROSSREF "); err != nil || request.Source != "crossref" || request.Sort != "newest" {
		t.Fatalf("normalize: %+v, %v", request, err)
	}
	for name, req := range map[string]PaperSearchRequest{
		"blank": {Query: " \n\t "}, "long": {Query: strings.Repeat("宇", 1001)},
		"source": {Query: "x", Source: "other"}, "limit negative": {Query: "x", Limit: -1},
		"limit large": {Query: "x", Limit: 21}, "year negative": {Query: "x", YearFrom: -1},
		"year large": {Query: "x", YearTo: 10000}, "reversed": {Query: "x", YearFrom: 2025, YearTo: 2024},
		"sort":            {Query: "x", Sort: "oldest"},
		"match":           {Query: "x", Match: "some"},
		"offset negative": {Query: "x", Offset: -1},
		"offset large":    {Query: "x", Offset: paperSearchMaxOffset + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := req.Normalize("arxiv"); err == nil {
				t.Fatalf("accepted %+v", req)
			}
		})
	}
	request = PaperSearchRequest{Query: "x"}
	if err := request.Normalize("bad"); err == nil {
		t.Fatal("invalid default source accepted")
	}
	request.Source = "arxiv"
	if err := request.Normalize("bad"); err != nil {
		t.Fatalf("explicit source should override default: %v", err)
	}
	request = PaperSearchRequest{Query: "x", Match: " ANY ", Offset: 3}
	if err := request.Normalize(""); err != nil || request.Match != PaperMatchAny || request.Offset != 3 {
		t.Fatalf("normalize match/offset: %+v, %v", request, err)
	}
	if err := json.Unmarshal([]byte(`{"query":"x","limit":"5"}`), &request); err == nil {
		t.Fatal("accepted string limit")
	}
}

func TestPaperProviderRequests(t *testing.T) {
	for _, source := range []string{"arxiv", "crossref"} {
		for _, sort := range []string{"relevance", "newest"} {
			t.Run(source+"/"+sort, func(t *testing.T) {
				client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.Header.Get("User-Agent") == "" {
						t.Errorf("unexpected request: %s %+v", r.Method, r.Header)
					}
					q := r.URL.Query()
					if source == "arxiv" {
						if r.URL.Path != "/arxiv" || q.Get("max_results") != "2" || q.Get("start") != "0" || q.Get("mailto") != "" {
							t.Errorf("arxiv request: %s", r.URL)
						}
						want := `(all:"quantum" AND all:"OR" AND all:"ti" AND all:"injected") AND submittedDate:[202001010000 TO 202412312359]`
						if q.Get("search_query") != want {
							t.Errorf("query %q, want %q", q.Get("search_query"), want)
						}
						sortBy := "relevance"
						if sort == "newest" {
							sortBy = "submittedDate"
						}
						if q.Get("sortBy") != sortBy || q.Get("sortOrder") != "descending" {
							t.Errorf("sort: %v", q)
						}
						io.WriteString(w, paperAtomFixture)
					} else {
						if r.URL.Path != "/crossref" || q.Get("rows") != "2" || q.Get("offset") != "0" || q.Get("query.bibliographic") != `quantum OR ti:"injected"` || q.Get("mailto") != "test@example.com" {
							t.Errorf("crossref request: %s", r.URL)
						}
						if q.Get("filter") != "from-pub-date:2020-01-01,until-pub-date:2024-12-31" {
							t.Errorf("filters: %v", q)
						}
						sortBy := "score"
						if sort == "newest" {
							sortBy = "published"
						}
						if q.Get("sort") != sortBy || q.Get("order") != "desc" {
							t.Errorf("sort: %v", q)
						}
						io.WriteString(w, paperCrossrefFixture)
					}
				})
				before := time.Now()
				result, err := client.search(context.Background(), PaperSearchRequest{Source: source, Query: ` quantum OR ti:"injected" `, Limit: 2, YearFrom: 2020, YearTo: 2024, Sort: sort}, " test@example.com ")
				if err != nil {
					t.Fatal(err)
				}
				if result.Source != source || result.Query != `quantum OR ti:"injected"` || len(result.Papers) != 2 || result.RetrievedAt.Before(before) {
					t.Fatalf("result: %+v", result)
				}
				wantMatch, wantCandidates := "all", 2
				if source == "crossref" {
					wantMatch, wantCandidates = "any", 3
				}
				if result.Match != wantMatch || result.Fallback || result.Candidates != wantCandidates || result.QuerySent == "" {
					t.Fatalf("diagnostics: %+v", result)
				}
			})
		}
	}
}

func TestPaperYearFilters(t *testing.T) {
	for _, tc := range []struct {
		from, to        int
		arxiv, crossref string
	}{
		{0, 0, `all:"x"`, ""},
		{1, 0, `(all:"x") AND submittedDate:[000101010000 TO 999912312359]`, "from-pub-date:0001-01-01"},
		{0, 9999, `(all:"x") AND submittedDate:[000101010000 TO 999912312359]`, "until-pub-date:9999-12-31"},
	} {
		query := paperArxivExpression(paperArxivTerms("x"), PaperMatchAll, tc.from, tc.to)
		if query != tc.arxiv {
			t.Fatalf("arxiv query: %q", query)
		}
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query(); q.Get("filter") != tc.crossref || q.Get("mailto") != "" || q.Get("rows") != "5" || q.Get("offset") != "0" {
				t.Errorf("query: %v", q)
			}
			io.WriteString(w, `{"status":"ok","message-type":"work-list","message":{"items":[]}}`)
		})
		if _, err := client.search(context.Background(), PaperSearchRequest{Source: "crossref", Query: "x", YearFrom: tc.from, YearTo: tc.to}, ""); err != nil {
			t.Fatal(err)
		}
	}
	// A query of nothing but punctuation yields no searchable term, so it is
	// refused instead of being sent as an empty provider query.
	if terms := paperArxivTerms(`:()"*`); len(terms) != 0 {
		t.Fatalf("punctuation produced terms: %v", terms)
	}
	client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unsendable query reached the provider") })
	if _, err := client.search(context.Background(), PaperSearchRequest{Query: `:()"*`}, ""); err == nil || !strings.Contains(err.Error(), "no searchable keywords") {
		t.Fatalf("error: %v", err)
	}
}

func TestPaperArxivTerms(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{`alpha beta`, []string{`all:"alpha"`, `all:"beta"`}},
		{`"decision tree"`, []string{`all:"decision tree"`}},
		// A bare span is split on punctuation, so a hyphenated compound becomes two
		// keywords; a quoted phrase keeps its internal punctuation and spacing.
		{`non-interactive "private decision tree" privacy`, []string{`all:"non"`, `all:"interactive"`, `all:"private decision tree"`, `all:"privacy"`}},
		// Provider operators and field prefixes in the model's text stay literal.
		{`a OR b ANDNOT c`, []string{`all:"a"`, `all:"OR"`, `all:"b"`, `all:"ANDNOT"`, `all:"c"`}},
		{`ti:decision abs:tree`, []string{`all:"ti"`, `all:"decision"`, `all:"abs"`, `all:"tree"`}},
		{`  spaced   out  `, []string{`all:"spaced"`, `all:"out"`}},
		// An unterminated quote keeps the rest of the text as one phrase.
		{`"unclosed phrase`, []string{`all:"unclosed phrase"`}},
		// A phrase must contain a letter or digit to be worth searching.
		{`"*" :()`, nil},
		{`""`, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := paperArxivTerms(tc.query); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("terms %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPaperArxivExpression(t *testing.T) {
	terms := []string{`all:"a"`, `all:"b"`}
	for _, tc := range []struct {
		match    string
		from, to int
		want     string
	}{
		{PaperMatchAll, 0, 0, `all:"a" AND all:"b"`},
		{PaperMatchAny, 0, 0, `all:"a" OR all:"b"`},
		// AND binds tighter than OR, so an any-of group must be parenthesized
		// before the date restriction is appended.
		{PaperMatchAny, 2020, 0, `(all:"a" OR all:"b") AND submittedDate:[202001010000 TO 999912312359]`},
	} {
		if got := paperArxivExpression(terms, tc.match, tc.from, tc.to); got != tc.want {
			t.Fatalf("expression %q, want %q", got, tc.want)
		}
	}
}

// The strict all-of query is tried first; only a zero-match result falls back to
// any-of, and the fallback is reported rather than hidden.
func TestPaperArxivMatchFallback(t *testing.T) {
	for _, tc := range []struct {
		match        string
		wantMatch    string
		wantFallback bool
		wantCalls    int32
		wantPapers   int
		wantCands    int
	}{
		{"", "any", true, 2, 1, 7},
		{PaperMatchAny, "any", false, 1, 1, 7},
		{PaperMatchAll, "all", false, 1, 0, 0},
	} {
		t.Run("match="+tc.match, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var queries []string
			client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				query := r.URL.Query().Get("search_query")
				mu.Lock()
				queries = append(queries, query)
				mu.Unlock()
				if strings.Contains(query, " OR ") {
					io.WriteString(w, paperTestFeed(7, paperTestEntry("http://arxiv.org/abs/2401.00001v1", "Found")))
					return
				}
				io.WriteString(w, paperTestFeed(0, ""))
			})
			result, err := client.search(context.Background(), PaperSearchRequest{Query: "alpha beta", Match: tc.match}, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Match != tc.wantMatch || result.Fallback != tc.wantFallback || len(result.Papers) != tc.wantPapers || result.Candidates != tc.wantCands {
				t.Fatalf("result: %+v", result)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("calls %d, want %d (%v)", got, tc.wantCalls, queries)
			}
			if tc.wantFallback {
				if result.Attempted != 0 || !strings.Contains(result.QuerySent, " OR ") {
					t.Fatalf("fallback diagnostics: %+v", result)
				}
				// The strict attempt must be the one that returned nothing.
				if len(queries) != 2 || strings.Contains(queries[0], " OR ") || !strings.Contains(queries[0], " AND ") {
					t.Fatalf("attempt order: %v", queries)
				}
			}
		})
	}
}

func TestPaperPagination(t *testing.T) {
	t.Run("arxiv start", func(t *testing.T) {
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query(); q.Get("start") != "10" || q.Get("max_results") != "5" {
				t.Errorf("request: %v", q)
			}
			io.WriteString(w, paperTestFeed(20, paperTestEntry("http://arxiv.org/abs/2401.00002v1", "Tenth")))
		})
		result, err := client.search(context.Background(), PaperSearchRequest{Query: "x", Offset: 10}, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Offset != 10 || result.Candidates != 20 || result.NextOffset() != 11 {
			t.Fatalf("result: %+v next=%d", result, result.NextOffset())
		}
		if !strings.Contains(result.Format(0), "Next offset: 11") {
			t.Fatalf("missing page hint: %s", result.Format(0))
		}
	})
	t.Run("crossref offset and score", func(t *testing.T) {
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query(); q.Get("offset") != "7" || q.Get("rows") != "5" {
				t.Errorf("request: %v", q)
			}
			io.WriteString(w, `{"status":"ok","message-type":"work-list","message":{"total-results":9,"items":[{"DOI":"10.1/a","title":["A"],"score":12.5}]}}`)
		})
		result, err := client.search(context.Background(), PaperSearchRequest{Source: "crossref", Query: "x", Offset: 7}, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Offset != 7 || result.Candidates != 9 || result.NextOffset() != 8 || result.Papers[0].Score != 12.5 {
			t.Fatalf("result: %+v", result)
		}
		formatted := result.Format(0)
		if !strings.Contains(formatted, "Score: 12.5") || !strings.Contains(formatted, "Match: any (9 candidates)") {
			t.Fatalf("diagnostics: %s", formatted)
		}
	})
	t.Run("last page has no next offset", func(t *testing.T) {
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, paperTestFeed(2, paperTestEntry("http://arxiv.org/abs/2401.00003v1", "Last")))
		})
		result, err := client.search(context.Background(), PaperSearchRequest{Query: "x", Offset: 1}, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.NextOffset() != 0 || strings.Contains(result.Format(0), "Next offset") {
			t.Fatalf("result: %+v", result)
		}
	})
}

// match=all is arxiv-only: refusing it beats silently ignoring the request.
func TestPaperCrossrefRejectsMatchAll(t *testing.T) {
	client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request sent despite unsupported match") })
	if _, err := client.search(context.Background(), PaperSearchRequest{Source: "crossref", Query: "x", Match: PaperMatchAll}, ""); err == nil || !strings.Contains(err.Error(), "arxiv-only") {
		t.Fatalf("error: %v", err)
	}
}

func TestPaperDiagnosticsHeader(t *testing.T) {
	fallback := &PaperSearchResult{
		Source: "arxiv", Query: "oblique decision tree privacy inference",
		QuerySent: `(all:"oblique" OR all:"decision")`, Match: PaperMatchAny, Fallback: true,
		Attempted: 0, Candidates: 298496, Sort: "newest", YearFrom: 2020, RetrievedAt: time.Now(),
		Papers: []Paper{{Title: "T", ID: "i"}},
	}
	got := fallback.Format(0)
	for _, want := range []string{
		"Status: ok",
		"Sent: (all:\"oblique\" OR all:\"decision\")",
		"Match: all→any (all returned 0 candidates; any returned 298496)",
		"Years: 2020–9999",
		"Sort: newest (arXiv first-submitted date)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	empty := &PaperSearchResult{Source: "crossref", Query: "q", Match: PaperMatchAny, Sort: "relevance", RetrievedAt: time.Now()}
	got = empty.Format(0)
	for _, want := range []string{"Status: empty", "No papers found", "match:\"any\"", "Sort: relevance"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPaperDecodeArxiv(t *testing.T) {
	papers, candidates, err := decodePaperArxiv([]byte(paperAtomFixture))
	if err != nil {
		t.Fatal(err)
	}
	if candidates != 2 {
		t.Fatalf("candidates: %d", candidates)
	}
	p := papers[0]
	if p.ID != "http://arxiv.org/abs/2401.12345v3" || p.ArxivID != "2401.12345v3" || p.URL != "https://arxiv.org/abs/2401.12345v3" || p.DOI != "10.1234/example" || p.Type != "preprint" || p.Date != "2024-01-12" || p.Venue != "Example 12 (2024)" {
		t.Fatalf("metadata: %+v", p)
	}
	if p.Title != "Quantum café 宇宙" || p.Abstract != "Résumé & 日本語." || !reflect.DeepEqual(p.Authors, []string{"Ada Lovelace", "李 明"}) {
		t.Fatalf("text: %+v", p)
	}
	if papers[1].ArxivID != "hep-th/9901001v2" || papers[1].Abstract != "" {
		t.Fatalf("partial: %+v", papers[1])
	}
	for _, fixture := range []string{`<feed><entry><id>http://arxiv.org/api/errors#bad_query</id><title>Error</title><summary>bad query</summary></entry></feed>`, `<feed><entry>`, `<html/>`} {
		if _, _, err := decodePaperArxiv([]byte(fixture)); err == nil {
			t.Fatalf("accepted %s", fixture)
		}
	}
}

func TestPaperDecodeCrossref(t *testing.T) {
	papers, candidates, err := decodePaperCrossref([]byte(paperCrossrefFixture))
	if err != nil {
		t.Fatal(err)
	}
	if candidates != 3 {
		t.Fatalf("candidates: %d", candidates)
	}
	p := papers[0]
	if p.ID != "10.1234/é" || p.DOI != p.ID || p.URL != "https://doi.org/10.1234/%C3%A9" || p.Title != "A & B" || p.Date != "2023-06-02" || p.Type != "journal-article" || p.Venue != "Example Journal" || p.ArxivID != "" {
		t.Fatalf("metadata: %+v", p)
	}
	if p.Abstract != "Résumé & 日本語 café ." || !reflect.DeepEqual(p.Authors, []string{"Ada Lovelace", "研究 Group"}) {
		t.Fatalf("text: %+v", p)
	}
	if papers[1].Date != "1999" || papers[1].Type != "posted-content" || papers[2].URL != "" {
		t.Fatalf("partial: %+v", papers)
	}
	for _, fixture := range []string{`{}`, `null`, `{"status":"error"}`, `{"status":"ok","message-type":"work","message":{"items":[]}}`, `{"status":"ok","message-type":"work-list","message":{}}`, `{"status":"ok","message-type":"work-list","message":{"items":null}}`, `{"status":"ok","message-type":"work-list","message":{"items":{}}}`, `{"status":"ok","message-type":"work-list","message":{"items":[]}} trailing`} {
		if _, _, err := decodePaperCrossref([]byte(fixture)); err == nil {
			t.Fatalf("accepted %s", fixture)
		}
	}
}

func TestPaperDecodeCrossrefDOIURLs(t *testing.T) {
	for _, tc := range []struct{ doi, suppliedURL, want string }{
		{"10.1234/a#b?c/d", "", "https://doi.org/10.1234/a%23b%3Fc/d"},
		{"10.1234/é%20", "", "https://doi.org/10.1234/%C3%A9%2520"},
		{"10.1234/a#b", "https://example.com/paper?view=full#abstract", "https://example.com/paper?view=full#abstract"},
	} {
		t.Run(tc.doi, func(t *testing.T) {
			fixture, err := json.Marshal(map[string]any{
				"status": "ok", "message-type": "work-list",
				"message": map[string]any{"items": []map[string]string{{"DOI": tc.doi, "URL": tc.suppliedURL}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			papers, _, err := decodePaperCrossref(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if p := papers[0]; p.DOI != tc.doi || p.ID != tc.doi || p.URL != tc.want {
				t.Fatalf("metadata: %+v", p)
			}
			if tc.suppliedURL == "" {
				parsed, err := url.Parse(papers[0].URL)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Path != "/"+tc.doi || parsed.RawQuery != "" || parsed.Fragment != "" {
					t.Fatalf("DOI escaped outside path: %+v", parsed)
				}
			}
		})
	}
}

func TestPaperMarkup(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`<jats:p title="a > b">α &lt; 3 &amp; β &#x1f30c;</jats:p>`, "α < 3 & β 🌌"},
		{`before<SCRIPT>if (a < b) { bad(); }</SCRIPT><STYLE>.x { display:none }</STYLE>after`, "beforeafter"},
		{`a<!-- hidden -->b<br/>c`, "ab c"},
		{`<p>2 < 3 and 4 > 2</p>`, "2 < 3 and 4 > 2"},
		{`x < y > z`, "x < y > z"},
		{"x <\ty > z", "x < y > z"},
		{`<p>x < y > z</p>`, "x < y > z"},
		{`< p>x</ p>`, "< p>x</ p>"},
		{`<![CDATA[A & B]]>`, "A & B"},
		{`<p><![CDATA[α < β &amp; <tag>]]> &amp; text</p>`, "α < β &amp; <tag> & text"},
		{`<script><![CDATA[secret </script>]]></script><style><![CDATA[hidden]]></style><p>visible</p>`, "visible"},
		{`<> </> <`, "<> </> <"},
		{`<script>never closed`, ""},
		{`<p>Résumé &nbsp; café</p>`, "Résumé café"},
	} {
		if got := paperMarkup(tc.input); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPaperHTTPFailuresAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		retry  string
		calls  int
		want   string
	}{
		{"bad request", 400, "", 1, "HTTP 400: rejected"},
		{"rate limit retry", 429, "0", 2, ""},
		{"unavailable retry", 503, "0", 2, ""},
		{"long retry", 429, "60", 1, "exceeds maximum retry wait"},
		{"huge retry", 503, "18446744073709551615", 1, "exceeds maximum retry wait"},
		{"invalid retry", 503, "invalid", 1, "invalid Retry-After"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", tc.retry)
					w.WriteHeader(tc.status)
					io.WriteString(w, "rejected")
					return
				}
				io.WriteString(w, paperTestFeed(1, ""))
			})
			_, err := client.search(context.Background(), PaperSearchRequest{Query: "x"}, "")
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error: %v", err)
			}
			if got := int(calls.Load()); got != tc.calls {
				t.Fatalf("calls %d, want %d", got, tc.calls)
			}
		})
	}
	t.Run("retry once only", func(t *testing.T) {
		var calls atomic.Int32
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
		})
		_, err := client.search(context.Background(), PaperSearchRequest{Query: "x"}, "")
		if err == nil || !strings.Contains(err.Error(), "HTTP 503") || calls.Load() != 2 {
			t.Fatalf("calls %d, err %v", calls.Load(), err)
		}
	})
	t.Run("response limit", func(t *testing.T) {
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, strings.Repeat("x", paperResponseLimit+1))
		})
		_, err := client.search(context.Background(), PaperSearchRequest{Query: "x"}, "")
		if err == nil || !strings.Contains(err.Error(), "response exceeds") {
			t.Fatalf("error: %v", err)
		}
	})
	t.Run("redirect refused", func(t *testing.T) {
		client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/other", 302) })
		client.httpClient.CheckRedirect = defaultPaperClient.httpClient.CheckRedirect
		_, err := client.search(context.Background(), PaperSearchRequest{Query: "x"}, "")
		if err == nil || !strings.Contains(err.Error(), "redirect refused") {
			t.Fatalf("error: %v", err)
		}
	})
}

func TestPaperRetryAfterDates(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
		fail  bool
	}{
		{"", time.Second, false}, {"0", 0, false}, {"10", 10 * time.Second, false}, {"11", 0, true},
		{now.Add(4 * time.Second).Format(http.TimeFormat), 4 * time.Second, false},
		{now.Add(-time.Second).Format(http.TimeFormat), 0, false},
		{now.Add(time.Hour).Format(http.TimeFormat), 0, true},
	} {
		delay, err := paperRetryDelay(tc.value, now)
		if (err != nil) != tc.fail || delay != tc.want {
			t.Errorf("%q: delay %s, error %v", tc.value, delay, err)
		}
	}
}

func TestPaperCancellation(t *testing.T) {
	for _, mode := range []string{"before request", "in flight", "retry wait"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "retry wait" {
					w.Header().Set("Retry-After", "5")
					w.WriteHeader(429)
					close(started)
					return
				}
				close(started)
				<-r.Context().Done()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "before request" {
				cancel()
			}
			done := make(chan error, 1)
			go func() {
				_, err := client.search(ctx, PaperSearchRequest{Query: "x"}, "")
				done <- err
			}()
			if mode != "before request" {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("request did not start")
				}
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation blocked")
			}
			if mode == "before request" && calls.Load() != 0 {
				t.Fatal("canceled request reached server")
			}
		})
	}
}

func TestPaperFormatBudgets(t *testing.T) {
	result := PaperSearchResult{Source: "arxiv", Query: "日本語", RetrievedAt: time.Now(), Papers: []Paper{
		{ID: "id-1", ArxivID: "2401.1v2", DOI: "10.1/one", URL: "https://example.com/one", Title: "宇宙", Abstract: strings.Repeat("🌌", 2001), Type: "preprint"},
		{ID: "id-2", URL: "https://example.com/two", Title: "Second"},
	}}
	full := result.Format(0)
	if !strings.Contains(full, strings.Repeat("🌌", 2000)+" [truncated]") || strings.Contains(full, strings.Repeat("🌌", 2001)) || !strings.Contains(full, "Abstract: [unavailable]") {
		t.Fatalf("abstract formatting: %s", full)
	}
	if full != result.Format(-1) || len(full) > 16384 {
		t.Fatal("default budget")
	}
	for budget := 1; budget <= len(full)+1; budget++ {
		formatted := result.Format(budget)
		if len(formatted) > budget || !utf8.ValidString(formatted) {
			t.Fatalf("budget %d: %d bytes, UTF-8 %v", budget, len(formatted), utf8.ValidString(formatted))
		}
		for i, paper := range result.Papers {
			if strings.Contains(formatted, fmt.Sprintf("%d. %s\n", i+1, paper.Title)) {
				if !strings.Contains(formatted, "ID: "+paper.ID+"\n") || !strings.Contains(formatted, "URL: "+paper.URL+"\n") || !strings.Contains(formatted, formatPaper(i+1, paper)) {
					t.Fatalf("partial record at budget %d", budget)
				}
			}
		}
	}
	limited := result.Format(len(full) - 1)
	if !strings.Contains(limited, "1 papers omitted") || !strings.Contains(limited, "arXiv ID: 2401.1v2") {
		t.Fatalf("omissions: %s", limited)
	}
	if tiny := result.Format(12); tiny != "2 omitted" {
		t.Fatalf("tiny: %q", tiny)
	}
	if got := (&PaperSearchResult{}).Format(0); !strings.Contains(got, "No papers found") {
		t.Fatalf("empty: %q", got)
	}
}

func TestPaperLimiterCancellation(t *testing.T) {
	for _, serial := range []bool{false, true} {
		limiter := newPaperLimiter(time.Hour, serial)
		release, err := limiter.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err = limiter.acquire(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("serial=%v: %v", serial, err)
		}
		release()
		// A canceled waiter must not retain the gate.
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		if _, err := limiter.acquire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled: %v", err)
		}
	}
}

func TestPaperLimiterConcurrentStarts(t *testing.T) {
	for _, serial := range []bool{false, true} {
		t.Run(fmt.Sprint(serial), func(t *testing.T) {
			interval := 5 * time.Millisecond
			limiter := newPaperLimiter(interval, serial)
			var mu sync.Mutex
			var starts []time.Time
			var active atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					release, err := limiter.acquire(ctx)
					if err != nil {
						t.Error(err)
						return
					}
					defer release()
					if serial && active.Add(1) != 1 {
						t.Error("serial requests overlapped")
					}
					mu.Lock()
					starts = append(starts, time.Now())
					mu.Unlock()
					if serial {
						time.Sleep(8 * time.Millisecond)
						active.Add(-1)
					}
				}()
			}
			wg.Wait()
			if len(starts) != 12 {
				t.Fatalf("starts: %d", len(starts))
			}
			for i := 1; i < len(starts); i++ {
				// Small scheduling variance after acquire is not a throttle failure.
				if delta := starts[i].Sub(starts[i-1]); delta < interval-time.Millisecond {
					t.Fatalf("start spacing: %s", delta)
				}
			}
		})
	}
}

func TestPaperLimiterSharedClients(t *testing.T) {
	entered := make(chan struct{}, 2)
	unblock := make(chan struct{})
	client := newPaperTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-unblock:
		case <-r.Context().Done():
			return
		}
		// A non-zero total keeps this a single-request probe of the shared gate;
		// a zero-match feed would trigger the all-of→any-of fallback and thus a
		// second request per caller.
		io.WriteString(w, paperTestFeed(1, ""))
	})
	other := *client // Separate callers still share the provider gate.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for _, c := range []*paperClient{client, &other} {
		go func(c *paperClient) { _, err := c.search(ctx, PaperSearchRequest{Query: "x"}, ""); done <- err }(c)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request blocked")
	}
	select {
	case <-entered:
		t.Error("arxiv requests overlapped")
	case <-time.After(20 * time.Millisecond):
	}
	close(unblock)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if defaultPaperClient.arxivLimiter.interval != 3*time.Second || !defaultPaperClient.arxivLimiter.serial || defaultPaperClient.crossrefLimiter.interval != time.Second {
		t.Fatal("unexpected production throttles")
	}
}
