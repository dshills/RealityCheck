// Package mdparse provides shared Markdown segmentation primitives used by the
// spec and plan parsers.
package mdparse

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Item is a discrete segment extracted from a Markdown document.
type Item struct {
	ID        string
	LineStart int
	LineEnd   int
	Text      string
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
}

// ParseFile reads the file at path and segments it using s.
func (s Segmenter) ParseFile(path string) ([]Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mdparse: open %s: %w", path, err)
	}
	defer f.Close()
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
	return segment(lines, s.IDPrefix, isNum, strip), nil
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

func segment(lines []string, prefix string, isNum IsNumberedItemFn, strip func(string) string) []Item {
	var items []Item
	counter := 0

	nextID := func() string {
		counter++
		return fmt.Sprintf("%s-%03d", prefix, counter)
	}

	type pending struct {
		lineStart int
		lineEnd   int // last consumed line (1-indexed), updated as lines are added
		buf       []string
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
		items = append(items, Item{
			ID:        nextID(),
			LineStart: p.lineStart,
			LineEnd:   p.lineEnd,
			Text:      text,
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
				cur = &pending{lineStart: lineNum, lineEnd: lineNum}
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
			}
			cur = &pending{lineStart: lineNum, lineEnd: lineNum}
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
			cur = &pending{lineStart: lineNum, lineEnd: lineNum}
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
				cur = &pending{lineStart: lineNum, lineEnd: lineNum}
			}
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
			i++
			continue
		}

		// Paragraph / continuation. Note: paragraph lines are appended verbatim
		// (preserving indentation), while list-item continuation lines are
		// TrimSpace'd by collectContinuation. This asymmetry is intentional.
		if cur == nil {
			cur = &pending{lineStart: lineNum, lineEnd: lineNum}
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
			return j+1 < len(b) && b[j+1] == ' '
		}
		break
	}
	return false
}

// IsBullet returns true for lines starting with "- ", "* ", or "• " (after trim).
// '•' is U+2022 BULLET (3 bytes in UTF-8); strings.HasPrefix operates on bytes
// so the comparison is correct.
func IsBullet(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "- ") ||
		strings.HasPrefix(trimmed, "* ") ||
		strings.HasPrefix(trimmed, "• ")
}

// IsIndented returns true for lines with a leading tab or at least two spaces.
func IsIndented(line string) bool {
	return strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")
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
// Supported separators: - = * _ ⸻ —
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
	if first != '-' && first != '=' && first != '*' && first != '_' && first != '⸻' && first != '—' {
		return false
	}
	return count >= 3
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
		if (ch == '.' || ch == ')') && j > 0 && j+1 < len(b) && b[j+1] == ' ' {
			return strings.TrimSpace(string(b[j+1:]))
		}
		break
	}
	for _, pfx := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(trimmed, pfx) {
			return strings.TrimSpace(trimmed[len(pfx):])
		}
	}
	return trimmed
}
