package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Node is the node definition the executor consumes, decoupled from domain; the
// application layer maps domain.PipelineNode onto it.
type Node struct {
	Key    string
	Type   string
	Config json.RawMessage
}

// Edge is a directed edge between nodes. Condition is an optional routing condition (see
// EdgeCondition): when non-empty, the edge activates and the message flows downstream only
// if the upstream node's output payload satisfies it; when empty it is unconditional and an
// upstream Pass activates it.
type Edge struct {
	From      string
	To        string
	Condition json.RawMessage
}

// Executor runs a DAG pipeline synchronously in topological order. A node can fan out to
// several downstream edges — parallel branches, whose payloads are isolated from each
// other — and edges can carry conditions for routing. A node dropping or failing prunes
// only its own downstream branch, leaving other parallel branches alone. Execution is
// always single-threaded in topological order: strongly deterministic, with no concurrency.
// Registry supplies the node processors.
type Executor struct {
	reg *Registry
}

func NewExecutor(reg *Registry) *Executor { return &Executor{reg: reg} }

// Run sends msg through the pipeline formed by nodes and edges. With dryRun=true each
// node's output is snapshotted into NodeTrace.Output, and AI nodes should skip
// persistence (each node decides for itself, based on rc.DryRun).
//
// The execution model (a DAG, walked in topological order):
//   - Every node gets its own input payload: a root node gets a copy of the initial
//     payload, and a non-root node gets a shallow merge of the outputs of those parents
//     that both ran and had their inbound edge activated (later parents in topological
//     order win). Note that the isolation is shallow: top-level keys are per-branch, but
//     nested map and slice values remain shared references. A node should therefore
//     replace a field it writes back wholesale (as the built-in nodes do, with
//     payload[k]=newVal) and never mutate a nested structure read from upstream in place,
//     or parallel branches will interfere with each other.
//   - After a node runs, Pass evaluates each outbound edge's condition to decide which
//     downstream nodes to activate; Drop or a failure activates none, which prunes that
//     branch's downstream.
//   - A node delivered by no activated inbound edge is skipped and does not appear in
//     Traces — matching the earlier short-circuit behavior, where "did not run" means
//     absent.
//   - Final state: any hard node failure gives failed; otherwise a terminal node ending
//     in Pass gives passed; otherwise (every branch ended in Drop) dropped.
func (e *Executor) Run(ctx context.Context, nodes []Node, edges []Edge, msg *Message, dryRun bool) ExecResult {
	res := ExecResult{Status: statusPassed}

	ordered, err := topoSort(nodes, edges)
	if err != nil {
		res.Status = statusFailed
		res.Err = err
		return res
	}

	if msg.Meta == nil {
		msg.Meta = map[string]any{}
	}
	initial := msg.Payload
	if initial == nil {
		initial = map[string]any{}
	}

	// Edge-level adjacency, indexed by position in the original edges slice, which naturally
	// supports multiple edges between the same pair of nodes.
	inIdx := make(map[string][]int, len(nodes))
	outIdx := make(map[string][]int, len(nodes))
	for i, ed := range edges {
		inIdx[ed.To] = append(inIdx[ed.To], i)
		outIdx[ed.From] = append(outIdx[ed.From], i)
	}
	edgeActive := make([]bool, len(edges))

	rc := &RunContext{DryRun: dryRun}
	outputs := make(map[string]map[string]any, len(nodes)) // output payload of each node that ran
	executed := make(map[string]bool, len(nodes))
	action := make(map[string]Action, len(nodes)) // final action of each node that ran, used to decide the overall state
	// res.DroppedAtNode / FailedAtNode are filled in at the end according to the overall
	// state, preserving the invariant "the field is non-empty if and only if the overall
	// state matches it". When one branch drops or fails but another passes, the overall
	// state is still passed and these fields must stay empty — otherwise a consumer (the
	// event list UI, say, which renders "dropped at X" purely from dropped_at_node) would
	// mislabel a passing event as dropped. Branch-level drops and failures remain visible
	// in the per-node Traces.
	var anyFailed bool
	var firstFailNode, firstDropNode string
	var firstErr error

	for _, n := range ordered {
		// 1) Compute the input payload and decide whether this node is reachable.
		ins := inIdx[n.Key]
		var input map[string]any
		if len(ins) == 0 {
			input = clonePayload(initial) // a root node
		} else {
			input = map[string]any{}
			delivered := false
			for _, i := range ins {
				ed := edges[i]
				if !executed[ed.From] || !edgeActive[i] {
					continue
				}
				for k, v := range outputs[ed.From] { // shallow merge across parents
					input[k] = v
				}
				delivered = true
			}
			if !delivered {
				continue // no activated inbound edge delivered it: pruned, absent from Traces
			}
		}

		// 2) Run the node. Each node owns its own Message (sharing Meta), which is what gives
		// each branch its own payload.
		rc.Msg = &Message{ID: msg.ID, Payload: input, Meta: msg.Meta}
		rc.NodeKey = n.Key
		rc.nodeNote = ""
		rc.deliveryPreview = nil
		executed[n.Key] = true
		outputs[n.Key] = input

		p, ok := e.reg.Get(n.Type)
		if !ok {
			perr := fmt.Errorf("unknown node type %q at node %q", n.Type, n.Key)
			res.Traces = append(res.Traces, NodeTrace{NodeKey: n.Key, NodeType: n.Type, Error: perr.Error()})
			anyFailed = true
			if firstFailNode == "" {
				firstFailNode = n.Key
				firstErr = perr
			}
			continue // activate no outbound edge, pruning what is downstream
		}

		start := time.Now()
		act, perr := p.Process(ctx, rc, n.Config)
		action[n.Key] = act
		trace := NodeTrace{
			NodeKey:   n.Key,
			NodeType:  n.Type,
			Action:    act.String(),
			LatencyMS: nowMillis(start),
		}
		if dryRun {
			trace.Output = clonePayload(input)
			trace.DeliveryPreview = rc.deliveryPreview
		}

		if perr != nil {
			trace.Error = perr.Error()
			res.Traces = append(res.Traces, trace)
			anyFailed = true
			if firstFailNode == "" {
				firstFailNode = n.Key
				firstErr = perr
			}
			continue // failed: prune this node's downstream
		}

		// Record a non-fatal remark (such as the failure reason when on_error=continue lets it
		// through) in the lineage; the pass action is unchanged.
		if rc.nodeNote != "" {
			trace.Error = rc.nodeNote
		}
		res.Traces = append(res.Traces, trace)

		if act == ActionDrop {
			if firstDropNode == "" {
				firstDropNode = n.Key
			}
			continue // dropped: prune this node's downstream
		}

		// 3) Pass: activate outbound edges according to their conditions.
		for _, i := range outIdx[n.Key] {
			edgeActive[i] = edgeActiveOnPass(edges[i], outputs[n.Key])
		}
	}

	res.AIResults = rc.AIResults()
	res.Status, res.FinalPayload = e.summarize(ordered, outIdx, edgeActive, executed, action, outputs, initial, anyFailed)
	// Fill in the node fields only when the overall state matches, preserving the
	// "non-empty if and only if the state matches" invariant.
	switch res.Status {
	case statusFailed:
		res.FailedAtNode = firstFailNode
		res.Err = firstErr
	case statusDropped:
		res.DroppedAtNode = firstDropNode
	}
	return res
}

