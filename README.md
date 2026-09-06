# OCPP Trace Replayer

Tool to replay [Open OCPP Trace](https://github.com/open-ocpp-trace/specification) files against an OCPP backend. Includes certain smarts to cater for overriding and
remapping transaction related values.

## Version support

- OCPP 1.5 (JSON, SOAP)
- OCPP 1.6 (JSON, SOAP)
- OCPP 2.0.1 (JSON)
- OCPP 2.1 (JSON)

## Install

```sh
go install github.com/road-labs/ocpp-trace-replayer@latest
```

## Usage

Basic usage, with the base level behaviour being applied - meaning CALLs are played verbatim:

```sh
ocpp-trace-replayer --server-url ws://csms.example.com/ocpp --trace-path ./trace.jsonl
```

Note that the trace file must currently relate to a single charge point.

### Overrides

- `--ocpp-identity` `[OCPP_IDENTITY]` - use this identity on the wire instead of the trace's `chargePointId`.
- `--advertise-addr` `[ADVERTISE_ADDR]` - SOAP only, and mostly ceremonial: the SOAP envelope's WS-Addressing `From`
  header has to be mandatory. Defaults to `http://replay.invalid/`.

### Filters

- `--actions Foo,Bar` `[ACTIONS]` - only replay these actions.
- `--from`, `--to` `[FROM]`, `[TO]` - RFC3339 timestamps bounding what to replay.
- `--delay 500ms` `[DELAY]` - pause between successful sends. Default 0 (no delay).

### Rewrites

- `--rewrite-transaction-id` `[REWRITE_TRANSACTION_ID]` - swap 1.6 transaction IDs to whatever the target CSMS issues.
- `--map-id-tag ORIGINAL=REPLACEMENT` `[MAP_ID_TAG]` - swap tag values before send. Repeatable, or comma-separated in a single
  flag.
- `--rewrite-message-id` `[REWRITE_MESSAGE_ID]` - instead of using the traces original message ID, generate a fresh one
  instead.

Putting a few together:

```sh
ocpp-trace-replayer \
  --server-url wss://csms.example.com/ocpp \
  --trace-path ./trace.jsonl \
  --ocpp-identity CP-STAGING-1 \
  --rewrite-transaction-id \
  --rewrite-message-id \
  --map-id-tag OLDTAG1=TESTTAG1,OLDTAG2=TESTTAG2 \
  --actions Authorize,StartTransaction,MeterValues,StopTransaction \
  --delay 200ms
```
