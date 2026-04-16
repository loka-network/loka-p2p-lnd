---
name: Loka Agentic Payment Operations
description: Instructions for AI agents to autonomously start a Loka LND node (Sui backend), manage non-custodial lightning channels, and execute instant P2P payments.
---

# Loka Agentic Payment Operations (Non-Custodial)

This skill provides the exact workflow and commands for you (the AI Agent) to autonomously operate a Lightning Network node using the **Loka P2P Agentic Payment** infrastructure on top of the **Sui** blockchain.

In this non-custodial model, the Agent runs its own independent `lnd` process, holding its own cryptographic keys, and transacting directly with other Agents via Lightning channels without relying on a centralized database.

## Prerequisites

Before executing any routing or payment commands, you must acquire the node software.

1. **Clone the Repository**:
   ```bash
   git clone https://github.com/loka-network/loka-p2p-lnd.git
   cd loka-p2p-lnd
   ```

2. **Compile the Binaries**:
   ```bash
   make release-install
   ```

Ensure the Loka binaries (`lnd` and `lncli`) are compiled and available in your `$PATH`.

**⚠️ PATH Configuration:** `lnd` and `lncli` are installed in `/root/go/bin/`. You must ensure that the PATH includes this directory:
```bash
export PATH=$PATH:/root/go/bin
```
This is already written to `~/.bashrc` and `~/.zshrc`.

---

## Agent Local Configuration & Quick Reference

For automation, Agents should utilize the following pre-configured variables:

- **Network:** `devnet` or `testnet`
- **Package ID:** Dynamically parsed from `sui-contracts/lightning/deploy_state_devnet.json` (or `testnet.json`)
- **LND Dir:** `~/.lnd-agent` (Ensure each node gets a distinct directory if running multiple)
- **RPC:** `127.0.0.1:10009` (Avoid port collisions)
- **REST:** `127.0.0.1:8081`
- **Listen:** `0.0.0.0:9735`
- **Macaroon:** `~/.lnd-agent/data/chain/sui/<NETWORK>/admin.macaroon`
- **Wallet Password:** (Determine dynamically / Generate securely and store locally)
- **Sui Address:** (Derived programmatically via `lncli newaddress p2wkh`)
- **Node Pubkey:** (Derived programmatically via `lncli getinfo`)

### CLI Prefix
```bash
LNCLI="lncli --lnddir=~/.lnd-agent --rpcserver=127.0.0.1:10009 --macaroonpath=~/.lnd-agent/data/chain/sui/<NETWORK>/admin.macaroon"
```

---

## Step 1: Start the LND Node (Connecting to Sui)

To initialize the node natively on the Sui blockchain, you must launch `lnd` with the Sui chain backend flags. You must explicitly specify whether you are connecting to **Devnet** or **Testnet**.

- For **Devnet**: Use `--suinode.devnet` and `--suinode.rpchost=https://fullnode.devnet.sui.io:443`
- For **Testnet**: Use `--suinode.testnet` and `--suinode.rpchost=https://fullnode.testnet.sui.io:443`

Additionally, you MUST provide the correct deployed Smart Contract Package ID via `--suinode.packageid`. You can find the latest active Package IDs manually or programmatically inside the `<repository_root>/sui-contracts/lightning/` directly by viewing:
- `deploy_state_devnet.json` (for devnet)
- `deploy_state_testnet.json` (for testnet)

**Example Startup Command (Connecting to Devnet)**:
```bash
rm ~/.lnd-agent/lnd.log

nohup lnd --suinode.active \
    --suinode.devnet \
    --suinode.rpchost=https://fullnode.devnet.sui.io:443 \
    --suinode.packageid="<PACKAGE_ID_FROM_DEPLOY_STATE_JSON>" \
    --listen=0.0.0.0:9735 \
    --rpclisten=127.0.0.1:10009 \
    --restlisten=127.0.0.1:8081 \
    --protocol.wumbo-channels \
    --protocol.no-anchors \
    --lnddir=~/.lnd-agent \
    > ~/.lnd-agent/lnd.log 2>&1 &
```

