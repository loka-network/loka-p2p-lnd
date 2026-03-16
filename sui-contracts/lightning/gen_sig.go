package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
)

func pad(b []byte) []byte {
	res := make([]byte, 32)
	copy(res[32-len(b):], b)
	return res
}

func getLowSSig(privKey *ecdsa.PrivateKey, hash []byte) ([]byte, []byte) {
	// secp256k1 curve order half
	halfOrder, _ := new(big.Int).SetString("7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0", 16)
	for {
		r, s, err := ecdsa.Sign(rand.Reader, privKey, hash)
		if err != nil {
			panic(err)
		}
		if s.Cmp(halfOrder) <= 0 {
			return pad(r.Bytes()), pad(s.Bytes())
		}
	}
}

func main() {
	// Bob's deterministic private key
	privKeyBytes, _ := hex.DecodeString("22a47fa09a223f2aa079edf85a7c2d4f8720ee63e502ee2869afab7de234b80c")
	privKey, _ := btcec.PrivKeyFromBytes(privKeyBytes)

	pubKey := privKey.PubKey().SerializeCompressed()
	fmt.Printf("pubkey: %x\n", pubKey)

	// --- 1. Force Close Message ---
	channelHex := "90c5264c9da2b340fdc9fbd15ad3f0a181a57afa7ae55b15a3c5dce6b31f45c8"
	channelIDBytes, err := hex.DecodeString(channelHex)
	if err != nil {
		panic(err)
	}
	var channelID [32]byte
	copy(channelID[:], channelIDBytes)

	stateNumBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(stateNumBuf, 5)

	revHashHex := "9f72ea0cf49536e3c66c787f705186df9a4378083753ae9536d65b3ad7fcddc4"
	revHashBytes, err := hex.DecodeString(revHashHex)
	if err != nil {
		panic(err)
	}

	payload := append(channelID[:], stateNumBuf...)
	payload = append(payload, revHashBytes...)
	hash := sha256.Sum256(payload)

	stdPrivKey := privKey.ToECDSA()

	rBuf, sBuf := getLowSSig(stdPrivKey, hash[:])
	sigRaw := append(rBuf, sBuf...)

	fmt.Printf("force_close hash: %x\n", hash)
	fmt.Printf("force_close payload: %x\n", payload)
	fmt.Printf("force_close sig: %x\n", sigRaw)

	// --- 2. Penalize Message ---
	revSecret := make([]byte, 32)
	for i := range revSecret {
		revSecret[i] = 0x22
	}
	fmt.Printf("raw revSecret in Go: %x\n", revSecret)

	hashRev := sha256.Sum256(revSecret)
	rBufRev, sBufRev := getLowSSig(stdPrivKey, hashRev[:])
	sigRawRev := append(rBufRev, sBufRev...)

	fmt.Printf("penalize hash: %x\n", hashRev)
	fmt.Printf("penalize sig: %x\n", sigRawRev)
}
