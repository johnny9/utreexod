// Copyright (c) 2026 The Utreexo developers
// Use of this source code is governed by an ISC license.
package netsync

import (
	"fmt"
	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/wire"
)

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
func (sm *SyncManager) drainProofBlocks() {
	if !sm.independentProofs() {
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
