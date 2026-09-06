package replay_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	ocpp "github.com/road-labs/ocpp-types-go"
	types16 "github.com/road-labs/ocpp-types-go/gen/ocpp16"
	types201 "github.com/road-labs/ocpp-types-go/gen/ocpp201"

	"github.com/road-labs/ocpp-trace-replayer/internal/replay"
	"github.com/road-labs/ocpp-trace-replayer/internal/trace"
)

// fakeCallWriter satisfies replay.callWriter. It records every WriteCall the Replayer makes and
// returns caller-supplied responses in order.
type fakeCallWriter struct {
	responses []any
	calls     []capturedCall
}

type capturedCall struct {
	messageID string
	action    string
	req       any
}

func (f *fakeCallWriter) WriteCall(_ context.Context, messageID, action string, req any) (any, error) {
	idx := len(f.calls)
	f.calls = append(f.calls, capturedCall{messageID: messageID, action: action, req: req})
	if idx < len(f.responses) {
		return f.responses[idx], nil
	}
	return nil, nil
}

func newTrace(t *testing.T, records []trace.Record) *trace.Trace {
	t.Helper()
	tr := trace.New(records)
	tr.OCPPVersion = ocpp.Version16
	return tr
}

func rec(dir, msgType, msgID, action, payload string) trace.Record {
	return trace.Record{
		Timestamp:   time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		Direction:   dir,
		MessageType: msgType,
		MessageID:   msgID,
		Action:      action,
		Payload:     json.RawMessage(payload),
	}
}

func TestRun_SkipsNonReplayableRecords(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCSMStoCP, trace.MsgCall, "1", "RemoteStartTransaction", `{}`),
		rec(trace.DirCPtoCSMS, trace.MsgCallResult, "2", "", `{}`),
		rec(trace.DirCPtoCSMS, trace.MsgCall, "3", "Heartbeat", `{}`),
	})
	w := &fakeCallWriter{}

	if err := replay.NewReplayer(tr, w, replay.Config{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(w.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(w.calls))
	}
	if w.calls[0].action != "Heartbeat" {
		t.Errorf("action = %q, want Heartbeat", w.calls[0].action)
	}
}

func TestRun_ActionAllowlist(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "1", "Heartbeat", `{}`),
		rec(trace.DirCPtoCSMS, trace.MsgCall, "2", "Authorize", `{"idTag":"ABC"}`),
	})
	w := &fakeCallWriter{}

	err := replay.NewReplayer(tr, w, replay.Config{Actions: []string{"Authorize"}}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(w.calls) != 1 || w.calls[0].action != "Authorize" {
		t.Errorf("expected single Authorize call, got %+v", w.calls)
	}
}

func TestRun_KeepsTraceMessageIDByDefault(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "orig-id", "Heartbeat", `{}`),
	})
	w := &fakeCallWriter{}

	if err := replay.NewReplayer(tr, w, replay.Config{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w.calls[0].messageID != "orig-id" {
		t.Errorf("messageID = %q, want orig-id", w.calls[0].messageID)
	}
}

func TestRun_RewriteMessageID(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "orig-id", "Heartbeat", `{}`),
	})
	w := &fakeCallWriter{}

	if err := replay.NewReplayer(tr, w, replay.Config{RewriteMessageID: true}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w.calls[0].messageID == "orig-id" || w.calls[0].messageID == "" {
		t.Errorf("expected fresh UUID, got %q", w.calls[0].messageID)
	}
}

func TestRun_MintsFreshMessageIDWhenTraceHasNone(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "", "Heartbeat", `{}`),
	})
	w := &fakeCallWriter{}

	if err := replay.NewReplayer(tr, w, replay.Config{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w.calls[0].messageID == "" {
		t.Error("expected fresh UUID when trace record has no messageId")
	}
}

func TestRun_IDTagRewrite(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "1", "Authorize", `{"idTag":"ABC"}`),
	})
	w := &fakeCallWriter{}

	cfg := replay.Config{IDTagMappings: map[string]string{"ABC": "XYZ"}}
	if err := replay.NewReplayer(tr, w, cfg).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sent, ok := w.calls[0].req.(*types16.Authorize)
	if !ok {
		t.Fatalf("req is %T, want *types16.Authorize", w.calls[0].req)
	}
	if sent.IDTag != "XYZ" {
		t.Errorf("sent IDTag = %q, want XYZ", sent.IDTag)
	}
}

func TestRun_IDTokenRewriteOn21TransactionEvent(t *testing.T) {
	tr := trace.New([]trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "1", "TransactionEvent",
			`{"eventType":"Started","triggerReason":"Authorized","seqNo":0,"timestamp":"2026-08-20T12:00:00Z","transactionInfo":{"transactionId":"t1"},"idToken":{"idToken":"ABC","type":"Central"}}`),
	})
	tr.OCPPVersion = ocpp.Version21
	w := &fakeCallWriter{}

	cfg := replay.Config{IDTagMappings: map[string]string{"ABC": "XYZ"}}
	if err := replay.NewReplayer(tr, w, cfg).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sent := w.calls[0].req
	// Poke the nested IDToken.IDToken via reflection-avoiding type assertion path.
	type withIDToken struct {
		IDToken struct {
			IDToken string
		}
	}
	// Instead of importing types21 in the test we JSON-round-trip and read the field back.
	b, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal sent: %v", err)
	}
	var probe withIDToken
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatalf("unmarshal probe: %v", err)
	}
	if probe.IDToken.IDToken != "XYZ" {
		t.Errorf("sent idToken.idToken = %q, want XYZ", probe.IDToken.IDToken)
	}
}

