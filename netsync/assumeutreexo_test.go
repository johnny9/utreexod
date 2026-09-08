package netsync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/utreexo/utreexo"
	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/chaincfg"
)

// TestAssumeUtreexoWithLaterHeaders reproduces a restart before the first
// post-checkpoint block was validated, with later headers already on disk.
func TestAssumeUtreexoWithLaterHeaders(t *testing.T) {
	for _, scenario := range []string{"valid checkpoint", "resume at startup", "wrong checkpoint", "missing checkpoint"} {
		t.Run(scenario, func(t *testing.T) {
			params := chaincfg.RegressionNetParams
			params.Checkpoints = nil
			genesis := *params.GenesisBlock
			genesis.Header.Timestamp = time.Now().Add(-time.Hour).Truncate(time.Second)
			require.True(t, solveTestBlock(&genesis.Header, &params))
			genesisHash := genesis.BlockHash()
			params.GenesisBlock, params.GenesisHash = &genesis, &genesisHash
			blocks := generateTestBlocks(t, &params, 5)
			params.AssumeUtreexoPoint = chaincfg.AssumeUtreexo{
				BlockHash: blocks[1].Hash(), BlockHeight: 2,
				NumLeaves: 1, Roots: []utreexo.Hash{{1}},
			}
			if scenario == "wrong checkpoint" {
				params.AssumeUtreexoPoint.BlockHash = blocks[0].Hash()
			}
			params.TTL.Stump = []utreexo.Stump{{NumLeaves: 3}}
			db, closeDB, err := dbSetup(t, &params)
			require.NoError(t, err)
			t.Cleanup(closeDB)
			chain, err := blockchain.New(&blockchain.Config{
				DB: db, ChainParams: &params,
				TimeSource:         blockchain.NewMedianTime(),
				UtreexoView:        blockchain.NewUtreexoViewpoint(),
				AssumeUtreexoPoint: params.AssumeUtreexoPoint,
			})
			require.NoError(t, err)
			sm, err := New(&Config{Chain: chain, ChainParams: &params, PeerNotifier: noopPeerNotifier{}})
			require.NoError(t, err)
			require.True(t, sm.headersBuildMode)
			require.False(t, sm.independentProofs(), "TTL-covered blocks retain their existing path")
			headerCount := len(blocks)
			if scenario == "missing checkpoint" {
				headerCount = 1
			}
			for _, block := range blocks[:headerCount] {
				_, err := chain.ProcessBlockHeader(&block.MsgBlock().Header, blockchain.BFNone)
				require.NoError(t, err)
			}
			if scenario == "resume at startup" {
				sm, err = New(&Config{Chain: chain, ChainParams: &params, PeerNotifier: noopPeerNotifier{}})
			} else {
				err = sm.initializeAssumedUtreexo()
			}
			if scenario == "wrong checkpoint" || scenario == "missing checkpoint" {
				require.Error(t, err)
				require.True(t, sm.headersBuildMode)
				require.Zero(t, chain.BestSnapshot().Height)
				require.Empty(t, chain.GetUtreexoView().GetRoots())
				return
			}
			require.NoError(t, err)
			require.False(t, sm.headersBuildMode)
			require.Equal(t, int32(2), chain.BestSnapshot().Height)
			require.Equal(t, *blocks[1].Hash(), chain.BestSnapshot().Hash)
			_, headerHeight := chain.BestHeader()
			require.Equal(t, int32(5), headerHeight)
			require.True(t, chain.IsCurrent(), "the assumed block is recent enough for the legacy timestamp heuristic")
			require.False(t, sm.current(), "known headers still need validated block/proof pairs before mining")
			roots := chain.GetUtreexoView().GetRoots()
			require.Len(t, roots, 1)
			require.Equal(t, [32]byte(params.AssumeUtreexoPoint.Roots[0]), [32]byte(*roots[0]))
			require.True(t, sm.independentProofs(), "post-checkpoint blocks use bounded independent proofs")
			resumed, err := New(&Config{Chain: chain, ChainParams: &params, PeerNotifier: noopPeerNotifier{}})
			require.NoError(t, err)
			require.True(t, resumed.independentProofs(), "a loaded TTL commitment must not disable independent proofs")
		})
	}
}
