# Compact mempool mining with Core and the sidecar

This branch contains the compatibility patch for upstream utreexod v0.6.0,
commit `fe71f3d9282ef0812f7f6087f0c0df9ce0fda508`. Use it with Bitcoin Core
31.1 and [sidecar v0.5.0-beta.1](https://github.com/johnny9/utreexo-core-rpc/tree/v0.5.0-beta.1).
The patch is already applied when building this branch.

Wallets submit ordinary transactions to Core. The sidecar obtains transactions
from Core and prepares proofs. Compact utreexod independently validates them,
maintains its own mempool, and supplies `getblocktemplate` and standard
`submitblock` to a local pool. Core supplies ordinary blocks and headers;
utreexod relays accepted mined blocks back to Core.

The examples below use mainnet with all services on one host.

## Build

Install Go 1.25 or newer, then:

```sh
git clone --branch core-sidecar-relay-v0.6.0 --single-branch \
  https://github.com/johnny9/utreexod.git utreexod-relay
cd utreexod-relay
go test -mod=readonly ./netsync ./mining ./mempool ./wire
go build -mod=readonly -o utreexod-relay .
```

This build does not require the optional BDK wallet or Rust toolchain.

## Prepare Core and the sidecar

Use an already synchronized, unpruned Core node with RPC and transaction relay
enabled. The relevant Core settings are `server=1`, `listen=1`, `prune=0`, and
`blocksonly=0`. A transaction index is not required. Core RPC normally listens
on port 8332 and its mainnet P2P listener on port 8333.

Configure the existing sidecar instance using its current checkpoint,
online-state, and proof-store paths. An example startup command is:

```sh
utreexo-bridge \
  --rpc-port=8332 --rpc-cookie=/var/lib/bitcoin/.cookie \
  --checkpoint=/var/lib/utreexo-bridge/mainnet-943013-compact.chk \
  --online-state=/var/lib/utreexo-bridge/forest --online-wal --follow \
  --proof-store=/var/lib/utreexo-bridge/proofs \
  --p2p-network=mainnet --p2p-bind=127.0.0.1 --p2p-port=8338 \
  --core-tx-peer=127.0.0.1:8333
```

Adjust these paths to the installation. The sidecar user needs read access to
Core's RPC cookie. For an existing service, update its current arguments and
restart that instance; keep a single writer for the sidecar state.

The RPC endpoint and `--core-tx-peer` must refer to the same Core node. Mainnet
requires the trusted AssumeUtreexo checkpoint at height 943,013, block hash
`00000000000000000001c595730bd4a5fb0e2b35af70882962ce7ae602f48aff`, or online
state resumed from it. The full proving checkpoint belongs to the sidecar;
compact utreexod uses its built-in AssumeUtreexo state. See the sidecar's
[checkpoint installation instructions](https://github.com/johnny9/utreexo-core-rpc/blob/v0.5.0-beta.1/README.md).

## Configure compact utreexod

Create a directory for the configuration:

```sh
mkdir -p "$HOME/.utreexod-compact"
```

Save the following as `~/.utreexod-compact/utreexod.conf`, replacing the RPC
password:

```ini
[Application Options]
datadir=~/.utreexod-compact/data
logdir=~/.utreexod-compact/logs
prune=550

connect=127.0.0.1:8333
connect=127.0.0.1:8338
utreexoproofpeer=127.0.0.1:8338
nolisten=1

rpcuser=pool
rpcpass=REPLACE_WITH_A_LONG_RANDOM_PASSWORD
rpclisten=127.0.0.1:8334
notls=1
```

Start the consumer:

```sh
chmod 600 "$HOME/.utreexod-compact/utreexod.conf"
./utreexod-relay --configfile="$HOME/.utreexod-compact/utreexod.conf"
```

The mempool, compact validation, and AssumeUtreexo bootstrap are enabled by
default. Omit `noutreexo`, `noassumeutreexo`, and proof-index options for this
mainnet deployment.

Proof providers are selected automatically from connected peers' advertised
services. Multiple providers share requests; blocks and headers can come from
ordinary Core peers. Add a standard v0.6 proof provider with another `connect`
or `addpeer` entry. It does not need `utreexoproofpeer`.

`utreexoproofpeer` only marks sidecar endpoints whose block archives use native
tree-height target positions instead of v0.6's fixed 63-row positions. Repeat it
for each such sidecar. Each marked endpoint must also appear in `connect` or
`addpeer` and use a numeric IPv4 address. This option does not restrict which
other peers may provide proofs.

The scheduler observes these advertised capabilities:

| Services | Proof range | Supplies blocks |
| --- | --- | --- |
| `NODE_UTREEXO` | New blocks | No promise |
| `NODE_UTREEXO \| NODE_NETWORK` | All historical blocks | Yes |
| `NODE_UTREEXO \| NODE_NETWORK_LIMITED` | Latest 288 blocks | Latest 288 |
| `NODE_UTREEXO_ARCHIVE` | All historical blocks | No promise |

A proof-only connection does not need `NODE_NETWORK` or `NODE_WITNESS`.
Historical catch-up requires a provider covering the requested heights;
`NODE_UTREEXO` alone is insufficient for a backlog. At most 32 block/proof pairs
are pending. A disconnect, invalid proof, or 15-second proof timeout retries
unfinished work with another eligible provider while retaining downloaded
blocks. If no eligible provider remains, validation waits for one to connect.
Unavailable proofs do not blame a block source that delivered its window.

The example binds pool RPC to loopback and disables TLS there. For a pool on
another machine, use a private tunnel or configure TLS.

`prune=550` is a block-pruning target in MiB, not a total disk limit. Roughly
5 GB is an initial planning allowance for compact utreexod, not a measured
minimum; Core and sidecar storage are additional.

## Check the mempool and configure the pool

Wait for the sidecar and utreexod to catch up. Compare `getbestblockhash` on Core
and utreexod, then inspect the compact mempool:

```sh
curl --user pool -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"1.0","id":1,"method":"getrawmempool","params":[]}' \
  http://127.0.0.1:8334
```

Curl prompts for the configured RPC password. Request a block template with:

```sh
curl --user pool -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"1.0","id":1,"method":"getblocktemplate","params":[{"rules":["segwit"],"capabilities":["coinbasevalue"]}]}' \
  http://127.0.0.1:8334
```

Point the pool's `getblocktemplate` and `submitblock` calls at utreexod on port
8334 with the configured RPC credentials. Submit the ordinary serialized block
hex. With `coinbasevalue`, the pool constructs its coinbase. A pool requesting
`coinbasetxn` instead needs a valid mainnet `miningaddr` in utreexod's config.

Wallet transaction submissions still go to Core on port 8332. Core and
utreexod enforce their own mempool rules, so their transaction sets can differ.
Build work from utreexod's template: submission fails if a needed transaction,
parent, or proof is unavailable, or the block no longer builds on the current
tip. Proof-peer reconnects after an accumulator tip change are expected.

## Validation and scope

The release integration uses real Core 31.1, the C++ sidecar, and this patched
compact consumer on regtest. It covers transaction relay, corrupt-proof
rejection, full/partial/zero-additional-hash requests, reconnect and restart
recovery, template contents, rejection of an incomplete submission, standard
`submitblock`, block relay in both directions, and matching accumulator roots.
The integration harness and reproduction command are in the sidecar's
[relay guide](https://github.com/johnny9/utreexo-core-rpc/blob/master/doc/core-backed-transaction-proof-relay.md).

The additional `core_utreexod_proof_peers.py` integration combines the actual
sidecar with a standard v0.6 utreexod proof generator. It exercises archive-only
and `NODE_UTREEXO`-only connections, timeout/invalid-proof/disconnect failover,
separate Core block downloads, transaction proofs, template submission, and
matching roots. These consumer changes follow sidecar v0.5.0-beta.1; use this
branch or the updated patch on sidecar master, rather than the beta.1 patch.

Full mainnet catch-up, sustained mainnet load, and a combined reorg integration
remain unvalidated. Production genesis synchronization, TTL proof serving, and
compact-wallet `sendrawtransaction` proof acquisition are outside this setup.
The upstream committed-TTL synchronization path is unchanged.
