package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// A stub node for tests: returns a fixed action or error, as configured.
type stubProcessor struct {
	typ    string
	action Action
	err    error
	mark   string         // on a hit, write a bool marker into the payload, to check execution order and short-circuiting
	writes map[string]any // on a hit, write arbitrary fields into the payload, to check merging and conditional routing
	note   string         // on a hit, call rc.Note, to check a non-fatal remark reaches the lineage
}

func (s *stubProcessor) Type() string                   { return s.typ }
func (s *stubProcessor) ConfigSchema() json.RawMessage  { return json.RawMessage(`{}`) }
func (s *stubProcessor) Validate(json.RawMessage) error { return nil }
func (s *stubProcessor) Process(_ context.Context, rc *RunContext, _ json.RawMessage) (Action, error) {
	if s.mark != "" {
		rc.Msg.Payload[s.mark] = true
	}
	for k, v := range s.writes {
		rc.Msg.Payload[k] = v
	}
	if s.note != "" {
		rc.Note(s.note)
	}
	return s.action, s.err
}

// traceByKey returns the named node's trace, or nil when there is none — meaning the node
// was pruned and never ran.
func traceByKey(res ExecResult, key string) *NodeTrace {
	for i := range res.Traces {
		if res.Traces[i].NodeKey == key {
			return &res.Traces[i]
		}
	}
	return nil
}

func newReg(ps ...Processor) *Registry {
	r := NewRegistry()
	for _, p := range ps {
		r.Register(p)
	}
	return r
}

func TestExecutor_LinearPass(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "a", action: ActionPass, mark: "a"},
		&stubProcessor{typ: "b", action: ActionPass, mark: "b"},
	)
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "n1", Type: "a"}, {Key: "n2", Type: "b"}}
	edges := []Edge{{From: "n1", To: "n2"}}

	res := ex.Run(context.Background(), nodes, edges, &Message{}, false)

	if res.Status != statusPassed {
		t.Fatalf("status = %s, want passed", res.Status)
	}
	if len(res.Traces) != 2 {
		t.Fatalf("traces = %d, want 2", len(res.Traces))
	}
	if res.Traces[0].NodeKey != "n1" || res.Traces[1].NodeKey != "n2" {
		t.Fatalf("execution order wrong: %v", res.Traces)
	}
}

func TestExecutor_DropShortCircuits(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "filter", action: ActionDrop},
		&stubProcessor{typ: "after", action: ActionPass, mark: "after"},
	)
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "f", Type: "filter"}, {Key: "x", Type: "after"}}
	edges := []Edge{{From: "f", To: "x"}}

	msg := &Message{}
	res := ex.Run(context.Background(), nodes, edges, msg, false)

	if res.Status != statusDropped {
		t.Fatalf("status = %s, want dropped", res.Status)
	}
	if res.DroppedAtNode != "f" {
		t.Fatalf("droppedAtNode = %s, want f", res.DroppedAtNode)
	}
	if len(res.Traces) != 1 {
		t.Fatalf("traces = %d, want 1 (short-circuit)", len(res.Traces))
	}
	if _, ran := msg.Payload["after"]; ran {
		t.Fatal("downstream node ran despite drop")
	}
}

func TestExecutor_NodeErrorFails(t *testing.T) {
	reg := newReg(&stubProcessor{typ: "boom", err: errors.New("kaboom")})
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "e", Type: "boom"}}

	res := ex.Run(context.Background(), nodes, nil, &Message{}, false)

	if res.Status != statusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if res.FailedAtNode != "e" || res.Err == nil {
		t.Fatalf("expected failure recorded at node e, got %+v", res)
	}
}

// A non-fatal remark (the failure reason when on_error=continue lets it through) must be
// recorded in that node's NodeTrace.Error, with the action still pass, and must not leak
// into downstream nodes.
func TestExecutor_NoteRecordedOnPassAndNotLeaked(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "ai", action: ActionPass, note: "ai_analysis: model call failed"},
		&stubProcessor{typ: "next", action: ActionPass},
	)
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "n1", Type: "ai"}, {Key: "n2", Type: "next"}}
	edges := []Edge{{From: "n1", To: "n2"}}

	res := ex.Run(context.Background(), nodes, edges, &Message{}, false)

	if res.Status != statusPassed {
		t.Fatalf("status = %s, want passed (continue must not count as a failure)", res.Status)
	}
	if res.Traces[0].Action != "pass" {
		t.Fatalf("n1 action = %s, want pass", res.Traces[0].Action)
	}
	if res.Traces[0].Error != "ai_analysis: model call failed" {
		t.Fatalf("n1 trace.Error = %q, should record the failure reason that was let through", res.Traces[0].Error)
	}
	if res.Traces[1].Error != "" {
		t.Fatalf("n2 trace.Error = %q, the remark must not leak downstream", res.Traces[1].Error)
	}
}

// Fan-out: with A -> B and A -> C, all three nodes should run and B and C should each see
// A's output.
func TestExecutor_FanOutBothBranchesRun(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "a", action: ActionPass, mark: "a"},
		&stubProcessor{typ: "b", action: ActionPass, mark: "b"},
		&stubProcessor{typ: "c", action: ActionPass, mark: "c"},
	)
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "A", Type: "a"}, {Key: "B", Type: "b"}, {Key: "C", Type: "c"}}
	edges := []Edge{{From: "A", To: "B"}, {From: "A", To: "C"}}

	res := ex.Run(context.Background(), nodes, edges, &Message{}, true)

	if res.Status != statusPassed {
		t.Fatalf("status = %s, want passed", res.Status)
	}
	if len(res.Traces) != 3 {
		t.Fatalf("traces = %d, want 3 (A,B,C all run)", len(res.Traces))
	}
	// The terminal nodes B and C have their outputs shallow-merged into FinalPayload.
	for _, k := range []string{"a", "b", "c"} {
		if res.FinalPayload[k] != true {
			t.Errorf("FinalPayload missing %q: %v", k, res.FinalPayload)
		}
	}
	// Payload isolation: the B branch must not see C's writes, or vice versa — checked via
	// trace.Output from the dry run.
	if bt := traceByKey(res, "B"); bt == nil || bt.Output["c"] == true {
		t.Errorf("branch B leaked C's write: %+v", bt)
	}
	if ct := traceByKey(res, "C"); ct == nil || ct.Output["b"] == true {
		t.Errorf("branch C leaked B's write: %+v", ct)
	}
}

