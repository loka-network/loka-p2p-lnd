package lnd

import (
	"fmt"

	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/setuwallet"
)

// buildSetuChainControl assembles a fully populated ChainControl for the Setu
// DAG backend.  It uses stub implementations of WalletController, Signer,
// BlockChainIO, and SecretKeyRing; all are wired via the zero-intrusion
// adapter pattern described in 1-refactor-docs/lnd-and-setu-integration.md.
//
// The function is called from DefaultWalletImpl.BuildChainControl when
// partialChainControl.Cfg.SetuMode is non-nil.
func buildSetuChainControl(
	pcc *chainreg.PartialChainControl) (*chainreg.ChainControl, func(), error) {

	walletController := setuwallet.New()
	keyRing := &setuwallet.SetuKeyRing{}
	signer := &setuwallet.SetuSigner{}
	chainIO := &setuwallet.SetuBlockChainIO{}

	lnWalletConfig := lnwallet.Config{
		Database:         pcc.Cfg.ChanStateDB,
		Notifier:         pcc.ChainNotifier,
		WalletController: walletController,
		Signer:           signer,
		FeeEstimator:     pcc.FeeEstimator,
		SecretKeyRing:    keyRing,
		ChainIO:          chainIO,
		// Use the zero Bitcoin net params as a placeholder; Setu does
		// not rely on chaincfg.Params for channel operations.
		NetParams:    *pcc.Cfg.ActiveNetParams.Params,
		AuxLeafStore: pcc.Cfg.AuxLeafStore,
		AuxSigner:    pcc.Cfg.AuxSigner,
	}

	activeChainControl, cleanUp, err := chainreg.NewChainControl(
		lnWalletConfig, walletController, pcc,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create Setu chain "+
			"control: %w", err)
	}

	return activeChainControl, cleanUp, nil
}
