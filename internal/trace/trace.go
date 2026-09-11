// Package trace loads open-ocpp-trace v1 JSONL files
// (https://open-ocpp-trace.github.io/schema/trace-v1.schema.json) for replay.
package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	ocpp "github.com/road-labs/ocpp-types-go"
)

// Direction enum values from the schema.
const (
	DirCPtoCSMS = "cp-to-csms"
	DirCSMStoCP = "csms-to-cp"
)

// MessageType enum values from the schema.
const (
	MsgCall       = "CALL"
	MsgCallResult = "CALLRESULT"
	MsgCallError  = "CALLERROR"
)

// Transport enum values from the schema.
const (
	TransportJSON = "json"
	TransportSOAP = "soap"
)

// Record mirrors an OcppTraceRecord. Only fields the replayer needs are named.
type Record struct {
	Timestamp     time.Time       `json:"timestamp"`
	OCPPVersion   string          `json:"ocppVersion,omitempty"`
	Transport     string          `json:"transport"`
	ChargePointID string          `json:"chargePointId,omitempty"`
	Direction     string          `json:"direction"`
	MessageType   string          `json:"messageType"`
	MessageID     string          `json:"messageId,omitempty"`
	Action        string          `json:"action,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`

	// index is the record's position in Trace.Records, set by New / Read so ResponseTo(rec) can look up the linked
	// CALLRESULT without callers having to track it. Unexported so encoding/json ignores it.
	index int
}

// Trace is a loaded JSONL trace file: the header fields shared by every record, plus the ordered records themselves.
// A small internal index links CALLs to their answering CALLRESULT payload so the tx-id remapping in replay can be a
// single lookup.
type Trace struct {
	ChargePointID string
	OCPPVersion   ocpp.Version
	Transport     string
	Records       []Record

	// responses maps a CALL's record index to the payload of the CALLRESULT that answered it,
	// when a matching one appears later in the trace with the opposite direction.
	responses map[int]json.RawMessage
}

// New constructs a Trace from an already-parsed set of records - used mostly by tests that want to skip the file+JSONL
// round trip. It stamps each record's position so ResponseTo works.
func New(records []Record) *Trace {
	for i := range records {
		records[i].index = i
	}
	tr := &Trace{Records: records}
	tr.linkResponses()
	return tr
}

// ResponseTo returns the payload of the CALLRESULT that answered rec, or nil if no matching response was found in the
// trace. rec must come from t.Records (or a copy of one) - the lookup uses the position stamped on the record by
// New / Read.
func (t *Trace) ResponseTo(rec Record) json.RawMessage {
	return t.responses[rec.index]
}

// Read parses the JSONL file at path, checks that chargePointId / ocppVersion / transport are consistent across
// records, and returns the trace ready for replay.
func Read(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tr := &Trace{}

	dec := json.NewDecoder(f)
	var (
		chargePointID string
		ocppVersion   string
		transport     string
	)

	for {
		var rec Record
		if err = dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("record %d: parse: %w", len(tr.Records)+1, err)
		}
		recordNo := len(tr.Records) + 1
		if err = checkConsistent("chargePointId", &chargePointID, rec.ChargePointID, recordNo); err != nil {
			return nil, err
		}
		if err = checkConsistent("ocppVersion", &ocppVersion, rec.OCPPVersion, recordNo); err != nil {
			return nil, err
		}
		if err = checkConsistent("transport", &transport, rec.Transport, recordNo); err != nil {
			return nil, err
		}
		rec.index = len(tr.Records)
		tr.Records = append(tr.Records, rec)
	}
	if len(tr.Records) == 0 {
		return nil, fmt.Errorf("trace file is empty")
	}
	if chargePointID == "" {
		return nil, fmt.Errorf("trace records have no chargePointId; cannot connect")
	}
	if ocppVersion == "" {
		return nil, fmt.Errorf("trace records have no ocppVersion; cannot select subprotocol")
	}
	if transport != TransportJSON && transport != TransportSOAP {
		return nil, fmt.Errorf("unsupported transport %q; want %q or %q", transport, TransportJSON, TransportSOAP)
	}

	version, err := ParseVersion(ocppVersion)
	if err != nil {
		return nil, err
	}

	tr.linkResponses()

	tr.ChargePointID = chargePointID
	tr.OCPPVersion = version
	tr.Transport = transport
	return tr, nil
}

// callKey identifies an outstanding CALL. Direction is part of the key so a CP→CSMS CALL and a CSMS→CP CALL sharing a
// messageId don't clobber each other in the linker — each CALLRESULT looks up the outstanding CALL of the *opposite*
// direction with the same messageId.
type callKey struct {
	Direction string
	MessageID string
}

// linkResponses walks records in order and, for each CALLRESULT, records its payload against the most recent preceding
// CALL that shares the messageId and has the opposite direction. A response that arrives before its CALL (or with no
// opposite-direction CALL outstanding) doesn't link — the CALL is simply left without a stored response.
func (t *Trace) linkResponses() {
	t.responses = map[int]json.RawMessage{}
	outstanding := map[callKey]int{}
	for i, rec := range t.Records {
		switch rec.MessageType {
		case MsgCall:
			if rec.MessageID != "" {
				outstanding[callKey{rec.Direction, rec.MessageID}] = i
			}
		case MsgCallResult:
			key := callKey{oppositeDirection(rec.Direction), rec.MessageID}
			if callIdx, ok := outstanding[key]; ok {
				t.responses[callIdx] = rec.Payload
				delete(outstanding, key)
			}
		}
	}
}

func oppositeDirection(d string) string {
	switch d {
	case DirCPtoCSMS:
		return DirCSMStoCP
	case DirCSMStoCP:
		return DirCPtoCSMS
	}
	return ""
}

// ParseVersion converts a schema ocppVersion string (e.g. "1.6") to the ocpp-types-go Version constant used as the
// WebSocket subprotocol / SOAP namespace anchor.
func ParseVersion(p string) (ocpp.Version, error) {
	switch p {
	case "1.5":
		return ocpp.Version15, nil
	case "1.6":
		return ocpp.Version16, nil
	case "2.0.1":
		return ocpp.Version201, nil
	case "2.1":
		return ocpp.Version21, nil
	default:
		return "", fmt.Errorf("unsupported ocppVersion %q; want 1.5, 1.6, 2.0.1 or 2.1", p)
	}
}

// checkConsistent enforces that a header-like field is the same on every record. The first non-empty value seen becomes
// the expected value; subsequent non-empty different values error. Empty on later records is tolerated (the schema
// marks these fields optional).
func checkConsistent(field string, seen *string, value string, recordNo int) error {
	if value == "" {
		return nil
	}
	if *seen == "" {
		*seen = value
		return nil
	}
	if *seen != value {
		return fmt.Errorf("record %d: inconsistent %s: previously %q, now %q", recordNo, field, *seen, value)
	}
	return nil
}
