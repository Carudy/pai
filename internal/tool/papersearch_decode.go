package tool

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// decodePaperArxiv returns the entries plus the feed's total match count. The
// total lets a caller tell an over-narrow query (0 candidates) from a malformed
// response, and it drives the all-of→any-of fallback.
func decodePaperArxiv(body []byte) ([]Paper, int, error) {
	var feed struct {
		XMLName xml.Name `xml:"feed"`
		// arXiv advertises the OpenSearch namespace, so the total must be matched
		// by namespace URI rather than by the local name alone.
		TotalResults string `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
		Entries      []struct {
			ID        string `xml:"id"`
			Title     string `xml:"title"`
			Summary   string `xml:"summary"`
			Published string `xml:"published"`
			DOI       string `xml:"doi"`
			Journal   string `xml:"journal_ref"`
			Authors   []struct {
				Name string `xml:"name"`
			} `xml:"author"`
			Links []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
				Type string `xml:"type,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, 0, fmt.Errorf("decode Atom feed: %w", err)
	}
	papers := make([]Paper, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		if strings.Contains(entry.ID, "/api/errors") || strings.EqualFold(strings.TrimSpace(entry.Title), "Error") {
			return nil, 0, fmt.Errorf("arxiv error entry: %s", paperText(entry.Summary, entry.ID))
		}
		id := strings.TrimSpace(entry.ID)
		arxivID := id
		if u, err := url.Parse(id); err == nil && u.Host != "" {
			arxivID = strings.TrimPrefix(u.Path, "/abs/")
		}
		p := Paper{
			ID: id, ArxivID: arxivID, Title: paperText(entry.Title, ""),
			Abstract: paperText(entry.Summary, ""), DOI: paperText(entry.DOI, ""),
			Venue: paperText(entry.Journal, ""), Date: paperText(entry.Published, ""), Type: "preprint",
		}
		if len(p.Date) >= 10 {
			p.Date = p.Date[:10]
		}
		if strings.HasPrefix(id, "https://") || strings.HasPrefix(id, "http://") {
			p.URL = id
		}
		for _, link := range entry.Links {
			if link.Rel == "alternate" && (link.Type == "text/html" || link.Type == "") {
				p.URL = strings.TrimSpace(link.Href)
				break
			}
		}
		for _, author := range entry.Authors {
			if name := paperText(author.Name, ""); name != "" {
				p.Authors = append(p.Authors, name)
			}
		}
		papers = append(papers, p)
	}
	candidates, err := strconv.Atoi(strings.TrimSpace(feed.TotalResults))
	if err != nil || candidates < len(papers) {
		// A missing or unparsable total must never understate what was returned.
		candidates = len(papers)
	}
	return papers, candidates, nil
}

type paperCrossrefDate struct {
	Parts [][]int `json:"date-parts"`
}

func (d paperCrossrefDate) format() string {
	if len(d.Parts) == 0 || len(d.Parts[0]) == 0 {
		return ""
	}
	parts := d.Parts[0]
	if parts[0] < 1 || parts[0] > 9999 {
		return ""
	}
	date := fmt.Sprintf("%04d", parts[0])
	if len(parts) > 1 && parts[1] >= 1 && parts[1] <= 12 {
		date += fmt.Sprintf("-%02d", parts[1])
		if len(parts) > 2 && parts[2] >= 1 && parts[2] <= 31 {
			date += fmt.Sprintf("-%02d", parts[2])
		}
	}
	return date
}

