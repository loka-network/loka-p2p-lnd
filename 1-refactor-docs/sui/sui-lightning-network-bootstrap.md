# Bootstrapping the Loka Sui-Lightning Network

Bootstrapping a brand new, isolated Loka Sui-Lightning network graph is like lighting the first fires in a dark forest. Without any pre-existing interconnected routing nodes, we must establish a highly available, discoverable **Seed Node (Hub)** architecture.

This guide details how to plan, bootstrap, and scale a completely new Agentic P2P payment network from scratch.

---

## 1. Network Topology: The Hub & Spoke Model

In a newly launched network, all AI Agents default to being isolated "islands". To solve this, we adopt a classic hybrid Star/Mesh topology:

1. **Seed Nodes (Core Hubs/Backbone)**: High-throughput routing nodes operated by the core team or large institutional liquidity providers. They have static public IPs and domain names. They must be directly connected to each other (Full Mesh) to form an indestructible routing backbone.
2. **Agent Nodes (Edge Spokes)**: Thousands of AI agents, mobile clients, and terminal devices. They do not need static IPs. Upon boot, they simply connect to any public Seed Node to instantly merge into the global routing graph.

> **Best Practice**: For an initial network, deploying **3 to 5 Seed Nodes** globally (e.g., US East, Europe, Asia) provides high availability without creating massive operational overhead or fragmenting initial liquidity.

---

## 2. Bootstrapping the First Seed Nodes

Whether launching `seed1` or `seed2`, the golden rule for a seed node is: **It must explicitly declare its public IP and bind to external ports so the outside world can discover it.**

### Example Startup Configuration

Assuming we deploy our first server in US East (IP: `198.51.100.1`):

```bash
lnd --chain=sui \
    --sui.env=testnet \
    --sui.rpc=https://fullnode.testnet.sui.io:443 \
    --listen=0.0.0.0:9735 \
    --externalip=198.51.100.1:9735 \
    --alias="Loka-Seed-EastUS" \
    --color="#1DA1F2" \
    --protocol.wumbo-channels \
    --protocol.no-anchors \
    --lnddir=~/.lnd-seed
```

**Key Parameters:**
- `--listen=0.0.0.0:9735`: Binds the node to listen on port 9735 across all network interfaces.
- `--externalip=<Public_IP>`: **Crucial for Hubs!** This encodes the node's public IP into the global Gossip network broadcasts. Without it, other nodes will hear about the seed's existence but won't know how to route TCP traffic to it.
- `--protocol.wumbo-channels`: **Critical for SUI!** Because SUI's base unit (MIST) is much smaller than Bitcoin's Satoshi in practical value, the default Lightning network cap of ~16M base units translates to pennies in SUI. Enabling the Wumbo toggle overrides the protocol's channel size limit, allowing for arbitrarily large liquidity channels.
- `--protocol.no-anchors`: **Highly Recommended for SUI!** Disables Bitcoin-specific CPFP (Child Pays For Parent) anchor dust outputs. Because Sui features deterministic fast finality and no mempool congestion, these 330-MIST dust outputs are completely unnecessary. Disabling them prevents the LND Sweeper subsystem from generating endless error logs trying to process `suiwallet` addresses.
- `--alias` and `--color`: Branding elements visible on network explorers.

---

## 3. DNS Configuration and Node Discovery

While hardcore users can connect using raw `Pubkey@IP:Port` strings, this is terrible for dynamic scaling and business integration. We must configure subdomains for our Seed nodes.

### 1. Direct A-Record Mapping
In your DNS provider (e.g., Cloudflare, Route53), map subdomains to your seed public IPs:

- `seed-us.loka.network`  ->  `A Record` -> `198.51.100.1`
- `seed-eu.loka.network`  ->  `A Record` -> `203.0.113.1`

This makes your Lighting URI incredibly elegant for edge agents:
> `03abcdef1234567890abcdef1234567890abcdef@seed-us.loka.network:9735`

### 2. DNS Round-Robin (Load Balancing)
Create a generic subdomain (e.g., `seeds.loka.network`) and attach multiple A-Records pointing to different seed nodes. When an Agent resolves this domain during startup, the DNS server returns a random healthy seed IP, natively achieving basic load balancing.

---

## 4. Building the Backbone & Liquidity (Large vs. Small Channels)

Starting 3 independent Seed Nodes doesn't automatically route payments between them. As the network architect, you must **manually connect the backbone nodes and fund massive liquidity pools between them.**

### Understanding Channel Sizes & Wumbo Requirement
In the Lightning Network, there is a fundamental difference between channel capacities:
- **Small Channels (Agent Channels)**: Opened by Edge AI Agents connecting to a Seed. They are usually small and only intended to cover the specific agent's daily micro-transaction needs.
- **Large Channels (Wumbo / Backbone Hub Channels)**: Inter-seed channels **MUST** be exceptionally large. In traditional Bitcoin LN, these are called "Wumbo Channels". By enabling `--protocol.wumbo-channels=true` during startup, LND bypasses the hard-coded 0.16 BTC size restriction, dynamically allowing us to pass massive `--local_amt` values required for SUI routing.

### Executing the Backbone Mesh
On the `seed1` server, actively connect to `seed2` and `seed3` and open **Large Channels**:

```bash
# Connect and open a massive liquidity channel to Europe Seed
lncli connect <Seed2_Pubkey>@seed-eu.loka.network:9735
lncli openchannel --node_key=<Seed2_Pubkey> --local_amt=50000000000 # E.g., injecting 50 SUI of routing liquidity

# Connect and open a massive liquidity channel to Asia Seed
lncli connect <Seed3_Pubkey>@seed-asia.loka.network:9735
lncli openchannel --node_key=<Seed3_Pubkey> --local_amt=50000000000
```
Once confirmed on the Sui blockchain, an incredibly fluid, high-capacity Iron Triangle backbone is formed!

---

## 5. Integrating Edge Agents

With the backbone deployed, the isolated Edge Agents can finally join the network.

When a third-party AI Agent boots up its `lnd` instance, it simply runs a persistent background connection command:
```bash
lncli connect <Seed1_Pubkey>@seed-us.loka.network:9735
```
Or, by hardcoding the seed in their `lnd.conf`:
```ini
[Application Options]
addpeer=<Seed1_Pubkey>@seed-us.loka.network:9735
```
The moment the Agent boots, it automatically syncs the global channel graph via the Gossip protocol and can instantly route payments to any other connected Agent worldwide.

---

## 6. Scaling: How to Add a New Seed Node

When the network scales (e.g., supporting 100,000 concurrent Agents), 3 nodes might struggle with the sheer volume of Gossip broadcast bandwidth. You can dynamically scale horizontally (add `seed4`) without network downtime:

1. **Deploy the Server**: Boot `seed4` with its new `--externalip` and unique Pubkey.
2. **Update DNS**: Add the new IP to a specific region `seed-jp.loka.network` or append it to the Round-Robin `seeds.loka.network` pool.
3. **Mesh into Backbone**: Crucially, run `lncli connect` and `lncli openchannel` on `seed4` to establish Large Channels with the existing `seed1` and `seed2`.
4. **Liquidity Propagation**: Thanks to the Gossip protocol, within **10 minutes** of connecting to `seed1`, the routing tables of all 100,000 Edge Agents will automatically update to include this brand new routing hub!
