package chainreg

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/require"
)

func TestSuiGenesisHashLengths(t *testing.T) {
	hashes := []chainhash.Hash{
		suiDevnetGenesisHash,
		SuiDevNetParams.GenesisHash,
		SuiTestNetParams.GenesisHash,
		SuiMainNetParams.GenesisHash,
		SuiSimNetParams.GenesisHash,
	}

	for i, h := range hashes {
		require.Equal(t, chainhash.HashSize, len(h), "hash %d length", i)
	}
}

func TestMustDecodeHashValid(t *testing.T) {
	hash := mustDecodeHash(suiDevnetGenesisHashHex)
	require.Equal(t, suiDevnetGenesisHash, hash)
}

func TestMustDecodeHashInvalid(t *testing.T) {
	require.Panics(t, func() {
		mustDecodeHash("00")
	})

	require.Panics(t, func() {
		mustDecodeHash("zz")
	})
}