// Branch-level pruning: A fans out to B(drop) -> B2 and C(pass) -> C2; B2 is pruned and
// absent, while C and C2 run as usual.
func TestExecutor_BranchDropPrunesOnlyThatBranch(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "a", action: ActionPass},
		&stubProcessor{typ: "bdrop", action: ActionDrop},
		&stubProcessor{typ: "b2", action: ActionPass, mark: "b2"},
		&stubProcessor{typ: "c", action: ActionPass},
		&stubProcessor{typ: "c2", action: ActionPass, mark: "c2"},
	)
	ex := NewExecutor(reg)
	nodes := []Node{
		{Key: "A", Type: "a"}, {Key: "B", Type: "bdrop"}, {Key: "B2", Type: "b2"},
		{Key: "C", Type: "c"}, {Key: "C2", Type: "c2"},
	}
	edges := []Edge{{From: "A", To: "B"}, {From: "B", To: "B2"}, {From: "A", To: "C"}, {From: "C", To: "C2"}}

	res := ex.Run(context.Background(), nodes, edges, &Message{}, false)

	if res.Status != statusPassed {
		t.Fatalf("status = %s, want passed (the C branch passes)", res.Status)
	}
	// When the overall state is passed, DroppedAtNode must stay empty, or consumers would
	// mislabel a passing event as dropped. The B branch's drop is still visible in its own
	// per-node trace.
	if res.DroppedAtNode != "" {
		t.Errorf("droppedAtNode = %q, want empty when overall passed", res.DroppedAtNode)
	}
	if bt := traceByKey(res, "B"); bt == nil || bt.Action != "drop" {
		t.Errorf("B's trace should record the drop action: %+v", bt)
	}
	if traceByKey(res, "B2") != nil {
		t.Error("B2 should be pruned and absent from traces")
	}
	if traceByKey(res, "C2") == nil {
		t.Error("C2 should run as usual")
	}
	if res.FinalPayload["c2"] != true {
		t.Errorf("FinalPayload should contain the C branch's result: %v", res.FinalPayload)
	}
}

// Multi-parent merging: roots A and B both feed C, and C should see a shallow merge of
// both outputs.
func TestExecutor_MergeShallowMerge(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "a", action: ActionPass, mark: "a"},
		&stubProcessor{typ: "b", action: ActionPass, mark: "b"},
		&stubProcessor{typ: "c", action: ActionPass, mark: "c"},
	)
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "A", Type: "a"}, {Key: "B", Type: "b"}, {Key: "C", Type: "c"}}
	edges := []Edge{{From: "A", To: "C"}, {From: "B", To: "C"}}

	res := ex.Run(context.Background(), nodes, edges, &Message{}, true)

	if res.Status != statusPassed {
		t.Fatalf("status = %s, want passed", res.Status)
	}
	ct := traceByKey(res, "C")
	if ct == nil {
		t.Fatal("C did not run")
	}
	for _, k := range []string{"a", "b", "c"} {
		if ct.Output[k] != true {
			t.Errorf("C's merged input is missing %q: %v", k, ct.Output)
		}
	}
}

// Conditional routing: A writes the route field; the A->B edge (route==x) activates while
// the A->C edge (route==y) does not.
func TestExecutor_ConditionRouting(t *testing.T) {
	reg := newReg(
		&stubProcessor{typ: "a", action: ActionPass, writes: map[string]any{"route": "x"}},
		&stubProcessor{typ: "b", action: ActionPass, mark: "b"},
		&stubProcessor{typ: "c", action: ActionPass, mark: "c"},
	)
	ex := NewExecutor(reg)
	nodes := []Node{{Key: "A", Type: "a"}, {Key: "B", Type: "b"}, {Key: "C", Type: "c"}}
	edges := []Edge{
		{From: "A", To: "B", Condition: json.RawMessage(`{"field":"route","op":"eq","value":"x"}`)},
		{From: "A", To: "C", Condition: json.RawMessage(`{"field":"route","op":"eq","value":"y"}`)},
	}

	res := ex.Run(context.Background(), nodes, edges, &Message{}, false)

	if traceByKey(res, "B") == nil {
		t.Error("B should run, activated by the matching conditional edge")
	}
	if traceByKey(res, "C") != nil {
		t.Error("C should be pruned because its condition does not match")
	}
}

func TestTopoSort_DetectsCycle(t *testing.T) {
	nodes := []Node{{Key: "a", Type: "x"}, {Key: "b", Type: "x"}}
	edges := []Edge{{From: "a", To: "b"}, {From: "b", To: "a"}}
	if _, err := topoSort(nodes, edges); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestTopoSort_OrdersByEdges(t *testing.T) {
	// The input order is the reverse of the topological order, checking that sorting follows
	// the edges rather than the input order.
	nodes := []Node{{Key: "b", Type: "x"}, {Key: "a", Type: "x"}}
	edges := []Edge{{From: "a", To: "b"}}
	out, err := topoSort(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Key != "a" || out[1].Key != "b" {
		t.Fatalf("order = %v, want [a b]", []string{out[0].Key, out[1].Key})
	}
}
