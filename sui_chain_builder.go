package lnd

import (
	"fmt"

	"github.com/btcsuite/btcwallet/wallet"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/suiwallet"
)

// buildSuiChainControl assembles a fully populated ChainControl for the Sui
// backend.  It uses stub implementations of WalletController, Signer,
// BlockChainIO, and SecretKeyRing; all are wired via the zero-intrusion
// adapter pattern described in 1-refactor-docs/lnd-and-sui-integration.md.
//
// The function is called from DefaultWalletImpl.BuildChainControl when
// partialChainControl.Cfg.SuiMode is non-nil.
func buildSuiChainControl(
	pcc *chainreg.PartialChainControl,
	walletConfig *btcwallet.Config) (*chainreg.ChainControl, func(), error) {

	// First, we'll create the wallet controller.  The Sui wallet
	// implementation currently requires no base wallet instance.
	walletController, err := btcwallet.New(
		*walletConfig, pcc.Cfg.BlockCache,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create wallet "+
			"controller: %w", err)
	}

	// Determine the Sui coin type based on the active network.
	suiCoinType := chainreg.CoinTypeSuiTestnet
	if pcc.Cfg.SuiMode.MainNet {
		suiCoinType = chainreg.CoinTypeSui
	}

	// Create the base Bitcoin keyring from the wallet, but using the Sui
	// coin type. This ensures that all derived keys follow the Sui BIP-44
	// path (m/1017'/784'/...).
	btcKeyRing := keychain.NewBtcWalletKeyRing(
		walletController.InternalWallet(), suiCoinType,
	)

	// Wrap it in the SuiKeyRing.
	keyRing := &suiwallet.SuiKeyRing{
		SecretKeyRing: btcKeyRing,
	}

	// Derive the node key to use as the default Sui address.
	// In Sui, an address is the Blake2b-256 hash of (flag || pubkey).
	// For now, we'll just use the hex-encoded pubkey as a placeholder or
	// implement the real Sui address derivation if we have the tools.
	nodeKeyDesc, err := keyRing.DeriveKey(keychain.KeyLocator{
		Family: keychain.KeyFamilyNodeKey,
		Index:  0,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("unable to derive node key: %w", err)
	}
	suiAddress := fmt.Sprintf("0x%x", nodeKeyDesc.PubKey.SerializeCompressed())

	suiClient := pcc.SuiClient.(suiwallet.SuiClient)
	suiWalletController := suiwallet.New(suiwallet.Config{
		SuiAddress: suiAddress,
		Client:     suiClient,
	})

	signer := suiwallet.NewSuiSigner(keyRing)
	chainIO := suiwallet.NewSuiBlockChainIO(suiClient)

	lnWalletConfig := lnwallet.Config{
		Database:         pcc.Cfg.ChanStateDB,
		Notifier:         pcc.ChainNotifier,
		WalletController: suiWalletController,
		Signer:           signer,
		FeeEstimator:     pcc.FeeEstimator,
		SecretKeyRing:    keyRing,
		ChainIO:          chainIO,
		// Use the zero Bitcoin net params as a placeholder; Sui does
		// not rely on chaincfg.Params for channel operations.
		NetParams:    *pcc.Cfg.ActiveNetParams.Params,
		AuxLeafStore: pcc.Cfg.AuxLeafStore,
		AuxSigner:    pcc.Cfg.AuxSigner,
	}

	activeChainControl, cleanUp, err := chainreg.NewChainControl(
		lnWalletConfig, suiWalletController, pcc,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create Sui chain "+
			"control: %w", err)
	}

	return activeChainControl, cleanUp, nil
}
