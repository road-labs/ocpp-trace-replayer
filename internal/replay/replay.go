package replay

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/road-labs/ocpp-types-go"
	types15 "github.com/road-labs/ocpp-types-go/gen/ocpp15"
	types16 "github.com/road-labs/ocpp-types-go/gen/ocpp16"
	types201 "github.com/road-labs/ocpp-types-go/gen/ocpp201"
	types21 "github.com/road-labs/ocpp-types-go/gen/ocpp21"

	"github.com/road-labs/ocpp-trace-replayer/internal/trace"
)

type callWriter interface {
	WriteCall(ctx context.Context, messageID, action string, req any) (any, error)
}

type Config struct {
	// Filters
	Actions []string
	From    time.Time
	To      time.Time
	Delay   time.Duration

	// Rewrite behaviour
	RewriteMessageID     bool
	RewriteTransactionID bool
	IDTagMappings        map[string]string
}

type Replayer struct {
	cfg     Config
	trace   *trace.Trace
	writer  callWriter
	allowed func(string) bool
	txMap   map[int]int
}

func NewReplayer(tr *trace.Trace, cw callWriter, cfg Config) *Replayer {
	return &Replayer{
		cfg:     cfg,
		trace:   tr,
		writer:  cw,
		allowed: allowlist(cfg.Actions),
		txMap:   map[int]int{},
	}
}

// Run walks the trace records dispatching each CP→CSMS CALL through the callWriter.
func (r *Replayer) Run(ctx context.Context) error {
	for _, rec := range r.trace.Records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if r.shouldSkip(rec) {
			continue
		}

		r.dispatch(ctx, rec)

		if r.cfg.Delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.cfg.Delay):
			}
		}
	}
	return nil
}

func (r *Replayer) shouldSkip(rec trace.Record) bool {
	if rec.Direction != trace.DirCPtoCSMS {
		return true
	}
	if rec.MessageType != trace.MsgCall {
		return true
	}
	if !r.allowed(rec.Action) {
		return true
	}
	if !inTimeWindow(rec.Timestamp, r.cfg.From, r.cfg.To) {
		return true
	}
	return false
}

func (r *Replayer) dispatch(ctx context.Context, rec trace.Record) {
	payload := []byte(rec.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	// Decode the JSON payload into the typed request struct so the transport can encode it for its wire (JSON for
	// OCPP-J, XML for OCPP-S), and so we can mutate fields directly for the map-id-tag and transaction-id rewrites.
	req, err := ocpp.ChargingStationActionToRequestStruct(ocpp.ChargingStationToCSMSAction(rec.Action), r.trace.OCPPVersion)
	if err != nil {
		slog.Error("skipping unknown action", slog.String("action", rec.Action), slog.Any("error", err))
		return
	}
	if err = json.Unmarshal(payload, req); err != nil {
		slog.Error("skipping unparseable payload", slog.String("action", rec.Action), slog.Any("error", err))
		return
	}

	r.applyRewrites(req)

	messageID := rec.MessageID
	if r.cfg.RewriteMessageID || rec.MessageID == "" {
		messageID = uuid.NewString()
	}

	slog.Info("replaying",
		slog.String("action", rec.Action),
		slog.String("messageId", rec.MessageID),
	)
	resp, sendErr := r.writer.WriteCall(ctx, messageID, rec.Action, req)
	if sendErr != nil {
		slog.Error("send failed", slog.String("action", rec.Action), slog.Any("error", sendErr))
		return
	}
	if r.cfg.RewriteTransactionID && rec.Action == "StartTransaction" {
		r.recordStartTxMapping(r.trace.ResponseTo(rec), resp)
	}
}

// applyRewrites mutates the typed request struct in place: remaps idTag / idToken.idToken from cfg.IDTagMappings and
// 1.x transactionId via txMap. Actions with no mappable fields are a no-op.
func (r *Replayer) applyRewrites(req any) {
	idTags := r.cfg.IDTagMappings
	switch v := req.(type) {
	case *types15.Authorize:
		v.IDTag = remap(v.IDTag, idTags)
	case *types16.Authorize:
		v.IDTag = remap(v.IDTag, idTags)
	case *types15.StartTransaction:
		v.IDTag = remap(v.IDTag, idTags)
	case *types16.StartTransaction:
		v.IDTag = remap(v.IDTag, idTags)
	case *types15.StopTransaction:
		v.IDTag = remapPtr(v.IDTag, idTags)
		v.TransactionID = remap(v.TransactionID, r.txMap)
	case *types16.StopTransaction:
		v.IDTag = remapPtr(v.IDTag, idTags)
		v.TransactionID = remap(v.TransactionID, r.txMap)
	case *types15.MeterValues:
		v.TransactionID = remapPtr(v.TransactionID, r.txMap)
	case *types16.MeterValues:
		v.TransactionID = remapPtr(v.TransactionID, r.txMap)
	case *types201.TransactionEventRequest:
		if v.IDToken != nil {
			v.IDToken.IDToken = remap(v.IDToken.IDToken, idTags)
		}
	case *types21.TransactionEventRequest:
		if v.IDToken != nil {
			v.IDToken.IDToken = remap(v.IDToken.IDToken, idTags)
		}
	}
}

// recordStartTxMapping records the {originalTxID → replayedTxID} pairing for a StartTransaction exchange. Both sides
// are decoded into the typed StartTransactionResponse struct: the replayed one comes back from the transport already
// typed, and the original — still JSON bytes on the trace record — is unmarshalled here into the version-appropriate
// struct via the ocpp helper.
func (r *Replayer) recordStartTxMapping(originalResult json.RawMessage, replayedResp any) {
	replayed := startTransactionID(replayedResp)
	if replayed == 0 {
		return
	}
	originalResp, err := ocpp.ChargingStationActionToResponseStruct(ocpp.StartTransactionAction, r.trace.OCPPVersion)
	if err != nil {
		return
	}
	if err = json.Unmarshal(originalResult, originalResp); err != nil {
		return
	}
	original := startTransactionID(originalResp)
	if original == 0 || original == replayed {
		return
	}
	r.txMap[original] = replayed
	slog.Info("mapped transactionId",
		slog.Int("original", original),
		slog.Int("replayed", replayed),
	)
}

func startTransactionID(resp any) int {
	switch r := resp.(type) {
	case *types15.StartTransactionResponse:
		return r.TransactionID
	case *types16.StartTransactionResponse:
		return r.TransactionID
	}
	return 0
}

// remap returns m[v] when v is a key, otherwise v itself.
func remap[T comparable](v T, m map[T]T) T {
	if r, ok := m[v]; ok {
		return r
	}
	return v
}

// remapPtr is the *T variant of remap. Nil input returns nil; a hit returns a fresh pointer to the replacement, and a
// miss returns the original pointer untouched.
func remapPtr[T comparable](p *T, m map[T]T) *T {
	if p == nil {
		return nil
	}
	if r, ok := m[*p]; ok {
		return &r
	}
	return p
}

func inTimeWindow(ts, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	if ts.IsZero() {
		return false
	}
	if !from.IsZero() && ts.Before(from) {
		return false
	}
	if !to.IsZero() && ts.After(to) {
		return false
	}
	return true
}

func allowlist(actions []string) func(string) bool {
	if len(actions) == 0 {
		return func(string) bool { return true }
	}
	set := make(map[string]struct{}, len(actions))
	for _, a := range actions {
		set[strings.TrimSpace(a)] = struct{}{}
	}
	return func(a string) bool {
		_, ok := set[a]
		return ok
	}
}
