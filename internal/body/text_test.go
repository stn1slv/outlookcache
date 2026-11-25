package body

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, html, want string
		cut              bool
	}{
		{
			name: "structure and entities",
			html: `<html><head><title>T</title><style>p{color:red}</style></head><body>` +
				`<span style="display:none">preheader</span><p>Hello&nbsp;Bob &amp; team,</p>` +
				`<p>line one<br>line   two</p><ul><li>first</li><li>second</li></ul>` +
				`<table><tr><td>a</td><td>b</td></tr></table><script>x()</script></body></html>`,
			want: "Hello Bob & team,\n\nline one\nline two\n\n- first\n- second\n\na b",
		},
		{
			name: "missing head end tag",
			html: `<html><head><meta charset="utf-8"><body><p>Visible</p></body></html>`,
			want: "Visible",
		},
		{
			name: "nested hidden elements",
			html: `<div style="mso-hide:all"><div>hidden <div>deep</div></div></div><p>shown</p>`,
			want: "shown",
		},
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
		"gmail":              `<div class="gmail_quote"><div>On Monday Alice wrote:</div><blockquote>old</blockquote></div>`,
		"apple mail":         `<blockquote type="cite"><p>old</p></blockquote>`,
		"original message":   `<p>-----Original Message-----<br>From: Alice</p><p>old</p>`,
		"header as text":     `<hr><div><b>From:</b> Alice &lt;a@example.com&gt;<br><b>Sent:</b> Monday<br><b>To:</b> Bob</div><p>old</p>`,
		"underscore line":    `<p>________________________________</p><p>From: Alice<br>Date: Monday</p><p>old</p>`,
	}
	for name, q := range quotes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := "<html><body>" + reply + q + "</body></html>"
			got := ToText([]byte(doc), true)
			assert.Equal(t, "New answer", got.Text)
			assert.True(t, got.QuoteRemoved)

			full := ToText([]byte(doc), false)
			assert.Contains(t, full.Text, "old")
			assert.False(t, full.QuoteRemoved)
		})
	}
}

func TestToTextKeepsPlainBlockquoteAndRule(t *testing.T) {
	t.Parallel()
	got := ToText([]byte(`<p>Intro</p><hr><blockquote>indented</blockquote><p>From: the sales team</p><p>end</p>`), true)
	assert.Equal(t, "Intro\n\nindented\n\nFrom: the sales team\n\nend", got.Text, "a From line without a Sent line is text")
	assert.False(t, got.QuoteRemoved)
}

func FuzzToText(f *testing.F) {
	f.Add([]byte(`<div id="divRplyFwdMsg"><p>x</p></div>`))
	f.Add([]byte(`<span style="display:none"><span>a</span></span>b`))
	f.Fuzz(func(_ *testing.T, b []byte) {
		ToText(b, true)
		ToText(b, false)
	})
}
