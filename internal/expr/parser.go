package expr

import (
	"errors"

	"github.com/bytedance/sonic"
)

// Parse converts the rule "when" JSON blob into an evaluable AST.
// Recognized top-level keys (single-key objects): all / any / not /
// gt / lt / gte / lte / eq / cross_up / cross_down / has_position /
// pnl_pct / days_held / ratio_from_entry.
// Strings are RefNodes, numbers are ConstNodes.
func Parse(raw []byte) (Node, error) {
	var v any
	if err := sonic.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return parseValue(v)
}

func parseValue(v any) (Node, error) {
	switch x := v.(type) {
	case nil:
		return nil, errors.New("expr: null is not a valid expression")
	case bool:
		return BoolNode{Value: x}, nil
	case float64:
		return ConstNode{Value: x}, nil
	case string:
		return RefNode{Name: x}, nil
	case map[string]any:
		if len(x) != 1 {
			return nil, errors.New("expr: object must have exactly one operator key")
		}
		for k, arg := range x {
			return parseOp(k, arg)
		}
	}
	return nil, errors.New("expr: unsupported value type")
}

func parseOp(op string, arg any) (Node, error) {
	switch op {
	case "all":
		return parseChildren(arg, func(c []Node) Node { return AndNode{Children: c} })
	case "any":
		return parseChildren(arg, func(c []Node) Node { return OrNode{Children: c} })
	case "not":
		child, err := parseValue(arg)
		if err != nil {
			return nil, err
		}
		return NotNode{Child: child}, nil
	case "gt", "lt", "gte", "lte", "eq":
		return parseCmp(CmpOp(op), arg)
	case "cross_up":
		return parseCross(CrossUp, arg)
	case "cross_down":
		return parseCross(CrossDown, arg)
	case "has_position", "pnl_pct", "days_held":
		if arg != nil && arg != true {
			return nil, errors.New("expr: " + op + " expects null or true")
		}
		return CallNode{Op: op}, nil
	case "ratio_from_entry":
		child, err := parseValue(arg)
		if err != nil {
			return nil, err
		}
		return CallNode{Op: op, Arg: child}, nil
	}
	return nil, errors.New("expr: unknown operator " + op)
}

func parseChildren(arg any, build func([]Node) Node) (Node, error) {
	arr, ok := arg.([]any)
	if !ok {
		return nil, errors.New("expr: all/any expects an array")
	}
	children := make([]Node, 0, len(arr))
	for _, item := range arr {
		c, err := parseValue(item)
		if err != nil {
			return nil, err
		}
		children = append(children, c)
	}
	return build(children), nil
}

func parseCmp(op CmpOp, arg any) (Node, error) {
	arr, ok := arg.([]any)
	if !ok || len(arr) != 2 {
		return nil, errors.New("expr: " + string(op) + " expects [lhs, rhs]")
	}
	lhs, err := parseValue(arr[0])
	if err != nil {
		return nil, err
	}
	rhs, err := parseValue(arr[1])
	if err != nil {
		return nil, err
	}
	return CmpNode{Op: op, Lhs: lhs, Rhs: rhs}, nil
}

func parseCross(dir CrossDir, arg any) (Node, error) {
	arr, ok := arg.([]any)
	if !ok || len(arr) != 2 {
		return nil, errors.New("expr: cross expects [a, b]")
	}
	a, err := parseValue(arr[0])
	if err != nil {
		return nil, err
	}
	b, err := parseValue(arr[1])
	if err != nil {
		return nil, err
	}
	return CrossNode{Dir: dir, A: a, B: b}, nil
}

// CollectRefs returns the alias names referenced in the AST (excluding OHLCV
// fields), used by validation to ensure every alias was declared.
func CollectRefs(n Node) []string {
	seen := map[string]struct{}{}
	var walk func(Node)
	walk = func(node Node) {
		switch x := node.(type) {
		case AndNode:
			for _, c := range x.Children {
				walk(c)
			}
		case OrNode:
			for _, c := range x.Children {
				walk(c)
			}
		case NotNode:
			walk(x.Child)
		case CmpNode:
			walk(x.Lhs)
			walk(x.Rhs)
		case CrossNode:
			walk(x.A)
			walk(x.B)
		case CallNode:
			if x.Arg != nil {
				walk(x.Arg)
			}
		case RefNode:
			switch x.Name {
			case "open", "high", "low", "close", "volume":
				return
			}
			alias := x.Name
			for k := 0; k < len(alias); k++ {
				if alias[k] == '.' {
					alias = alias[:k]
					break
				}
			}
			seen[alias] = struct{}{}
		}
	}
	walk(n)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}
