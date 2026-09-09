// Copyright (c) 2026 The Utreexo developers
// Use of this source code is governed by an ISC license.

package netsync

import (
	"fmt"
	"sort"
	"time"

	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/peer"
	"github.com/utreexo/utreexod/wire"
)

const (
	maxProofWork        = 32
	proofRequestTimeout = 15 * time.Second
)

// blockProofRequest tracks proof ownership independently of the block source.
// Completed responses remain in the bounded work window until validation.
type blockProofRequest struct {
	height int32
	peer   *peer.Peer
	sent   time.Time
}

// servesBlocks identifies peers eligible to supply Bitcoin block data.
func servesBlocks(services wire.ServiceFlag) bool {
	return services&(wire.SFNodeNetwork|wire.SFNodeNetworkLimited) != 0
}

// servesProof applies the advertised historical range, not the peer's address.
func servesProof(services wire.ServiceFlag, height, tip, peerTip int32) bool {
	if services.HasFlag(wire.SFNodeUtreexoArchive) {
		return true
	}
	if !services.HasFlag(wire.SFNodeUtreexo) {
		return false
	}
	if services.HasFlag(wire.SFNodeNetwork) {
		return true
	}
	if services.HasFlag(wire.SFNodeNetworkLimited) {
		return height > peerTip-wire.NodeNetworkLimitedBlockThreshold
	}
	// NODE_UTREEXO alone promises new-block proofs, not a historical window.
	return height >= tip
}

// independentProofs leaves the upstream committed-TTL download path unchanged.
func (sm *SyncManager) independentProofs() bool {
	if !sm.chain.IsUtreexoViewActive() {
		return false
	}
	// A configured TTL commitment remains present after AssumeUtreexo and on
	// restart. Only blocks covered by it need the upstream TTL download path.
	return sm.committedTTLAcc == nil ||
		uint64(sm.chain.BestChainHeaderForkHeight())+1 >= sm.committedTTLAcc.NumLeaves
}

func (sm *SyncManager) waitingOnlyForProofs() bool {
	waiting := false
	for hash := range sm.proofRequests {
		if sm.queuedBlocks[hash] == nil {
			return false
		}
		waiting = waiting || sm.queuedUtreexoProofs[hash] == nil
	}
	return waiting
}

// trackProof bounds both downloaded and outstanding block/proof pairs.
func (sm *SyncManager) trackProof(hash chainhash.Hash, height int32) bool {
	if sm.proofRequests[hash] != nil {
		return true
	}
	if len(sm.proofRequests) >= maxProofWork {
		return false
	}
	sm.proofRequests[hash] = &blockProofRequest{height: height}
	return true
}

// forgetProof releases all bookkeeping for accepted or obsolete work.
func (sm *SyncManager) forgetProof(hash chainhash.Hash) {
	delete(sm.proofRequests, hash)
	delete(sm.queuedUtreexoProofs, hash)
	delete(sm.queuedBlocks, hash)
	delete(sm.requestedBlocks, hash)
	for _, state := range sm.peerStates {
		delete(state.requestedUtreexoProofs, hash)
		delete(state.requestedBlocks, hash)
	}
}

// releaseProofPeer makes unfinished requests available after disconnection.
// Already received proofs remain independently verifiable without the sender.
func (sm *SyncManager) releaseProofPeer(source *peer.Peer) {
	for hash, request := range sm.proofRequests {
		if request.peer == source && sm.queuedUtreexoProofs[hash] == nil {
			request.peer = nil
		}
	}
}

// failProofPeer withdraws this source's unverified proofs and retries elsewhere.
// It never discards block bytes received from a different connection.
func (sm *SyncManager) failProofPeer(source *peer.Peer, reason string) {
	if state := sm.peerStates[source]; state != nil {
		state.proofFailed = true
		clear(state.requestedUtreexoProofs)
	}
	for hash, request := range sm.proofRequests {
		if request.peer == source {
			delete(sm.queuedUtreexoProofs, hash)
			request.peer = nil
		}
	}
	log.Warnf("Retrying proofs from another peer: %s: %s", source.Addr(), reason)
	source.Disconnect()
}

