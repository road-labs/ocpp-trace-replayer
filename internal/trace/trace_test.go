package trace

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTrace(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestReadHappy(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31.407Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"m1","action":"BootNotification","payload":{"chargePointVendor":"v","chargePointModel":"m"}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31.500Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALLRESULT","messageId":"m1","payload":{"status":"Accepted","currentTime":"2026-08-18T19:31:31Z","interval":60}}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(tr.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(tr.Records))
	}
	if tr.ChargePointID != "CP1" {
		t.Errorf("ChargePointID = %q, want CP1", tr.ChargePointID)
	}
	if string(tr.OCPPVersion) != "ocpp1.6" {
		t.Errorf("OCPPVersion = %q, want ocpp1.6", tr.OCPPVersion)
	}
	if payload := tr.ResponseTo(tr.Records[0]); payload == nil {
		t.Errorf("expected ResponseTo(Records[0]) to return a payload")
	}
}

func TestResponseTo_EarlierResponseDoesNotLink(t *testing.T) {
	// A CALLRESULT that precedes any CALL with its messageId must not be linked to the later
	// CALL — the CALL is left with no response.
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALLRESULT","messageId":"m1","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"m1","action":"Heartbeat","payload":{}}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if payload := tr.ResponseTo(tr.Records[1]); payload != nil {
		t.Errorf("ResponseTo(Records[1]) = %s, want nil", string(payload))
	}
}

func TestResponseTo_ReusedMessageIdPicksMostRecentCall(t *testing.T) {
	// Two CP→CSMS CALLs share messageId m1. The following CALLRESULT should link to the most
	// recent CALL (index 1), not the earlier one.
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"m1","action":"Heartbeat","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"m1","action":"Heartbeat","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:33Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALLRESULT","messageId":"m1","payload":{"currentTime":"a"}}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := string(tr.ResponseTo(tr.Records[1])); got != `{"currentTime":"a"}` {
		t.Errorf("ResponseTo(Records[1]) = %s, want the CALLRESULT payload", got)
	}
	if payload := tr.ResponseTo(tr.Records[0]); payload != nil {
		t.Errorf("ResponseTo(Records[0]) = %s, want nil (earlier CALL is unanswered)", string(payload))
	}
}

func TestResponseTo_FirstCALLRESULTLinks(t *testing.T) {
	// A CALL followed by two CALLRESULTs with the same messageId: only the first CALLRESULT is
	// linked to the CALL. A subsequent CALLRESULT with no outstanding CALL is silently dropped.
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"m1","action":"Heartbeat","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALLRESULT","messageId":"m1","payload":{"n":1}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:33Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALLRESULT","messageId":"m1","payload":{"n":2}}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := string(tr.ResponseTo(tr.Records[0])); got != `{"n":1}` {
		t.Errorf("ResponseTo(Records[0]) = %s, want the first CALLRESULT payload", got)
	}
}

func TestResponseTo_OppositeDirectionSameMessageID(t *testing.T) {
	// A valid but tricky exchange: a CSMS→CP RemoteStartTransaction and a CP→CSMS
	// StartTransaction both use messageId=1, and their CALLRESULTs interleave. Each CALL should
	// link to the CALLRESULT of the opposite direction, without one clobbering the other.
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALL","messageId":"1","action":"RemoteStartTransaction","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"1","action":"StartTransaction","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:33Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALLRESULT","messageId":"1","payload":{"status":"Accepted"}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:34Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"csms-to-cp","messageType":"CALLRESULT","messageId":"1","payload":{"transactionId":42}}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := string(tr.ResponseTo(tr.Records[0])); got != `{"status":"Accepted"}` {
		t.Errorf("ResponseTo(Records[0]) = %s, want RemoteStartTransaction reply", got)
	}
	if got := string(tr.ResponseTo(tr.Records[1])); got != `{"transactionId":42}` {
		t.Errorf("ResponseTo(Records[1]) = %s, want StartTransaction reply", got)
	}
}

func TestResponseTo_SameDirectionDoesNotLink(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","messageId":"m1","action":"Heartbeat","payload":{}}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALLRESULT","messageId":"m1","payload":{}}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if payload := tr.ResponseTo(tr.Records[0]); payload != nil {
		t.Errorf("ResponseTo(Records[0]) = %s, want nil", string(payload))
	}
}

func TestReadInconsistentChargePointID(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP2","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
	})
	if _, err := Read(path); err == nil {
		t.Fatalf("expected inconsistency error, got nil")
	}
}

func TestReadInconsistentOCPPVersion(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","ocppVersion":"2.0.1","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
	})
	if _, err := Read(path); err == nil {
		t.Fatalf("expected inconsistency error, got nil")
	}
}

func TestReadSOAPAccepted(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"soap","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
	})
	tr, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tr.Transport != "soap" {
		t.Errorf("Transport = %q, want soap", tr.Transport)
	}
}

func TestReadUnknownTransport(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"mqtt","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
	})
	if _, err := Read(path); err == nil {
		t.Fatalf("expected unknown-transport error, got nil")
	}
}

func TestReadEmpty(t *testing.T) {
	path := writeTrace(t, nil)
	if _, err := Read(path); err == nil {
		t.Fatalf("expected empty-file error, got nil")
	}
}

func TestReadLaterEmptyFieldsAreTolerated(t *testing.T) {
	path := writeTrace(t, []string{
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:31Z","ocppVersion":"1.6","transport":"json","chargePointId":"CP1","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
		`{"schemaVersion":"1","timestamp":"2026-08-18T19:31:32Z","transport":"json","direction":"cp-to-csms","messageType":"CALL","action":"Heartbeat"}`,
	})
	if _, err := Read(path); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}
