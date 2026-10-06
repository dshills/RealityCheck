// Package mdparse provides shared Markdown segmentation primitives used by the
// spec and plan parsers.
package mdparse

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Item is a discrete segment extracted from a Markdown document.
type Item struct {
	// ID is set only for normative items (e.g. "SPEC-001"), unless the
	// segmenter numbers all items. Informational items have an empty ID.
	ID        string
	LineStart int
	LineEnd   int
	Text      string
	// Section is the nearest preceding heading: an ATX heading, an isolated
	// numbered heading such as "6. Flags", or a short title-case line.
	Section string
	// Normative is true for requirements and plan steps that must be
	// accounted for in coverage. Informational items (prose, intros, code
	// examples) are context only.
	Normative bool
}

// RequiredItems returns the items that carry an ID, in document order.
// These are the items coverage must account for.
func RequiredItems(items []Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.ID != "" {
			out = append(out, it)
		}
	}
	return out
}

// IsNumberedItemFn determines whether a line starts a new numbered item.
type IsNumberedItemFn func(line string) bool

// Segmenter segments a Markdown file into discrete items.
type Segmenter struct {
	IDPrefix       string           // e.g., "SPEC" or "PLAN"
	IsNumberedItem IsNumberedItemFn // defaults to DefaultIsNumberedItem if nil
	// StripPrefix, if set, is called to strip the item prefix from a line before
	// storing it as item text. Falls back to StripListPrefix if nil.
	StripPrefix func(line string) string
	// AllItems numbers every item and marks it normative, restoring the
	// pre-classification behaviour. Heading-like lines and separators are
	// still not items.
	AllItems bool
}

// ParseFile reads the file at path and segments it using s.
func (s Segmenter) ParseFile(path string) ([]Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mdparse: open %s: %w", path, err)
	}
	// Read-only file: a Close error cannot lose data and is not actionable.
	defer func() { _ = f.Close() }()
	return s.ParseReader(f)
}

// ParseReader reads from r and segments it using s.
// This enables testing without requiring files on disk.
func (s Segmenter) ParseReader(r io.Reader) ([]Item, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	// Increase buffer to handle long lines (e.g. base64 content in code blocks).
	// Start with 64KB initial buffer; allow up to 1MB for long lines
	// (e.g., base64-encoded content in code blocks).
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("mdparse: scan: %w", err)
	}

	isNum := s.IsNumberedItem
	if isNum == nil {
		isNum = DefaultIsNumberedItem
	}
	strip := s.StripPrefix
	if strip == nil {
		strip = StripListPrefix
	}
	return segment(lines, s.IDPrefix, isNum, strip, s.AllItems), nil
}

// fencePrefix returns the opening fence string (e.g. "```" or "~~~~") if line
// starts a fenced code block, otherwise returns "".
// CommonMark allows up to 3 leading spaces before the fence marker.
// Lines with 4 or more leading spaces are indented code blocks, not fences.
// The info string (e.g. "go" in "```go") is intentionally not validated or
// stripped; callers only need the fence marker prefix.
//
// For closing fence detection callers should use isClosingFence, which
// additionally verifies that no non-space characters follow the fence markers
// (per CommonMark: a closing fence must have only optional trailing spaces).
func fencePrefix(line string) string {
	// Count leading spaces in the original line.
	leading := 0
	for leading < len(line) && line[leading] == ' ' {
		leading++
	}
	if leading >= 4 {
		return "" // indented code block, not a fence
	}
	stripped := line[leading:]
	for _, marker := range []byte{'`', '~'} {
		if len(stripped) < 3 || stripped[0] != marker {
			continue
		}
		count := 0
		for count < len(stripped) && stripped[count] == marker {
			count++
		}
		if count >= 3 {
			return stripped[:count]
		}
	}
	return ""
}

