// Copyright (c) 2026 The Utreexo developers
// Use of this source code is governed by an ISC license.
package netsync

import (
	"fmt"
	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	peerpkg "github.com/utreexo/utreexod/peer"
	"github.com/utreexo/utreexod/wire"
)

type sidecarFetchMsg struct{}

// All calls are on the sync manager goroutine. The configured proof endpoint
// supplies no chain-selection information and is never a block sync candidate.
func (sm *SyncManager) proofPeer() *peerpkg.Peer {
	for peer := range sm.peerStates {
		if peer.Addr() == sm.proofPeerAddress && peer.IsUtreexoEnabled() {
			return peer
		}
	}
	return nil
}

// Standard submitblock obtains proofs already verified and remembered by the
// compact mempool. Missing data fails closed; it never asks Core to validate.
func (sm *SyncManager) attachMempoolProof(block *btcutil.Block) error {
	if len(block.MsgBlock().Transactions) == 0 {
		return fmt.Errorf("submitblock has no coinbase")
	}
	best := sm.chain.BestSnapshot()
	if block.MsgBlock().Header.PrevBlock != best.Hash {
		return fmt.Errorf("submitblock proof requires the current tip")
	}
	block.SetHeight(best.Height + 1)
	_, _, skip, _ := blockchain.DedupeBlock(block)
	var leaves []wire.LeafData
	var inputIndex uint32
	for txIndex, tx := range block.Transactions() {
		if txIndex == 0 {
			inputIndex += uint32(len(tx.MsgTx().TxIn))
			continue
		}
		txLeaves, err := sm.txMemPool.FetchLeafDatas(tx.Hash())
		if err != nil {
			return fmt.Errorf("submitblock proof unavailable for %s: %w", tx.Hash(), err)
		}
		if len(txLeaves) != len(tx.MsgTx().TxIn) {
			return fmt.Errorf("submitblock leaf/input count mismatch")
		}
		for i := range tx.MsgTx().TxIn {
			if len(skip) > 0 && skip[0] == inputIndex {
				skip = skip[1:]
			} else {
				if txLeaves[i].IsUnconfirmed() {
					return fmt.Errorf("submitblock missing unconfirmed parent")
				}
				leaves = append(leaves, txLeaves[i])
			}
			inputIndex++
		}
	}
	proof, err := sm.chain.GenerateUData(leaves)
	if err != nil {
		return err
	}
	block.SetUtreexoData(proof)
	return nil
}

// Blocks and proofs arrive independently; drain ready pairs in chain order.
// Do not enqueue onto our own channel while handling a message.
func (sm *SyncManager) drainSidecarBlocks() {
	if sm.proofPeerAddress == "" {
		return
	}
	for {
		best := sm.chain.BestSnapshot()
		hash, err := sm.chain.HeaderHashByHeight(best.Height + 1)
		if err != nil {
			return
		}
		block := sm.queuedBlocks[*hash]
		if block == nil || sm.queuedUtreexoProofs[*hash] == nil {
			return
		}
		sm.handleBlockMsg(block)
		if sm.chain.BestSnapshot().Hash == best.Hash {
			return
		}
	}
}

// A tip change closes the proof connection to disambiguate transaction
// announcements. Reissue the bounded outstanding block proofs on reconnect;
// ordinary block/header peers and chain choice remain independent.
func (sm *SyncManager) recoverSidecarRequests() {
	peer := sm.proofPeer()
	if peer == nil {
		return
	}
	state := sm.peerStates[peer]
	count := 0
	for blockPeer, blockState := range sm.peerStates {
		if blockPeer == peer {
			continue
		}
		for hash := range blockState.requestedBlocks {
			if sm.queuedUtreexoProofs[hash] != nil {
				continue
			}
			if _, pending := state.requestedUtreexoProofs[hash]; pending {
				continue
			}
			msg := &wire.MsgGetUtreexoProof{BlockHash: hash}
			msg.SetTargetRequestBit()
			msg.SetProofHashRequestBit()
			msg.SetLeafDataRequestBit()
			state.requestedUtreexoProofs[hash] = struct{}{}
			peer.QueueMessage(msg, nil)
			count++
			if count >= 32 {
				return
			}
		}
	}
	sm.fetchHeaderBlocks(nil)
}
