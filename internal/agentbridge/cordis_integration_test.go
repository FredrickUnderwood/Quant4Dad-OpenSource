//go:build bridgeintegration

package agentbridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// The Node fixture compiles this test once, owns the real Cordis/Gateway
// processes and supplies synthetic credentials. Normal go test excludes it;
// npm run test:control always runs all four cross-language scenarios.
type cordisInput struct {
	URL        string        `json:"url"`
	Token      string        `json:"token"`
	Phase      string        `json:"phase"`
	StateFile  string        `json:"state_file"`
	Session    SessionCreate `json:"session"`
	Prompt     PromptRequest `json:"prompt"`
	NextPrompt PromptRequest `json:"next_prompt"`
}
type cordisState struct {
	Created  SessionCreated   `json:"created"`
	Accepted PromptAccepted   `json:"accepted"`
	Run      Run              `json:"run"`
	Events   []Event          `json:"events"`
	Pages    []TranscriptPage `json:"pages"`
	Closed   SessionClosed    `json:"closed"`
}

func TestCordisBridge(t *testing.T) {
	var input cordisInput
	if decode([]byte(os.Getenv("Q4D_BRIDGE_TEST_INPUT")), &input) != nil {
		t.Fatal("missing/invalid Cordis integration fixture input")
	}
	client, err := New(Config{BaseURL: input.URL, Token: input.Token})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(client.Health(ctx))
	caps, err := client.Capabilities(ctx)
	must(err)
	must(caps.CheckCompatibility())
	hash, err := ProvisionHash(input.Session)
	must(err)
	if hash != input.Session.ProvisionRequestHash {
		t.Fatal("Go/JavaScript provisioning hashes differ")
	}
	created, err := client.CreateSession(ctx, input.Session)
	must(err)
	retry, err := client.CreateSession(ctx, input.Session)
	must(err)
	if created != retry {
		t.Fatal("create retry changed durable identity")
	}
	state := cordisState{Created: created}

	if input.Phase == "restart" {
		data, err := os.ReadFile(input.StateFile)
		must(err)
		var before cordisState
		// Public Event.Data decodes generically in this saved assertion artifact;
		// wire events themselves always pass through the typed SSE parser.
		must(wireJSON.Unmarshal(data, &before))
		if created != before.Created {
			t.Fatal("restart changed creation provenance")
		}
		run, err := client.GetRun(ctx, input.Session.SessionID, input.Prompt.RunID)
		must(err)
		if run != before.Run {
			t.Fatal("cold Run changed across process restart")
		}
		ack, err := client.Prompt(ctx, input.Prompt)
		must(err)
		if ack != before.Accepted {
			t.Fatal("completed retry changed admission")
		}
		var replay []Event
		_, err = client.ConsumeEvents(ctx, input.Session.SessionID, input.Prompt.RunID, "0", func(e Event) error { replay = append(replay, e); return nil })
		must(err)
		encoded, _ := wireJSON.Marshal(replay)
		var generic []Event
		must(wireJSON.Unmarshal(encoded, &generic))
		if !reflect.DeepEqual(generic, before.Events) {
			t.Fatal("cold replay changed events")
		}
		input.Prompt = input.NextPrompt
	}

	accepted, err := client.Prompt(ctx, input.Prompt)
	must(err)
	state.Accepted = accepted
	again, err := client.Prompt(ctx, input.Prompt)
	must(err)
	if accepted != again {
		t.Fatal("prompt retry changed admission")
	}
	var proposed ToolProposedData
	pause := errors.New("fixture pause before terminal persistence")
	callback := func(e Event) error {
		if p, ok := e.Data.(ToolProposedData); ok {
			proposed = p
		}
		if input.Phase == "first" && e.Terminal() {
			return pause
		}
		if input.Phase == "cancel" && e.Type == "tool.started" {
			_, err := client.CloseSession(ctx, input.Session.SessionID)
			assertBridgeError(t, err, "agent_run_in_progress", 409, false)
			_, err = client.CancelRun(ctx, input.Session.SessionID, input.Prompt.RunID)
			if err != nil {
				return err
			}
		}
		if p, ok := e.Data.(ApprovalRequiredData); ok {
			run, err := client.GetRun(ctx, input.Session.SessionID, input.Prompt.RunID)
			if err != nil {
				return err
			}
			if run.State != "waiting_approval" {
				t.Fatal("missing approval wait state")
			}
			decision := ApprovalDecision{RunID: input.Prompt.RunID, ToolCallID: p.ToolCallID, Decision: "reject"}
			if input.Phase == "allow" {
				// Only the fixture signer uses this pipe; approval still reaches the
				// runtime through Client.DecideApproval with the opaque receipt.
				data, _ := wireJSON.Marshal(proposed)
				fmt.Fprintln(os.Stdout, "Q4D_APPROVE\t"+string(data))
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Buffer(make([]byte, 1024), 8193)
				if !scanner.Scan() {
					t.Fatal("fixture receipt missing")
				}
				decision.Decision = "allow_once"
				decision.ApprovalReceipt = scanner.Text()
			}
			ack, err := client.DecideApproval(ctx, p.ApprovalID, decision)
			if err != nil {
				return err
			}
			if ack.RunID != decision.RunID || ack.ToolCallID != decision.ToolCallID || ack.Decision != decision.Decision {
				t.Fatal("approval acknowledgement mismatch")
			}
			_, err = client.DecideApproval(ctx, p.ApprovalID, decision)
			assertBridgeError(t, err, "agent_approval_missing", 409, false)
		}
		state.Events = append(state.Events, e)
		return nil
	}
	result, err := client.ConsumeEvents(ctx, input.Session.SessionID, input.Prompt.RunID, "0", callback)
	if input.Phase == "first" {
		if err != pause || result.LastEventID == "0" || result.TerminalType != "" {
			t.Fatal("unaccepted terminal advanced cursor")
		}
		result, err = client.ConsumeEvents(ctx, input.Session.SessionID, input.Prompt.RunID, result.LastEventID, func(e Event) error { state.Events = append(state.Events, e); return nil })
	}
	must(err)
	expected := "run.completed"
	if input.Phase == "cancel" {
		expected = "run.cancelled"
	}
	if result.TerminalType != expected {
		t.Fatal("unexpected terminal")
	}
	state.Run, err = client.GetRun(ctx, input.Session.SessionID, input.Prompt.RunID)
	must(err)
	if !state.Run.Terminal || state.Run.LastEventID != result.LastEventID || state.Run.MessageID != accepted.MessageID {
		t.Fatal("Run/stream admission identity mismatch")
	}
	late, err := client.CancelRun(ctx, input.Session.SessionID, input.Prompt.RunID)
	must(err)
	if late != state.Run {
		t.Fatal("late cancel rewrote completion")
	}
	if _, err := client.ConsumeEvents(ctx, input.Session.SessionID, input.Prompt.RunID, result.LastEventID, func(Event) error { t.Fatal("terminal replay duplicated event"); return nil }); err != ErrStreamInterrupted {
		t.Fatal("empty terminal replay must reconcile GetRun")
	}

	query := TranscriptQuery{Limit: 2}
	for pages := 0; pages < 100; pages++ {
		page, err := client.Transcript(ctx, input.Session.SessionID, query)
		must(err)
		state.Pages = append(state.Pages, page)
		if !page.HasMore {
			break
		}
		query.SnapshotSeq = page.SnapshotSeq
		query.BeforeSeq = *page.NextBeforeSeq
	}
	if len(state.Pages) == 0 || state.Pages[len(state.Pages)-1].HasMore {
		t.Fatal("transcript pagination did not terminate")
	}
	state.Closed, err = client.CloseSession(ctx, input.Session.SessionID)
	must(err)
	closed, err := client.CloseSession(ctx, input.Session.SessionID)
	must(err)
	if closed != state.Closed {
		t.Fatal("close is not idempotent")
	}
	encoded, err := wireJSON.Marshal(state)
	must(err)
	must(os.WriteFile(input.StateFile, encoded, 0600))
}
