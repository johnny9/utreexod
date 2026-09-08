# Custom AssumeUtreexo snapshots

This fork accepts compact JSON snapshots exported by
[`utreexo-core-rpc`](https://github.com/johnny9/utreexo-core-rpc)'s
`tools/export-assumeutreexo.py`. Upstream v0.6.0 supports compiled AssumeUtreexo
points; its `--addcheckpoint` option does not import accumulator roots.

Start a fresh compact node with both options:

```sh
./utreexod \
  --datadir=/mnt/data/utreexod-snapshot/data \
  --assumeutreexo-snapshot=/path/to/snapshot.json \
  --assumeutreexo-snapshot-sha256=<EXPECTED-SHA256> \
  --connect=<CORE-BLOCK-PEER> --connect=<SIDECAR-PROOF-PEER>
```

Use a trusted snapshot and obtain its expected SHA256 independently of an
untrusted download. The pin protects file integrity; Bitcoin headers do not
authenticate accumulator roots. The snapshot state is explicitly assumed
through its block. Later blocks and proofs undergo normal validation.

Keep the exact file and both options on restart. The database pins this anchor
and rejects changes, omission, or importing into an already progressing node.
The imported block becomes a checkpoint; the node verifies its hash against
the downloaded header chain before activating the roots. Roots remain queryable
when the snapshot is at the tip and no suffix block has arrived yet.
On regtest, add `--regtestkeepdb` on every run to preserve the database; the
upstream default removes it on startup. Custom signet challenges are unsupported.

Custom heights must be positive and at or above all configured checkpoints and
the committed TTL boundary. Current mainnet permits height 943,013 or later;
testnet3 permits 4,898,397 or later; standard signet permits 294,687 or later;
regtest permits any positive height. The provider needs proofs for the entire
suffix after that height. Full header download is still required.

Custom snapshots cannot be combined with `--noassumeutreexo`, `--noutreexo`,
proof-index modes, or `--nocheckpoints`. Mining remains gated on the validated
tip reaching the best header. Existing defaults are unchanged when the snapshot
options are absent on a database that has never imported a custom snapshot.

The version 1 JSON format and export/validation instructions are documented in
the sidecar's
[`doc/assumeutreexo-snapshots.md`](https://github.com/johnny9/utreexo-core-rpc/blob/master/doc/assumeutreexo-snapshots.md).
