package main

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"

	"github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/store"
)

// quoteMarkers are lower-case strings that mark quoted or forwarded text
// in HTML bodies of common mail clients.
var quoteMarkers = []string{
	`id="divrplyfwdmsg"`, `id="appendonsend"`, `id="mail-editor-reference-message-container"`, `class="gmail_quote`,
	`outlookmessageheader`, `<blockquote`, `-----original message-----`, `border-top:solid #e1e1e1`, `<hr`,
	`id="ms-outlook-mobile-signature"`, `id="signature"`, `x_divrplyfwdmsg`, `class="moz-cite-prefix"`,
	`id="olk_src_body_section"`, `>from:<`, `>from: <`, `<b>from:</b>`, `<b>from: </b>`,
}

// quoteCheck counts quote markers in the bodies of all messages, split by
// whether the message is a reply (it has a reply-to link). Counts only.
func quoteCheck(data []byte, profile string, w io.Writer) error {
	s, err := store.Load(data, "HxStore.hxd")
	if err != nil {
		return err
	}
	c := map[string]int{}
	for _, m := range s.Messages {
		ref, err := s.Body(m.ID)
		if err != nil {
			continue
		}
		html, _, err := body.HTML(ref, profile, 0)
		if err != nil {
			continue
		}
		cut, full := body.ToText(html, true), body.ToText(html, false)
		c[fmt.Sprintf("text: quote removed=%t", cut.QuoteRemoved)]++
		switch {
		case full.Text == "":
			c[fmt.Sprintf("text: empty even without cutting; raw text outside head/script/style=%s; class %s", rawTextSize(html), m.ItemClass)]++
		case cut.Text == "":
			c["text: empty after cutting by "+cut.QuoteMarker]++
		}
		if cut.QuoteRemoved {
			c[fmt.Sprintf("text: kept share of full text %s", share(len(cut.Text), len(full.Text)))]++
		}
		if leftHeader.MatchString(cut.Text) {
			var ms []string
			low := asciiLower(html)
			for _, mk := range quoteMarkers {
				if bytes.Contains(low, []byte(mk)) {
					ms = append(ms, mk)
				}
			}
			c[fmt.Sprintf("text: From/Sent header left (quote removed=%t, by %q) markers in html %v", cut.QuoteRemoved, cut.QuoteMarker, ms)]++
		}
		if cut.QuoteRemoved {
			c["text: cut by "+cut.QuoteMarker]++
		}
		if strings.HasPrefix(strings.ToLower(m.Subject), markerPrefix) {
			c[fmt.Sprintf("probe: text holds the marker=%t", strings.Contains(strings.ToLower(cut.Text), markerPrefix))]++
		}
		reply := m.ReplyToID != ""
		c[fmt.Sprintf("bodies, reply=%t", reply)]++
		low := asciiLower(html)
		any := false
		for _, mk := range quoteMarkers {
			if bytes.Contains(low, []byte(mk)) {
				c[fmt.Sprintf("reply=%t marker %s", reply, mk)]++
				any = true
			}
		}
		if !any {
			c[fmt.Sprintf("reply=%t no marker", reply)]++
		}
	}
	printCounts(w, "quote markers (counts only)", c)
	return nil
}

// leftHeader matches a quoted-message header block that cutting missed.
var leftHeader = regexp.MustCompile(`(?m)^(From|Von|De|От):.*\n(Sent|Gesendet|Envoy|Отправлено|Date):`)

func share(a, b int) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d0%%", min(9, a*10/b))
}

// rawTextSize counts non-space text outside head, script and style with a
// plain tokenizer, to tell empty bodies from bodies the converter hid.
func rawTextSize(doc []byte) string {
	z := xhtml.NewTokenizer(bytes.NewReader(doc))
	skip, n := 0, 0
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			switch {
			case n == 0:
				return "none"
			case n < 50:
				return "<50 chars"
			default:
				return ">=50 chars"
			}
		case xhtml.StartTagToken:
			name, _ := z.TagName()
			if s := string(name); s == "head" || s == "script" || s == "style" {
				skip++
			}
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			if s := string(name); (s == "head" || s == "script" || s == "style") && skip > 0 {
				skip--
			}
		case xhtml.SelfClosingTagToken, xhtml.CommentToken, xhtml.DoctypeToken:
			// no text
		case xhtml.TextToken:
			if skip == 0 {
				n += len(strings.TrimSpace(strings.ReplaceAll(string(z.Text()), "\u00a0", "")))
			}
		}
	}
}