// isClosingFence returns true if line is a valid closing fence for openFence.
// A closing fence must use the same fence character, be at least as long as
// the opening fence, and have only optional trailing spaces after the markers.
//
// Safety note: fencePrefix returns `stripped[:count]` where stripped = line[leading:],
// so len(fp) == count and leading+count <= len(line) always holds. The slice
// `line[leading+len(fp):]` is therefore always in bounds.
// `leading` is re-derived here (rather than returned by fencePrefix) to keep
// fencePrefix's interface minimal; both derivations use the same counting logic.
func isClosingFence(line, openFence string) bool {
	if len(openFence) == 0 {
		return false
	}
	fp := fencePrefix(line)
	if fp == "" || fp[0] != openFence[0] || len(fp) < len(openFence) {
		return false
	}
	// Count leading spaces (same logic as fencePrefix, 0–3 at most).
	leading := 0
	for leading < len(line) && line[leading] == ' ' {
		leading++
	}
	rest := strings.TrimLeft(line[leading+len(fp):], " ")
	return rest == ""
}

// collectContinuation collects indented continuation lines (and their fenced code
// blocks) starting at lines[i]. addLn is called for each accepted line.
// Returns the updated index into lines.
//
// Design decisions:
//   - Only indented lines are accepted as continuation; lazy (non-indented)
//     continuations are intentionally not supported — they become separate items.
//   - A blank line terminates continuation (fence-free context only). Because the
//     blank-line check runs after the fence check, blank lines inside a fenced
//     code block are treated as code content and do not terminate the item.
//   - A fence opener is only accepted as continuation when it is indented, to
//     avoid silently merging document-level code blocks into the preceding list
//     item. Once the fence is open, all subsequent lines (including blank lines)
//     are code-block content.
//   - Lines inside a fenced code block are appended verbatim (not TrimSpace'd)
//     to preserve code block content. Non-fence continuation lines are TrimSpace'd.
//     This asymmetry is intentional.
//   - An unclosed innerFence at the end of the continuation range is silently
//     discarded; the caller's outer fence state (openFence) is NOT affected.
func collectContinuation(lines []string, i int, addLn func(lineNum int, text string)) int {
	var innerFence string
	for i < len(lines) {
		next := lines[i]
		nextNum := i + 1
		nfp := fencePrefix(next)
		if innerFence != "" {
			// Inside a code block: blank lines are content, not terminators.
			if isClosingFence(next, innerFence) {
				addLn(nextNum, next)
				innerFence = ""
			} else {
				addLn(nextNum, next)
			}
			i++
			continue
		}
		// Accept a fence opener only when indented, so document-level code
		// blocks are not inadvertently merged into the preceding list item.
		if nfp != "" && IsIndented(next) {
			innerFence = nfp
			addLn(nextNum, next)
			i++
			continue
		}
		// A blank line terminates continuation (fence-free context only).
		if strings.TrimSpace(next) == "" {
			break
		}
		if IsIndented(next) {
			addLn(nextNum, strings.TrimSpace(next))
			i++
		} else {
			break
		}
	}
	return i
}

// itemKind records how an item started, for classification.
type itemKind int

const (
	kindParagraph itemKind = iota
	kindList               // numbered, bullet, or plan step item
	kindCode               // started with a fenced code block
)

