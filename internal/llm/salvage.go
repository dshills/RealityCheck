package llm

import "encoding/json"

// salvageResult describes JSON recovered from a truncated model response.
type salvageResult struct {
	// JSON is the input cut back to the last complete object or array
	// element, with the still-open brackets closed.
	JSON string
	// CompleteTopLevel holds the top-level object keys whose values were
	// fully emitted before the cut. Keys added only by closing brackets
	// during salvage are not included.
	CompleteTopLevel map[string]bool
}

// salvageTruncatedJSON recovers the complete prefix of a JSON object that was
// cut off mid-stream, typically because the model hit its output token limit.
//
// It is built for the report and completion schemas, whose top-level values
// are all objects or arrays and whose entries are objects inside the
// outermost arrays. It scans s once, tracking string state and the stack of
// open brackets, and remembers the position just after the last whole entry
// or top-level container value that closed. Top-level scalar values are not
// tracked: they are neither cut points nor reported in CompleteTopLevel. The result is s up to that point
// plus closing brackets for whatever was still open. Partially emitted
// elements, keys, and scalars after that point are discarded.
//
// It returns ok=false when s does not start with an object, when its
// brackets balance (the input was not truncated, so salvage cannot help), or
// when nothing complete was emitted.
func salvageTruncatedJSON(s string) (salvageResult, bool) {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	if start >= len(s) || s[start] != '{' {
		return salvageResult{}, false
	}

	var (
		stack     []byte
		inString  bool
		escaped   bool
		expectKey bool // at depth 1, the next string is a top-level key
		inKey     bool
		keyStart  int
		topKey    string
		lastSafe  = -1
		safeStack []byte
		complete  = map[string]bool{}
		atSafe    = map[string]bool{}
	)

	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				if inKey {
					inKey = false
					// Decode escapes so the key matches what encoding/json
					// sees (e.g. "\u0064rift" is "drift").
					if err := json.Unmarshal([]byte(s[keyStart-1:i+1]), &topKey); err != nil {
						return salvageResult{}, false
					}
					// A repeated key starts over: encoding/json keeps the
					// last occurrence, which has not completed yet.
					delete(complete, topKey)
				}
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			if len(stack) == 1 && expectKey {
				inKey = true
				keyStart = i + 1
				expectKey = false
			}
		case '{', '[':
			stack = append(stack, c)
			if len(stack) == 1 {
				expectKey = true
			}
		case '}', ']':
			if len(stack) == 0 || !matches(stack[len(stack)-1], c) {
				return salvageResult{}, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return salvageResult{}, false // balanced: not truncated
			}
			if len(stack) == 1 {
				complete[topKey] = true
			}
			// Only cut after a whole report entry or a whole top-level
			// value. A report entry is an element of the outermost array on
			// the path (drift[i], violations[i], coverage.spec[i], ...).
			// Cutting after anything nested inside an entry, such as its
			// spec_reference or one of its evidence items, would keep an
			// entry whose status survived but whose other fields were lost.
			if len(stack) == 1 || (stack[len(stack)-1] == '[' && arrayDepth(stack) == 1) {
				lastSafe = i + 1
				safeStack = append(safeStack[:0], stack...)
				atSafe = copyKeys(complete)
			}
		case ',':
			if len(stack) == 1 {
				expectKey = true
			}
		}
	}

	if lastSafe < 0 {
		return salvageResult{}, false
	}
	out := []byte(s[start:lastSafe])
	for i := len(safeStack) - 1; i >= 0; i-- {
		if safeStack[i] == '{' {
			out = append(out, '}')
		} else {
			out = append(out, ']')
		}
	}
	return salvageResult{JSON: string(out), CompleteTopLevel: atSafe}, true
}

// arrayDepth counts the open arrays in stack.
func arrayDepth(stack []byte) int {
	n := 0
	for _, b := range stack {
		if b == '[' {
			n++
		}
	}
	return n
}

func matches(open, close byte) bool {
	return (open == '{' && close == '}') || (open == '[' && close == ']')
}

func copyKeys(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
