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
   make install
   ```

Ensure the Loka binaries (`lnd` and `lncli`) are compiled and available in your `$PATH`.

---

## Step 1: Start the LND Node (Connecting to Sui)

To initialize the node natively on the Sui blockchain, you must launch `lnd` with the Sui chain backend flags. By default, you should connect to the SUI Testnet or Devnet.

```bash
lnd --chain=sui \
    --sui.env=testnet \
    --sui.rpc=https://fullnode.testnet.sui.io:443 \
    --rpclisten=localhost:10009 \
    --listen=localhost:9735 \
    --restlisten=localhost:8080 \
    --lnddir=~/.lnd-agent
```

> **Agent Tip:** If running multiple agents on the same machine, ensure `--rpclisten`, `--listen`, `--restlisten`, and `--lnddir` are configured to unique ports/paths for each instance. Run this process in the background.

---

## Step 2: Wallet Creation and Funding

Before transacting, the node must generate a wallet and acquire native Sui coins to pay for channel open/close gas fees.

1. **Create the Wallet** (Only required on first boot):
   ```bash
   lncli --lnddir=~/.lnd-agent create
   ```
   *(You will be prompted to enter a wallet password. You must script the standard input or use the `--wallet-password` flag via a temp file for full autonomy).*

2. **Generate a Sui Address**:
   ```bash
   lncli --lnddir=~/.lnd-agent newaddress p2wkh
   ```
   Wait for the JSON response and parse the `"address"` field. Note that while the command is `p2wkh` (Bitcoin standard), the Loka Zero-Intrusion Adapter automatically translates this into a valid **Sui Address (0x...)**.

3. **Check Balance**:
   ```bash
   lncli --lnddir=~/.lnd-agent walletbalance
   ```
   > **Note:** For testing, if the balance is 0, invoke the Sui Testnet Web Faucet using the address generated above before proceeding.

---

## Step 3: Connect to an External Agent (Peer)

To route payments, you must first establish a P2P TCP connection with the target Agent's node. You need their Lightning Identity Pubkey and network host.

```bash
lncli --lnddir=~/.lnd-agent connect <TARGET_PUBKEY>@<TARGET_HOST>:<PORT>
```
*Example: `lncli connect 03abcdef...@12.34.56.78:9735`*

Verify the peer connection was successful:
```bash
lncli --lnddir=~/.lnd-agent listpeers
```

---

## Step 4: Open a Channel (Deploying Move Smart Contract)

Open a payment channel with the connected peer. This step generates a real on-chain Sui transaction that interacts with the `lightning.move` smart contract.

```bash
lncli --lnddir=~/.lnd-agent openchannel --node_key=<TARGET_PUBKEY> --local_amt=<AMOUNT_IN_MIST>
```
*Example: `--local_amt=100000000` (100,000,000 MIST = 0.1 SUI).*

**Crucial Agent Check:** Since this is an on-chain operation on Sui (DAG-BFT), finality is sub-second. You should quickly verify the channel transitioned from "pending" to "active":
```bash
lncli --lnddir=~/.lnd-agent listchannels
```
*(Wait until `"active": true` is visible for the target pubkey).*

---

## Step 5: Execute an Agent-to-Agent Payment

Once the channel is active, payments route over the Lightning Network infinitely with zero on-chain gas fees and zero latency.

### 1. The Receiving Agent Generates an Invoice
The Agent receiving the payment must generate an invoice indicating the required amount.
```bash
# Executed by the Receiving Agent
lncli addinvoice --amt=<AMOUNT_IN_MIST> --memo="API Service Payment"
```
Parse the `"payment_request"` string (e.g., `lnsb1...`) from the JSON output and transmit it to the Paying Agent.

### 2. The Paying Agent Pays the Invoice
The paying Agent routes the payment through the established channel using the payment request.
```bash
# Executed by the Paying Agent
lncli --lnddir=~/.lnd-agent payinvoice --pay_req=<PAYMENT_REQUEST_STRING> --force
```

### 3. Verify Payment Success
Both agents should inspect the route and completion status:
- Paying Agent checks: `lncli listpayments`
- Receiving Agent checks: `lncli lookupinvoice --r_hash=<PAYMENT_HASH>`

---

## Automated Shutdown & Cleanup (Force Close)

If the target Agent becomes completely unresponsive (livelock), you can unilaterally execute a Force Close via the Sui Smart Contract:

```bash
lncli --lnddir=~/.lnd-agent closechannel --force <CHANNEL_POINT_TXID> <OUTPUT_INDEX>
```
*(You can find the Channel Point in the `listchannels` output).*
