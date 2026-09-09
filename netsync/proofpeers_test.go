// Copyright (c) 2026 The Utreexo developers
// Use of this source code is governed by an ISC license.

package netsync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/peer"
	"github.com/utreexo/utreexod/wire"
)

func TestProofServiceCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name           string
		services       wire.ServiceFlag
		height         int32
		blocks, proofs bool
	}{
		{"ordinary archive", wire.SFNodeNetwork, 1, true, false},
		{"ordinary pruned", wire.SFNodeNetworkLimited, 1000, true, false},
		{"new proof only", wire.SFNodeUtreexo, 1000, false, true},
		{"new proof is not historical", wire.SFNodeUtreexo, 999, false, false},
		{"archive proof only", wire.SFNodeUtreexoArchive, 1, false, true},
		{"combined archive", wire.SFNodeUtreexo | wire.SFNodeNetwork, 1, true, true},
		{"limited inside window", wire.SFNodeUtreexo | wire.SFNodeNetworkLimited, 713, true, true},
		{"limited outside window", wire.SFNodeUtreexo | wire.SFNodeNetworkLimited, 712, true, false},
		{"no services", 0, 1000, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.blocks, servesBlocks(tc.services))
			require.Equal(t, tc.proofs, servesProof(tc.services, tc.height, 1000, 1000))
		})
	}
}

func TestNotFoundReleasesOnlySourceTransactions(t *testing.T) {
	sm, _ := proofTestManager(t, 1)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexo)
	b := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexo)
	aHash, bHash := chainhash.Hash{1}, chainhash.Hash{2}
	sm.peerStates[a].requestedTxns = map[chainhash.Hash]struct{}{aHash: {}}
	sm.peerStates[b].requestedTxns = map[chainhash.Hash]struct{}{bHash: {}}
	sm.requestedTxns = map[chainhash.Hash]struct{}{aHash: {}, bHash: {}}
	msg := wire.NewMsgNotFound()
	require.NoError(t, msg.AddInvVect(wire.NewInvVect(wire.InvTypeWitnessUtreexoTx, &aHash)))
	require.NoError(t, msg.AddInvVect(wire.NewInvVect(wire.InvTypeUtreexoTx, &bHash)))
	sm.handleNotFoundMsg(&notFoundMsg{peer: a, notFound: msg})
	require.Empty(t, sm.peerStates[a].requestedTxns)
	require.NotContains(t, sm.requestedTxns, aHash)
	require.Contains(t, sm.requestedTxns, bHash, "unsolicited notfound cannot release another peer's request")
	require.Contains(t, sm.peerStates[b].requestedTxns, bHash)
}

// proofTestManager provides real validated headers, without starting goroutines.
func proofTestManager(t *testing.T, count int) (*SyncManager, []chainhash.Hash) {
	t.Helper()
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil
	sm, teardown := makeMockSyncManager(t, &params)
	t.Cleanup(teardown)
	blocks := generateTestBlocks(t, &params, count)
	hashes := make([]chainhash.Hash, len(blocks))
	for i, block := range blocks {
		_, err := sm.chain.ProcessBlockHeader(&block.MsgBlock().Header, blockchain.BFNone)
		require.NoError(t, err)
		hashes[i] = *block.Hash()
	}
	return sm, hashes
}

// proofTestPeer models a completed handshake's advertised service snapshot.
func proofTestPeer(t *testing.T, sm *SyncManager, address string, services wire.ServiceFlag) *peer.Peer {
	t.Helper()
	p, err := peer.NewOutboundPeer(&peer.Config{ChainParams: sm.chainParams}, address)
	require.NoError(t, err)
	sm.peerStates[p] = &peerSyncState{
		services:               services,
		requestedBlocks:        make(map[chainhash.Hash]struct{}),
		requestedUtreexoProofs: make(map[chainhash.Hash]struct{}),
	}
	return p
}

func TestProofSchedulingBalancesAndBoundsWork(t *testing.T) {
	sm, hashes := proofTestManager(t, maxProofWork+1)
	core := proofTestPeer(t, sm, "127.0.0.1:10000", wire.SFNodeNetwork)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexoArchive)
	b := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeNetwork|wire.SFNodeUtreexo)
	for i, hash := range hashes[:maxProofWork] {
		require.True(t, sm.trackProof(hash, int32(i+1)))
	}
	require.False(t, sm.trackProof(hashes[maxProofWork], maxProofWork+1))
	now := time.Now()
	sm.scheduleProofs(now)
	require.Empty(t, sm.peerStates[core].requestedUtreexoProofs)
	require.Len(t, sm.peerStates[a].requestedUtreexoProofs, maxProofWork/2)
	require.Len(t, sm.peerStates[b].requestedUtreexoProofs, maxProofWork/2)
	sequence := sm.proofSequence
	sm.scheduleProofs(now.Add(time.Second))
	require.Equal(t, sequence, sm.proofSequence, "do not duplicate outstanding requests")
}