func segment(lines []string, prefix string, isNum IsNumberedItemFn, strip func(string) string, allItems bool) []Item {
	var items []Item
	section := ""
	// lastKind is the kind of the most recent item, so an indented paragraph
	// after a list item reads as its continuation rather than as code.
	lastKind := kindParagraph

	type pending struct {
		lineStart int
		lineEnd   int // last consumed line (1-indexed), updated as lines are added
		buf       []string
		kind      itemKind
		bullets   bool // contains merged indented bullet lines
	}

	addLine := func(p *pending, lineNum int, text string) {
		p.buf = append(p.buf, text)
		if lineNum > p.lineEnd {
			p.lineEnd = lineNum
		}
	}

	flush := func(p *pending) {
		if p == nil {
			return
		}
		text := strings.TrimSpace(strings.Join(p.buf, "\n"))
		if text == "" {
			return
		}
		// A lone short title-case line is a heading in documents exported
		// without Markdown heading syntax ("Purpose", "Phase 1 — Foundation").
		if p.kind == kindParagraph && len(p.buf) == 1 && !p.bullets && looksLikeHeading(text) {
			section = text
			lastKind = kindParagraph
			return
		}
		lastKind = p.kind
		items = append(items, Item{
			LineStart: p.lineStart,
			LineEnd:   p.lineEnd,
			Text:      text,
			Section:   section,
			Normative: isNormative(text, strings.Join(p.buf, "\n"), p.kind, p.bullets, section),
		})
	}

	var cur *pending
	// openFence is non-empty when inside a top-level fenced code block.
	// The openFence block at the top of the loop uses `continue`, so the
	// heading/blank-line/list handlers below only execute when openFence == "".
	var openFence string
	i := 0

	for i < len(lines) {
		line := lines[i]
		lineNum := i + 1 // 1-indexed

		// Fenced code block handling — must come first so that fence content
		// is consumed before any structural checks (heading, blank, list).
		fp := fencePrefix(line)
		if openFence != "" {
			// Inside a code block — look for a matching closing fence.
			if isClosingFence(line, openFence) {
				if cur != nil {
					addLine(cur, lineNum, line)
					// A standalone code block ends at its closing fence, so
					// prose that follows without a blank line is its own
					// item. Fences inside a paragraph stay part of it.
					if cur.kind == kindCode {
						flush(cur)
						cur = nil
					}
				}
				openFence = ""
			} else {
				if cur == nil {
					cur = &pending{lineStart: lineNum, lineEnd: lineNum}
				}
				addLine(cur, lineNum, line)
			}
			i++
			continue
		}
		if fp != "" {
			// Opening fence.
			if cur == nil {
				cur = &pending{lineStart: lineNum, lineEnd: lineNum, kind: kindCode}
			}
			openFence = fp
			addLine(cur, lineNum, line)
			i++
			continue
		}

		// Any-level ATX heading — flush current item; heading itself is not an item.
		// Limitation: setext-style headings (text underlined with --- or ===) are
		// not supported. The underline is treated as a decorator and flushed, while
		// the preceding text line becomes a standalone item.
		if IsHeading(line) {
			if cur != nil {
				flush(cur)
				cur = nil
			}
			section = headingText(line)
			lastKind = kindParagraph
			i++
			continue
		}

		// Blank line — flush current item.
		if strings.TrimSpace(line) == "" {
			if cur != nil {
				flush(cur)
				cur = nil
			}
			i++
			continue
		}

		// Numbered item (standard "1. " / "1) " or caller-defined), not indented.
		// isNum is always guarded by !IsIndented(line), so callers need not account
		// for indentation in their IsNumberedItemFn implementations.
		if isNum(line) && !IsIndented(line) {
			if cur != nil {
				flush(cur)
				cur = nil
			}
			// An isolated numbered line that reads like a title ("6. Flags")
			// is a section heading in exported documents, not a list item.
			if isNumberedHeading(lines, i, isNum, strip) {
				section = strip(line)
				lastKind = kindParagraph
				i++
				continue
			}
			cur = &pending{lineStart: lineNum, lineEnd: lineNum, kind: kindList}
			addLine(cur, lineNum, strip(line))
			i++ // advance past the current item line
			// collectContinuation is synchronous; cur is not reassigned until
			// after the call returns, so the closure captures the right pointer.
			i = collectContinuation(lines, i, func(n int, s string) { addLine(cur, n, s) })
			// Flush explicitly; do not rely on the outer blank-line handler.
			// An unclosed innerFence means malformed input; we do NOT propagate
			// it to openFence because doing so would incorrectly consume subsequent
			// non-fenced content as code-block lines. This is a known limitation.
			flush(cur)
			cur = nil
			continue
		}

		// Top-level bullet (not indented).
		if IsBullet(line) && !IsIndented(line) {
			if cur != nil {
				flush(cur)
			}
			cur = &pending{lineStart: lineNum, lineEnd: lineNum, kind: kindList}
			addLine(cur, lineNum, strip(line))
			i++ // advance past the current bullet line
			// collectContinuation is synchronous; see numbered-item comment above.
			i = collectContinuation(lines, i, func(n int, s string) { addLine(cur, n, s) })
			// Same unclosed-fence policy as numbered items.
			flush(cur)
			cur = nil
			continue
		}

		// Indented bullet — merge into current item.
		// If cur == nil (e.g., indented bullet at start of section), start a new item.
		if IsIndented(line) && IsBullet(strings.TrimSpace(line)) {
			if cur == nil {
				cur = &pending{lineStart: lineNum, lineEnd: lineNum, kind: kindList}
			}
			cur.bullets = true
			addLine(cur, lineNum, strings.TrimSpace(line))
			i++
			continue
		}

		// Horizontal rule / decorator — flush.
		if IsDecorator(line) {
			if cur != nil {
				flush(cur)
				cur = nil
			}
			lastKind = kindParagraph
			i++
			continue
		}

		// Paragraph / continuation. Note: paragraph lines are appended verbatim
		// (preserving indentation), while list-item continuation lines are
		// TrimSpace'd by collectContinuation. This asymmetry is intentional.
		// An indented code block ends at the first line that is not
		// indented enough to continue it.
		if cur != nil && cur.kind == kindCode && !isCodeIndent(line) {
			flush(cur)
			cur = nil
		}
		if cur == nil {
			cur = &pending{lineStart: lineNum, lineEnd: lineNum}
			// An indented code block (four spaces or a tab) cannot interrupt
			// a paragraph, so it only starts here. Indented numbered lists
			// from exported documents are not code, and an indented
			// paragraph after a list item is that item's continuation.
			//
			// Limitation: this is a segmenter, not a CommonMark parser.
			// Blank lines, separators, and bullets inside an indented code
			// block are still handled structurally and split the block.
			if isCodeIndent(line) && !DefaultIsNumberedItem(line) && lastKind != kindList {
				cur.kind = kindCode
			}
		}
		if IsIndented(line) && DefaultIsNumberedItem(line) {
			cur.bullets = true // an indented numbered sub-list
		}
		addLine(cur, lineNum, line)
		i++
	}

	// Flush final pending item. If an unclosed top-level fence was open, all
	// remaining lines have been added to cur as code-block content; they are
	// flushed here as part of the enclosing item rather than discarded.
	if cur != nil {
		flush(cur)
	}

	// Number normative items (or all items) in document order.
	counter := 0
	for i := range items {
		if allItems {
			items[i].Normative = true
		}
		if items[i].Normative {
			counter++
			items[i].ID = fmt.Sprintf("%s-%03d", prefix, counter)
		}
	}
	return items
}

