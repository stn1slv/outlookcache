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
	// QuoteRemoved is true when quoted or forwarded text was cut off.
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

// void elements have no end tag.
var void = map[atom.Atom]bool{
	atom.Br: true, atom.Hr: true, atom.Img: true, atom.Meta: true, atom.Link: true, atom.Input: true,
	atom.Col: true, atom.Area: true, atom.Base: true, atom.Wbr: true, atom.Source: true, atom.Embed: true,
}

// Quote starts written by common mail clients (verified counts in
// docs/STORAGE.md): classic Outlook, Outlook on the web, new Outlook and
// Outlook for Mac, Gmail, Thunderbird and Apple Mail.
var (
	quoteIDs     = map[string]bool{"divrplyfwdmsg": true, "x_divrplyfwdmsg": true, "appendonsend": true, "mail-editor-reference-message-container": true, "olk_src_body_section": true}
	quoteClasses = []string{"gmail_quote", "moz-cite-prefix", "outlookmessageheader"}
	quoteBorder  = regexp.MustCompile(`border-top:\s*solid\s+#(e1e1e1|b5c4df)`)
	originalMsg  = regexp.MustCompile(`(?i)-{3,}\s*original message\s*-{3,}`)
	hidden       = regexp.MustCompile(`display:\s*none|mso-hide:\s*all`)
	spaces       = regexp.MustCompile(`[ \t\f\v\x{00a0}\x{200b}]+`)
	blankLines   = regexp.MustCompile(`\n{3,}`)
	// headerBlock is the header of a quoted message written as text: a
	// From line followed within two lines by a Sent or Date line (English,
	// German, French, Dutch, Italian, Spanish, Russian).
	headerBlock = regexp.MustCompile(`(?m)^(?:From|Von|De|Van|Da|От):[^\n]*\n(?:[^\n]*\n){0,2}?(?:Sent|Date|Gesendet|Datum|Envoyé|Verzonden|Inviato|Enviado|Отправлено|Дата):`)
	// separatorLine is a line of underscores or dashes that clients put
	// above a quoted header.
	separatorLine = regexp.MustCompile(`\n[_\-]{8,}\s*$`)
)

// ToText converts an HTML body to plain text. With cutQuote it stops at
// the first quote start, so only the new part of a reply or forward is
// kept.
func ToText(doc []byte, cutQuote bool) Text {
	z := html.NewTokenizer(bytes.NewReader(doc))
	var out strings.Builder
	var stack []atom.Atom // open elements that hide their content
	pre := 0
	newline := func() { out.WriteByte('\n') }
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return finish(out.String(), "", cutQuote)
		case html.TextToken:
			if len(stack) > 0 {
				continue
			}
			t := string(z.Text())
			if cutQuote {
				if loc := originalMsg.FindStringIndex(t); loc != nil {
					out.WriteString(t[:loc[0]])
					return finish(out.String(), "original message", cutQuote)
				}
			}
			if pre == 0 {
				t = strings.ReplaceAll(t, "\n", " ")
				t = strings.ReplaceAll(t, "\r", " ")
			}
			out.WriteString(t)
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			a := tok.DataAtom
			if m := quoteStart(tok); cutQuote && m != "" {
				return finish(out.String(), m, cutQuote)
			}
			if a == atom.Body {
				stack = stack[:0] // a <head> without an end tag hides nothing in the body
			}
			hide := skipped[a] || hidden.MatchString(strings.ToLower(attr(tok, "style")))
			if hide && !void[a] && tt == html.StartTagToken {
				stack = append(stack, a)
				continue
			}
			if len(stack) > 0 {
				if !void[a] && tt == html.StartTagToken && a == stack[len(stack)-1] {
					stack = append(stack, a) // nested element of the same kind
				}
				continue
			}
			switch {
			case a == atom.Br:
				newline()
			case a == atom.Li:
				out.WriteString("\n- ")
			case a == atom.Td || a == atom.Th:
				out.WriteByte('\t')
			case block[a]:
				newline()
			}
			if a == atom.Pre && tt == html.StartTagToken {
				pre++
			}
		case html.CommentToken, html.DoctypeToken:
			// no text
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if len(stack) > 0 {
				if a == stack[len(stack)-1] {
					stack = stack[:len(stack)-1]
				}
				continue
			}
			if a == atom.Pre && pre > 0 {
				pre--
			}
			if block[a] {
				newline()
			}
		}
	}
}

// quoteStart names the marker when an element starts quoted or forwarded
// text, else returns "".
func quoteStart(tok html.Token) string {
	if id := strings.ToLower(attr(tok, "id")); quoteIDs[id] {
		return "id=" + id
	}
	class := strings.ToLower(attr(tok, "class"))
	for _, c := range quoteClasses {
		if strings.Contains(class, c) {
			return "class=" + c
		}
	}
	if tok.DataAtom == atom.Blockquote && strings.EqualFold(attr(tok, "type"), "cite") {
		return "blockquote cite"
	}
	if tok.DataAtom == atom.Div && quoteBorder.MatchString(strings.ToLower(attr(tok, "style"))) {
		return "border-top separator"
	}
	return ""
}

func attr(tok html.Token, key string) string {
	for _, a := range tok.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// finish trims every line, collapses runs of spaces and of blank lines.
// With cut, it also cuts at the first quoted header written as text.
func finish(s, marker string, cut bool) Text {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
	}
	s = blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	if cut {
		if loc := headerBlock.FindStringIndex(s); loc != nil {
			s = separatorLine.ReplaceAllString(strings.TrimSpace(s[:loc[0]]), "")
			if marker == "" {
				marker = "from/sent header"
			}
		}
	}
	return Text{Text: strings.TrimSpace(s), QuoteRemoved: marker != "", QuoteMarker: marker}
}
