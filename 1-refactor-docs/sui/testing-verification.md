# Testing and Verification Documentation

This document defines the testing and verification strategy for the Sui/MoveVM adapted version of LND, covering unit tests, contract tests, and end-to-end tests.

## Objectives and Scope

- Verify that the Sui adaptation does not break existing LND behavior.
- Verify the logical correctness of the Move contract on critical paths (channel establishment, HTLC, payment settlement, channel closure).
- Cover Go adaptation layer unit tests, Move contract unit tests, and LND+Sui end-to-end integration tests.

## Unit Tests

### 1. Go Adaptation Layer Tests

- Maintain the same unit test structure and naming conventions as upstream LND.
- Focus coverage on: `suinotify/`, `suiwallet/`, `input/sui_channel.go`, `chainfee/sui_estimator`.
- Verify type mapping: 1:1 mapping between `wire.OutPoint.Hash` and Sui `ObjectID`.

How to run:
```sh
make unit tags=sui
```

### 2. Move Contract Tests

- Verify the permission control, signature validation, and state transitions of contract methods.
- Focus coverage on: fund locking in `open_channel`, preimage validation in `htlc_claim`, and pure Hash digest validation in `penalize` (the original uses ECDSA signature validation, which is now changed to pure hash comparison validation based on the security assumptions of the Lightning state machine).

How to run (in the contract directory):
```sh
sui move test
```

## End-to-End Tests (Sui + LND + Payment Flow)

We provide an automated integration test script `scripts/itest_sui.sh` to simulate a complete two-node (Alice & Bob) payment flow locally.

### Prerequisites

- Go 1.25.5
- Sui CLI (configured with local network environment, used to request testnet faucet)
- `jq` (for parsing JSON output)
- Ensure the LND binaries are compiled (run `make build` to generate `lnd-debug` and `lncli-debug`)

### Run Automated Script (Recommended)

You can directly run the provided integration test script to complete the end-to-end channel establishment and payment verification:

```sh
./scripts/itest_sui.sh
```

**The automated flow of this script includes:**
1. **Environment Cleanup**: Clear the test data directory `/tmp/lnd-sui-test/` from the previous run.
2. **Start Nodes**: Start two LND nodes, Alice and Bob, with the `--suinode.active` and `--suinode.devnet` parameters.
3. **Fund Preparation**: Generate a new Sui address for Alice and call `sui client faucet` to get Devnet test coins.
4. **P2P Connection**: Alice connects to Bob's Lightning Network node.
5. **Establish Channel**: Alice initiates an `openchannel` request to Bob, registering the Channel Object on the Sui chain.
6. **Execute Payment**: Bob creates a receiving invoice, and Alice completes the payment (`payinvoice`) through the newly established channel.

### Step-by-Step Manual Verification (Reference)
If you wish to execute the steps manually, you can refer to the command flow in `scripts/itest_sui.sh`, start the nodes sequentially, and use the `./lncli-debug --lnddir=...` interactive command for verification.

## Common Issues and Troubleshooting

- **Insufficient Gas**: Check if the LND wallet account has enough SUI to pay for the Move Call transaction fee.
- **Event Subscription Failure**: Ensure the Sui RPC node supports WebSocket subscriptions.
- **Signature Validation Failure**: Confirm that `ecdsa_k1::secp256k1_verify` in the Move contract is using a compressed public key.
