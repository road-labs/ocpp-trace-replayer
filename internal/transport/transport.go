package transport

import (
	"context"
	"fmt"

	ocpp "github.com/road-labs/ocpp-types-go"
)

// Transport is the send-side abstraction over both OCPP wire bindings.
type Transport interface {
	// Start opens the underlying connection. For OCPP-J this dials the WebSocket and spawns the reconnect supervisor;
	// for OCPP-S it just constructs the HTTP client.
	Start(ctx context.Context) error

	// WriteCall dispatches one CALL to the peer.
	WriteCall(ctx context.Context, messageID, action string, req any) (any, error)

	// Shutdown closes the underlying connection (if applicable). Safe to call after ctx has cancelled.
	Shutdown(ctx context.Context) error
}

// Kind identifies which wire binding a trace is on. Values match the open-ocpp-trace `transport` enum.
type Kind string

const (
	KindJSON Kind = "json"
	KindSOAP Kind = "soap"
)

// Config holds everything a Transport needs to be constructed.
type Config struct {
	Kind          Kind
	Version       ocpp.Version
	OCPPIdentity  string
	ServerURL     string
	AdvertiseAddr string
}

// New picks the right Transport for the given wire binding.
func New(cfg Config) (Transport, error) {
	switch cfg.Kind {
	case KindJSON:
		return newOCPPJ(cfg.Version, cfg.OCPPIdentity, cfg.ServerURL), nil
	case KindSOAP:
		if cfg.AdvertiseAddr == "" {
			return nil, fmt.Errorf("advertiseAddr is required when transport is soap")
		}
		return newOCPPS(cfg.Version, cfg.OCPPIdentity, cfg.ServerURL, cfg.AdvertiseAddr)
	default:
		return nil, fmt.Errorf("unsupported transport %q", cfg.Kind)
	}
}

func soapURNFor(v ocpp.Version) (string, error) {
	switch v {
	case ocpp.Version15:
		return "urn://Ocpp/Cs/2012/06/", nil
	case ocpp.Version16:
		return "urn://Ocpp/Cs/2015/10/", nil
	default:
		return "", fmt.Errorf("no SOAP binding for OCPP %s", v)
	}
}