func TestProofDisconnectReassignsWithoutLosingBlocks(t *testing.T) {
	sm, hashes := proofTestManager(t, 1)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexoArchive)
	require.True(t, sm.trackProof(hashes[0], 1))
	sm.scheduleProofs(time.Now())
	require.Same(t, a, sm.proofRequests[hashes[0]].peer)
	cached := &blockMsg{}
	sm.queuedBlocks[hashes[0]] = cached
	b := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexoArchive)
	sm.handleDonePeerMsg(a)
	sm.scheduleProofs(time.Now())
	require.Same(t, b, sm.proofRequests[hashes[0]].peer)
	require.Same(t, cached, sm.queuedBlocks[hashes[0]])
}

func TestProofTimeoutReassignsAndQuarantinesConnection(t *testing.T) {
	sm, hashes := proofTestManager(t, 1)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexoArchive)
	require.True(t, sm.trackProof(hashes[0], 1))
	now := time.Now()
	sm.scheduleProofs(now)
	b := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexoArchive)
	sm.scheduleProofs(now.Add(proofRequestTimeout + time.Second))
	require.True(t, sm.peerStates[a].proofFailed)
	require.Empty(t, sm.peerStates[a].requestedUtreexoProofs)
	require.Same(t, b, sm.proofRequests[hashes[0]].peer)
}

func TestProofBatchProgressRenewsOnlyItsProvider(t *testing.T) {
	sm, hashes := proofTestManager(t, 3)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexoArchive)
	b := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexoArchive)
	started := time.Now()
	for i, hash := range hashes {
		require.True(t, sm.trackProof(hash, int32(i+1)))
		sm.proofRequests[hash].peer = a
		sm.proofRequests[hash].sent = started
	}
	sm.proofRequests[hashes[2]].peer = b
	sm.queuedUtreexoProofs[hashes[0]] = &utreexoProofMsg{peer: a}
	progress := started.Add(proofRequestTimeout - time.Second)
	sm.noteProofProgress(a, progress)
	require.Equal(t, started, sm.proofRequests[hashes[0]].sent, "completed responses need no renewed deadline")
	require.Equal(t, progress, sm.proofRequests[hashes[1]].sent)
	require.Equal(t, started, sm.proofRequests[hashes[2]].sent, "another provider cannot mask a stalled connection")
	sm.scheduleProofs(started.Add(proofRequestTimeout + time.Second))
	require.False(t, sm.peerStates[a].proofFailed)
	require.True(t, sm.peerStates[b].proofFailed)
	sm.scheduleProofs(progress.Add(proofRequestTimeout + time.Second))
	require.True(t, sm.peerStates[a].proofFailed, "a stalled batch must still time out after progress stops")
}

func TestLocalValidationDoesNotConsumeProofDeadline(t *testing.T) {
	sm, hashes := proofTestManager(t, 2)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexoArchive)
	requested := time.Now()
	require.True(t, sm.trackProof(hashes[0], 1))
	sm.scheduleProofs(requested)
	started := requested.Add(time.Second)
	finished := started.Add(3 * proofRequestTimeout)
	sm.pauseProofTimeouts(started, finished)
	sm.scheduleProofs(finished.Add(proofRequestTimeout - 2*time.Second))
	require.False(t, sm.peerStates[a].proofFailed)
	sm.scheduleProofs(finished.Add(proofRequestTimeout))
	require.True(t, sm.peerStates[a].proofFailed, "only local work is excluded, not actual peer inactivity")
}

func TestFailedProofDiscardsOnlyProviderData(t *testing.T) {
	sm, hashes := proofTestManager(t, 2)
	a := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexoArchive)
	for i, hash := range hashes {
		require.True(t, sm.trackProof(hash, int32(i+1)))
		sm.queuedBlocks[hash] = &blockMsg{}
	}
	sm.scheduleProofs(time.Now())
	for _, hash := range hashes {
		sm.queuedUtreexoProofs[hash] = &utreexoProofMsg{peer: a}
	}
	b := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexoArchive)
	sm.failProofPeer(a, "bad proof")
	sm.scheduleProofs(time.Now())
	require.Len(t, sm.queuedBlocks, 2)
	require.Empty(t, sm.queuedUtreexoProofs)
	for _, hash := range hashes {
		require.Same(t, b, sm.proofRequests[hash].peer)
	}
}

