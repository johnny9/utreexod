# Orange Pi compact node and Public Pool

The `core-sidecar-relay-v0.6.0` branch has been deployed on an ARM64 Orange Pi
CM5 with 8 GiB RAM and an ext4 NVMe data filesystem mounted at `/mnt/data`.
Bitcoin Core supplies blocks and headers; the sidecar supplies Utreexo proofs.
The node uses compact validation and the built-in AssumeUtreexo checkpoint at
height 943013. It does not require a full UTXO database or a proof index.

An example node configuration is:

```ini
[Application Options]
datadir=/mnt/data/utreexod/data
logdir=/mnt/data/utreexod/logs
prune=550
nolisten=1
connect=CORE_HOST:8333
connect=127.0.0.1:18338
rpcuser=publicpool
rpcpass=REPLACE_WITH_A_RANDOM_SECRET
rpclisten=127.0.0.1:8334
notls=1
maxpeers=16
```

Replace `CORE_HOST` with the Core host's reachable address and generate a unique
RPC secret. The second peer in this example is an SSH reverse tunnel: from the
sidecar host, forward the Orange Pi's loopback port 18338 to the sidecar's
loopback port 8338 using `ssh -N -R 127.0.0.1:18338:127.0.0.1:8338` and the
Orange Pi SSH destination. Both the tunnel and node can run as supervised
services. Wait for the sidecar to catch Core's tip and open its proof listener.

Run the node as a dedicated service user, require the `/mnt/data` mount before
startup, keep its writable paths on that filesystem and enable service restart
on failure. The deployment uses `GOMEMLIMIT=4GiB`; this is a Go runtime target,
not a hard limit on the entire process. `prune=550` is a block-pruning target in
MiB, not a total disk-usage limit.

Use the [Public Pool compatibility branch](https://github.com/johnny9/public-pool/tree/utreexod-compact)
and [matching UI branch](https://github.com/johnny9/public-pool-ui/tree/utreexod-compact).
The pool uses `getmininginfo` for its initial RPC probe. Its environment must use
the node's loopback RPC address, port, username and secret. Conventional listener
ports are 3333 for Stratum and 3334 for the pool API.

The pool's `contrib/utreexod/wait-for-template.py` startup helper waits for
validated blocks to reach known headers and connected block/proof-provider
heights, then requires a successful SegWit block template. Run it with the same
environment as Public Pool before starting the pool service. This prevents work
from being offered while compact proof validation is still catching up.

Serve the UI's production build and reverse-proxy `/api/` to the local pool API
on the same origin. The UI derives its Stratum host from the page hostname.

## Validation and timing

The deployed builds passed an isolated ARM64 regtest exercise: Stratum
subscription, authorization, a mining job, an accepted share and a valid block
advancing height 1 to 2 with changed accumulator roots. Independent proof-provider
failover and matching accumulator roots are also covered by the sidecar's
Core/utreexod integration test.

At 2026-09-09 13:54 UTC, the compact node, Core and sidecar matched at height
966214. The sidecar's accumulator roots and leaf count matched the node, and
Public Pool delivered a Stratum job matching the node's block template. This
mainnet check did not submit a share or mine a block.

The first post-checkpoint block was validated at 2026-09-08 18:34:37 UTC. The
23,090-block suffix through height 966103 took about 10 hours 32 minutes.
A stale header target and a ban caused by normal transaction-proof cache misses
then stalled the original build. After a restart at 13:50:26 UTC, validation
continued from the existing database and reached Core's live tip by 13:54 UTC.
Total observed wall time was about 19 hours 19 minutes, including about
8 hours 44 minutes stalled. Excluding that stall gives approximately
10 hours 36 minutes; this is not an uninterrupted benchmark and excludes
earlier header bootstrap and sidecar recovery. The current branch fixes header
tracking and the cache-miss ban; a fresh uninterrupted timing run is still needed.

For elapsed-time measurements, record when block 943014 is first validated and
sample the validated block height alongside Core's current height. The
`getblockchaininfo` header height alone does not prove compact synchronization
is complete. Record completion when the validated node tip matches Core's tip,
then check the current block template and pool job separately.
