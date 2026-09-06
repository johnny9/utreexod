// Copyright (c) 2025 The utreexo developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package wire

import (
	"bytes"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
	"github.com/utreexo/utreexo"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
)

func TestUtreexoProofDecodeLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []byte
		count  uint64
	}{
		{"hashes", nil, MaxProofHashes + 1},
		{"targets", []byte{0}, MaxPossibleInputsPerBlock + 1},
		{"leaves", []byte{0, 0}, MaxPossibleInputsPerBlock + 1},
		{"overflow", nil, ^uint64(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload bytes.Buffer
			payload.Write(make([]byte, 32))
			payload.Write(tc.prefix)
			require.NoError(t, WriteVarInt(&payload, 0, tc.count))
			var message MsgUtreexoProof
			err := message.BtcDecode(&payload, ProtocolVersion, BaseEncoding)
			require.IsType(t, &MessageError{}, err)
		})
	}
	var message MsgUtreexoProof
	reader := iotest.OneByteReader(bytes.NewReader(make([]byte, 35)))
	require.NoError(t, message.BtcDecode(reader, ProtocolVersion, BaseEncoding))
}

func TestUtreexoProofSerialize(t *testing.T) {
	tests := []struct {
		data MsgUtreexoProof
	}{
		{
			data: MsgUtreexoProof{
				BlockHash: chainhash.HashH([]byte{1}),
				ProofHashes: []utreexo.Hash{
					{0xff, 0x0, 0x4},
					{0xff, 0x1, 0xd, 0xdf},
				},
				Targets: []uint64{1254548, 481754},
				LeafDatas: []LeafData{
					{
						Height:     784611,
						IsCoinBase: true,
						Amount:     631465945,
						PkScript:   []byte{},
					},
					{
						Height:     784631,
						IsCoinBase: false,
						Amount:     637465960,
						PkScript:   []byte{},
					},
				},
			},
		},
	}

	for _, test := range tests {
		var buf bytes.Buffer
		err := test.data.BtcEncode(&buf, 0, LatestEncoding)
		if err != nil {
			t.Fatal(err)
		}

		b := buf.Bytes()

		// Check data.
		r := bytes.NewBuffer(b)
		got := MsgUtreexoProof{}
		err = got.BtcDecode(r, 0, LatestEncoding)
		if err != nil {
			t.Fatal(err)
		}

		require.Equal(t, test.data, got)
	}
}
