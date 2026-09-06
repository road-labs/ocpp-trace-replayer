package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/road-labs/ocpp-trace-replayer/internal/replay"
	"github.com/road-labs/ocpp-trace-replayer/internal/trace"
	"github.com/road-labs/ocpp-trace-replayer/internal/transport"
)

func main() {
	cmd := &cli.Command{
		Name:  "replay",
		Usage: "Replay an open-ocpp-trace JSONL file against a live CSMS.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "server-url",
				Usage:    "CSMS endpoint URL (ws[s]:// for OCPP-J, http[s]:// for OCPP-S).",
				Required: true,
				Sources:  cli.EnvVars("SERVER_URL"),
			},
			&cli.StringFlag{
				Name:     "trace-path",
				Usage:    "Path to the open-ocpp-trace JSONL file.",
				Required: true,
				Sources:  cli.EnvVars("TRACE_PATH"),
			},
			&cli.StringFlag{
				Name:    "advertise-addr",
				Usage:   "SOAP only: URL placed in the outgoing WS-Addressing From header. The replayer is send-only so this is effectively just a stub value.",
				Value:   "http://example.invalid/",
				Sources: cli.EnvVars("ADVERTISE_ADDR"),
			},
			&cli.StringFlag{
				Name:    "ocpp-identity",
				Usage:   "Override the chargePointId taken from the trace. When unset the trace's identity is used verbatim.",
				Sources: cli.EnvVars("OCPP_IDENTITY"),
			},
			&cli.BoolFlag{
				Name:    "rewrite-message-id",
				Usage:   "Mint a fresh UUID for each replayed CALL instead of reusing the trace's messageId. Off by default; the trace's messageId is used verbatim (a fresh UUID is still minted when the trace record has no messageId).",
				Sources: cli.EnvVars("REWRITE_MESSAGE_ID"),
			},
			&cli.BoolFlag{
				Name:    "rewrite-transaction-id",
				Usage:   "For 1.5/1.6: track the CSMS-issued transactionId from each StartTransaction response and rewrite it into any subsequent MeterValues / StopTransaction payloads that carry the original id. Off by default; without this the replayer sends the trace's ids verbatim, which a fresh CSMS will reject.",
				Sources: cli.EnvVars("REWRITE_TRANSACTION_ID"),
			},
			&cli.StringSliceFlag{
				Name:    "actions",
				Usage:   "Allowlist of OCPP action names to replay. Empty means replay every action.",
				Sources: cli.EnvVars("ACTIONS"),
			},
			&cli.DurationFlag{
				Name:    "delay",
				Usage:   "Delay between CALLs (e.g. 500ms). 0 = no delay.",
				Sources: cli.EnvVars("DELAY"),
			},
			&cli.StringSliceFlag{
				Name:    "map-id-tag",
				Usage:   "ID tag remap in ORIGINAL=REPLACEMENT form, repeatable. Applied to Authorize, StartTransaction, StopTransaction and TransactionEvent payloads before send.",
				Sources: cli.EnvVars("MAP_ID_TAG"),
			},
			&cli.TimestampFlag{
				Name:    "from",
				Usage:   "Only replay records whose timestamp is at or after this time (RFC3339).",
				Config:  cli.TimestampConfig{Layouts: []string{time.RFC3339Nano, time.RFC3339}},
				Sources: cli.EnvVars("FROM"),
			},
			&cli.TimestampFlag{
				Name:    "to",
				Usage:   "Only replay records whose timestamp is at or before this time (RFC3339).",
				Config:  cli.TimestampConfig{Layouts: []string{time.RFC3339Nano, time.RFC3339}},
				Sources: cli.EnvVars("TO"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			idTagMappings, err := parseKeyValueMappings(cmd.StringSlice("map-id-tag"))
			if err != nil {
				return fmt.Errorf("--map-id-tag: %w", err)
			}

			tr, err := trace.Read(cmd.String("trace-path"))
			if err != nil {
				return fmt.Errorf("read trace: %w", err)
			}
			chargePointID := tr.ChargePointID
			if override := cmd.String("ocpp-identity"); override != "" {
				chargePointID = override
			}
			slog.Info("trace loaded",
				slog.String("chargePointId", chargePointID),
				slog.String("ocppVersion", string(tr.OCPPVersion)),
				slog.String("transport", tr.Transport),
				slog.Int("records", len(tr.Records)),
			)

			tp, err := transport.New(transport.Config{
				Kind:          transport.Kind(tr.Transport),
				Version:       tr.OCPPVersion,
				OCPPIdentity:  chargePointID,
				ServerURL:     cmd.String("server-url"),
				AdvertiseAddr: cmd.String("advertise-addr"),
			})
			if err != nil {
				return err
			}
			if err = tp.Start(ctx); err != nil {
				return fmt.Errorf("transport start: %w", err)
			}
			defer func() {
				_ = tp.Shutdown(context.Background())
			}()

			return replay.NewReplayer(tr, tp, replay.Config{
				Actions:              cmd.StringSlice("actions"),
				From:                 cmd.Timestamp("from"),
				To:                   cmd.Timestamp("to"),
				Delay:                cmd.Duration("delay"),
				RewriteMessageID:     cmd.Bool("rewrite-message-id"),
				RewriteTransactionID: cmd.Bool("rewrite-transaction-id"),
				IDTagMappings:        idTagMappings,
			}).Run(ctx)
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

func parseKeyValueMappings(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		key, value, ok := strings.Cut(e, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("expected ORIGINAL=REPLACEMENT, got %q", e)
		}
		out[key] = value
	}
	return out, nil
}
