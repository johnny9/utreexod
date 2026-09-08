package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btcsuite/btclog"
	"github.com/stretchr/testify/require"
	"github.com/utreexo/utreexo"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/database"
	"github.com/utreexo/utreexod/wire"
)

func testSnapshot() assumeUtreexoSnapshot {
	return assumeUtreexoSnapshot{
		Format: "utreexod-assumeutreexo", Version: 1, Network: "regtest",
		GenesisHash: chaincfg.RegressionNetParams.GenesisHash.String(),
		Height:      120, BlockHash: strings.Repeat("12", 32), Bits: 0x207fffff,
		BlockSize: 200, BlockWeight: 800, NumTxns: 1, TotalTxns: 121,
		MedianTime: 1_700_000_000, NumLeaves: 3, RootsEncoding: snapshotRootsEncoding,
		Roots: []string{strings.Repeat("01", 32), strings.Repeat("02", 32)},
	}
}

func writeTestSnapshot(t *testing.T, raw []byte) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	digest := sha256.Sum256(raw)
	return path, hex.EncodeToString(digest[:])
}

func TestLoadAssumeUtreexoSnapshot(t *testing.T) {
	for _, name := range []string{"valid", "network", "genesis", "height", "zero leaves", "large leaves",
		"root count", "root encoding", "root hex", "block hash", "bits", "weight", "transactions",
		"median time", "format", "version", "TTL", "checkpoint", "checkpoint hash", "pin", "unknown field", "trailing", "oversized"} {
		t.Run(name, func(t *testing.T) {
			s := testSnapshot()
			params := chaincfg.RegressionNetParams
			switch name {
			case "network":
				s.Network = "mainnet"
			case "genesis":
				s.GenesisHash = strings.Repeat("00", 32)
			case "height":
				s.Height = 0
			case "zero leaves":
				s.NumLeaves = 0
			case "large leaves":
				s.NumLeaves = 1<<63 + 1
			case "root count":
				s.Roots = s.Roots[:1]
			case "root encoding":
				s.RootsEncoding = "bitcoin_hex"
			case "root hex":
				s.Roots[0] = "ab"
			case "block hash":
				s.BlockHash = "00"
			case "bits":
				s.Bits = 0
			case "weight":
				s.BlockWeight = 4_000_001
			case "transactions":
				s.TotalTxns = 1
			case "median time":
				s.MedianTime = 0
			case "format":
				s.Format = "sidecar-forest"
			case "version":
				s.Version = 2
			case "TTL":
				params.TTL.Stump = []utreexo.Stump{{NumLeaves: 122}}
			case "checkpoint":
				params.Checkpoints = []chaincfg.Checkpoint{{Height: 121, Hash: params.GenesisHash}}
			case "checkpoint hash":
				params.Checkpoints = []chaincfg.Checkpoint{{Height: 120, Hash: params.GenesisHash}}
			}
			raw, err := json.Marshal(s)
			require.NoError(t, err)
			switch name {
			case "unknown field":
				raw = append([]byte(`{"surprise":1,`), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(" {}")...)
			case "oversized":
				raw = append(raw, []byte(strings.Repeat(" ", maxAssumeUtreexoSnapshotBytes))...)
			}
			path, pin := writeTestSnapshot(t, raw)
			if name == "pin" {
				pin = strings.Repeat("00", 32)
			}
			point, err := loadAssumeUtreexoSnapshot(path, pin, &params)
			if name != "valid" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, int32(120), point.BlockHeight)
			require.Equal(t, s.BlockHash, point.BlockHash.String())
			require.Equal(t, uint64(3), point.NumLeaves)
			require.Equal(t, s.Roots[0], hex.EncodeToString(point.Roots[0][:]))
			require.Equal(t, s.Roots[1], hex.EncodeToString(point.Roots[1][:]))
		})
	}
}

func TestConfigureAssumeUtreexoSnapshot(t *testing.T) {
	raw, err := json.Marshal(testSnapshot())
	require.NoError(t, err)
	path, pin := writeTestSnapshot(t, raw)
	for _, name := range []string{"valid", "no path", "no pin", "no assume", "no compact", "no checkpoints", "custom checkpoint", "custom signet"} {
		t.Run(name, func(t *testing.T) {
			c := config{AssumeUtreexoSnapshot: path, AssumeUtreexoSnapshotSHA256: pin}
			switch name {
			case "no path":
				c.AssumeUtreexoSnapshot = ""
			case "no pin":
				c.AssumeUtreexoSnapshotSHA256 = ""
			case "no assume":
				c.NoAssumeUtreexo = true
			case "no compact":
				c.NoUtreexo = true
			case "no checkpoints":
				c.DisableCheckpoints = true
			case "custom checkpoint":
				c.addCheckpoints = []chaincfg.Checkpoint{{Height: 121, Hash: chaincfg.RegressionNetParams.GenesisHash}}
			case "custom signet":
				c.SigNetChallenge = "51"
			}
			err := c.loadAssumeUtreexoSnapshot(&chaincfg.RegressionNetParams)
			if name != "valid" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, c.addCheckpoints, 1)
			require.Equal(t, c.assumeUtreexoSnapshot.BlockHash, c.addCheckpoints[0].Hash)
		})
	}
}

func TestBindAssumeUtreexoSnapshot(t *testing.T) {
	db, err := database.Create("ffldb", filepath.Join(t.TempDir(), "blocks"), wire.TestNet)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	pin := strings.Repeat("12", 32)
	require.NoError(t, bindAssumeUtreexoSnapshot(db, "", 120))
	require.ErrorContains(t, bindAssumeUtreexoSnapshot(db, pin, 120), "fresh data directory")
	require.NoError(t, bindAssumeUtreexoSnapshot(db, pin, 0))
	require.NoError(t, bindAssumeUtreexoSnapshot(db, pin, 121))
	require.ErrorContains(t, bindAssumeUtreexoSnapshot(db, "", 121), "original")
	require.ErrorContains(t, bindAssumeUtreexoSnapshot(db, strings.Repeat("34", 32), 121), "original")
}

func TestKeepRegressionDatabase(t *testing.T) {
	previous := cfg
	previousLog := btcdLog
	btcdLog = btclog.Disabled
	t.Cleanup(func() { cfg, btcdLog = previous, previousLog })
	for _, scenario := range []struct{ regtest, keep bool }{{false, false}, {true, true}, {true, false}} {
		path := filepath.Join(t.TempDir(), "blocks_ffldb")
		require.NoError(t, os.Mkdir(path, 0700))
		marker := filepath.Join(path, "saved-state")
		require.NoError(t, os.WriteFile(marker, []byte("preserved"), 0600))
		cfg = &config{RegressionTest: scenario.regtest, RegressionTestKeepDB: scenario.keep}
		require.NoError(t, removeRegressionDB(path))
		if scenario.regtest && !scenario.keep {
			_, err := os.Stat(path)
			require.True(t, os.IsNotExist(err))
		} else {
			contents, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, "preserved", string(contents))
		}
	}
}