// noteProofProgress gives a responsive connection time to deliver the rest of
// its bounded batch. Large proofs share one stream, so later requests must not
// expire merely because earlier responses consumed the transfer budget.
func (sm *SyncManager) noteProofProgress(source *peer.Peer, now time.Time) {
	for hash, request := range sm.proofRequests {
		if request.peer == source && sm.queuedUtreexoProofs[hash] == nil {
			request.sent = now
		}
	}
}

// pauseProofTimeouts excludes local validation time: while the sync-manager
// goroutine processes a block it cannot dispatch proofs already in msgChan.
func (sm *SyncManager) pauseProofTimeouts(started, finished time.Time) {
	if !finished.After(started) {
		return
	}
	for _, request := range sm.proofRequests {
		if request.peer != nil && request.sent.Before(started) {
			request.sent = request.sent.Add(finished.Sub(started))
		}
	}
}

// selectProofPeer prefers peers advertising coverage of the requested height.
// When none are available, a proof-only NODE_UTREEXO peer may retain a suffix
// after an assumed checkpoint. Probe it with an ordinary, bounded proof request;
// its service bit does not promise that history. Disconnects, invalid proofs,
// and timeouts use the same failover path as advertised coverage.
// Only the sync-manager goroutine accesses this state.
func (sm *SyncManager) selectProofPeer(height, tip int32) *peer.Peer {
	var selected *peer.Peer
	var selectedState *peerSyncState
	var selectedCoverage bool
	for candidate, state := range sm.peerStates {
		if state.proofFailed {
			continue
		}
		coverage := servesProof(state.services, height, tip, candidate.LastBlock())
		if !coverage && (!state.services.HasFlag(wire.SFNodeUtreexo) || servesBlocks(state.services)) {
			continue
		}
		if selected != nil && selectedCoverage && !coverage {
			continue
		}
		if selected == nil || (coverage && !selectedCoverage) ||
			len(state.requestedUtreexoProofs) < len(selectedState.requestedUtreexoProofs) ||
			(len(state.requestedUtreexoProofs) == len(selectedState.requestedUtreexoProofs) &&
				(state.proofLastAssigned < selectedState.proofLastAssigned ||
					(state.proofLastAssigned == selectedState.proofLastAssigned && candidate.Addr() < selected.Addr()))) {
			selected, selectedState = candidate, state
			selectedCoverage = coverage
		}
	}
	return selected
}

// scheduleProofs retries disconnects and timeouts and requests missing proofs
// in height order. Obsolete header-branch work is dropped before scheduling.
func (sm *SyncManager) scheduleProofs(now time.Time) {
	_, tip := sm.chain.BestHeader()
	var hashes []chainhash.Hash
	for hash, request := range sm.proofRequests {
		header, err := sm.chain.HeaderHashByHeight(request.height)
		have, haveErr := sm.chain.HaveBlock(&hash)
		if err != nil || *header != hash || (haveErr == nil && have) {
			sm.forgetProof(hash)
			continue
		}
		if sm.queuedUtreexoProofs[hash] != nil {
			continue
		}
		if request.peer != nil && now.Sub(request.sent) >= proofRequestTimeout {
			sm.failProofPeer(request.peer, "proof request timed out")
		}
		hashes = append(hashes, hash)
	}
	sort.Slice(hashes, func(i, j int) bool {
		return sm.proofRequests[hashes[i]].height < sm.proofRequests[hashes[j]].height
	})
	for _, hash := range hashes {
		request := sm.proofRequests[hash]
		if request.peer != nil {
			continue
		}
		source := sm.selectProofPeer(request.height, tip)
		if source == nil {
			continue
		}
		request.peer, request.sent = source, now
		state := sm.peerStates[source]
		sm.proofSequence++
		state.proofLastAssigned = sm.proofSequence
		state.requestedUtreexoProofs[hash] = struct{}{}
		message := &wire.MsgGetUtreexoProof{BlockHash: hash}
		message.SetTargetRequestBit()
		message.SetProofHashRequestBit()
		message.SetLeafDataRequestBit()
		log.Debugf("Requesting block proof %s from %s", hash, source.Addr())
		source.QueueMessage(message, nil)
	}
}

