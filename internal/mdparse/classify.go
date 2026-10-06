package mdparse

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// modalRe matches language that makes a statement a requirement or an
// acceptance condition.
var modalRe = regexp.MustCompile(`(?i)\b(must|shall|should|required|requires|will|may not|cannot|can't|never|always|done when)\b`)

// normativeSectionRe matches section headings whose plain paragraphs are
// requirements even without modal language (e.g. a usage line under
// "Command", a table under "Flags").
var normativeSectionRe = regexp.MustCompile(`(?i)\b(requirements?|constraints?|behaviou?rs?|acceptance|rules?|interfaces?|commands?|flags?|contracts?|guarantees?|invariants?)\b`)

// headingStopWords may be lowercase in a title-case heading.
var headingStopWords = map[string]bool{
	"with": true, "from": true, "into": true, "over": true, "than": true,
	"that": true, "this": true, "when": true, "then": true, "upon": true,
}

// isNormative classifies an item. Requirements and plan steps are
// normative; prose, list intros, and code examples are informational. raw
// is the item's lines before trimming, so table cells keep their tabs.
func isNormative(text, raw string, kind itemKind, bullets bool, section string) bool {
	switch {
	case kind == kindCode, isDataExample(text):
		return false
	case isTable(raw):
		return true // tables in specs define contracts: flags, codes, verdicts
	case modalRe.MatchString(text):
		return true
	case kind == kindList, bullets:
		return true
	case strings.HasSuffix(text, ":"):
		return false // an intro to what follows
	case normativeSectionRe.MatchString(section):
		return true
	}
	return false
}

// looksLikeHeading reports whether a single line reads like a title: short,
// no sentence punctuation at the end, no modal language, first word
// capitalized, and every significant word (four or more letters, not a
// stop word) capitalized. Words are allowed to start with digits or
// punctuation ("(Phase", "1)", "—").
func looksLikeHeading(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || len(t) > 80 || strings.ContainsAny(t, "\t") {
		return false
	}
	if strings.ContainsAny(t[len(t)-1:], ".:;,?!") || modalRe.MatchString(t) {
		return false
	}
	words := strings.Fields(t)
	if len(words) > 8 {
		return false
	}
	for i, w := range words {
		w = strings.TrimLeftFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if w == "" {
			continue // pure punctuation, e.g. "—"
		}
		r := []rune(w)
		if i == 0 {
			if unicode.IsLower(r[0]) {
				return false
			}
			continue
		}
		letters := 0
		for _, c := range r {
			if unicode.IsLetter(c) {
				letters++
			}
		}
		if letters >= 4 && !headingStopWords[strings.ToLower(w)] && unicode.IsLower(r[0]) {
			return false
		}
	}
	return true
}

// isNumberedHeading reports whether the numbered line at lines[i] is a
// section heading rather than a list item: it reads like a title, stands
// alone between blank lines, and is neither preceded nor followed by
// another numbered item (which would make it part of a loose list).
// Separators and headings count as blank on either side.
func isNumberedHeading(lines []string, i int, isNum IsNumberedItemFn, strip func(string) string) bool {
	if !looksLikeHeading(strip(lines[i])) {
		return false
	}
	isGap := func(line string) bool {
		return strings.TrimSpace(line) == "" || IsDecorator(line) || IsHeading(line)
	}
	if i > 0 && !isGap(lines[i-1]) {
		return false
	}
	if i+1 < len(lines) && !isGap(lines[i+1]) {
		return false
	}
	isListItem := func(line string) bool { return isNum(line) && !IsIndented(line) }
	// Preceded by another numbered item (skipping blank lines and that
	// item's indented continuation): part of a loose list. A separator or
	// heading in between starts a new section, so it does not count.
	for j := i - 1; j >= 0; j-- {
		line := lines[j]
		// Boundaries first: an ATX heading may itself be indented.
		if IsDecorator(line) || IsHeading(line) {
			break
		}
		if strings.TrimSpace(line) == "" || IsIndented(line) {
			continue
		}
		if isListItem(line) {
			return false
		}
		break
	}
	// Followed by another numbered item: part of a loose list. A separator
	// or heading first means the next section starts, so it is a heading.
	for j := i + 1; j < len(lines); j++ {
		line := lines[j]
		if IsDecorator(line) || IsHeading(line) {
			return true
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// An indented continuation means this line is a list item with
		// content of its own, not a title.
		if IsIndented(line) {
			return false
		}
		return !isListItem(line)
	}
	return true
}

// mdLinkRe matches a paragraph that opens with a Markdown inline or
// reference link, e.g. "[1](step.md)" or "[Clients][clients]".
var mdLinkRe = regexp.MustCompile(`^\[[^\]\n]*\][(\[]`)

// tableDelimiterRe matches a Markdown table delimiter row, with or without
// outer pipes: "--- | ---", "|:--|--:|".
var tableDelimiterRe = regexp.MustCompile(`^\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)+\|?$`)

// isDataExample reports whether a paragraph is an unfenced data example,
// such as a JSON object or array. Examples illustrate a format;
// requirements about the format are stated elsewhere.
//
// Valid JSON always counts. Documentation examples often are not valid
// (placeholders such as "..."), so a block also counts when its first line
// is a lone "{" or "[" and it ends with the matching "}" or "]". Prose that
// starts or ends with brackets, such as links or citations ("[1] Clients
// must authenticate [RFC 123]"), is not an example.
func isDataExample(text string) bool {
	t := strings.TrimSpace(text)
	if len(t) < 2 || mdLinkRe.MatchString(t) {
		return false
	}
	if (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) {
		return true
	}
	first, _, multiline := strings.Cut(t, "\n")
	if !multiline {
		return false
	}
	switch strings.TrimSpace(first) {
	case "{":
		return t[len(t)-1] == '}'
	case "[":
		return t[len(t)-1] == ']'
	}
	return false
}

// isTable reports whether a paragraph is a table: at least two lines, and
// either a Markdown delimiter row ("--- | ---") or every line containing a
// tab or starting with a pipe (tab-separated tables come from exports).
func isTable(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return false
	}
	for _, l := range lines {
		if tableDelimiterRe.MatchString(strings.TrimSpace(l)) {
			return true
		}
	}
	for _, l := range lines {
		// Check tabs on the untrimmed line: a row with an empty first or
		// last cell has its only separator at the edge.
		if !strings.Contains(l, "\t") && !strings.HasPrefix(strings.TrimSpace(l), "|") {
			return false
		}
	}
	return true
}
