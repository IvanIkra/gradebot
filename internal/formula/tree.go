package formula

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Node is a node of the tree as JSON gives it: numbers as json.Number, float64 or int.
type Node = map[string]any

// Tree is a formula: named nodes in their order, and the final one.
type Tree struct {
	Names []string
	Nodes map[string]Node
	Final Node
	// what a tree read from JSON had at the top, for Check
	hasFinal  bool
	badNodes  bool
	finalJSON any
}

// NewTree makes a tree of named nodes (name, node, name, node, ...) and the final node.
func NewTree(final Node, named ...any) *Tree {
	t := &Tree{Nodes: map[string]Node{}, Final: final, hasFinal: true, finalJSON: final}
	for n := 0; n+1 < len(named); n += 2 {
		name := named[n].(string)
		t.Names = append(t.Names, name)
		t.Nodes[name] = named[n+1].(Node)
	}
	return t
}

// Named is the node of a name.
func (t *Tree) Named(name string) (Node, bool) {
	n, ok := t.Nodes[name]
	return n, ok
}

// Parse reads a tree from JSON, keeping the order of the named nodes.
func Parse(raw []byte) (*Tree, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, &Error{"a formula is {nodes, final}"}
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, &Error{"a formula is {nodes, final}"}
	}
	t := &Tree{Nodes: map[string]Node{}}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, &Error{"a formula is {nodes, final}"}
		}
		key, _ := keyTok.(string)
		switch key {
		case "nodes":
			if err := t.readNodes(dec); err != nil {
				return nil, err
			}
		case "final":
			var v any
			if err := dec.Decode(&v); err != nil {
				return nil, &Error{"a formula is {nodes, final}"}
			}
			t.hasFinal, t.finalJSON = true, v
			t.Final, _ = v.(Node)
		default:
			var skip any
			if err := dec.Decode(&skip); err != nil {
				return nil, &Error{"a formula is {nodes, final}"}
			}
		}
	}
	return t, nil
}

func (t *Tree) readNodes(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return &Error{"a formula is {nodes, final}"}
	}
	if tok == nil { // "nodes": null is no nodes
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.badNodes = true
		if ok && (d == '[') {
			var skip []any
			for dec.More() {
				var v any
				if err := dec.Decode(&v); err != nil {
					return &Error{"a formula is {nodes, final}"}
				}
				skip = append(skip, v)
			}
			_, _ = dec.Token()
		}
		return nil
	}
	for dec.More() {
		nameTok, err := dec.Token()
		if err != nil {
			return &Error{"a formula is {nodes, final}"}
		}
		name := nameTok.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return &Error{"a formula is {nodes, final}"}
		}
		if _, seen := t.Nodes[name]; !seen {
			t.Names = append(t.Names, name)
		}
		node, _ := v.(Node)
		if node == nil {
			node = Node{"__not_object": true}
		}
		t.Nodes[name] = node
	}
	_, err = dec.Token()
	return err
}

// MarshalJSON writes {"nodes": {...}, "final": ...} with the named nodes in order.
func (t *Tree) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"nodes": {`)
	for n, name := range t.Names {
		if n > 0 {
			b.WriteString(", ")
		}
		key, _ := marshal(name)
		value, err := marshal(t.Nodes[name])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteString(": ")
		b.Write(value)
	}
	b.WriteString(`}, "final": `)
	final, err := marshal(t.Final)
	if err != nil {
		return nil, err
	}
	b.Write(final)
	b.WriteString("}")
	return b.Bytes(), nil
}

// JSON is the tree as the database keeps it.
func (t *Tree) JSON() string {
	raw, err := t.MarshalJSON()
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// --- values of JSON ---------------------------------------------------------------------------------------------

// num is a number of the tree; false for anything else.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// isInt is Python's isinstance(v, int): a JSON number written without a point or an exponent.
func isInt(v any) bool {
	switch x := v.(type) {
	case json.Number:
		return !strings.ContainsAny(string(x), ".eE")
	case int, int64, bool:
		return true
	case float64:
		return false
	}
	return false
}

func f(v any) float64 {
	x, _ := num(v)
	return x
}

func intOf(v any) int {
	x, _ := num(v)
	return int(x)
}

// truthy is Python's truth of a JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case Node:
		return len(x) > 0
	}
	if n, ok := num(v); ok {
		return n != 0
	}
	return true
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func nodeOf(v any) Node {
	n, _ := v.(Node)
	return n
}

func get(n Node, key string, def any) any {
	if v, ok := n[key]; ok {
		return v
	}
	return def
}

// number is a number as written in words: f"{value:g}".
func number(v any) string {
	x, ok := num(v)
	if !ok {
		return fmt.Sprint(v)
	}
	return gfmt(x)
}

func gfmt(x float64) string {
	if x == math.Trunc(x) && math.Abs(x) < 1e6 {
		return fmt.Sprintf("%d", int64(x))
	}
	return pyG(x)
}
