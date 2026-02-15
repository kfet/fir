// Ported from: packages/tui/src/components/markdown.ts
// Upstream hash: 1caadb2e
package components

import (
	"bytes"
	"strings"

	"github.com/kfet/tau/pkg/tui"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// DefaultTextStyle provides default styling for markdown text content.
type DefaultTextStyle struct {
	Color         func(string) string
	BgColor       func(string) string
	Bold          bool
	Italic        bool
	Strikethrough bool
	Underline     bool
}

// MarkdownTheme provides styling functions for markdown elements.
type MarkdownTheme struct {
	Heading         func(string) string
	Link            func(string) string
	LinkURL         func(string) string
	Code            func(string) string
	CodeBlock       func(string) string
	CodeBlockBorder func(string) string
	Quote           func(string) string
	QuoteBorder     func(string) string
	HR              func(string) string
	ListBullet      func(string) string
	Bold            func(string) string
	Italic          func(string) string
	Strikethrough   func(string) string
	Underline       func(string) string
	HighlightCode   func(code string, lang string) []string // optional
	CodeBlockIndent string                                   // default "  "
}

// Markdown renders markdown text as styled terminal output.
type Markdown struct {
	text             string
	paddingX         int
	paddingY         int
	defaultTextStyle *DefaultTextStyle
	theme            MarkdownTheme

	// Cache
	cachedText  string
	cachedWidth int
	cachedLines []string
	cacheValid  bool
}

var _ tui.Component = (*Markdown)(nil)

// NewMarkdown creates a Markdown component.
func NewMarkdown(text string, paddingX, paddingY int, theme MarkdownTheme, defaultTextStyle *DefaultTextStyle) *Markdown {
	return &Markdown{
		text:             text,
		paddingX:         paddingX,
		paddingY:         paddingY,
		theme:            theme,
		defaultTextStyle: defaultTextStyle,
	}
}

// SetText updates the markdown text.
func (m *Markdown) SetText(text string) {
	m.text = text
	m.cacheValid = false
}

// Invalidate clears cached rendering state.
func (m *Markdown) Invalidate() {
	m.cacheValid = false
}

// Render returns styled terminal lines for the given width.
func (m *Markdown) Render(width int) []string {
	if m.cacheValid && m.cachedText == m.text && m.cachedWidth == width {
		return m.cachedLines
	}

	contentWidth := width - m.paddingX*2
	if contentWidth < 1 {
		contentWidth = 1
	}

	if m.text == "" || strings.TrimSpace(m.text) == "" {
		result := []string{}
		m.cachedText = m.text
		m.cachedWidth = width
		m.cachedLines = result
		m.cacheValid = true
		return result
	}

	normalizedText := strings.ReplaceAll(m.text, "\t", "   ")

	// Parse markdown
	source := []byte(normalizedText)
	parser := goldmark.DefaultParser()
	doc := parser.Parse(text.NewReader(source))

	// Render tokens to lines
	renderedLines := m.renderNode(doc, source, contentWidth, "")

	// Wrap lines
	var wrappedLines []string
	for _, line := range renderedLines {
		if tui.IsImageLine(line) {
			wrappedLines = append(wrappedLines, line)
		} else {
			wrappedLines = append(wrappedLines, tui.WrapTextWithAnsi(line, contentWidth)...)
		}
	}

	// Add margins and background
	leftMargin := strings.Repeat(" ", m.paddingX)
	rightMargin := strings.Repeat(" ", m.paddingX)
	bgFn := m.getBgFn()
	var contentLines []string

	for _, line := range wrappedLines {
		if tui.IsImageLine(line) {
			contentLines = append(contentLines, line)
			continue
		}

		lineWithMargins := leftMargin + line + rightMargin

		if bgFn != nil {
			contentLines = append(contentLines, tui.ApplyBackgroundToLine(lineWithMargins, width, bgFn))
		} else {
			visibleLen := tui.VisibleWidth(lineWithMargins)
			pad := width - visibleLen
			if pad < 0 {
				pad = 0
			}
			contentLines = append(contentLines, lineWithMargins+strings.Repeat(" ", pad))
		}
	}

	// Add top/bottom padding
	emptyLine := strings.Repeat(" ", width)
	var emptyLines []string
	for i := 0; i < m.paddingY; i++ {
		if bgFn != nil {
			emptyLines = append(emptyLines, tui.ApplyBackgroundToLine(emptyLine, width, bgFn))
		} else {
			emptyLines = append(emptyLines, emptyLine)
		}
	}

	result := make([]string, 0, len(emptyLines)*2+len(contentLines))
	result = append(result, emptyLines...)
	result = append(result, contentLines...)
	result = append(result, emptyLines...)

	if len(result) == 0 {
		result = []string{""}
	}

	m.cachedText = m.text
	m.cachedWidth = width
	m.cachedLines = result
	m.cacheValid = true

	return result
}

func (m *Markdown) getBgFn() func(string) string {
	if m.defaultTextStyle != nil {
		return m.defaultTextStyle.BgColor
	}
	return nil
}

func (m *Markdown) applyDefaultStyle(s string) string {
	if m.defaultTextStyle == nil {
		return s
	}
	styled := s
	if m.defaultTextStyle.Color != nil {
		styled = m.defaultTextStyle.Color(styled)
	}
	if m.defaultTextStyle.Bold {
		styled = m.theme.Bold(styled)
	}
	if m.defaultTextStyle.Italic {
		styled = m.theme.Italic(styled)
	}
	if m.defaultTextStyle.Strikethrough {
		styled = m.theme.Strikethrough(styled)
	}
	if m.defaultTextStyle.Underline {
		styled = m.theme.Underline(styled)
	}
	return styled
}

// renderNode walks the AST and produces styled lines.
func (m *Markdown) renderNode(node ast.Node, source []byte, width int, nextType string) []string {
	var lines []string

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		nextSib := child.NextSibling()
		nextTypeName := ""
		if nextSib != nil {
			nextTypeName = nodeTypeName(nextSib)
		}

		childLines := m.renderSingleNode(child, source, width, nextTypeName)
		lines = append(lines, childLines...)
	}

	return lines
}