// DefaultIsNumberedItem returns true for lines starting with "N. " or "N) "
// where N is one or more decimal digits.
func DefaultIsNumberedItem(line string) bool {
	trimmed := strings.TrimSpace(line)
	b := []byte(trimmed)
	for j := 0; j < len(b); j++ {
		ch := b[j]
		if ch >= '0' && ch <= '9' {
			continue
		}
		if (ch == '.' || ch == ')') && j > 0 {
			// '.' and ')' are ASCII (single byte); j+1 bounds-checked via &&.
			return j+1 < len(b) && isListGap(b[j+1])
		}
		break
	}
	return false
}

// IsBullet returns true for lines starting with "-", "*", or "•" followed by a
// space or tab (after trim).
// '•' is U+2022 BULLET (3 bytes in UTF-8); strings.HasPrefix operates on bytes
// so the comparison is correct.
func IsBullet(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, marker := range []string{"-", "*", "•"} {
		if rest, ok := strings.CutPrefix(trimmed, marker); ok && len(rest) > 0 && isListGap(rest[0]) {
			return true
		}
	}
	return false
}

// isListGap reports whether b may follow a list marker. Documents exported
// from word processors often use a tab ("•\tItem", "1.\tItem").
func isListGap(b byte) bool {
	return b == ' ' || b == '\t'
}

