// SPDX-License-Identifier: MIT

package checkgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// schemaTypes maps the JSON Schema types the fields check reads to its own.
var schemaTypes = map[string]string{"string": "string", "number": "number", "integer": "number", "boolean": "bool", "array": "list"}

// schemaRead is every keyword SchemaFields reads; annotations change nothing, so they pass silently.
var schemaRead = []string{"type", "properties", "required", "pattern", "format", "$schema", "$id", "title", "description"}

type schemaNode struct {
	Type       json.RawMessage `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Required   []string        `json:"required"`
	Pattern    string          `json:"pattern"`
	Format     string          `json:"format"`
	props      []schemaProp
}

// schemaProp is one property in the order the schema wrote it, which a split into chunks keeps.
type schemaProp struct {
	name string
	raw  json.RawMessage
}

// SchemaFields reads a JSON Schema's top-level properties, in order, as the field list, an
// object one level down as dotted names. It also returns the keywords it did not read, sorted.
func SchemaFields(raw []byte) ([]Field, []string, error) {
	unknown := map[string]bool{}
	root, err := readNode(raw, unknown)
	if err != nil {
		return nil, nil, fmt.Errorf("schema: %w", err)
	}
	if len(root.props) == 0 {
		return nil, nil, errors.New("schema: no top-level properties to check")
	}
	var fields []Field
	for _, p := range root.props {
		f, err := schemaField(p.name, p.raw, slices.Contains(root.Required, p.name), unknown)
		if err != nil {
			return nil, nil, err
		}
		fields = append(fields, f...)
	}
	return fields, slices.Sorted(maps.Keys(unknown)), nil
}

// schemaField is one property, or an object's properties as parent.child, each required only when both are.
func schemaField(name string, raw json.RawMessage, required bool, unknown map[string]bool) ([]Field, error) {
	n, err := readNode(raw, unknown)
	if err != nil {
		return nil, fmt.Errorf("schema: %s: %w", name, err)
	}
	t, err := n.typeName()
	if err != nil {
		return nil, fmt.Errorf("schema: %s: %w", name, err)
	}
	if t != "object" {
		return []Field{newSchemaField(name, n, t, required, unknown)}, nil
	}
	var out []Field
	for _, p := range n.props {
		child := p.name
		c, err := readNode(p.raw, unknown)
		if err != nil {
			return nil, fmt.Errorf("schema: %s.%s: %w", name, child, err)
		}
		ct, err := c.typeName()
		if err == nil && ct == "object" {
			err = errors.New("objects nest more than one level deep, which the fields check does not read; flatten the schema or check this part another way")
		}
		if err != nil {
			return nil, fmt.Errorf("schema: %s.%s: %w", name, child, err)
		}
		out = append(out, newSchemaField(name+"."+child, c, ct, required && slices.Contains(n.Required, child), unknown))
	}
	return out, nil
}

// newSchemaField marks strings and numbers as copied from the source; Generate drops that with no source.
// A string's pattern, or its format where a pattern is known for it, is held to a regular expression;
// one Go's RE2 cannot compile is reported as a keyword not read, not guessed at.
func newSchemaField(name string, n schemaNode, t string, required bool, unknown map[string]bool) Field {
	f := Field{Name: name, Type: t, Required: required, FromSource: t == "string" || t == "number"}
	if t != "string" {
		return f
	}
	if f.Pattern = n.Pattern; f.Pattern == "" {
		f.Pattern = schemaFormats[n.Format]
	}
	if _, err := anchored(f.Pattern); err != nil {
		f.Pattern, unknown["pattern"] = "", true
	}
	return f
}

func readNode(raw []byte, unknown map[string]bool) (schemaNode, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return schemaNode{}, errors.New("not a JSON object")
	}
	for k := range keys {
		if !slices.Contains(schemaRead, k) {
			unknown[k] = true
		}
	}
	var n schemaNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return schemaNode{}, err
	}
	if len(n.Properties) == 0 {
		return n, nil
	}
	dec := json.NewDecoder(bytes.NewReader(n.Properties))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return schemaNode{}, errors.New(`"properties" is not an object`)
	}
	for dec.More() {
		var p schemaProp
		key, err := dec.Token()
		if err == nil {
			p.name, _ = key.(string)
			err = dec.Decode(&p.raw)
		}
		if err != nil {
			return schemaNode{}, err
		}
		n.props = append(n.props, p)
	}
	return n, nil
}

// typeName reads type as one name, or a name and "null"; an object is "object".
func (n schemaNode) typeName() (string, error) {
	var names []string
	if err := json.Unmarshal(n.Type, &names); err != nil {
		var one string
		if json.Unmarshal(n.Type, &one) != nil {
			return "", errors.New(`no "type"`)
		}
		names = []string{one}
	}
	names = slices.DeleteFunc(names, func(s string) bool { return s == "null" })
	if len(names) == 1 && names[0] == "object" {
		return "object", nil
	}
	if len(names) == 1 && schemaTypes[names[0]] != "" {
		return schemaTypes[names[0]], nil
	}
	return "", fmt.Errorf("type %s is not one of string, number, integer, boolean, array or object", n.Type)
}
