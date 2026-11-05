// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strconv"
	"strings"
)

// Card is one Argus email's HTML: a coloured header with the title, the body rows, the buttons and
// a footer. Every Argus mail is one (alerts, notices, the password reset, the reports), so they
// look alike everywhere, Outlook for Windows included.
//
// Outlook for Windows draws mail with Word, which ignores max-width, padding and margins on a div,
// padding on a link, rounded corners and rgba colours, and falls back to Times without a font on
// every cell. So the layout is nested tables only: the width is fixed for Word by a conditional
// 560-pixel table and capped by max-width for the rest, colours are solid and given as bgcolor too,
// spacing is cell padding, a button is a coloured cell (mso-padding-alt gives Word its padding),
// and each cell names its font. Dark mode: Apple Mail, iOS and Outlook on phones honour
// prefers-color-scheme through the classes; Outlook for Windows applies its own.
type Card struct {
	Color   string   // the header's colour, #rrggbb
	Title   string   // the header's text (plain)
	Body    []string // body rows, from CardText / CardMuted / CardCallout / cardDetails / cardImage
	Buttons []Button // under the body, side by side
	Foot    string   // the footer's HTML ("" = "Sent by Argus")
}

// Button is a link drawn as a button.
type Button struct{ Label, Href, Color string }

const (
	cardFont  = `'Segoe UI',-apple-system,Roboto,Helvetica,Arial,sans-serif`
	cardWidth = 560
	// AccentColor is the Argus accent: a notice, a link, a button.
	AccentColor = "#2ea8c9"
)

// CardText is a body paragraph (its HTML is the caller's, escaped already).
func CardText(html string) string {
	return `<tr><td class="txt" style="padding:0 0 12px;font-family:` + cardFont + `;font-size:14px;line-height:20px;color:#111827">` + html + `</td></tr>`
}

// CardMuted is a quieter, smaller paragraph.
func CardMuted(html string) string {
	return `<tr><td class="mut" style="padding:0 0 12px;font-family:` + cardFont + `;font-size:13px;line-height:19px;color:#6b7280">` + html + `</td></tr>`
}

// CardCallout is a paragraph set off by an accent bar (a sensor's note, its runbook).
func CardCallout(html string) string {
	return `<tr><td style="padding:0 0 12px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>` +
		`<td class="call txt" bgcolor="#eaf6fa" style="background:#eaf6fa;border-left:3px solid ` + AccentColor + `;padding:8px 10px;font-family:` + cardFont + `;font-size:13px;line-height:19px;color:#111827">` + html + `</td>` +
		`</tr></table></td></tr>`
}

// cardDetail is one line of the details table: its label, its value and, for the severity, a colour.
type cardDetail struct{ label, value, color string }

// cardDetails is the label / value table under the paragraphs.
func cardDetails(rows []cardDetail) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<tr><td style="padding:0 0 4px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">`)
	for _, r := range rows {
		vs, cls := "color:#111827", "txt"
		if r.color != "" {
			vs, cls = "color:"+r.color+";font-weight:bold", ""
		}
		b.WriteString(`<tr><td class="mut" width="110" valign="top" style="width:110px;padding:4px 0;font-family:` + cardFont + `;font-size:13px;line-height:19px;color:#6b7280">` + htmlEscape(r.label) + `</td>` +
			`<td class="` + cls + `" valign="top" style="padding:4px 0;font-family:` + cardFont + `;font-size:13px;line-height:19px;` + vs + `">` + htmlEscape(r.value) + `</td></tr>`)
	}
	b.WriteString(`</table></td></tr>`)
	return b.String()
}

// cardImage is an inline image the width of the body (cid: reference).
func cardImage(src, alt string) string {
	w := cardWidth - 36 - 2 // the body's padding, the image's border
	return `<tr><td style="padding:10px 0 4px"><img class="chart" src="` + htmlEscape(src) + `" alt="` + htmlEscape(alt) + `" width="` + strconv.Itoa(w) + `" ` +
		`style="display:block;width:100%;max-width:` + strconv.Itoa(w) + `px;height:auto;border:1px solid #e5e7eb;border-radius:8px"></td></tr>`
}

// HTML renders the card as a whole document.
func (c Card) HTML() string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office"><head>` +
		`<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark">` +
		`<!--[if mso]><noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml></noscript>` +
		`<style>table,td,a,span{font-family:'Segoe UI',Arial,sans-serif !important}</style><![endif]-->` +
		`<style>@media (prefers-color-scheme: dark){body,.bg{background:#0e1218 !important}.card{background:#151b23 !important;border-color:#262f3b !important}` +
		`.txt{color:#eef2f8 !important}.mut{color:#8b98a8 !important}.call{background:#13262e !important}.foot,.chart{border-color:#262f3b !important}}</style></head>`)
	b.WriteString(`<body class="bg" bgcolor="#f3f4f6" style="margin:0;padding:0;background:#f3f4f6">`)
	b.WriteString(`<table role="presentation" class="bg" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#f3f4f6" style="background:#f3f4f6"><tr><td align="center" style="padding:16px 8px">`)
	b.WriteString(`<!--[if mso]><table role="presentation" width="` + strconv.Itoa(cardWidth) + `" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`)
	b.WriteString(`<table role="presentation" class="card" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#ffffff" ` +
		`style="max-width:` + strconv.Itoa(cardWidth) + `px;background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;border-collapse:separate">`)
	b.WriteString(`<tr><td bgcolor="` + c.Color + `" style="background:` + c.Color + `;padding:14px 18px;border-radius:11px 11px 0 0;font-family:` + cardFont +
		`;font-size:16px;line-height:22px;font-weight:bold;color:#ffffff">` + htmlEscape(c.Title) + `</td></tr>`)
	if len(c.Body) > 0 {
		b.WriteString(`<tr><td style="padding:16px 18px 4px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">`)
		for _, r := range c.Body {
			b.WriteString(r)
		}
		b.WriteString(`</table></td></tr>`)
	}
	if len(c.Buttons) > 0 {
		b.WriteString(`<tr><td style="padding:8px 18px 16px"><table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>`)
		for i, bt := range c.Buttons {
			if i > 0 {
				b.WriteString(`<td width="8" style="width:8px;font-size:0;line-height:0">&nbsp;</td>`)
			}
			b.WriteString(`<td bgcolor="` + bt.Color + `" style="background:` + bt.Color + `;border-radius:7px;mso-padding-alt:9px 16px">` +
				`<a href="` + htmlEscape(bt.Href) + `" style="display:inline-block;padding:9px 16px;font-family:` + cardFont +
				`;font-size:13px;line-height:16px;font-weight:bold;color:#ffffff;text-decoration:none;border-radius:7px">` + htmlEscape(bt.Label) + `</a></td>`)
		}
		b.WriteString(`</tr></table></td></tr>`)
	}
	foot := c.Foot
	if foot == "" {
		foot = "Sent by Argus"
	}
	b.WriteString(`<tr><td class="foot mut" style="padding:10px 18px;border-top:1px solid #e5e7eb;font-family:` + cardFont + `;font-size:11px;line-height:16px;color:#6b7280">` + foot + `</td></tr>`)
	b.WriteString(`</table><!--[if mso]></td></tr></table><![endif]--></td></tr></table></body></html>`)
	return b.String()
}