func TestRun_RewriteTransactionID(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "1", "StartTransaction",
			`{"connectorId":1,"idTag":"ABC","meterStart":0,"timestamp":"2026-08-20T12:00:00Z"}`),
		rec(trace.DirCSMStoCP, trace.MsgCallResult, "1", "",
			`{"transactionId":100,"idTagInfo":{"status":"Accepted"}}`),
		rec(trace.DirCPtoCSMS, trace.MsgCall, "2", "StopTransaction",
			`{"transactionId":100,"meterStop":50,"timestamp":"2026-08-20T12:01:00Z"}`),
	})
	// The CSMS replies with a different transactionId — the Replayer should remap it into the
	// subsequent StopTransaction.
	w := &fakeCallWriter{responses: []any{
		&types16.StartTransactionResponse{TransactionID: 999, IDTagInfo: types16.StartTransactionResponseIDTagInfo{Status: types16.StartTransactionResponseIDTagInfoStatusAccepted}},
	}}

	cfg := replay.Config{RewriteTransactionID: true}
	if err := replay.NewReplayer(tr, w, cfg).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(w.calls) != 2 {
		t.Fatalf("expected 2 sent calls, got %d", len(w.calls))
	}
	stop, ok := w.calls[1].req.(*types16.StopTransaction)
	if !ok {
		t.Fatalf("call[1].req is %T, want *types16.StopTransaction", w.calls[1].req)
	}
	if stop.TransactionID != 999 {
		t.Errorf("StopTransaction.TransactionID = %d, want 999", stop.TransactionID)
	}
}

func TestRun_RewriteTransactionIDOff_LeavesIDsAlone(t *testing.T) {
	tr := newTrace(t, []trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "1", "StartTransaction",
			`{"connectorId":1,"idTag":"ABC","meterStart":0,"timestamp":"2026-08-20T12:00:00Z"}`),
		rec(trace.DirCSMStoCP, trace.MsgCallResult, "1", "",
			`{"transactionId":100,"idTagInfo":{"status":"Accepted"}}`),
		rec(trace.DirCPtoCSMS, trace.MsgCall, "2", "StopTransaction",
			`{"transactionId":100,"meterStop":50,"timestamp":"2026-08-20T12:01:00Z"}`),
	})
	w := &fakeCallWriter{responses: []any{
		&types16.StartTransactionResponse{TransactionID: 999},
	}}

	// Flag off → verbatim replay.
	if err := replay.NewReplayer(tr, w, replay.Config{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	stop := w.calls[1].req.(*types16.StopTransaction)
	if stop.TransactionID != 100 {
		t.Errorf("StopTransaction.TransactionID = %d, want 100 (verbatim)", stop.TransactionID)
	}
}

func TestRun_TimeWindowFilter(t *testing.T) {
	baseTime := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	makeRec := func(minute int) trace.Record {
		r := rec(trace.DirCPtoCSMS, trace.MsgCall, "", "Heartbeat", `{}`)
		r.Timestamp = baseTime.Add(time.Duration(minute) * time.Minute)
		return r
	}
	tr := newTrace(t, []trace.Record{makeRec(0), makeRec(5), makeRec(10)})
	w := &fakeCallWriter{}

	cfg := replay.Config{From: baseTime.Add(time.Minute), To: baseTime.Add(6 * time.Minute)}
	if err := replay.NewReplayer(tr, w, cfg).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(w.calls) != 1 {
		t.Errorf("expected 1 call (only the t+5 record), got %d", len(w.calls))
	}
}

func TestRun_UnusedIDTokenRewrite(t *testing.T) {
	// Types201 request without IDToken should be sent unchanged.
	tr := trace.New([]trace.Record{
		rec(trace.DirCPtoCSMS, trace.MsgCall, "1", "TransactionEvent",
			`{"eventType":"Updated","triggerReason":"MeterValuePeriodic","seqNo":1,"timestamp":"2026-08-20T12:00:00Z","transactionInfo":{"transactionId":"t1"}}`),
	})
	tr.OCPPVersion = ocpp.Version201

	w := &fakeCallWriter{}
	cfg := replay.Config{IDTagMappings: map[string]string{"ABC": "XYZ"}}
	if err := replay.NewReplayer(tr, w, cfg).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sent, ok := w.calls[0].req.(*types201.TransactionEventRequest)
	if !ok {
		t.Fatalf("req is %T, want *types201.TransactionEventRequest", w.calls[0].req)
	}
	if sent.IDToken != nil {
		t.Errorf("IDToken = %+v, want nil", sent.IDToken)
	}
}