func TestProofProbesUnspecifiedHistoryAndPrefersAdvertisedCoverage(t *testing.T) {
	sm, hashes := proofTestManager(t, 2)
	proofTestPeer(t, sm, "127.0.0.1:10000", wire.SFNodeNetwork)
	newOnly := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexo)
	require.True(t, sm.trackProof(hashes[0], 1))
	require.True(t, sm.trackProof(hashes[1], 2))
	sm.scheduleProofs(time.Now())
	require.Same(t, newOnly, sm.proofRequests[hashes[0]].peer)
	require.Same(t, newOnly, sm.proofRequests[hashes[1]].peer)
	archive := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexoArchive)
	require.Same(t, archive, sm.selectProofPeer(1, 2))
	// A missing historical proof must release the fallback request and preserve
	// the separately downloaded block for an advertised provider.
	sm.queuedBlocks[hashes[0]] = &blockMsg{}
	sm.scheduleProofs(time.Now().Add(proofRequestTimeout + time.Second))
	require.True(t, sm.peerStates[newOnly].proofFailed)
	require.Same(t, archive, sm.proofRequests[hashes[0]].peer)
	require.NotNil(t, sm.queuedBlocks[hashes[0]])
}

func TestProofFallbackRemainsBoundedAndHonorsLimitedCoverage(t *testing.T) {
	sm, hashes := proofTestManager(t, maxProofWork+1)
	core := proofTestPeer(t, sm, "127.0.0.1:10000", wire.SFNodeNetwork)
	limited := proofTestPeer(t, sm, "127.0.0.1:10001", wire.SFNodeUtreexo|wire.SFNodeNetworkLimited)
	// proofTestPeer has no advertised tip, so use an older synthetic height to
	// exercise selection outside the explicit 288-block promise.
	require.Nil(t, sm.selectProofPeer(-1000, 1000))
	fallback := proofTestPeer(t, sm, "127.0.0.1:10002", wire.SFNodeUtreexo)
	require.Same(t, fallback, sm.selectProofPeer(-1000, 1000))
	delete(sm.peerStates, limited)
	for i, hash := range hashes[:maxProofWork] {
		require.True(t, sm.trackProof(hash, int32(i+1)))
	}
	require.False(t, sm.trackProof(hashes[maxProofWork], maxProofWork+1))
	sm.scheduleProofs(time.Now())
	require.Empty(t, sm.peerStates[core].requestedUtreexoProofs)
	require.Len(t, sm.peerStates[fallback].requestedUtreexoProofs, maxProofWork)
}

func TestObsoleteProofWorkIsReleased(t *testing.T) {
	sm, _ := proofTestManager(t, 1)
	unknown := chainhash.Hash{1}
	require.True(t, sm.trackProof(unknown, 1))
	sm.queuedBlocks[unknown] = &blockMsg{}
	sm.queuedUtreexoProofs[unknown] = &utreexoProofMsg{}
	sm.scheduleProofs(time.Now())
	require.Empty(t, sm.proofRequests)
	require.Empty(t, sm.queuedBlocks)
	require.Empty(t, sm.queuedUtreexoProofs)
}

func TestProofAvailabilityDoesNotBlameBlockSource(t *testing.T) {
	sm, hashes := proofTestManager(t, 2)
	require.False(t, sm.waitingOnlyForProofs())
	for i, hash := range hashes {
		require.True(t, sm.trackProof(hash, int32(i+1)))
	}
	sm.queuedBlocks[hashes[0]] = &blockMsg{}
	require.False(t, sm.waitingOnlyForProofs(), "a block is still missing")
	sm.queuedBlocks[hashes[1]] = &blockMsg{}
	require.True(t, sm.waitingOnlyForProofs(), "all blocks arrived; proofs are missing")
	for _, hash := range hashes {
		sm.queuedUtreexoProofs[hash] = &utreexoProofMsg{}
	}
	require.False(t, sm.waitingOnlyForProofs(), "proofs no longer block validation")
}

func TestBlockCommitmentsBeforeProofAttribution(t *testing.T) {
	params := chaincfg.RegressionNetParams
	block := generateTestBlocks(t, &params, 1)[0]
	require.NoError(t, verifyBlockCommitments(block, false))
	changed := *block.MsgBlock()
	changed.Header.MerkleRoot[0] ^= 1
	require.Error(t, verifyBlockCommitments(btcutil.NewBlock(&changed), false))
	require.Error(t, verifyBlockCommitments(btcutil.NewBlock(&wire.MsgBlock{}), false))
}
