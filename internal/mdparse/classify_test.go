package mdparse

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func parseExported(t *testing.T, all bool) []Item {
	t.Helper()
	f, err := os.Open("../../testdata/exported_spec.md")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	items, err := Segmenter{IDPrefix: "SPEC", AllItems: all}.ParseReader(f)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func findItem(t *testing.T, items []Item, prefix string) Item {
	t.Helper()
	for _, it := range items {
		if strings.HasPrefix(it.Text, prefix) {
			return it
		}
	}
	t.Fatalf("no item starting with %q", prefix)
	return Item{}
}

func TestClassify_ExportedDocument(t *testing.T) {
	items := parseExported(t, false)

	for _, it := range items {
		switch strings.TrimSpace(it.Text) {
		case "Widget Service — Specification", "Purpose", "Interface", "Command", "Data Model", "⸻", "—":
			t.Errorf("heading or separator %q became an item", it.Text)
		}
	}

	tests := []struct {
		prefix    string
		normative bool
		section   string
	}{
		{"The widget service stores", false, "Purpose"},          // prose
		{"It provides:", true, "Purpose"},                        // intro with tab bullets
		{"widgetd serve", true, "Command"},                       // usage line in a normative section
		{"Flag\tMeaning", true, "Command"},                       // table
		{"{", false, "Data Model"},                               // data example
		{"Widgets must have a unique name.", true, "Data Model"}, // modal
		{"Fields:", false, "Data Model"},                         // intro without list
		{"```go", false, "Data Model"},                           // code block
		{"Create Store", true, "Steps"},                          // loose list item, not a heading
		{"Add Handlers", true, "Steps"},
		{"- an indented example", true, "Embedded Example"}, // CommonMark: up to 3 spaces is a heading
		{"Widgets are cheap.", false, "Embedded Example"},
	}
	for _, tc := range tests {
		it := findItem(t, items, tc.prefix)
		if it.Normative != tc.normative || it.Section != tc.section {
			t.Errorf("%q: normative=%v section=%q, want %v %q", tc.prefix, it.Normative, it.Section, tc.normative, tc.section)
		}
		if (it.ID != "") != it.Normative {
			t.Errorf("%q: ID %q must be set exactly when normative", tc.prefix, it.ID)
		}
	}

	// IDs are contiguous over normative items, in document order.
	n := 0
	for _, it := range items {
		if it.Normative {
			n++
			if want := "SPEC-" + pad3(n); it.ID != want {
				t.Errorf("ID = %s, want %s", it.ID, want)
			}
		}
	}
	if got := len(RequiredItems(items)); got != n {
		t.Errorf("RequiredItems = %d, want %d", got, n)
	}
}

func TestClassify_AllItemsNumbersEverything(t *testing.T) {
	items := parseExported(t, true)
	for i, it := range items {
		if !it.Normative || it.ID != "SPEC-"+pad3(i+1) {
			t.Errorf("item %d %q: normative=%v id=%q", i, it.Text, it.Normative, it.ID)
		}
	}
	if len(items) != len(parseExported(t, false)) {
		t.Error("AllItems must not change segmentation, only numbering")
	}
}

func pad3(n int) string { return fmt.Sprintf("%03d", n) }

func TestLooksLikeHeading(t *testing.T) {
	for line, want := range map[string]bool{
		"Purpose":                                       true,
		"Core Philosophy":                               true,
		"Non-Goals (Phase 1)":                           true,
		"Output: Canonical JSON Schema (v1)":            true,
		"Phase 1 — Foundation":                          true,
		"Intent Enforcement for Agentic Coding Systems": true,
		"Correct code can still be wrong code.":         false, // sentence
		"Initialize Go module":                          false, // lowercase significant word
		"realitycheck check [path]":                     false, // lowercase first word
		"Output Must Be JSON":                           false, // modal
		"Fields:":                                       false,
		"Flag\tMeaning":                                 false,
		"":                                              false,
	} {
		if got := looksLikeHeading(line); got != want {
			t.Errorf("looksLikeHeading(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestListMarkersAcceptTabs(t *testing.T) {
	if !IsBullet("•\tItem") || !IsBullet("-\tItem") || IsBullet("-Item") || IsBullet("•") {
		t.Error("IsBullet should accept a space or tab after the marker, and nothing else")
	}
	if !DefaultIsNumberedItem("1.\tItem") || DefaultIsNumberedItem("1.Item") {
		t.Error("DefaultIsNumberedItem should accept a tab after the marker")
	}
	if StripListPrefix("•\tItem") != "Item" || StripListPrefix("2.\tItem") != "Item" {
		t.Error("StripListPrefix should strip tab-separated markers")
	}
}

func TestNumberedHeading_StopsAtSectionBoundary(t *testing.T) {
	src := "3. Steps\n\n⸻\n\n1. Do the thing.\n2. Do more.\n"
	items, err := Segmenter{IDPrefix: "PLAN"}.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Section != "Steps" || items[0].Text != "Do the thing." {
		t.Errorf("a numbered heading before a separator should be a heading; got %+v", items)
	}
}

func TestIndentedATXHeadingsSetSection(t *testing.T) {
	for _, indent := range []string{"", " ", "  ", "   "} {
		src := indent + "## Rules\n\n- Must do A.\n"
		items, err := Segmenter{IDPrefix: "SPEC"}.ParseReader(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Section != "Rules" {
			t.Errorf("indent %q: items = %+v, want section Rules", indent, items)
		}
	}
}

func TestIsDataExample(t *testing.T) {
	for text, want := range map[string]bool{
		`{ "id": 1 }`:  true,
		`[{"id": 1}]`:  true,
		`[ "a", "b" ]`: true,
		`[1, 2]`:       true,
		`[]`:           true,
		`[Clients](clients.md) must authenticate.`: false,
		`[optional] flags may be given`:            false,
	} {
		if got := isDataExample(text); got != want {
			t.Errorf("isDataExample(%q) = %v, want %v", text, got, want)
		}
	}
	for _, req := range []string{"[Clients](clients.md) must authenticate.", "[1] must authenticate."} {
		if !isNormative(req, req, kindParagraph, false, "") {
			t.Errorf("%q must be normative", req)
		}
	}
}

func TestIsTable_PipeTableWithoutOuterPipes(t *testing.T) {
	if !isTable("Name | Value\n--- | ---\nTimeout | 30") {
		t.Error("pipe table without outer pipes should be a table")
	}
	table := "Name | Value\n--- | ---\nTimeout | 30"
	if !isNormative(table, table, kindParagraph, false, "") {
		t.Error("tables are normative")
	}
	if !isTable("Name | Value\n- | -\nTimeout | 30") {
		t.Error("short delimiter cells should be a table")
	}
	if isTable("This line has a | pipe\nand so does | this one") {
		t.Error("prose containing pipes is not a table")
	}
}

func TestIndentedCodeIsNotAHeading(t *testing.T) {
	src := "## Rules\n\n    Purpose\n\n- Must do A.\n"
	items, err := Segmenter{IDPrefix: "SPEC"}.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want the code line kept plus the bullet", items)
	}
	if items[0].Normative || items[0].Section != "Rules" {
		t.Errorf("indented code: %+v, want context in section Rules", items[0])
	}
	if items[1].Section != "Rules" {
		t.Errorf("section after indented code = %q, want Rules", items[1].Section)
	}
}

func TestIsTable_EmptyEdgeCells(t *testing.T) {
	if !isTable("\tB\nA\t\nC\tD") {
		t.Error("tab rows with empty first or last cells should be a table")
	}
}

func TestHeadingText(t *testing.T) {
	for line, want := range map[string]string{
		"## Requirements":    "Requirements",
		"## Requirements ##": "Requirements",
		"# Title #  ":        "Title",
		"## C#":              "C#",
		"  ### Rules ###":    "Rules",
	} {
		if got := headingText(line); got != want {
			t.Errorf("headingText(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestCodeItemsEndBeforeFollowingProse(t *testing.T) {
	for name, src := range map[string]string{
		"fenced":   "```\nexample\n```\nThe service must authenticate requests.\n",
		"indented": "    example code\nThe service must authenticate requests.\n",
	} {
		items, err := Segmenter{IDPrefix: "SPEC"}.ParseReader(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 || items[0].Normative || !items[1].Normative || items[1].Text != "The service must authenticate requests." {
			t.Errorf("%s: items = %+v, want code (context) then a separate requirement", name, items)
		}
	}
	// A fence inside a paragraph stays part of that paragraph.
	items, err := Segmenter{IDPrefix: "SPEC"}.ParseReader(strings.NewReader("Run this:\n```\ncmd\n```\nthen continue.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("embedded fence: items = %+v, want one paragraph", items)
	}
}

func TestNumberedHeading_IndentedHeadingIsBoundary(t *testing.T) {
	src := "1. First item\n\n  ## Next\n\n2. Overview\n\nSome prose.\n"
	items, err := Segmenter{IDPrefix: "SPEC"}.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Text == "Overview" {
			t.Errorf("standalone numbered title after an indented heading should be a heading, got item %+v", it)
		}
	}
	if last := items[len(items)-1]; last.Section != "Overview" {
		t.Errorf("section = %q, want Overview", last.Section)
	}
}

func TestLooseListWithIndentedContinuation(t *testing.T) {
	src := "1. Create Store\n\n    Initialization must be idempotent.\n\n2. Add Handlers\n"
	items, err := Segmenter{IDPrefix: "PLAN"}.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{}
	for _, it := range items {
		texts = append(texts, it.Text)
		if !it.Normative {
			t.Errorf("%q should be normative", it.Text)
		}
	}
	if len(items) != 3 || items[0].Text != "Create Store" || items[2].Text != "Add Handlers" {
		t.Errorf("items = %q, want both list items and the continuation", texts)
	}
}

func TestSingleStepWithIndentedContinuation(t *testing.T) {
	src := "1. Create Store\n\n    Initialization must be idempotent.\n"
	items, err := Segmenter{IDPrefix: "PLAN"}.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Text != "Create Store" || !items[0].Normative {
		t.Errorf("items = %+v, want the step kept as a normative item", items)
	}
}

func TestHeadingResetsListContinuation(t *testing.T) {
	src := "- Must do A.\n\n## Examples\n\n    client MUST send a token\n"
	items, err := Segmenter{IDPrefix: "SPEC"}.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].Normative || items[1].Section != "Examples" {
		t.Errorf("items = %+v, want indented code in a new section to stay context", items)
	}
}
