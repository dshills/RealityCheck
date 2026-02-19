package mdparse

import (
	"strings"
	"testing"
)

// --- Helper predicates ---

func TestDefaultIsNumberedItem(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"1. foo", true},
		{"12. bar", true},
		{"1) baz", true},
		{"- bullet", false},
		{"foo", false},
		{"1.no space", false},
		{"1)no space", false},
		{"  1. indented", true}, // indented but IsNumberedItem doesn't check indent
		{"", false},
	}
	for _, c := range cases {
		if got := DefaultIsNumberedItem(c.line); got != c.want {
			t.Errorf("DefaultIsNumberedItem(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsBullet(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"- item", true},
		{"* item", true},
		{"• item", true},
		{"  - indented", true},
		{"1. numbered", false},
		{"", false},
		{"-no space", false},
		{"*no space", false},
	}
	for _, c := range cases {
		if got := IsBullet(c.line); got != c.want {
			t.Errorf("IsBullet(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsIndented(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"  indented", true},
		{"\tindented", true},
		{" one space", false},
		{"not indented", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsIndented(c.line); got != c.want {
			t.Errorf("IsIndented(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsHeading(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"# H1", true},
		{"## H2", true},
		{"###### H6", true},
		{"####### too many hashes", false},
		{"#nospace", false},
		{"not a heading", false},
		{"    # indented code block", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsHeading(c.line); got != c.want {
			t.Errorf("IsHeading(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsDecorator(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"---", true},
		{"===", true},
		{"***", true},
		{"___", true},
		{"⸻", false}, // only 1 rune, < 3
		{"——", false}, // only 2 em-dashes, < 3
		{"ab-", false},
		{"", false},
		{"-", false},
		{"--", false},
	}
	for _, c := range cases {
		if got := IsDecorator(c.line); got != c.want {
			t.Errorf("IsDecorator(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestStripListPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"1. foo", "foo"},
		{"12. bar baz", "bar baz"},
		{"1) qux", "qux"},
		{"- item", "item"},
		{"* item", "item"},
		{"• item", "item"},
		{"plain text", "plain text"},
		{"  - indented bullet", "indented bullet"},
	}
	for _, c := range cases {
		if got := StripListPrefix(c.in); got != c.want {
			t.Errorf("StripListPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- fencePrefix (unexported, tested via segment behaviour) ---

func TestFencePrefix(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"```", "```"},
		{"```go", "```"},
		{"````", "````"},
		{"~~~", "~~~"},
		{"~~~~bash", "~~~~"},
		{"    ```", ""}, // 4 leading spaces → indented code block
		{"   ```", "```"}, // 3 leading spaces → fence
		{"``", ""},   // only 2 backticks
		{"", ""},
	}
	for _, c := range cases {
		if got := fencePrefix(c.line); got != c.want {
			t.Errorf("fencePrefix(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestIsClosingFence(t *testing.T) {
	cases := []struct {
		line, open string
		want       bool
	}{
		{"```", "```", true},
		{"```  ", "```", true},  // trailing spaces allowed
		{"````", "```", true},   // longer or equal is valid closing
		{"```go", "```", false}, // info string → not a closer
		{"~~~", "```", false},   // different marker
		{"~~", "~~~", false},    // too short
		{"", "```", false},
		{"```", "", false}, // empty openFence
	}
	for _, c := range cases {
		if got := isClosingFence(c.line, c.open); got != c.want {
			t.Errorf("isClosingFence(%q, %q) = %v, want %v", c.line, c.open, got, c.want)
		}
	}
}

// --- Segmenter.ParseReader ---

func parse(t *testing.T, src string) []Item {
	t.Helper()
	s := Segmenter{IDPrefix: "T"}
	items, err := s.ParseReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	return items
}

func TestSegmenter_BulletList(t *testing.T) {
	src := `## Section

- First item.
- Second item.
`
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Text != "First item." {
		t.Errorf("item 0 text: %q", items[0].Text)
	}
	if items[1].Text != "Second item." {
		t.Errorf("item 1 text: %q", items[1].Text)
	}
}

func TestSegmenter_NumberedList(t *testing.T) {
	src := `## Phase 1

1. Initialize module.
2. Define types.
`
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Text != "Initialize module." {
		t.Errorf("item 0: %q", items[0].Text)
	}
	if items[1].Text != "Define types." {
		t.Errorf("item 1: %q", items[1].Text)
	}
}

func TestSegmenter_NestedBulletsMerged(t *testing.T) {
	src := `1. Accept a JSON request body.
   - Validate required fields.
2. Return a JSON response.
`
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %v", len(items), items)
	}
	if !strings.Contains(items[0].Text, "Accept") || !strings.Contains(items[0].Text, "Validate") {
		t.Errorf("nested bullet not merged: %q", items[0].Text)
	}
}

func TestSegmenter_Paragraph(t *testing.T) {
	src := `Some standalone paragraph text.

Another paragraph.
`
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestSegmenter_HeadingNotAnItem(t *testing.T) {
	src := `# Top-level heading

- Only bullet.
`
	items := parse(t, src)
	if len(items) != 1 {
		t.Fatalf("expected 1 item (heading excluded), got %d", len(items))
	}
}

func TestSegmenter_FencedCodeBlockInItem(t *testing.T) {
	// No blank line between the item and the indented fence — continuation
	// collects the code block into the same item. A blank line would flush
	// the item before the fence is reached (correct parser behavior).
	src := "1. Run this command.\n   ```bash\n   go build ./...\n   ```\n\n2. Second step.\n"
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if !strings.Contains(items[0].Text, "go build") {
		t.Errorf("fenced code block not included in item text: %q", items[0].Text)
	}
}

func TestSegmenter_DocumentLevelFenceNotMerged(t *testing.T) {
	// A document-level (non-indented) code block should NOT be merged into
	// the preceding list item.
	src := "- Bullet item.\n\n```go\nfunc foo() {}\n```\n"
	items := parse(t, src)
	// The bullet and the code block should be separate items.
	if len(items) < 2 {
		t.Fatalf("expected bullet and code block as separate items, got %d: %v", len(items), items)
	}
	if strings.Contains(items[0].Text, "func foo") {
		t.Error("document-level code block incorrectly merged into preceding bullet")
	}
}

func TestSegmenter_LineNumbers(t *testing.T) {
	src := "## Section\n\n- First.\n- Second.\n"
	// Line 1: heading, Line 2: blank, Line 3: first bullet, Line 4: second bullet
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].LineStart != 3 || items[0].LineEnd != 3 {
		t.Errorf("item 0 lines: start=%d end=%d, want 3..3", items[0].LineStart, items[0].LineEnd)
	}
	if items[1].LineStart != 4 || items[1].LineEnd != 4 {
		t.Errorf("item 1 lines: start=%d end=%d, want 4..4", items[1].LineStart, items[1].LineEnd)
	}
}

func TestSegmenter_IDPrefix(t *testing.T) {
	s := Segmenter{IDPrefix: "SPEC"}
	items, _ := s.ParseReader(strings.NewReader("- One.\n- Two.\n"))
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
	if items[0].ID != "SPEC-001" {
		t.Errorf("item 0 ID: %q, want SPEC-001", items[0].ID)
	}
	if items[1].ID != "SPEC-002" {
		t.Errorf("item 1 ID: %q, want SPEC-002", items[1].ID)
	}
}

func TestSegmenter_EmptyInput(t *testing.T) {
	items := parse(t, "")
	if len(items) != 0 {
		t.Errorf("expected 0 items for empty input, got %d", len(items))
	}
}

func TestSegmenter_DecoratorFlushesCurrent(t *testing.T) {
	src := "- Item one.\n---\n- Item two.\n"
	items := parse(t, src)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}
