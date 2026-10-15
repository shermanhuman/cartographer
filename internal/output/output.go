// Package output renders query results as compact, ranked, token-budgeted
// text: the shape agents read cheaply.
package output

import (
	"fmt"
	"strings"
)

// CharsPerToken is the rough ratio used to turn a token budget into characters.
const CharsPerToken = 4

// DefaultBudget is the default token budget for one result.
const DefaultBudget = 2000

// Doc is a rendered result: header lines always print; body lines are
// assumed to be ranked most-relevant first and are cut at the budget.
type Doc struct {
	Header []string
	Body   []string
	// Footer prints after the body (and after any truncation note).
	Footer []string
}

func (d *Doc) H(format string, a ...any) { d.Header = append(d.Header, fmt.Sprintf(format, a...)) }
func (d *Doc) L(format string, a ...any) { d.Body = append(d.Body, fmt.Sprintf(format, a...)) }
func (d *Doc) F(format string, a ...any) { d.Footer = append(d.Footer, fmt.Sprintf(format, a...)) }

// Render joins the document, cutting body lines once budget tokens are used.
// budget <= 0 means DefaultBudget.
func (d *Doc) Render(budget int) string {
	if budget <= 0 {
		budget = DefaultBudget
	}
	limit := budget * CharsPerToken
	var b strings.Builder
	for _, h := range d.Header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	for _, f := range d.Footer {
		limit -= len(f) + 1
	}
	total := b.Len()
	for _, l := range d.Body {
		total += len(l) + 1
	}
	if total > limit {
		limit -= 80 // room for the truncation note
	}
	for i, l := range d.Body {
		if b.Len()+len(l)+1 > limit {
			fmt.Fprintf(&b, "…+%d more lines (raise budget or narrow the query)\n", len(d.Body)-i)
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	for _, f := range d.Footer {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	return b.String()
}