func nodeTypeName(n ast.Node) string {
	switch n.Kind() {
	case ast.KindHeading:
		return "heading"
	case ast.KindParagraph:
		return "paragraph"
	case ast.KindFencedCodeBlock, ast.KindCodeBlock:
		return "code"
	case ast.KindList:
		return "list"
	case ast.KindBlockquote:
		return "blockquote"
	case ast.KindThematicBreak:
		return "hr"
	case ast.KindHTMLBlock:
		return "html"
	default:
		return "unknown"
	}
}

func (m *Markdown) renderSingleNode(node ast.Node, source []byte, width int, nextType string) []string {
	var lines []string

	switch node.Kind() {
	case ast.KindHeading:
		heading := node.(*ast.Heading)
		headingText := m.renderInline(node, source)
		level := heading.Level
		var styled string
		switch level {
		case 1:
			styled = m.theme.Heading(m.theme.Bold(m.theme.Underline(headingText)))
		case 2:
			styled = m.theme.Heading(m.theme.Bold(headingText))
		default:
			prefix := strings.Repeat("#", level) + " "
			styled = m.theme.Heading(m.theme.Bold(prefix + headingText))
		}
		lines = append(lines, styled)
		if nextType != "" {
			lines = append(lines, "")
		}

	case ast.KindParagraph:
		paraText := m.renderInline(node, source)
		lines = append(lines, paraText)
		if nextType != "" && nextType != "list" {
			lines = append(lines, "")
		}

	case ast.KindFencedCodeBlock:
		cb := node.(*ast.FencedCodeBlock)
		lang := string(cb.Language(source))
		indent := m.theme.CodeBlockIndent
		if indent == "" {
			indent = "  "
		}
		lines = append(lines, m.theme.CodeBlockBorder("```"+lang))

		var codeBuf bytes.Buffer
		for i := 0; i < cb.Lines().Len(); i++ {
			line := cb.Lines().At(i)
			codeBuf.Write(line.Value(source))
		}
		codeText := strings.TrimRight(codeBuf.String(), "\n")

		if m.theme.HighlightCode != nil {
			hlLines := m.theme.HighlightCode(codeText, lang)
			for _, hl := range hlLines {
				lines = append(lines, indent+hl)
			}
		} else {
			for _, codeLine := range strings.Split(codeText, "\n") {
				lines = append(lines, indent+m.theme.CodeBlock(codeLine))
			}
		}
		lines = append(lines, m.theme.CodeBlockBorder("```"))
		if nextType != "" {
			lines = append(lines, "")
		}

	case ast.KindCodeBlock:
		// Indented code block
		indent := m.theme.CodeBlockIndent
		if indent == "" {
			indent = "  "
		}
		lines = append(lines, m.theme.CodeBlockBorder("```"))
		var codeBuf bytes.Buffer
		cb := node.(*ast.CodeBlock)
		for i := 0; i < cb.Lines().Len(); i++ {
			line := cb.Lines().At(i)
			codeBuf.Write(line.Value(source))
		}
		codeText := strings.TrimRight(codeBuf.String(), "\n")
		for _, codeLine := range strings.Split(codeText, "\n") {
			lines = append(lines, indent+m.theme.CodeBlock(codeLine))
		}
		lines = append(lines, m.theme.CodeBlockBorder("```"))
		if nextType != "" {
			lines = append(lines, "")
		}

	case ast.KindList:
		listNode := node.(*ast.List)
		lines = append(lines, m.renderList(listNode, source, 0)...)

	case ast.KindBlockquote:
		// Render blockquote content inline, then add border
		var quoteLines []string
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			quoteText := m.renderInline(child, source)
			quoteLines = append(quoteLines, quoteText)
		}

		quoteContentWidth := width - 2 // "│ " prefix
		if quoteContentWidth < 1 {
			quoteContentWidth = 1
		}
		for _, ql := range quoteLines {
			wrappedQuote := tui.WrapTextWithAnsi(m.theme.Quote(m.theme.Italic(ql)), quoteContentWidth)
			for _, wl := range wrappedQuote {
				lines = append(lines, m.theme.QuoteBorder("│ ")+wl)
			}
		}
		if nextType != "" {
			lines = append(lines, "")
		}

	case ast.KindThematicBreak:
		hrWidth := width
		if hrWidth > 80 {
			hrWidth = 80
		}
		lines = append(lines, m.theme.HR(strings.Repeat("─", hrWidth)))
		if nextType != "" {
			lines = append(lines, "")
		}

	case ast.KindHTMLBlock:
		raw := extractRawText(node, source)
		if raw != "" {
			lines = append(lines, m.applyDefaultStyle(strings.TrimSpace(raw)))
		}

	default:
		// Unknown block type - try raw text
		raw := extractRawText(node, source)
		if raw != "" {
			lines = append(lines, raw)
		}
	}

	return lines
}