// fetchProofBlocks requests a bounded block window independently of which
// proof providers are currently connected. Cached block bytes survive failover.
func (sm *SyncManager) fetchProofBlocks(source *peer.Peer) {
	if !servesBlocks(source.Services()) || sm.headersBuildMode {
		return
	}
	sm.scheduleProofs(time.Now())
	state := sm.peerStates[source]
	_, tip := sm.chain.BestHeader()
	message := wire.NewMsgGetData()
	for height := sm.chain.BestChainHeaderForkHeight() + 1; height <= tip; height++ {
		hash, err := sm.chain.HeaderHashByHeight(height)
		if err != nil || !sm.trackProof(*hash, height) {
			break
		}
		if sm.queuedBlocks[*hash] != nil {
			continue
		}
		requested := false
		for _, other := range sm.peerStates {
			if _, exists := other.requestedBlocks[*hash]; exists {
				requested = true
				break
			}
		}
		if requested {
			continue
		}
		state.requestedBlocks[*hash] = struct{}{}
		sm.requestedBlocks[*hash] = struct{}{}
		kind := wire.InvTypeBlock
		if source.IsWitnessEnabled() {
			kind = wire.InvTypeWitnessBlock
		}
		message.AddInvVect(wire.NewInvVect(kind, hash))
	}
	if len(message.InvList) > 0 {
		source.QueueMessage(message, nil)
	}
	sm.scheduleProofs(time.Now())
}

// verifyBlockProof authenticates the provider's data before ProcessBlock can
// attribute any failure to the block or modify chain state.
func (sm *SyncManager) verifyBlockProof(block *btcutil.Block, data *wire.UData) error {
	_, _, skip, _ := blockchain.DedupeBlock(block)
	var inputs []*wire.TxIn
	var index uint32
	for txIndex, tx := range block.MsgBlock().Transactions {
		for _, input := range tx.TxIn {
			if len(skip) > 0 && skip[0] == index {
				skip = skip[1:]
			} else if txIndex != 0 {
				inputs = append(inputs, input)
			}
			index++
		}
	}
	if len(data.LeafDatas) != len(inputs) || len(data.AccProof.Targets) != len(inputs) {
		return fmt.Errorf("block proof input/leaf/target count mismatch")
	}
	if len(inputs) == 0 && len(data.AccProof.Proof) != 0 {
		return fmt.Errorf("unexpected proof hashes for a block with no confirmed inputs")
	}
	for _, leaf := range data.LeafDatas {
		if leaf.IsUnconfirmed() {
			return fmt.Errorf("unconfirmed leaf in block proof")
		}
	}
	return sm.chain.VerifyFullUData(data, inputs)
}

// verifyBlockCommitments authenticates the block bytes before blaming a proof
// provider. Headers are already validated before scheduling these downloads.
func verifyBlockCommitments(block *btcutil.Block, segwit bool) error {
	transactions := block.Transactions()
	if len(transactions) == 0 || !blockchain.IsCoinBase(transactions[0]) {
		return fmt.Errorf("block has no coinbase")
	}
	for _, tx := range transactions {
		if err := blockchain.CheckTransactionSanity(tx); err != nil {
			return err
		}
	}
	root := blockchain.CalcMerkleRoot(transactions, false)
	if root != block.MsgBlock().Header.MerkleRoot {
		return fmt.Errorf("block transaction root does not match header")
	}
	if segwit {
		return blockchain.ValidateWitnessCommitment(block)
	}
	return nil
}
