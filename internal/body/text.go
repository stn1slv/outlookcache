package body

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Text is a body converted to plain text.
type Text struct {
	Text string
	// QuoteRemoved is true when quoted or forwarded text was cut off. The
	// cut text can be empty (a forward without added text, or a body whose
	// quote comes first); callers that need content should then use the
	// text converted without cutting.
	QuoteRemoved bool
	// QuoteMarker names what started the cut text, for example
	// "id=divrplyfwdmsg".
	QuoteMarker string
}

// skipped elements never produce text.
var skipped = map[atom.Atom]bool{
	atom.Head: true, atom.Script: true, atom.Style: true, atom.Title: true, atom.Noscript: true,
	atom.Template: true, atom.Svg: true, atom.Object: true, atom.Iframe: true,
}

// block elements start and end on their own line.
var block = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Table: true, atom.Tr: true, atom.Ul: true, atom.Ol: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Pre: true,
	atom.Blockquote: true, atom.Hr: true, atom.Section: true, atom.Article: true, atom.Header: true, atom.Footer: true,
	atom.Center: true, atom.Address: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
}

// Quote starts. Counted on the whole store (docs/STORAGE.md):
// divRplyFwdMsg, x_divRplyFwdMsg, appendonsend,
// mail-editor-reference-message-container, gmail_quote and the #E1E1E1
// border. Known from other clients but not seen in the store (UNVERIFIED
// here): OLK_SRC_BODY_SECTION, moz-cite-prefix, OutlookMessageHeader, the
// #B5C4DF border, blockquote type=cite and "-----Original Message-----".
var (
	quoteIDs     = map[string]bool{"divrplyfwdmsg": true, "x_divrplyfwdmsg": true, "appendonsend": true, "mail-editor-reference-message-container": true, "olk_src_body_section": true}
	quoteClasses = map[string]bool{"gmail_quote": true, "gmail_quote_container": true, "moz-cite-prefix": true, "outlookmessageheader": true}
	quoteBorder  = regexp.MustCompile(`border-top:\s*solid\s+#(e1e1e1|b5c4df)`)
	originalMsg  = regexp.MustCompile(`(?i)-{3,}\s*original message\s*-{3,}`)
	// hidden matches inline styles that hide an element. Best effort: CSS
	// in <style> blocks and other tricks are not evaluated.
	hidden     = regexp.MustCompile(`display:\s*none|mso-hide:\s*all|visibility:\s*hidden`)
	spaceRun   = regexp.MustCompile(`[ \t\r\n\f\v\x{00a0}\x{200b}]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
	// separatorLine is a line of underscores or dashes that clients put
	// above a quoted header.
	separatorLine = regexp.MustCompile(`(?:^|\n)[_\-]{8,}\s*$`)
)

// Header labels of a quoted message written as text (English, German,
// French, Dutch, Italian, Spanish, Russian).
var (
	fromLabel = regexp.MustCompile(`^(?:From|Von|De|Van|Da|От):`)
	dateLabel = regexp.MustCompile(`^(?:Sent|Date|Gesendet|Datum|Envoyé|Verzonden|Inviato|Enviado|Отправлено|Дата):`)
	toLabel   = regexp.MustCompile(`^(?:To|Cc|Subject|An|Betreff|À|Objet|Aan|Onderwerp|A|Oggetto|Para|Asunto|Кому|Тема):`)
)

// ToText converts an HTML body to plain text. With cutQuote it stops at
// the first quote start, so only the new part of a reply or forward is
// kept. Hidden and non-visible elements give no text, and quote markers
// inside them are ignored.
func ToText(doc []byte, cutQuote bool) Text {
	root, err := html.Parse(bytes.NewReader(doc))
	if err != nil { // html.Parse fails only on reader errors
		return Text{}
	}
	w := walker{cut: cutQuote}
	w.node(root, false)
	return w.finish()
}

type walker struct {
	out    strings.Builder
	last   byte // last byte written, 0 at the start
	cut    bool
	marker string
}

func (w *walker) put(s string) {
	if s != "" {
		w.out.WriteString(s)
		w.last = s[len(s)-1]
	}
}

// node writes the text of n and its children; it returns false once a
// quote start was found.
func (w *walker) node(n *html.Node, pre bool) bool {
	switch n.Type {
	case html.TextNode:
		return w.text(n.Data, pre)
	case html.ElementNode:
		if skipped[n.DataAtom] || hasAttr(n, "hidden") || hidden.MatchString(strings.ToLower(attr(n, "style"))) {
			return true
		}
		if m := quoteStart(n); w.cut && m != "" {
			w.marker = m
			return false
		}
		w.open(n)
		pre = pre || n.DataAtom == atom.Pre
	case html.CommentNode, html.DoctypeNode:
		return true
	case html.ErrorNode, html.DocumentNode, html.RawNode:
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if !w.node(c, pre) {
			return false
		}
	}
	if n.Type == html.ElementNode && block[n.DataAtom] {
		w.put("\n")
	}
	return true
}

func (w *walker) open(n *html.Node) {
	switch a := n.DataAtom; {
	case a == atom.Br:
		w.put("\n")
	case a == atom.Li:
		w.put("\n- ")
	case a == atom.Td || a == atom.Th:
		if w.last != 0 && w.last != '\n' {
			w.put("\t") // between cells, not at the start of a row
		}
	case block[a]:
		w.put("\n")
	}
}

// text writes a text node. Outside <pre>, runs of white space become one
// space and a space at the start of a line is dropped.
func (w *walker) text(t string, pre bool) bool {
	stop := false
	if w.cut {
		if loc := originalMsg.FindStringIndex(t); loc != nil {
			t, stop, w.marker = t[:loc[0]], true, "original message"
		}
	}
	if !pre {
		t = spaceRun.ReplaceAllString(t, " ")
		if w.last == 0 || w.last == '\n' || w.last == '\t' || w.last == ' ' {
			t = strings.TrimLeft(t, " ")
		}
	}
	w.put(t)
	return !stop
}

// finish trims trailing white space on every line and collapses runs of
// blank lines. With cutting, it also cuts at the first quoted header
// written as text.
func (w *walker) finish() Text {
	lines := strings.Split(w.out.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r ")
	}
	if w.cut {
		if k := headerLine(lines); k >= 0 {
			lines, w.marker = lines[:k], "from/sent header"
		}
	}
	s := blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	if w.marker != "" {
		s = separatorLine.ReplaceAllString(strings.TrimRight(s, " \n"), "")
	}
	s = strings.Trim(s, "\n")
	return Text{Text: s, QuoteRemoved: w.marker != "", QuoteMarker: w.marker}
}

// headerLine returns the index of the first line that starts a quoted
// header written as text: a From line followed within six lines by a
// Sent or Date line and by a To, Cc or Subject line. It returns -1 when
// there is none.
func headerLine(lines []string) int {
	for i, l := range lines {
		if !fromLabel.MatchString(strings.TrimSpace(l)) {
			continue
		}
		var date, to bool
		for _, next := range lines[i+1 : min(len(lines), i+7)] {
			next = strings.TrimSpace(next)
			date = date || dateLabel.MatchString(next)
			to = to || toLabel.MatchString(next)
		}
		if date && to {
			return i
		}
	}
	return -1
}

// quoteStart names the marker when an element starts quoted or forwarded
// text, else returns "".
func quoteStart(n *html.Node) string {
	if id := strings.ToLower(attr(n, "id")); quoteIDs[id] {
		return "id=" + id
	}
	for _, c := range strings.Fields(strings.ToLower(attr(n, "class"))) {
		if quoteClasses[c] {
			return "class=" + c
		}
	}
	if n.DataAtom == atom.Blockquote && strings.EqualFold(attr(n, "type"), "cite") {
		return "blockquote cite"
	}
	if n.DataAtom == atom.Div && quoteBorder.MatchString(strings.ToLower(attr(n, "style"))) {
		return "border-top separator"
	}
	return ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}