func decodePaperCrossref(body []byte) ([]Paper, int, error) {
	type work struct {
		DOI      string   `json:"DOI"`
		URL      string   `json:"URL"`
		Title    []string `json:"title"`
		Venue    []string `json:"container-title"`
		Abstract string   `json:"abstract"`
		Type     string   `json:"type"`
		Authors  []struct {
			Given  string `json:"given"`
			Family string `json:"family"`
			Name   string `json:"name"`
		} `json:"author"`
		Published paperCrossrefDate `json:"published"`
		Print     paperCrossrefDate `json:"published-print"`
		Online    paperCrossrefDate `json:"published-online"`
		Issued    paperCrossrefDate `json:"issued"`
		// Score is Crossref's relevance score, present when results are
		// relevance-ranked. It is provider-specific and not comparable across
		// sources, so it is reported for judgement rather than filtering.
		Score float64 `json:"score"`
	}
	var envelope struct {
		Status  string `json:"status"`
		Type    string `json:"message-type"`
		Message *struct {
			TotalResults *int    `json:"total-results"`
			Items        *[]work `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, 0, fmt.Errorf("decode Crossref response: %w", err)
	}
	if envelope.Status != "ok" || envelope.Type != "work-list" || envelope.Message == nil || envelope.Message.Items == nil {
		return nil, 0, fmt.Errorf("malformed Crossref envelope: expected ok work-list with message.items array")
	}
	papers := make([]Paper, 0, len(*envelope.Message.Items))
	for _, item := range *envelope.Message.Items {
		doi := strings.TrimSpace(item.DOI)
		p := Paper{
			ID: doi, DOI: doi, URL: strings.TrimSpace(item.URL),
			Title: paperMarkup(strings.Join(item.Title, "; ")), Venue: paperMarkup(strings.Join(item.Venue, "; ")),
			Abstract: paperMarkup(item.Abstract), Type: paperText(item.Type, ""), Score: item.Score,
		}
		if p.URL == "" && doi != "" {
			doiURL := url.URL{Scheme: "https", Host: "doi.org", Path: "/" + doi}
			p.URL = doiURL.String()
		}
		for _, date := range []paperCrossrefDate{item.Published, item.Print, item.Online, item.Issued} {
			if p.Date = date.format(); p.Date != "" {
				break
			}
		}
		for _, author := range item.Authors {
			name := paperText(author.Name, "")
			if name == "" {
				name = paperText(author.Given+" "+author.Family, "")
			}
			if name != "" {
				p.Authors = append(p.Authors, name)
			}
		}
		papers = append(papers, p)
	}
	candidates := 0
	if envelope.Message.TotalResults != nil {
		candidates = *envelope.Message.TotalResults
	}
	if candidates < len(papers) {
		candidates = len(papers)
	}
	return papers, candidates, nil
}

// Crossref abstracts are often JATS fragments, not well-formed XML documents.
// Scan tags while respecting quoted '>' characters, preserving ordinary math
// comparisons, and discarding script/style contents before decoding entities.
func paperMarkup(s string) string {
	var b strings.Builder
	ignored := ""
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "<![CDATA[") {
			content := s[i+9:]
			end := strings.Index(content, "]]>")
			if end < 0 {
				end = len(content)
			}
			if ignored == "" {
				// CDATA is literal text; protect its entities from the final decode.
				b.WriteString(html.EscapeString(content[:end]))
			}
			i += 9 + end
			if i < len(s) {
				i += 3
			}
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if s[i] != '<' {
			if ignored == "" {
				b.WriteByte(s[i])
			}
			i++
			continue
		}
		end := paperTagEnd(s, i+1)
		if end < 0 {
			if ignored == "" {
				b.WriteByte(s[i])
			}
			i++
			continue
		}
		tag := s[i+1 : end]
		closing := strings.HasPrefix(tag, "/")
		tag = strings.TrimPrefix(tag, "/")
		// XML requires the name immediately after '<' or '</'. Whitespace
		// here indicates ordinary text, such as "x < y > z", not a tag.
		if tag != "" && unicode.IsSpace([]rune(tag)[0]) {
			if ignored == "" {
				b.WriteByte(s[i])
			}
			i++
			continue
		}
		fields := strings.FieldsFunc(tag, func(r rune) bool { return unicode.IsSpace(r) || r == '/' })
		if len(fields) == 0 {
			if ignored == "" {
				b.WriteString(s[i : end+1])
			}
			i = end + 1
			continue
		}
		name := strings.ToLower(fields[0])
		// Numeric/comparison text isn't markup. XML names start with a letter,
		// underscore, or colon; declarations are discarded as markup too.
		first := []rune(name)[0]
		if !unicode.IsLetter(first) && first != '_' && first != ':' && first != '!' && first != '?' {
			if ignored == "" {
				b.WriteByte(s[i])
			}
			i++
			continue
		}
		if ignored != "" {
			if closing && name == ignored {
				ignored = ""
			}
		} else if (name == "script" || name == "style") && !closing && !strings.HasSuffix(tag, "/") {
			ignored = name
		} else {
			b.WriteByte(' ')
		}
		i = end + 1
	}
	return paperText(html.UnescapeString(b.String()), "")
}

func paperTagEnd(s string, start int) int {
	var quote byte
	for i := start; i < len(s); i++ {
		if quote != 0 {
			if s[i] == quote {
				quote = 0
			}
			continue
		}
		switch s[i] {
		case '\'', '"':
			quote = s[i]
		case '>':
			return i
		case '<':
			return -1
		}
	}
	return -1
}
