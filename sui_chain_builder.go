package lnd

import (
	"fmt"

	"github.com/btcsuite/btcwallet/waddrmgr"
	"github.com/lightningnetwork/lnd/chainntnfs/suinotify"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/btcwallet"
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

	// Use the CoinType from the underlying active Bitcoin network parameters
	// (e.g., 115 for simnet, 0 for mainnet). The internally managed btcwallet 
	// hardcodes the creation of scopes based on the netParams, so we must
	// match it perfectly to avoid 'scope not found' crashes on DeriveKey.
	suiCoinType := pcc.Cfg.ActiveNetParams.CoinType

	// First, we'll create the wallet controller.  The Sui wallet
	// implementation currently requires no base wallet instance.
	walletController, err := btcwallet.New(
		*walletConfig, pcc.Cfg.BlockCache,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create wallet "+
			"controller: %w", err)
	}

	if walletController.InternalWallet().AddrManager().IsLocked() {
		if walletConfig.PrivatePass != nil {
			if err := walletController.InternalWallet().Unlock(walletConfig.PrivatePass, nil); err != nil {
				return nil, nil, fmt.Errorf("unable to unlock internal btc wallet for sui: %w", err)
			}
		}
	}

	bWallet := walletController.InternalWallet()

	// In Sui mode, the internal btcwallet is wrapped and its Start() method
	// may be bypassed by the orchestrator. Because the custom LND 1017 scope 
	// is normally lazily initialized inside BtcWallet.Start(), we must do 
	// it here manually or else DeriveKey will crash with scope not found.
	suiScope := waddrmgr.KeyScope{
		Purpose: keychain.BIP0043Purpose,
		Coin:    suiCoinType,
	}
	_, err = bWallet.AddrManager().FetchScopedKeyManager(suiScope)
	if waddrmgr.IsError(err, waddrmgr.ErrScopeNotFound) {
		_, err = bWallet.AddScopeManager(suiScope, waddrmgr.ScopeAddrSchema{
			ExternalAddrType: waddrmgr.WitnessPubKey,
			InternalAddrType: waddrmgr.WitnessPubKey,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create sui keyscope: %w", err)
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch sui keyscope: %w", err)
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



	suiClient := pcc.SuiClient.(suinotify.SuiClient)
	suiWalletController := suiwallet.New(suiwallet.Config{
		KeyRing: keyRing,
		Client:  suiClient,
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
