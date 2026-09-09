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

The scheduler prefers providers advertising coverage of the requested height.
If none are available, it tries proof-only `NODE_UTREEXO` peers, which may retain
proofs after an assumed checkpoint. This is a bounded availability probe, not an
archive guarantee: the 32-pair work window, proof verification, and timeout and
disconnect failover still apply. Explicit `NODE_NETWORK_LIMITED` ranges remain
respected.

Blocks and headers come from peers advertising block services. Proof-only peers
need neither block-service bits nor witness support. Historical catch-up requires
a proof provider covering the requested heights. Up to 32 block/proof pairs are
pending; invalid proofs, disconnects, and 15 seconds without proof progress
retry another eligible provider while retaining downloaded blocks. Local block
validation time is excluded from that deadline. Mining readiness requires the
validated block tip to match the greatest-work header, and `getblockchaininfo`
reports the independently downloaded header height.

Header download continues when new blocks are announced during compact IBD,
even while older block proofs are outstanding. Transaction inventories are
deferred until validation catches up. Proof-aware transaction `notfound` replies
use the normal transaction ban scoring: ten transient points per missing
transaction, with disconnection and banning above the configured threshold.
The temporary exemption for proof-cache misses has been removed. The sidecar
regenerates requested expired/evicted preparations when possible instead of
immediately replying `notfound`. A sidecar may reconnect when its transaction
proof anchor changes; normal proof-provider failover handles that reconnect.

Full block proofs are checked before block processing even when the mempool
already remembers proofs for the same inputs. Transaction partial-proof checks
can reuse those cached hashes; using them to precheck a block could hide a
corrupt supplied hash until block processing. The full check keeps that failure
attributed to the proof provider and allows retry with another provider.

The mempool tracks input-proof ownership across both accepted transactions and
orphans. Fee replacement and orphan promotion transfer ownership without
pruning a proof still needed by the surviving transaction. This prevents a
valid replacement from entering the mempool and then breaking template
generation because its shared input proof was uncached with the old entry.

The v0.18 accumulator adapter also promotes a cached proof sibling to a
remembered input after successful transaction verification, and persists cleared
remember flags when an input is released. Reusing an expanded proof path must
neither discard a surviving input on the next block nor retain unused siblings
indefinitely. Template selection keeps its leaf-data snapshot instead of fetching
the same transaction again after a concurrent replacement may have removed it.

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
mining, and matching accumulator roots. Mainnet proof validation beyond the
AssumeUtreexo checkpoint has completed on ARM64, with matching sidecar roots and
a Public Pool job matching the compact node's template. Sustained load and a
combined reorg integration remain unvalidated. Production genesis synchronization,
TTL proof serving, and compact-wallet proof acquisition are outside this setup;
the upstream committed-TTL synchronization path is unchanged.

See the [Orange Pi deployment notes](orangepi-compact-pool.md) for NVMe data
storage, a loopback proof tunnel, Public Pool readiness and sync timing.
