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

You can directly run the provided integration test script to complete the end-to-end channel establishment and payment verification. By default, it runs on the **Sui official Devnet**, but you can also pass `localnet` to automatically launch and use a local Sui instance.

```sh
# Run on official Sui Devnet (default)
./scripts/itest_sui.sh devnet

# Run on Local Sui Node (auto-starts \`sui start\` if not running)
./scripts/itest_sui.sh localnet
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

#### Manual Steps(localnet)

1. Exec `RUST_LOG="off,sui_node=info" sui start --with-faucet --force-regenesis`, manually start the localnet node by yourself

2. **Publish the Lightning Move Contracts:**
   ```sh
   sui client switch --env localnet
   sui client faucet
   PUBLISH_JSON=$(sui client publish --json --gas-budget 100000000 ./sui-contracts/lightning)
   PACKAGE_ID=$(echo "$PUBLISH_JSON" | jq -r '.objectChanges[] | select(.type == "published") | .packageId')
   echo "Package ID: $PACKAGE_ID"
   ```

3. **Start Alice's LND Node:**
   ```sh
   ./lnd-debug \
       --lnddir="/tmp/lnd-sui-test/alice" \
       --listen="127.0.0.1:10011" \
       --rpclisten="127.0.0.1:10009" \
       --restlisten="127.0.0.1:8081" \
       --suinode.active \
       --suinode.devnet \
       --suinode.rpchost="http://127.0.0.1:9000" \
       --suinode.packageid="$PACKAGE_ID" \
       --noseedbackup
   ```

4. **Start Bob's LND Node (in a new terminal window):**
   ```sh
   ./lnd-debug \
       --lnddir="/tmp/lnd-sui-test/bob" \
       --listen="127.0.0.1:10012" \
       --rpclisten="127.0.0.1:10010" \
       --restlisten="127.0.0.1:8082" \
       --suinode.active \
       --suinode.devnet \
       --suinode.rpchost="http://127.0.0.1:9000" \
       --suinode.packageid="$PACKAGE_ID" \
       --noseedbackup
   ```

5. **Setup CLI Aliases (for convenience in a new terminal):**
   ```sh
   alias alice-cli="./lncli-debug --lnddir=/tmp/lnd-sui-test/alice --rpcserver=localhost:10009 --macaroonpath=/tmp/lnd-sui-test/alice/data/chain/sui/devnet/admin.macaroon"
   alias bob-cli="./lncli-debug --lnddir=/tmp/lnd-sui-test/bob --rpcserver=localhost:10010 --macaroonpath=/tmp/lnd-sui-test/bob/data/chain/sui/devnet/admin.macaroon"
   ```

6. **Fund Alice's Wallet:**
   ```sh
   ALICE_ADDR=$(alice-cli newaddress p2wkh | jq -r '.address')
   
   # Request faucet twice so Alice has distinct coins for funding vs. gas fee
   sui client faucet --url "http://127.0.0.1:9123" --address "$ALICE_ADDR"
   sleep 2
   sui client faucet --url "http://127.0.0.1:9123" --address "$ALICE_ADDR"
   sleep 2
   
   alice-cli walletbalance
   ```

7. **Connect and Open Channel:**
   ```sh
   BOB_PUBKEY=$(bob-cli getinfo | jq -r '.identity_pubkey')
   alice-cli connect "${BOB_PUBKEY}@127.0.0.1:10012"
   
   alice-cli openchannel --node_key=$BOB_PUBKEY --local_amt=10000000
   
   # Verify channel status
   alice-cli pendingchannels
   alice-cli listchannels
   ```

8. **Test Lightning Payment:**
   ```sh
   INVOICE=$(bob-cli addinvoice --amt=1000 --memo="manual-test" | jq -r '.payment_request')
   alice-cli payinvoice --pay_req="$INVOICE" --force
   ```

## Common Issues and Troubleshooting

- **Insufficient Gas**: Check if the LND wallet account has enough SUI to pay for the Move Call transaction fee.
- **Event Subscription Failure**: Ensure the Sui RPC node supports WebSocket subscriptions.
- **Signature Validation Failure**: Confirm that `ecdsa_k1::secp256k1_verify` in the Move contract is using a compressed public key.
