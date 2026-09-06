# Compact validation and mining with independent proof peers

This fork extends upstream utreexod v0.6.0 with independent block and proof
sources and compact mempool mining. Connect to block peers and proof providers
using ordinary `connect` or `addpeer` entries. Provider selection depends on
advertised services, without endpoint-specific flags or format adapters.

```ini
[Application Options]
connect=127.0.0.1:8333
connect=127.0.0.1:8338
prune=550
nolisten=1
rpcuser=pool
rpcpass=REPLACE_WITH_A_LONG_RANDOM_PASSWORD
rpclisten=127.0.0.1:8334
notls=1
```

Replace the example peer endpoints with your block and proof providers. The
mempool, compact validation, and mainnet AssumeUtreexo bootstrap are enabled by
default. Omit proof indexes, `noutreexo`, and `noassumeutreexo` for this deployment.
The RPC example disables TLS only for a loopback listener.

| Services | Proof availability |
| --- | --- |
| `NODE_UTREEXO` | New blocks and transaction proofs |
| `NODE_UTREEXO` with `NODE_NETWORK` | All historical blocks |
| `NODE_UTREEXO` with `NODE_NETWORK_LIMITED` | Latest 288 blocks |
| `NODE_UTREEXO_ARCHIVE` | Historical proofs without requiring block service |

Blocks and headers come from peers advertising block services. Proof-only peers
need neither block-service bits nor witness support. Historical catch-up requires
a proof provider covering the requested heights. Up to 32 block/proof pairs are
pending; invalid proofs, disconnects, and 15-second timeouts retry another
eligible provider while retaining downloaded blocks.

Submit wallet transactions to a node that accepts ordinary transaction
submissions and relays them to proof providers. Compact utreexod validates the
proof-bearing transactions it receives and maintains its own mempool. Point the
pool at its `getblocktemplate` and standard `submitblock` RPCs. Request the
`segwit` rule and submit ordinary serialized block hex. Missing transactions,
parents, or mempool proofs cause submission to fail closed.

For a submission matching the current template's parent and ordered transaction
witness IDs, `submitblock` reuses the template's assembled input proof. Nonce,
timestamp, and coinbase changes do not require rebuilding that proof. Each
submission receives a deep copy, and all normal block and proof validation still
runs. A changed transaction body, stale tip, or missing cached template uses the
existing local mempool proof path. Only the current template is retained.

`prune=550` is a block-pruning target in MiB, not a total disk limit. Regtest
validation covers independent proof sources, failure recovery, transaction relay,
mining, and matching accumulator roots. Mainnet catch-up, sustained load, and a
combined reorg integration remain unvalidated. Production genesis synchronization,
TTL proof serving, and compact-wallet proof acquisition are outside this setup;
the upstream committed-TTL synchronization path is unchanged.