// renderInline renders all inline children of a node to a styled string.
func (m *Markdown) renderInline(node ast.Node, source []byte) string {
	var buf strings.Builder
	m.renderInlineChildren(&buf, node, source)
	return buf.String()
}

func (m *Markdown) renderInlineChildren(buf *strings.Builder, node ast.Node, source []byte) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		m.renderInlineNode(buf, child, source)
	}
}

func (m *Markdown) renderInlineNode(buf *strings.Builder, node ast.Node, source []byte) {
	switch node.Kind() {
	case ast.KindText:
		t := node.(*ast.Text)
		buf.WriteString(m.applyDefaultStyle(string(t.Segment.Value(source))))
		if t.HardLineBreak() || t.SoftLineBreak() {
			buf.WriteString("\n")
		}

	case ast.KindString:
		buf.WriteString(m.applyDefaultStyle(string(node.Text(source))))

	case ast.KindEmphasis:
		em := node.(*ast.Emphasis)
		var inner strings.Builder
		m.renderInlineChildren(&inner, node, source)
		if em.Level == 2 {
			buf.WriteString(m.theme.Bold(inner.String()))
		} else {
			buf.WriteString(m.theme.Italic(inner.String()))
		}

	case ast.KindCodeSpan:
		var inner strings.Builder
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			inner.Write(child.Text(source))
		}
		buf.WriteString(m.theme.Code(inner.String()))

	case ast.KindLink:
		link := node.(*ast.Link)
		var inner strings.Builder
		m.renderInlineChildren(&inner, node, source)
		linkText := inner.String()
		href := string(link.Destination)

		// If link text matches href, show only once
		rawText := extractInlineText(node, source)
		hrefForComp := href
		if strings.HasPrefix(hrefForComp, "mailto:") {
			hrefForComp = hrefForComp[7:]
		}
		if rawText == href || rawText == hrefForComp {
			buf.WriteString(m.theme.Link(m.theme.Underline(linkText)))
		} else {
			buf.WriteString(m.theme.Link(m.theme.Underline(linkText)))
			buf.WriteString(m.theme.LinkURL(" (" + href + ")"))
		}

	case ast.KindAutoLink:
		al := node.(*ast.AutoLink)
		url := string(al.URL(source))
		buf.WriteString(m.theme.Link(m.theme.Underline(m.applyDefaultStyle(url))))

	case ast.KindImage:
		img := node.(*ast.Image)
		var inner strings.Builder
		m.renderInlineChildren(&inner, node, source)
		altText := inner.String()
		if altText == "" {
			altText = string(img.Destination)
		}
		buf.WriteString(m.theme.Link("[" + altText + "]"))

	case ast.KindRawHTML:
		raw := string(node.Text(source))
		buf.WriteString(m.applyDefaultStyle(raw))

	case ast.KindParagraph:
		m.renderInlineChildren(buf, node, source)

	default:
		// Fallback: try to render children or text
		if node.HasChildren() {
			m.renderInlineChildren(buf, node, source)
		} else {
			raw := string(node.Text(source))
			if raw != "" {
				buf.WriteString(m.applyDefaultStyle(raw))
			}
		}
	}
}

