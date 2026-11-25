package body

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToText(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, html, want string }{
		{
			name: "structure and entities",
			html: `<html><head><title>T</title><style>p{color:red}</style></head><body>` +
				`<span style="display:none">preheader</span><p>Hello&nbsp;Bob &amp; team,</p>` +
				`<p>line one<br>line   two</p><ul><li>first</li><li>second</li></ul>` +
				`<table><tr><td>a</td><td>b</td></tr></table><script>x()</script></body></html>`,
			want: "Hello Bob & team,\n\nline one\nline two\n\n- first\n- second\n\na\tb",
		},
		{"missing head end tag", `<html><head><meta charset="utf-8"><body><p>Visible</p></body></html>`, "Visible"},
		{"nested hidden elements", `<div style="mso-hide:all"><div>hidden <div>deep</div></div></div><p>shown</p>`, "shown"},
		{"implied end of a hidden paragraph", `<p style="display:none">pre<p>Visible one</p><p>Visible two</p>`, "Visible one\n\nVisible two"},
		{"hidden cell without end tag", `<table><tr><td style="display:none">x<td>visible</td></tr></table><p>after</p>`, "visible\n\nafter"},
		{"hidden list item", `<ul><li style="display:none">a<li>b</ul>`, "- b"},
		{"hidden attribute and visibility", `<div hidden>a</div><span style="visibility:hidden">b</span><p>c</p>`, "c"},
		{"preformatted text", "<p>code:</p><pre>  if x {\n    y()\n  }</pre>", "code:\n\n  if x {\n    y()\n  }"},
		{"line breaks in source text", "<p>one\r\ntwo</p>", "one two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ToText([]byte(tt.html), true)
			assert.Equal(t, tt.want, got.Text)
			assert.False(t, got.QuoteRemoved)
		})
	}
}

func TestToTextCutsQuotes(t *testing.T) {
	t.Parallel()
	const reply = "<p>New answer</p>"
	quotes := map[string]string{
		"outlook on the web": `<div id="appendonsend"></div><hr style="display:inline-block"><div id="divRplyFwdMsg"><b>From:</b> Alice</div><p>old</p>`,
		"classic outlook":    `<div style="border:none;border-top:solid #E1E1E1 1.0pt;padding:3.0pt 0in 0in 0in"><p><b>From:</b> Alice</p></div><p>old</p>`,
		"new outlook":        `<div id="mail-editor-reference-message-container"><p>old</p></div>`,
		"gmail":              `<div class="gmail_quote gmail_quote_container"><div>On Monday Alice wrote:</div><blockquote>old</blockquote></div>`,
		"apple mail":         `<blockquote type="cite"><p>old</p></blockquote>`,
		"original message":   "<p>-----Original Message-----<br>From: Alice</p><p>old</p>",
		"header as text":     `<hr><div><b>From:</b> Alice &lt;a@example.com&gt;<br><b>Sent:</b> Monday<br><b>To:</b> Bob</div><p>old</p>`,
		"underscore line":    `<p>________________________________</p><p>From: Alice<br>Date: Monday<br>Subject: Plan</p><p>old</p>`,
	}
	for name, q := range quotes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := "<html><body>" + reply + q + "</body></html>"
			got := ToText([]byte(doc), true)
			assert.Equal(t, "New answer", got.Text)
			assert.True(t, got.QuoteRemoved)
			assert.NotEmpty(t, got.QuoteMarker)

			full := ToText([]byte(doc), false)
			assert.Contains(t, full.Text, "old")
			assert.False(t, full.QuoteRemoved)
		})
	}
}

func TestToTextQuoteEdgeCases(t *testing.T) {
	t.Parallel()
	got := ToText([]byte(`<div style="display:none"><div id="appendonsend"></div><div class="gmail_quote">x</div></div><p>visible</p>`), true)
	assert.Equal(t, Text{Text: "visible"}, got, "markers inside hidden parts do not cut")

	got = ToText([]byte(`<p>________________________________</p><p>From: Alice<br>Sent: Monday<br>To: Bob</p><p>old</p>`), true)
	assert.Equal(t, Text{Text: "", QuoteRemoved: true, QuoteMarker: "from/sent header"}, got, "a forward without added text")

	got = ToText([]byte(`<p>Trip details</p><p>From: Paris<br>Date: Monday</p><p>Bring passport</p>`), true)
	assert.Equal(t, "Trip details\n\nFrom: Paris\nDate: Monday\n\nBring passport", got.Text, "From and Date without To or Subject are text")

	got = ToText([]byte(`<div class="gmail_quote_x">kept</div>`), true)
	assert.Equal(t, "kept", got.Text, "classes match whole words")
}

func TestToTextKeepsPlainBlockquoteAndRule(t *testing.T) {
	t.Parallel()
	got := ToText([]byte(`<p>Intro</p><hr><blockquote>indented</blockquote><p>From: the sales team</p><p>end</p>`), true)
	assert.Equal(t, "Intro\n\nindented\n\nFrom: the sales team\n\nend", got.Text)
	assert.False(t, got.QuoteRemoved)
}

func FuzzToText(f *testing.F) {
	f.Add([]byte(`<div id="divRplyFwdMsg"><p>x</p></div>`))
	f.Add([]byte(`<span style="display:none"><span>a</span></span>b`))
	f.Add([]byte("<pre>a\n b</pre><p>From: x<br>Sent: y<br>To: z</p>"))
	f.Fuzz(func(_ *testing.T, b []byte) {
		ToText(b, true)
		ToText(b, false)
	})
}
