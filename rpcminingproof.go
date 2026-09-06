// Copyright (c) 2026 The Utreexo developers
// Use of this source code is governed by an ISC license.

package main

import (
	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/mining"
	"github.com/utreexo/utreexod/wire"
)

// cacheTemplate records immutable transaction identities alongside the template
// proof. The caller holds the work-state lock. Only the current template is kept.
func (state *gbtWorkState) cacheTemplate(template *mining.BlockTemplate) {
	state.template = template
	state.templateTxHashes = nil
	if template == nil || template.UData == nil || len(template.Block.Transactions) == 0 {
		return
	}
	state.templateTxHashes = make([]chainhash.Hash, len(template.Block.Transactions)-1)
	for i, tx := range template.Block.Transactions[1:] {
		state.templateTxHashes[i] = tx.WitnessHash()
	}
}

// cachedTemplateProof avoids rebuilding the input proof for an unchanged mining
// transaction body. Coinbase and header mutations do not alter those inputs.
// Return a deep copy: block validation may modify proof and leaf data.
func (state *gbtWorkState) cachedTemplateProof(block *btcutil.Block, tip chainhash.Hash) *wire.UData {
	if state == nil {
		return nil
	}
	state.Lock()
	defer state.Unlock()

	template := state.template
	if template == nil || template.UData == nil ||
		block.MsgBlock().Header.PrevBlock != tip || template.Block.Header.PrevBlock != tip {
		return nil
	}
	transactions := block.Transactions()
	if len(transactions) == 0 || len(transactions) != len(template.Block.Transactions) ||
		len(transactions)-1 != len(state.templateTxHashes) || !blockchain.IsCoinBase(transactions[0]) {
		return nil
	}
	for i, tx := range transactions[1:] {
		if *tx.WitnessHash() != state.templateTxHashes[i] {
			return nil
		}
	}
	return template.UData.Copy()
}