// renderList renders a list (ordered or unordered) with nesting.
func (m *Markdown) renderList(listNode *ast.List, source []byte, depth int) []string {
	var lines []string
	indent := strings.Repeat("  ", depth)
	itemIndex := 0
	start := listNode.Start
	if start == 0 {
		start = 1
	}

	for child := listNode.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Kind() != ast.KindListItem {
			continue
		}

		var bullet string
		if listNode.IsOrdered() {
			bullet = itoa(start+itemIndex) + ". "
		} else {
			bullet = "- "
		}

		itemLines := m.renderListItem(child, source, depth)

		if len(itemLines) > 0 {
			lines = append(lines, indent+m.theme.ListBullet(bullet)+itemLines[0])
			for _, rest := range itemLines[1:] {
				lines = append(lines, indent+"  "+rest)
			}
		} else {
			lines = append(lines, indent+m.theme.ListBullet(bullet))
		}

		itemIndex++
	}

	return lines
}

// renderListItem renders a single list item's children.
func (m *Markdown) renderListItem(node ast.Node, source []byte, parentDepth int) []string {
	var lines []string

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.Kind() {
		case ast.KindList:
			nestedList := child.(*ast.List)
			nestedLines := m.renderList(nestedList, source, parentDepth+1)
			lines = append(lines, nestedLines...)

		case ast.KindParagraph:
			paraText := m.renderInline(child, source)
			lines = append(lines, paraText)

		case ast.KindFencedCodeBlock, ast.KindCodeBlock:
			codeLines := m.renderSingleNode(child, source, 80, "")
			lines = append(lines, codeLines...)

		default:
			txt := m.renderInline(child, source)
			if txt != "" {
				lines = append(lines, txt)
			}
		}
	}

	return lines
}

// extractRawText extracts raw source text of a node.
func extractRawText(node ast.Node, source []byte) string {
	var buf bytes.Buffer
	for i := 0; i < node.Lines().Len(); i++ {
		line := node.Lines().At(i)
		buf.Write(line.Value(source))
	}
	return buf.String()
}

// extractInlineText extracts plain text from inline nodes (no styling).
func extractInlineText(node ast.Node, source []byte) string {
	var buf strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.Kind() {
		case ast.KindText:
			buf.Write(child.(*ast.Text).Segment.Value(source))
		case ast.KindString:
			buf.Write(child.Text(source))
		default:
			if child.HasChildren() {
				buf.WriteString(extractInlineText(child, source))
			}
		}
	}
	return buf.String()
}
