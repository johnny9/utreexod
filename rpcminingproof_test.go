// Copyright (c) 2026 The Utreexo developers
// Use of this source code is governed by an ISC license.

package main

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/utreexo/utreexo"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/mining"
	"github.com/utreexo/utreexod/wire"
)

func proofTemplate() *mining.BlockTemplate {
	coinbase := wire.NewMsgTx(2)
	coinbase.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: ^uint32(0)}, SignatureScript: []byte{1, 1}})
	coinbase.AddTxOut(wire.NewTxOut(50, []byte{0x51}))
	block := wire.NewMsgBlock(&wire.BlockHeader{PrevBlock: chainhash.Hash{1}})
	block.AddTransaction(coinbase)
	for i := byte(1); i <= 2; i++ {
		tx := wire.NewMsgTx(2)
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{i}},
			Witness: wire.TxWitness{[]byte{i}}})
		tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
		block.AddTransaction(tx)
	}
	return &mining.BlockTemplate{Block: block, Height: 101, UData: &wire.UData{
		AccProof:  utreexo.Proof{Targets: []uint64{1, 2}, Proof: []utreexo.Hash{{3}}},
		LeafDatas: []wire.LeafData{{PkScript: []byte{0x51}}, {PkScript: []byte{0x52}}},
	}}
}

func copyTemplateBlock(template *mining.BlockTemplate) *wire.MsgBlock {
	block := wire.NewMsgBlock(&template.Block.Header)
	for _, tx := range template.Block.Transactions {
		block.AddTransaction(tx.Copy())
	}
	return block
}

func TestCachedTemplateProofMatching(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*wire.MsgBlock)
		hit    bool
	}{
		{"unchanged", func(*wire.MsgBlock) {}, true},
		{"mining fields", func(b *wire.MsgBlock) {
			b.Header.Nonce++
			b.Header.Timestamp = time.Now()
			b.Header.Version++
			b.Transactions[0].TxIn[0].SignatureScript = []byte{1, 1, 9}
			b.Transactions[0].TxOut[0].PkScript = []byte{0x52}
			b.Header.MerkleRoot = chainhash.Hash{9}
		}, true},
		{"different parent", func(b *wire.MsgBlock) { b.Header.PrevBlock[0]++ }, false},
		{"transaction removed", func(b *wire.MsgBlock) { b.Transactions = b.Transactions[:2] }, false},
		{"transaction order", func(b *wire.MsgBlock) { b.Transactions[1], b.Transactions[2] = b.Transactions[2], b.Transactions[1] }, false},
		{"transaction changed", func(b *wire.MsgBlock) { b.Transactions[1].TxOut[0].Value++ }, false},
		{"witness changed", func(b *wire.MsgBlock) {
			txid := b.Transactions[1].TxHash()
			b.Transactions[1].TxIn[0].Witness[0][0]++
			require.Equal(t, txid, b.Transactions[1].TxHash())
		}, false},
		{"missing coinbase", func(b *wire.MsgBlock) { b.Transactions[0] = b.Transactions[1].Copy() }, false},
		{"empty block", func(b *wire.MsgBlock) { b.Transactions = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := proofTemplate()
			state := newGbtWorkState(nil)
			state.cacheTemplate(template)
			block := copyTemplateBlock(template)
			tc.mutate(block)
			proof := state.cachedTemplateProof(btcutil.NewBlock(block), template.Block.Header.PrevBlock)
			require.Equal(t, tc.hit, proof != nil)
		})
	}
}

func TestCachedTemplateProofIsolationAndInvalidation(t *testing.T) {
	template := proofTemplate()
	state := newGbtWorkState(nil)
	block := btcutil.NewBlock(copyTemplateBlock(template))
	tip := template.Block.Header.PrevBlock
	require.Nil(t, state.cachedTemplateProof(block, tip))
	state.cacheTemplate(template)
	proof := state.cachedTemplateProof(block, tip)
	require.Equal(t, template.UData, proof)
	proof.AccProof.Targets[0]++
	proof.AccProof.Proof[0][0]++
	proof.LeafDatas[0].PkScript[0]++
	require.Equal(t, template.UData, state.cachedTemplateProof(block, tip), "validation cannot mutate cached data")
	require.Nil(t, state.cachedTemplateProof(block, chainhash.Hash{2}), "advanced tip invalidates reuse")
	replacement := proofTemplate()
	replacement.Block.Transactions[1].TxOut[0].Value++
	state.cacheTemplate(replacement)
	require.Nil(t, state.cachedTemplateProof(block, tip), "only the current template is retained")
	replacement.UData = nil
	state.cacheTemplate(replacement)
	require.Nil(t, state.cachedTemplateProof(btcutil.NewBlock(copyTemplateBlock(replacement)), tip))
}

func TestCachedTemplateProofConcurrentReaders(t *testing.T) {
	state := newGbtWorkState(nil)
	state.cacheTemplate(proofTemplate())
	var done sync.WaitGroup
	for i := 0; i < 4; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			template := proofTemplate()
			for j := 0; j < 50; j++ {
				proof := state.cachedTemplateProof(btcutil.NewBlock(copyTemplateBlock(template)), template.Block.Header.PrevBlock)
				if proof == nil {
					t.Error("matching proof unavailable")
					return
				}
				proof.LeafDatas[0].PkScript[0]++
			}
		}()
	}
	for i := 0; i < 50; i++ {
		state.Lock()
		state.cacheTemplate(proofTemplate())
		state.Unlock()
	}
	done.Wait()
}

// Compare proof reuse with the existing local assembly operation. This excludes
// consensus validation and mempool lookups; it is not an end-to-end RPC timing.
func BenchmarkTemplateProof(b *testing.B) {
	const inputs = 500
	forest := utreexo.NewMapPollard(true)
	leaves := make([]wire.LeafData, inputs)
	adds := make([]utreexo.Leaf, inputs*4)
	for i := range adds {
		leaf := wire.LeafData{BlockHash: chainhash.Hash{1}, Height: 1, Amount: 1,
			OutPoint: wire.OutPoint{Index: uint32(i)}, PkScript: []byte{0x51}}
		adds[i] = utreexo.Leaf{Hash: leaf.LeafHash(), Remember: true}
		if i < inputs {
			leaves[i] = leaf
		}
	}
	require.NoError(b, forest.Modify(adds, nil, utreexo.Proof{}))
	proof, err := wire.GenerateUData(leaves, &forest)
	require.NoError(b, err)
	template := proofTemplate()
	template.Block.Transactions = template.Block.Transactions[:1]
	for i := range leaves {
		tx := wire.NewMsgTx(2)
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: leaves[i].OutPoint})
		tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
		template.Block.AddTransaction(tx)
	}
	template.UData = proof
	state := newGbtWorkState(nil)
	state.cacheTemplate(template)
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Fresh wrappers ensure submitted transaction hashing is measured.
			if state.cachedTemplateProof(btcutil.NewBlock(template.Block), template.Block.Header.PrevBlock) == nil {
				b.Fatal("cache miss")
			}
		}
	})
	b.Run("assemble", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := wire.GenerateUData(leaves, &forest); err != nil {
				b.Fatal(err)
			}
		}
	})
}