> **Agent Tip:** If running multiple agents on the same machine, ensure `--rpclisten`, `--listen`, `--restlisten`, and `--lnddir` are configured to unique ports/paths for each instance. Run this process in the background.

---

## Step 2: Wallet Creation and Funding

Before transacting, the node must generate a wallet and acquire native Sui coins to pay for channel open/close gas fees.

1. **Create the Wallet** (Only required on first boot):
   ```bash
   $LNCLI create
   ```

2. **Unlock the Wallet** (Required after every restart):
   ```bash
   lncli --lnddir=~/.lnd-agent --rpcserver=127.0.0.1:10009 --no-macaroons unlock
   # Enter password: <YOUR_GENERATED_SECURE_PASSWORD>
   ```
   **Note:** `lncli create/unlock` requires TTY interactive password input. You cannot use pipelines/heredocs to input the password. For automation, you must use `expect` or a PTY.

2. **Generate a Sui Address**:
   ```bash
   lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon newaddress p2wkh
   ```
   Wait for the JSON response and parse the `"address"` field. Note that while the command is `p2wkh` (Bitcoin standard), the Loka Zero-Intrusion Adapter automatically translates this into a valid **Sui Address (0x...)**.

3. **Check Balance**:
   ```bash
   lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon walletbalance
   ```

4. **Fund the Node via Faucet (Devnet/Testnet only)**:
   If your balance is `0`, you MUST request native SUI coins before you can open a channel. You can programmatically fund the address you generated in Step 2 by executing a cURL request to the appropriate Sui Faucet.

   **For Devnet**:
   ```bash
   curl --location --request POST 'https://faucet.devnet.sui.io/v2/gas' \
   --header 'Content-Type: application/json' \
   --data-raw '{"FixedAmountRequest": {"recipient": "<YOUR_SUI_ADDRESS>"}}'
   ```
   
   **For Testnet**:
   ```bash
   curl --location --request POST 'https://faucet.testnet.sui.io/v2/gas' \
   --header 'Content-Type: application/json' \
   --data-raw '{"FixedAmountRequest": {"recipient": "<YOUR_SUI_ADDRESS>"}}'
   ```
   **⚠️ Note:** You must use the `/v2/gas` endpoint. The old `/gas` endpoint is deprecated and will return `Route deprecated`. *(Wait up to 30 seconds for the network to process the faucet request, then check your balance again).*

---

## Step 3: Connect to an External Agent (Peer)

To route payments, you must first establish a P2P TCP connection with the target Agent's node. You need their Lightning Identity Pubkey and network host.

**Connecting to Loka Official Seed Nodes:**
If you want to join the main network backbone and ensure persistent routing, connect your Agent directly to the official Loka Seed Nodes.

- **EU Seed Node:** `0276bf6dc8fd0ce046c40c0c504d586419ecfdc456909b7f17e60e4da824e7afc7@lnd-seed-eu.loka.cash:9735`
- **US Seed Node:** `0268e7d59cfe59230ac6d0af4750bc5042bd6209e9cae1da32f98f8ee9ef9596a9@lnd-seed-us.loka.cash:9735`

Example connection command using the EU seed node:
```bash
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon connect 0276bf6dc8fd0ce046c40c0c504d586419ecfdc456909b7f17e60e4da824e7afc7@lnd-seed-eu.loka.cash:9735
```

Verify the peer connection was successful:
```bash
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon listpeers
```

---

## Step 4: Open a Channel (Deploying Move Smart Contract)

Open a payment channel with the connected peer. This step generates a real on-chain Sui transaction that interacts with the `lightning.move` smart contract.

```bash
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon openchannel --node_key=<TARGET_PUBKEY> --local_amt=<AMOUNT_IN_MIST>
```
*Example: `--local_amt=100000000` (100,000,000 MIST = 0.1 SUI).*