// isCodeIndent reports whether line is indented enough for an indented code
// block: a leading tab or at least four spaces.
func isCodeIndent(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}

// IsIndented returns true for lines with a leading tab or at least two spaces.
func IsIndented(line string) bool {
	return strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")
}

// closingHashesRe matches an optional ATX closing sequence: whitespace
// followed by hashes at the end of the line.
var closingHashesRe = regexp.MustCompile(`[ \t]+#+[ \t]*$`)

// headingText returns an ATX heading's title without the opening hashes or
// an optional closing sequence ("## Rules ##" is "Rules"). Hashes not
// preceded by whitespace are part of the title ("## C#" is "C#").
func headingText(line string) string {
	t := strings.TrimLeft(strings.TrimSpace(line), "#")
	t = closingHashesRe.ReplaceAllString(t, "")
	return strings.TrimSpace(t)
}

// IsHeading returns true for ATX Markdown headings (# through ######).
// A space immediately after the hashes is required (CommonMark ATX heading syntax).
// Lines with 4 or more leading spaces are indented code blocks, not headings.
func IsHeading(line string) bool {
	// Count leading spaces; 4+ means indented code block per CommonMark.
	leading := 0
	for leading < len(line) && line[leading] == ' ' {
		leading++
	}
	if leading >= 4 {
		return false
	}
	t := strings.TrimSpace(line)
	// hashes is the index of the first non-hash character in the trimmed string,
	// which equals the number of leading '#' characters.
	hashes := strings.IndexFunc(t, func(r rune) bool { return r != '#' })
	return hashes > 0 && hashes <= 6 && len(t) > hashes && t[hashes] == ' '
}

// IsDecorator returns true for lines composed entirely of the same separator
// character repeated at least 3 times (consistent with CommonMark thematic breaks).
// Supported separators: - = * _ (at least three) and ⸻ — ― (one or more).
// Requires all-same characters to avoid false positives on mixed-character lines.
// Spaced patterns like "* * *" or "- - -" are not detected as decorators.
//
// Limitation: setext heading underlines (--- or === under text) are treated as
// decorators and flushed; the preceding paragraph text becomes a standalone item.
// Setext-style headings are not supported by this parser.
func IsDecorator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	// Determine the first rune and require all runes to match it.
	var first rune
	count := 0
	for _, ch := range trimmed {
		if count == 0 {
			first = ch
		}
		if ch != first {
			return false
		}
		count++
	}
	switch first {
	case '⸻', '—', '―':
		// Wide dash glyphs are used alone as section separators in exported
		// documents; one is enough.
		return true
	case '-', '=', '*', '_':
		return count >= 3
	}
	return false
}

// StripListPrefix removes "N. ", "N) ", "- ", "* ", or "• " from the start of
// a line. The '.' and ')' separators are ASCII (single-byte), so byte-level
// indexing after the digit scan is safe. Returns the trimmed text unchanged if
// no known prefix is found.
func StripListPrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	b := []byte(trimmed)
	for j := 0; j < len(b); j++ {
		ch := b[j]
		if ch >= '0' && ch <= '9' {
			continue
		}
		// '.' and ')' are ASCII; bounds-check and require a trailing space
		// (consistent with DefaultIsNumberedItem which also requires a space).
		if (ch == '.' || ch == ')') && j > 0 && j+1 < len(b) && isListGap(b[j+1]) {
			return strings.TrimSpace(string(b[j+1:]))
		}
		break
	}
	for _, marker := range []string{"-", "*", "•"} {
		if rest, ok := strings.CutPrefix(trimmed, marker); ok && len(rest) > 0 && isListGap(rest[0]) {
			return strings.TrimSpace(rest)
		}
	}
	return trimmed
}
