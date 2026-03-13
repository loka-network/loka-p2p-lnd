package lnd

import (
	"fmt"

	"github.com/lightningnetwork/lnd/chainreg"
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
	pcc *chainreg.PartialChainControl) (*chainreg.ChainControl, func(), error) {

	suiClient := pcc.SuiClient.(suiwallet.SuiClient)
	walletController := suiwallet.New(suiwallet.Config{
		SuiAddress: "0xPLACEHOLDER", // TODO: Derive from KeyRing
		Client:     suiClient,
	})
	keyRing := &suiwallet.SuiKeyRing{}
	signer := &suiwallet.SuiSigner{}
	chainIO := &suiwallet.SuiBlockChainIO{}

	lnWalletConfig := lnwallet.Config{
		Database:         pcc.Cfg.ChanStateDB,
		Notifier:         pcc.ChainNotifier,
		WalletController: walletController,
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
		lnWalletConfig, walletController, pcc,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create Sui chain "+
			"control: %w", err)
	}

	return activeChainControl, cleanUp, nil
}