// summarize computes the final state and the final payload. A terminal node is one that
// ran and has no activated outbound edge; FinalPayload is the shallow merge of every
// terminal node's output (for a linear chain that is just the last node, as before).
func (e *Executor) summarize(
	ordered []Node, outIdx map[string][]int, edgeActive []bool,
	executed map[string]bool, action map[string]Action, outputs map[string]map[string]any,
	initial map[string]any, anyFailed bool,
) (string, map[string]any) {
	final := map[string]any{}
	hasTerminal := false
	passedLeaf := false
	for _, n := range ordered {
		if !executed[n.Key] {
			continue
		}
		terminal := true
		for _, i := range outIdx[n.Key] {
			if edgeActive[i] {
				terminal = false
				break
			}
		}
		if !terminal {
			continue
		}
		hasTerminal = true
		for k, v := range outputs[n.Key] {
			final[k] = v
		}
		if action[n.Key] == ActionPass {
			passedLeaf = true
		}
	}
	if !hasTerminal {
		final = clonePayload(initial) // fallback for an empty pipeline
	}

	switch {
	case anyFailed:
		return statusFailed, final
	case passedLeaf:
		return statusPassed, final
	default:
		return statusDropped, final
	}
}

// edgeActiveOnPass decides whether an outbound edge activates after an upstream Pass: an
// unconditional edge always does, and a conditional one is evaluated against the upstream
// output. A condition that fails to parse — which validation at save time should already
// have caught — is conservatively treated as inactive.
func edgeActiveOnPass(ed Edge, output map[string]any) bool {
	cond, err := ParseCondition(ed.Condition)
	if err != nil {
		return false // conservative: a bad condition does not open the branch
	}
	if cond == nil {
		return true // unconditional
	}
	return cond.Eval(output)
}

// TopoCheck exposes the topological check: it returns either the sorted order or an error
// for a cycle or an invalid edge, so the service layer can validate a pipeline's structure
// before saving it.
func TopoCheck(nodes []Node, edges []Edge) ([]Node, error) { return topoSort(nodes, edges) }

// topoSort topologically sorts the nodes. Zero-in-degree nodes are dequeued in their
// original order within nodes, which gives a linear chain a deterministic and intuitive
// execution order. A cycle is an error.
func topoSort(nodes []Node, edges []Edge) ([]Node, error) {
	if len(nodes) == 0 {
		return nil, nil
	}

	byKey := make(map[string]Node, len(nodes))
	order := make(map[string]int, len(nodes))
	indeg := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if n.Key == "" {
			return nil, fmt.Errorf("node at index %d has empty key", i)
		}
		if _, dup := byKey[n.Key]; dup {
			return nil, fmt.Errorf("duplicate node key %q", n.Key)
		}
		byKey[n.Key] = n
		order[n.Key] = i
		indeg[n.Key] = 0
	}

	adj := make(map[string][]string, len(nodes))
	for _, ed := range edges {
		if _, ok := byKey[ed.From]; !ok {
			return nil, fmt.Errorf("edge references unknown node %q", ed.From)
		}
		if _, ok := byKey[ed.To]; !ok {
			return nil, fmt.Errorf("edge references unknown node %q", ed.To)
		}
		adj[ed.From] = append(adj[ed.From], ed.To)
		indeg[ed.To]++
	}

	// A simple selection-sort-flavored Kahn, using the original order as the tie-breaker.
	visited := make(map[string]bool, len(nodes))
	out := make([]Node, 0, len(nodes))
	for len(out) < len(nodes) {
		next := ""
		nextOrder := -1
		for k, d := range indeg {
			if visited[k] || d != 0 {
				continue
			}
			if next == "" || order[k] < nextOrder {
				next = k
				nextOrder = order[k]
			}
		}
		if next == "" {
			return nil, fmt.Errorf("pipeline has a cycle")
		}
		visited[next] = true
		out = append(out, byKey[next])
		for _, to := range adj[next] {
			indeg[to]--
		}
	}
	return out, nil
}

func clonePayload(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}
