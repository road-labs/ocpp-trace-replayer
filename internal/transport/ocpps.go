package transport

import (
	"context"
	"encoding/xml"
	"fmt"

	ocpp "github.com/road-labs/ocpp-types-go"
	"github.com/road-labs/ocpps-go/ocpps"
	"github.com/road-labs/ocpps-go/ocpps/message"
)

// ocppsTransport wraps a stateless OCPP-S (SOAP over HTTP) client.
type ocppsTransport struct {
	version       ocpp.Version
	ocppIdentity  string
	serverURL     string
	advertiseAddr string

	client *ocpps.Client
}

func newOCPPS(version ocpp.Version, ocppIdentity, serverURL, advertiseAddr string) (*ocppsTransport, error) {
	if _, err := soapURNFor(version); err != nil {
		return nil, err
	}
	return &ocppsTransport{
		version:       version,
		ocppIdentity:  ocppIdentity,
		serverURL:     serverURL,
		advertiseAddr: advertiseAddr,
	}, nil
}

func (t *ocppsTransport) Start(_ context.Context) error {
	urn, err := soapURNFor(t.version)
	if err != nil {
		return err
	}
	client, err := ocpps.NewClient(t.serverURL, urn, t.ocppIdentity, t.advertiseAddr, ocppsClientHooks{})
	if err != nil {
		return err
	}
	t.client = client
	return nil
}

func (t *ocppsTransport) WriteCall(ctx context.Context, messageID, action string, req any) (any, error) {
	payload, err := xml.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", action, err)
	}
	result, err := t.client.WriteCall(ctx, message.Call{
		MessageID: messageID,
		Action:    action,
		Payload:   payload,
	})
	if err != nil {
		return nil, err
	}
	respPtr, err := ocpp.ChargingStationActionToResponseStruct(ocpp.ChargingStationToCSMSAction(action), t.version)
	if err != nil {
		return nil, fmt.Errorf("resolve %s response struct: %w", action, err)
	}
	if err := xml.Unmarshal(result.Payload, respPtr); err != nil {
		return nil, fmt.Errorf("unmarshal %s response: %w", action, err)
	}
	return respPtr, nil
}

func (t *ocppsTransport) Shutdown(context.Context) error {
	return nil
}

// ocppsClientHooks satisfies ocpps.ClientHooks with no-ops.
type ocppsClientHooks struct{}

func (ocppsClientHooks) OnCallWritten(context.Context, *ocpps.ClientInfo, message.Call) error {
	return nil
}

func (ocppsClientHooks) OnCallResultRead(context.Context, *ocpps.ClientInfo, message.CallResult) error {
	return nil
}

func (ocppsClientHooks) OnCallErrorRead(context.Context, *ocpps.ClientInfo, message.CallError) error {
	return nil
}
