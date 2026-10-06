package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSalvageTruncatedJSON(t *testing.T) {
	findings := `"drift":[{"id":"DRIFT-001","severity":"WARN","evidence":[{"path":"a.go"}]}],"violations":[]`
	entry := func(id string) string {
		return `{"id":"` + id + `","status":"IMPLEMENTED","spec_reference":{"line_start":1,"line_end":1},"evidence":[{"path":"a.go"}]}`
	}

	tests := []struct {
		name         string
		in           string
		ok           bool
		wantSpecIDs  []string
		wantComplete []string
		wantMissing  []string
	}{
		{
			name:         "cut inside second entry keeps first",
			in:           `{` + findings + `,"coverage":{"spec":[` + entry("SPEC-001") + `,{"id":"SPEC-002","status":"IMPL`,
			ok:           true,
			wantSpecIDs:  []string{"SPEC-001"},
			wantComplete: []string{"drift", "violations"},
			wantMissing:  []string{"coverage"},
		},
		{
			name:        "cut after nested object inside entry drops the partial entry",
			in:          `{` + findings + `,"coverage":{"spec":[` + entry("SPEC-001") + `,{"id":"SPEC-002","status":"IMPLEMENTED","spec_reference":{"line_start":2,"line_end":2},"evid`,
			ok:          true,
			wantSpecIDs: []string{"SPEC-001"},
		},
		{
			name:         "cut before coverage keeps findings",
			in:           `{` + findings + `,"coverage":{"spe`,
			ok:           true,
			wantSpecIDs:  nil,
			wantComplete: []string{"drift", "violations"},
		},
		{
			name:         "cut inside drift reports drift incomplete",
			in:           `{"drift":[{"id":"DRIFT-001","severity":"WARN"},{"id":"DRIFT-0`,
			ok:           true,
			wantMissing:  []string{"drift", "violations"},
			wantComplete: nil,
		},
		{
			name: "braces and escapes inside strings are ignored",
			in:   `{"drift":[{"description":"a } ] \" [ {"}],"violations":[],"coverage":{"spec":[` + entry("SPEC-001") + `,{"id":"x\"}`,
			ok:   true, wantSpecIDs: []string{"SPEC-001"}, wantComplete: []string{"drift", "violations"},
		},
		{
			name:        "cut inside an entry's evidence array drops the whole entry",
			in:          `{` + findings + `,"coverage":{"spec":[` + entry("SPEC-001") + `,{"id":"SPEC-002","status":"IMPLEMENTED","evidence":[{"path":"b.go"},{"pa`,
			ok:          true,
			wantSpecIDs: []string{"SPEC-001"},
		},
		{
			name:        "cut inside a drift entry's evidence keeps drift incomplete",
			in:          `{"drift":[{"id":"DRIFT-001","evidence":[{"path":"a.go"}]},{"id":"DRIFT-002","evidence":[{"path":"b.go"},`,
			ok:          true,
			wantMissing: []string{"drift"},
		},
		{
			name:         "repeated top-level key resets completeness",
			in:           `{"drift":[],"violations":[],"drift":[{"id":"DRIFT-001"},{"id":"DRI`,
			ok:           true,
			wantMissing:  []string{"drift"},
			wantComplete: []string{"violations"},
		},
		{
			name:         "escaped top-level key is decoded",
			in:           `{"\u0064rift":[],"violations":[],"coverage":{"spec":[` + entry("SPEC-001") + `,{"id`,
			ok:           true,
			wantSpecIDs:  []string{"SPEC-001"},
			wantComplete: []string{"drift", "violations"},
		},
		{name: "balanced input is not truncated", in: `{"drift":[]}`, ok: false},
		{name: "not an object", in: `[1,2`, ok: false},
		{name: "nothing complete", in: `{"drift":[{"id":"D`, ok: false},
		{name: "mismatched brackets", in: `{"drift":[}`, ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sr, ok := salvageTruncatedJSON(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (result %q)", ok, tc.ok, sr.JSON)
			}
			if !ok {
				return
			}
			var v struct {
				Coverage struct {
					Spec []struct {
						ID       string          `json:"id"`
						Evidence json.RawMessage `json:"evidence"`
					} `json:"spec"`
				} `json:"coverage"`
			}
			if err := json.Unmarshal([]byte(sr.JSON), &v); err != nil {
				t.Fatalf("salvaged JSON does not parse: %v\n%s", err, sr.JSON)
			}
			var ids []string
			for _, e := range v.Coverage.Spec {
				ids = append(ids, e.ID)
				if len(e.Evidence) == 0 {
					t.Errorf("entry %s kept without its evidence", e.ID)
				}
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantSpecIDs, ",") {
				t.Errorf("spec IDs = %v, want %v", ids, tc.wantSpecIDs)
			}
			for _, k := range tc.wantComplete {
				if !sr.CompleteTopLevel[k] {
					t.Errorf("%q should be complete", k)
				}
			}
			for _, k := range tc.wantMissing {
				if sr.CompleteTopLevel[k] {
					t.Errorf("%q should not be complete", k)
				}
			}
		})
	}
}
