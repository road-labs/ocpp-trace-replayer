package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	ocpp "github.com/road-labs/ocpp-types-go"
	"github.com/road-labs/ocppj-go/ocppj"
	"github.com/road-labs/ocppj-go/ocppj/clientopt"
	"github.com/road-labs/ocppj-go/ocppj/message"
)

const reconnectBackoff = 3 * time.Second

// ocppjTransport is a supervised OCPP-J WebSocket connection.
type ocppjTransport struct {
	path    string
	version ocpp.Version

	client atomic.Pointer[ocppj.Client]

	mux     sync.Mutex
	ready   bool
	readyCh chan struct{}

	cancel func()
	done   chan struct{}
}

func newOCPPJ(version ocpp.Version, ocppIdentity, serverURL string) *ocppjTransport {
	return &ocppjTransport{
		path:    fmt.Sprintf("%s/%s", serverURL, ocppIdentity),
		version: version,
		readyCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (t *ocppjTransport) Start(ctx context.Context) error {
	sCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	if err := t.dial(sCtx); err != nil {
		cancel()
		return err
	}
	t.setReady(true)
	slog.Info("ocppj connected")
	go t.supervise(sCtx)
	return nil
}

func (t *ocppjTransport) dial(ctx context.Context) error {
	client, err := ocppj.Open(ctx, t.path,
		clientHooks{},
		clientopt.WithSupportedProtocols([]string{string(t.version)}),
		clientopt.WithWebsocketPingInterval(time.Second*30),
		clientopt.WithLogger(slog.Default()),
	)
	if err != nil {
		return err
	}
	t.client.Store(client)
	return nil
}

// supervise runs the client's Read loop; if it returns while ctx is still live, that means the connection dropped and
// we should try to reconnect. If ctx is cancelled we just exit.
func (t *ocppjTransport) supervise(ctx context.Context) {
	defer close(t.done)
	for {
		client := t.client.Load()
		_ = client.Read(ctx)
		_ = client.Close()

		select {
		case <-ctx.Done():
			return
		default:
			t.setReady(false)
			slog.Warn("ocppj disconnected; reconnecting")
			if err := t.reconnect(ctx); err != nil {
				return
			}
		}
	}
}

func (t *ocppjTransport) reconnect(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectBackoff):
		}
		if err := t.dial(ctx); err != nil {
			slog.Warn("ocppj reconnect failed", slog.Any("error", err))
			continue
		}
		t.setReady(true)
		slog.Info("ocppj reconnected")
		return nil
	}
}

// setReady atomically flips the ready flag. When becoming ready, close the current readyCh so waiters unblock; when
// becoming not-ready, install a fresh channel so subsequent waiters block until the next reconnect closes it.
func (t *ocppjTransport) setReady(ready bool) {
	t.mux.Lock()
	defer t.mux.Unlock()
	if t.ready == ready {
		return
	}
	t.ready = ready
	if ready {
		close(t.readyCh)
	} else {
		t.readyCh = make(chan struct{})
	}
}

// waitReady blocks until the WebSocket is connected, or until ctx is cancelled.
func (t *ocppjTransport) waitReady(ctx context.Context) error {
	t.mux.Lock()
	if t.ready {
		t.mux.Unlock()
		return nil
	}
	c := t.readyCh
	t.mux.Unlock()
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *ocppjTransport) WriteCall(ctx context.Context, messageID, action string, req any) (any, error) {
	if err := t.waitReady(ctx); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", action, err)
	}
	result, err := t.client.Load().SyncWriteCall(ctx, message.Call{
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
	if err = json.Unmarshal(result.Payload, respPtr); err != nil {
		return nil, fmt.Errorf("unmarshal %s response: %w", action, err)
	}
	return respPtr, nil
}

func (t *ocppjTransport) Shutdown(ctx context.Context) error {
	if t.cancel != nil {
		t.cancel()
	}
	if c := t.client.Load(); c != nil {
		_ = c.GracefulClose(ctx, "replay complete")
	}
	<-t.done
	return nil
}

type clientHooks struct{}

func (clientHooks) OnCallRead(ctx context.Context, client *ocppj.Client, call message.Call) error {
	return client.WriteCallError(ctx, message.CallError{
		MessageID:        call.MessageID,
		ErrorCode:        "NotImplemented",
		ErrorDescription: "replayer does not handle inbound calls",
		ErrorDetails:     json.RawMessage(`{}`),
	})
}

func (clientHooks) OnCallResultRead(context.Context, *ocppj.Client, message.CallResult) error {
	return nil
}

func (clientHooks) OnCallErrorRead(context.Context, *ocppj.Client, message.CallError) error {
	return nil
}

func (clientHooks) OnCallResultErrorRead(context.Context, *ocppj.Client, message.CallResultError) error {
	return nil
}

func (clientHooks) OnSendRead(context.Context, *ocppj.Client, message.Send) error {
	return nil
}

func (clientHooks) OnInvalidMessageRead(context.Context, *ocppj.Client, []byte, error) error {
	return nil
}

func (clientHooks) OnCallWritten(context.Context, *ocppj.Client, message.Call) error {
	return nil
}

func (clientHooks) OnCallResultWritten(context.Context, *ocppj.Client, message.CallResult) error {
	return nil
}

func (clientHooks) OnCallErrorWritten(context.Context, *ocppj.Client, message.CallError) error {
	return nil
}

func (clientHooks) OnCallResultErrorWritten(context.Context, *ocppj.Client, message.CallResultError) error {
	return nil
}

func (clientHooks) OnSendWritten(context.Context, *ocppj.Client, message.Send) error {
	return nil
}
