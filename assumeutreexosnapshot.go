package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"os"
	"time"

	"github.com/utreexo/utreexo"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
)

const snapshotRootsEncoding = "sha512_256_internal_bytes_high_row_to_low_row_present_roots"
const maxAssumeUtreexoSnapshotBytes = 16 * 1024

// assumeUtreexoSnapshot is a compact, explicitly trusted bootstrap state, not a
// UTXO dump or a full forest. Roots retain internal byte order (unlike block hashes).
type assumeUtreexoSnapshot struct {
	Format        string   `json:"format"`
	Version       uint32   `json:"version"`
	Network       string   `json:"network"`
	GenesisHash   string   `json:"genesis_hash"`
	Height        int32    `json:"height"`
	BlockHash     string   `json:"block_hash"`
	Bits          uint32   `json:"bits"`
	BlockSize     uint64   `json:"block_size"`
	BlockWeight   uint64   `json:"block_weight"`
	NumTxns       uint64   `json:"num_txns"`
	TotalTxns     uint64   `json:"total_txns"`
	MedianTime    int64    `json:"median_time"`
	NumLeaves     uint64   `json:"num_leaves"`
	RootsEncoding string   `json:"roots_encoding"`
	Roots         []string `json:"roots"`
}

func snapshotHash(value string) ([]byte, error) {
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != value {
		return nil, fmt.Errorf("snapshot hashes must be 64 lowercase hexadecimal characters")
	}
	return b, nil
}

func loadAssumeUtreexoSnapshot(path, pin string, params *chaincfg.Params) (*chaincfg.AssumeUtreexo, error) {
	expected, err := snapshotHash(pin)
	if err != nil {
		return nil, fmt.Errorf("--assumeutreexo-snapshot-sha256: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAssumeUtreexoSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssumeUtreexoSnapshotBytes {
		return nil, fmt.Errorf("AssumeUtreexo snapshot exceeds 16 KiB")
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(digest[:], expected) {
		return nil, fmt.Errorf("AssumeUtreexo snapshot SHA256 does not match the supplied pin")
	}
	var s assumeUtreexoSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid AssumeUtreexo snapshot: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("AssumeUtreexo snapshot has trailing JSON data")
	}
	if s.Format != "utreexod-assumeutreexo" || s.Version != 1 || s.RootsEncoding != snapshotRootsEncoding {
		return nil, fmt.Errorf("unsupported AssumeUtreexo snapshot format, version, or root encoding")
	}
	if s.Network != params.Name || s.GenesisHash != params.GenesisHash.String() {
		return nil, fmt.Errorf("AssumeUtreexo snapshot belongs to a different network")
	}
	if _, err := snapshotHash(s.BlockHash); err != nil {
		return nil, err
	}
	hash, err := chainhash.NewHashFromStr(s.BlockHash)
	if err != nil || *hash == (chainhash.Hash{}) {
		return nil, fmt.Errorf("invalid snapshot block hash")
	}
	if s.Height <= 0 || s.NumLeaves == 0 || s.NumLeaves > 1<<63 || len(s.Roots) != bits.OnesCount64(s.NumLeaves) {
		return nil, fmt.Errorf("invalid snapshot height, leaf count, or root count")
	}
	for _, stump := range params.TTL.Stump {
		if stump.NumLeaves > 0 && uint64(s.Height) < stump.NumLeaves-1 {
			return nil, fmt.Errorf("snapshot height %d is below the committed TTL boundary %d", s.Height, stump.NumLeaves-1)
		}
	}
	for _, checkpoint := range params.Checkpoints {
		if s.Height < checkpoint.Height || (s.Height == checkpoint.Height && *hash != *checkpoint.Hash) {
			return nil, fmt.Errorf("snapshot conflicts with checkpoint at height %d", checkpoint.Height)
		}
	}
	if s.Bits == 0 || s.BlockSize < 81 || s.BlockSize > 4_000_000 ||
		s.BlockWeight < s.BlockSize || s.BlockWeight > 4_000_000 || s.BlockWeight > 4*s.BlockSize ||
		s.NumTxns == 0 || s.NumTxns > s.BlockSize/10 || s.TotalTxns < s.NumTxns+uint64(s.Height) || s.MedianTime <= 0 {
		return nil, fmt.Errorf("invalid snapshot block statistics")
	}
	roots := make([]utreexo.Hash, len(s.Roots))
	for i, value := range s.Roots {
		b, err := snapshotHash(value)
		if err != nil {
			return nil, fmt.Errorf("snapshot root %d: %w", i, err)
		}
		copy(roots[i][:], b)
	}
	return &chaincfg.AssumeUtreexo{
		BlockHash: hash, BlockHeight: s.Height, Bits: s.Bits,
		BlockSize: s.BlockSize, BlockWeight: s.BlockWeight, NumTxns: s.NumTxns,
		TotalTxns: s.TotalTxns, MedianTime: time.Unix(s.MedianTime, 0),
		NumLeaves: s.NumLeaves, Roots: roots,
	}, nil
}

func (c *config) loadAssumeUtreexoSnapshot(params *chaincfg.Params) error {
	if c.AssumeUtreexoSnapshot == "" && c.AssumeUtreexoSnapshotSHA256 == "" {
		return nil
	}
	if c.AssumeUtreexoSnapshot == "" || c.AssumeUtreexoSnapshotSHA256 == "" {
		return fmt.Errorf("--assumeutreexo-snapshot and --assumeutreexo-snapshot-sha256 must be supplied together")
	}
	if c.NoAssumeUtreexo || c.NoUtreexo || c.DisableCheckpoints {
		return fmt.Errorf("custom AssumeUtreexo snapshots require compact mode, AssumeUtreexo, and checkpoints enabled")
	}
	if c.SigNetChallenge != "" {
		return fmt.Errorf("custom AssumeUtreexo snapshot network binding only supports standard signet")
	}
	// Check operator checkpoints too; the snapshot itself becomes a checkpoint
	// so a reorganization cannot cross its trusted bootstrap boundary.
	customParams := *params
	customParams.Checkpoints = mergeCheckpoints(params.Checkpoints, c.addCheckpoints)
	point, err := loadAssumeUtreexoSnapshot(cleanAndExpandPath(c.AssumeUtreexoSnapshot), c.AssumeUtreexoSnapshotSHA256, &customParams)
	if err != nil {
		return err
	}
	c.assumeUtreexoSnapshot = point
	c.addCheckpoints = mergeCheckpoints(c.addCheckpoints, []chaincfg.Checkpoint{{Height: point.BlockHeight, Hash: point.BlockHash}})
	return nil
}

// Pin the chosen trust anchor to this database. Import is only allowed before
// any non-genesis block state exists; restarts must keep the same exact file pin.
func bindAssumeUtreexoSnapshot(db database.DB, pin string, bestHeight int32) error {
	return db.Update(func(tx database.Tx) error {
		key := []byte("assumeutreexosnapshot-sha256")
		stored := tx.Metadata().Get(key)
		if stored != nil {
			if string(stored) != pin {
				return fmt.Errorf("database requires its original --assumeutreexo-snapshot and SHA256 pin; use a fresh data directory to change snapshots")
			}
			return nil
		}
		if pin == "" {
			return nil
		}
		if bestHeight != 0 {
			return fmt.Errorf("importing an AssumeUtreexo snapshot requires a fresh data directory")
		}
		return tx.Metadata().Put(key, []byte(pin))
	})
}
