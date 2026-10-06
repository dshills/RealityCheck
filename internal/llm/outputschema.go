package llm

import (
	"bytes"
	"encoding/json"

	"google.golang.org/genai"
)

// OutputSchema is a JSON schema that providers use to constrain model output
// natively (Anthropic output_config.format, OpenAI response_format
// json_schema, Gemini responseSchema).
//
// Property order is significant: providers emit keys in schema order, and
// the report must put drift and violations before coverage so that a
// truncated response still carries complete findings (see
// salvageTruncatedJSON). The schema is therefore built from ordered nodes
// rather than Go maps, whose keys encoding/json sorts alphabetically.
type OutputSchema struct {
	Name string
	root *schemaNode
}

// schemaNode is one node of an ordered JSON schema. Every object property is
// required and objects disallow additional properties, which is what
// OpenAI's strict mode demands and what the other providers accept.
type schemaNode struct {
	typ   string // "object", "array", "string", "boolean"
	desc  string
	enum  []string
	props []schemaProp // ordered; objects only
	items *schemaNode  // arrays only
}

type schemaProp struct {
	name string
	node *schemaNode
}

func objectNode(props ...schemaProp) *schemaNode { return &schemaNode{typ: "object", props: props} }
func arrayNode(items *schemaNode) *schemaNode    { return &schemaNode{typ: "array", items: items} }
func stringNode(desc string) *schemaNode         { return &schemaNode{typ: "string", desc: desc} }
func enumNode(values ...string) *schemaNode      { return &schemaNode{typ: "string", enum: values} }
func prop(name string, n *schemaNode) schemaProp { return schemaProp{name: name, node: n} }

// MarshalJSON writes standard JSON schema with properties in declaration order.
func (n *schemaNode) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"type":`)
	writeJSON(&b, n.typ)
	if n.desc != "" {
		b.WriteString(`,"description":`)
		writeJSON(&b, n.desc)
	}
	if len(n.enum) > 0 {
		b.WriteString(`,"enum":`)
		writeJSON(&b, n.enum)
	}
	if n.typ == "object" {
		b.WriteString(`,"properties":`)
		props, err := n.propertiesJSON()
		if err != nil {
			return nil, err
		}
		b.Write(props)
		b.WriteString(`,"required":`)
		writeJSON(&b, n.propNames())
		b.WriteString(`,"additionalProperties":false`)
	}
	if n.items != nil {
		b.WriteString(`,"items":`)
		items, err := n.items.MarshalJSON()
		if err != nil {
			return nil, err
		}
		b.Write(items)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// propertiesJSON writes the "properties" object in declaration order.
func (n *schemaNode) propertiesJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range n.props {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSON(&b, p.name)
		b.WriteByte(':')
		child, err := p.node.MarshalJSON()
		if err != nil {
			return nil, err
		}
		b.Write(child)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (n *schemaNode) propNames() []string {
	names := make([]string, len(n.props))
	for i, p := range n.props {
		names[i] = p.name
	}
	return names
}

// writeJSON writes a string or string slice; both always marshal.
func writeJSON(b *bytes.Buffer, v any) {
	enc, _ := json.Marshal(v)
	b.Write(enc)
}

// JSON returns the schema as ordered JSON.
func (s *OutputSchema) JSON() (json.RawMessage, error) {
	return s.root.MarshalJSON()
}

// anthropicMap returns the schema as the map Anthropic's SDK expects. The
// top level must be a map, so "properties" is passed as raw JSON to keep
// its order; encoding/json writes json.RawMessage values verbatim.
func (s *OutputSchema) anthropicMap() (map[string]any, error) {
	props, err := s.root.propertiesJSON()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":                 "object",
		"properties":           json.RawMessage(props),
		"required":             s.root.propNames(),
		"additionalProperties": false,
	}, nil
}

// genaiSchema converts the schema for Gemini, setting PropertyOrdering on
// every object because Gemini otherwise orders properties alphabetically.
func (s *OutputSchema) genaiSchema() *genai.Schema {
	return s.root.toGenai()
}

func (n *schemaNode) toGenai() *genai.Schema {
	out := &genai.Schema{Description: n.desc, Enum: n.enum}
	switch n.typ {
	case "object":
		out.Type = genai.TypeObject
		out.Properties = make(map[string]*genai.Schema, len(n.props))
		for _, p := range n.props {
			out.Properties[p.name] = p.node.toGenai()
		}
		out.PropertyOrdering = n.propNames()
		out.Required = n.propNames()
	case "array":
		out.Type = genai.TypeArray
		out.Items = n.items.toGenai()
	case "boolean":
		out.Type = genai.TypeBoolean
	default:
		out.Type = genai.TypeString
	}
	return out
}

// ── Report schemas ───────────────────────────────────────────────────────────

func evidenceNode() *schemaNode {
	return arrayNode(objectNode(
		prop("path", stringNode("A path from the CODE INVENTORY")),
		prop("symbol", stringNode("A symbol from the CODE INVENTORY, or empty")),
		prop("confidence", enumNode("HIGH", "MEDIUM", "LOW")),
	))
}

func coverageEntryNode(prefix string) *schemaNode {
	return objectNode(
		prop("id", stringNode("A "+prefix+" ID exactly as listed")),
		prop("status", enumNode("IMPLEMENTED", "PARTIAL", "NOT_IMPLEMENTED", "UNCLEAR")),
		prop("evidence", evidenceNode()),
		prop("notes", stringNode("Brief explanation, or empty")),
	)
}

func coverageNode() *schemaNode {
	return objectNode(
		prop("spec", arrayNode(coverageEntryNode("SPEC"))),
		prop("plan", arrayNode(coverageEntryNode("PLAN"))),
	)
}

func severityNode() *schemaNode { return enumNode("INFO", "WARN", "CRITICAL") }

// reportSchema constrains the main analysis response. Findings come first.
var reportSchema = &OutputSchema{
	Name: "realitycheck_report",
	root: objectNode(
		prop("drift", arrayNode(objectNode(
			prop("id", stringNode("DRIFT-001, DRIFT-002, ...")),
			prop("severity", severityNode()),
			prop("description", stringNode("")),
			prop("evidence", evidenceNode()),
			prop("why_unjustified", stringNode("")),
			prop("impact", stringNode("")),
			prop("recommendation", stringNode("")),
		))),
		prop("violations", arrayNode(objectNode(
			prop("id", stringNode("VIOLATION-001, VIOLATION-002, ...")),
			prop("severity", severityNode()),
			prop("description", stringNode("")),
			prop("spec_id", stringNode("The SPEC ID the code contradicts")),
			prop("evidence", evidenceNode()),
			prop("impact", stringNode("")),
			prop("blocking", &schemaNode{typ: "boolean"}),
		))),
		prop("coverage", coverageNode()),
	),
}

// completionSchema constrains the coverage completion response.
var completionOutputSchema = &OutputSchema{
	Name: "realitycheck_coverage",
	root: objectNode(prop("coverage", coverageNode())),
}