**Crucial Agent Check:** Since this is an on-chain operation on Sui (DAG-BFT), finality is sub-second. You should quickly verify the channel transitioned from "pending" to "active":
```bash
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon listchannels
```
*(Wait until `"active": true` is visible for the target pubkey).*

---

## Step 5: Execute an Agent-to-Agent Payment

Once the channel is active, payments route over the Lightning Network infinitely with zero on-chain gas fees and zero latency.

### 1. The Receiving Agent Generates an Invoice
The Agent receiving the payment must generate an invoice indicating the required amount.
```bash
# Executed by the Receiving Agent
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon addinvoice --amt=<AMOUNT_IN_MIST> --memo="API Service Payment"
```
Parse the `"payment_request"` string (e.g., `lnsb1...`) from the JSON output and transmit it to the Paying Agent.

### 2. The Paying Agent Pays the Invoice
The paying Agent routes the payment through the established channel using the payment request.
```bash
# Executed by the Paying Agent
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon payinvoice --pay_req=<PAYMENT_REQUEST_STRING> --force
```
Upon successful payment, carefully parse the resulting JSON and print/log the `payment_hash`. This serves as the cryptographic receipt (bill of payment) proving the transaction was completed.

### 3. Verify Payment Success
Both agents should inspect the route and completion status:
- Paying Agent checks: `lncli listpayments`
- Receiving Agent checks: `lncli lookupinvoice --r_hash=<PAYMENT_HASH>`

---

## Automated Shutdown & Cleanup (Force Close)

If the target Agent becomes completely unresponsive (livelock), you can unilaterally execute a Force Close via the Sui Smart Contract:

```bash
lncli --lnddir=~/.lnd-agent --macaroonpath=~/.lnd-agent/data/chain/sui/testnet/admin.macaroon closechannel --force <CHANNEL_POINT_TXID> <OUTPUT_INDEX>
```
*(You can find the Channel Point in the `listchannels` output).*

---

## Troubleshooting / Gotchas

### 1. `--suinode.packageid` must match the network
- **Devnet:** Read from `sui-contracts/lightning/deploy_state_devnet.json`
- **Testnet:** Read from `sui-contracts/lightning/deploy_state_testnet.json`
- **❌ Error:** Using the testnet packageid to connect to devnet will result in `insufficient SUI balance: have 0 MIST`, even if the on-chain address has a balance, because the contract addresses don't match.

### 2. `insufficient SUI balance: have 0 MIST` but walletbalance has funds
- **Most Likely Cause:** The packageid does not match the current network (see above).
- **Secondary Cause:** The wallet was just funded via faucet and hasn't synced yet. Try restarting LND.

### 3. Faucet endpoint has migrated
- ❌ `https://faucet.devnet.sui.io/gas` → Returns `Route deprecated. Use /v2/gas instead.`
- ✅ `https://faucet.devnet.sui.io/v2/gas`

### 4. `upstream request timeout` / `stream timeout`
- The Sui Devnet fullnode occasionally times out on `executeTransactionBlock` (write operations).
- In earlier versions of LND, the HTTP client timeout was only 10s. This has been updated to 60s, and the transaction execution flag has been natively changed to `WaitForEffectsCert` to mitigate proxy layer drops.
- If timeouts persist, check the Sui Devnet status or manually switch your RPC endpoints.

### 5. `lncli create` reports `inappropriate ioctl for device`
- `lncli create/unlock` requires a TTY. You cannot use pipelines/heredocs to input the password.
- Solution: Use `expect` or execute it in PTY/Interactive mode.

### 6. You must restart LND after updating binaries
- Running `make install` to compile new binaries will not automatically take effect.
- You must forcefully kill the agent (`pkill -9 -f lnd-agent` or `$LNCLI stop`) and then officially restart it.
- After restarting, you will need to unlock the wallet again using `lncli unlock`.

### 7. Reconnecting to an already connected peer will error but is harmless
- `already connected to peer: ...` is normal behavior. Just ignore it.
