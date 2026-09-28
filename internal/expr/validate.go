package expr

import "errors"

// ValidateCondition checks all branches without evaluating against a sample
// portfolio. Short-circuit evaluation could otherwise hide an invalid operand
// until the first position is opened.
func ValidateCondition(node Node) error {
	typ, err := expressionType(node)
	if err != nil {
		return err
	}
	if typ != booleanType {
		return errors.New("expr: rule condition must return boolean; compare numeric helpers such as {\"gte\":[{\"days_held\":null},1]}")
	}
	return nil
}

type valueType uint8

const (
	numberType valueType = iota
	booleanType
)

func expressionType(node Node) (valueType, error) {
	switch n := node.(type) {
	case ConstNode, RefNode:
		return numberType, nil
	case BoolNode:
		return booleanType, nil
	case AndNode:
		return booleanChildren("all", n.Children)
	case OrNode:
		return booleanChildren("any", n.Children)
	case NotNode:
		return booleanChildren("not", []Node{n.Child})
	case CmpNode:
		left, err := expressionType(n.Lhs)
		if err != nil {
			return 0, err
		}
		right, err := expressionType(n.Rhs)
		if err != nil {
			return 0, err
		}
		if left != right || (n.Op != OpEQ && left != numberType) {
			return 0, errors.New("expr: comparisons require numeric operands; eq also accepts two boolean operands")
		}
		return booleanType, nil
	case CrossNode:
		for _, child := range []Node{n.A, n.B} {
			typ, err := expressionType(child)
			if err != nil {
				return 0, err
			}
			if typ != numberType {
				return 0, errors.New("expr: cross_up/cross_down require numeric operands")
			}
		}
		return booleanType, nil
	case CallNode:
		switch n.Op {
		case "has_position":
			return booleanType, nil
		case "days_held", "pnl_pct":
			return numberType, nil
		case "ratio_from_entry":
			ref, ok := n.Arg.(RefNode)
			if !ok || (ref.Name != "open" && ref.Name != "high" && ref.Name != "low" && ref.Name != "close" && ref.Name != "volume") {
				return 0, errors.New("expr: ratio_from_entry requires an OHLCV field reference")
			}
			return numberType, nil
		}
	}
	return 0, errors.New("expr: unsupported expression node")
}

func booleanChildren(op string, children []Node) (valueType, error) {
	if len(children) == 0 {
		return 0, errors.New("expr: " + op + " requires at least one boolean operand")
	}
	for _, child := range children {
		typ, err := expressionType(child)
		if err != nil {
			return 0, err
		}
		if typ != booleanType {
			return 0, errors.New("expr: " + op + " requires boolean operands; compare numeric helpers such as {\"gte\":[{\"days_held\":null},1]}")
		}
	}
	return booleanType, nil
}
