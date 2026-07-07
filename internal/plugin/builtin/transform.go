package builtin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
)

// Transform is one JSONPath-lite field manipulation (spec/14-plugins.md
// §4). Target addresses a field relative to the message root, e.g.
// "$.params.arguments.env" or "$.result.content[0].text".
type Transform struct {
	Hook   string // "before" | "after"
	Target string
	Action string // "set" | "delete" | "prefix" | "suffix" | "replace"
	Value  string
}

// TransformPlugin applies configured field manipulations to requests
// (Before) and responses (After). Unlike full JSONPath, Target here only
// supports the shape spec/14's own example uses: dot-separated field names
// with an optional "[N]" array index on any segment — enough to reach into
// tool call arguments and response content without pulling in a JSONPath
// library for a feature whose whole config surface is a handful of fields.
type TransformPlugin struct {
	before []Transform
	after  []Transform
}

// NewTransformPlugin partitions transforms by hook so Before/After don't
// re-filter the full list on every call.
func NewTransformPlugin(transforms []Transform) *TransformPlugin {
	p := &TransformPlugin{}
	for _, t := range transforms {
		switch t.Hook {
		case "after":
			p.after = append(p.after, t)
		default:
			p.before = append(p.before, t)
		}
	}
	return p
}

func (p *TransformPlugin) Name() string    { return "transform" }
func (p *TransformPlugin) Version() string { return "1.0.0" }

func (p *TransformPlugin) Before(_ context.Context, req *mcp.Message) (*mcp.Message, error) {
	if len(p.before) == 0 {
		return req, nil
	}
	tree := messageToTree(req)
	for _, t := range p.before {
		applyTransform(tree, t)
	}
	return treeToMessage(req, tree), nil
}

func (p *TransformPlugin) After(_ context.Context, _, resp *mcp.Message) (*mcp.Message, error) {
	if len(p.after) == 0 {
		return resp, nil
	}
	tree := messageToTree(resp)
	for _, t := range p.after {
		applyTransform(tree, t)
	}
	return treeToMessage(resp, tree), nil
}

func (p *TransformPlugin) Shutdown(context.Context) error { return nil }

// messageToTree unmarshals a message's params/result into a generic tree
// keyed exactly as a Target path expects: {"params": ..., "result": ...}.
func messageToTree(msg *mcp.Message) map[string]any {
	tree := map[string]any{}
	if len(msg.Params) > 0 {
		var v any
		if json.Unmarshal(msg.Params, &v) == nil {
			tree["params"] = v
		}
	}
	if len(msg.Result) > 0 {
		var v any
		if json.Unmarshal(msg.Result, &v) == nil {
			tree["result"] = v
		}
	}
	return tree
}

// treeToMessage re-marshals tree's "params"/"result" keys back onto a copy
// of the original message, leaving jsonrpc/id/method/error untouched.
func treeToMessage(original *mcp.Message, tree map[string]any) *mcp.Message {
	out := *original
	if v, ok := tree["params"]; ok {
		if b, err := json.Marshal(v); err == nil {
			out.Params = b
		}
	}
	if v, ok := tree["result"]; ok {
		if b, err := json.Marshal(v); err == nil {
			out.Result = b
		}
	}
	return &out
}

// pathSegment is one "." delimited piece of a Target, with an optional
// "[N]" array index (index -1 means "no index").
type pathSegment struct {
	key   string
	index int
}

func parsePath(path string) []pathSegment {
	path = strings.TrimPrefix(path, "$.")
	parts := strings.Split(path, ".")
	segments := make([]pathSegment, 0, len(parts))
	for _, part := range parts {
		key, index := part, -1
		if open := strings.IndexByte(part, '['); open != -1 && strings.HasSuffix(part, "]") {
			key = part[:open]
			if n, err := strconv.Atoi(part[open+1 : len(part)-1]); err == nil {
				index = n
			}
		}
		segments = append(segments, pathSegment{key: key, index: index})
	}
	return segments
}

// applyTransform navigates tree to t.Target's parent container and applies
// t.Action to the final field. Any failure to resolve the path (missing
// field, wrong type, out-of-range index) is silently skipped — a transform
// naming a field this particular message doesn't have is a configuration
// mismatch, not a reason to break the call.
func applyTransform(tree map[string]any, t Transform) {
	segments := parsePath(t.Target)
	if len(segments) == 0 {
		return
	}

	parent, ok := navigateToParent(tree, segments[:len(segments)-1])
	if !ok {
		return
	}
	last := segments[len(segments)-1]

	container, key, ok := resolveContainer(parent, last)
	if !ok {
		return
	}

	switch t.Action {
	case "set", "replace":
		setField(container, key, t.Value)
	case "delete":
		deleteField(container, key)
	case "prefix":
		if s, ok := getField(container, key).(string); ok {
			setField(container, key, t.Value+s)
		}
	case "suffix":
		if s, ok := getField(container, key).(string); ok {
			setField(container, key, s+t.Value)
		}
	}
}

// navigateToParent walks every segment except the last, descending through
// maps (by key) and slices (by index), and returns the container the final
// segment lives in.
func navigateToParent(root any, segments []pathSegment) (any, bool) {
	cur := root
	for _, seg := range segments {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, exists := m[seg.key]
		if !exists {
			return nil, false
		}
		if seg.index >= 0 {
			arr, ok := next.([]any)
			if !ok || seg.index >= len(arr) {
				return nil, false
			}
			next = arr[seg.index]
		}
		cur = next
	}
	return cur, true
}

// container is either a map[string]any (addressed by a string key) or a
// []any (addressed by an integer index encoded as a string).
type container struct {
	m   map[string]any
	arr []any
	idx int
}

// resolveContainer resolves the final path segment against parent, which is
// always a map (every message tree's non-leaf nodes are objects in
// practice for the paths this plugin targets) — it returns the concrete
// container and key/index to read or write.
func resolveContainer(parent any, seg pathSegment) (container, string, bool) {
	m, ok := parent.(map[string]any)
	if !ok {
		return container{}, "", false
	}
	if seg.index < 0 {
		return container{m: m}, seg.key, true
	}
	arr, ok := m[seg.key].([]any)
	if !ok || seg.index >= len(arr) {
		return container{}, "", false
	}
	return container{arr: arr, idx: seg.index}, "", true
}

func getField(c container, key string) any {
	if c.arr != nil {
		return c.arr[c.idx]
	}
	return c.m[key]
}

func setField(c container, key string, value any) {
	if c.arr != nil {
		c.arr[c.idx] = value
		return
	}
	c.m[key] = value
}

func deleteField(c container, key string) {
	if c.arr != nil {
		c.arr[c.idx] = nil
		return
	}
	delete(c.m, key)
}

var _ plugin.ConduitPlugin = (*TransformPlugin)(nil)
