package chainreg

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/require"
)

func TestSetuGenesisHashLengths(t *testing.T) {
	hashes := []chainhash.Hash{
		setuDevnetGenesisHash,
		SetuDevNetParams.GenesisHash,
		SetuTestNetParams.GenesisHash,
		SetuMainNetParams.GenesisHash,
		SetuSimNetParams.GenesisHash,
	}

	for i, h := range hashes {
		require.Equal(t, chainhash.HashSize, len(h), "hash %d length", i)
	}
}

func TestMustDecodeHashValid(t *testing.T) {
	hash := mustDecodeHash(setuDevnetGenesisHashHex)
	require.Equal(t, setuDevnetGenesisHash, hash)
}

func TestMustDecodeHashInvalid(t *testing.T) {
	require.Panics(t, func() {
		mustDecodeHash("00")
	})

	require.Panics(t, func() {
		mustDecodeHash("zz")
	})
}
