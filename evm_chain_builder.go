package lnd

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/btcsuite/btcwallet/waddrmgr"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/btcwallet"
	"github.com/lightningnetwork/lnd/lnwallet/evmwallet"
)

// evmDecimalsTimeout bounds the one-shot ERC20 decimals() startup query.
const evmDecimalsTimeout = 30 * time.Second

// buildEvmChainControl assembles a fully populated ChainControl for the EVM
// backend, mirroring buildSuiChainControl. EVM reuses secp256k1, so the
// internal btcwallet keystore and BtcWalletKeyRing carry over unchanged — only
// the BIP-44 coin type differs (60 for mainnets, testnet type otherwise).
//
// The function is called from DefaultWalletImpl.BuildChainControl when
// partialChainControl.Cfg.EvmMode is active.
func buildEvmChainControl(
	pcc *chainreg.PartialChainControl,
	walletConfig *btcwallet.Config) (*chainreg.ChainControl, func(), error) {

	cfg := pcc.Cfg

	// Use the CoinType from the active net params (EvmNetParams set it to
	// the sub-network's coin type). The internally managed btcwallet
	// hardcodes scope creation from netParams, so we must match it to
	// avoid 'scope not found' crashes on DeriveKey.
	evmCoinType := cfg.ActiveNetParams.CoinType

	// First, create the base wallet controller hosting the HD keystore.
	walletController, err := btcwallet.New(
		*walletConfig, cfg.BlockCache,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create wallet "+
			"controller: %w", err)
	}

	bWallet := walletController.InternalWallet()

	if bWallet.AddrManager().IsLocked() {
		if walletConfig.PrivatePass != nil {
			err := bWallet.Unlock(walletConfig.PrivatePass, nil)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to unlock "+
					"internal btc wallet for evm: %w", err)
			}
		}
	}

	// The custom LND 1017 scope is normally lazily initialised inside
	// BtcWallet.Start(), which the EVM orchestration bypasses; create it
	// here manually or DeriveKey crashes with scope-not-found (same
	// workaround as the Sui builder).
	evmScope := waddrmgr.KeyScope{
		Purpose: keychain.BIP0043Purpose,
		Coin:    evmCoinType,
	}
	_, err = bWallet.AddrManager().FetchScopedKeyManager(evmScope)
	if waddrmgr.IsError(err, waddrmgr.ErrScopeNotFound) {
		_, err = bWallet.AddScopeManager(
			evmScope, waddrmgr.ScopeAddrSchema{
				ExternalAddrType: waddrmgr.WitnessPubKey,
				InternalAddrType: waddrmgr.WitnessPubKey,
			},
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create evm "+
				"keyscope: %w", err)
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch evm keyscope: %w",
			err)
	}

	// EVM reuses secp256k1 directly, so the plain BtcWalletKeyRing is the
	// keyring — no Ed25519-style wrapper is needed (unlike Sui).
	keyRing := keychain.NewBtcWalletKeyRing(bWallet, evmCoinType)

	// Recover the shared RPC client dialled by newEvmPartialChainControl.
	evmClient, ok := pcc.EvmClient.(evmnotify.EvmClient)
	if !ok {
		return nil, nil, fmt.Errorf("evm chain control: EvmClient is "+
			"%T, not evmnotify.EvmClient", pcc.EvmClient)
	}

	// Resolve the sub-network params the same way config.go did and query
	// the ERC20's decimals — the Decimals Scaling Factor every amount
	// conversion depends on.
	evmParams := chainreg.ResolveEvmParams(
		cfg.EvmMode.Chain, cfg.EvmMode.ChainID,
		cfg.EvmMode.TokenAddress, cfg.EvmMode.ContractAddress,
	)
	tokenDecimals, err := queryEvmTokenDecimals(
		evmClient, evmParams.TokenAddress,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to query ERC20 decimals "+
			"for %s: %w", evmParams.TokenAddress, err)
	}

	// Record the EIP-712 domain and token precision for the commitment
	// bridge (lnwallet/evm_commitment.go). This must happen before any
	// channel state machine starts; SetEvmChainActive was already called
	// during config validation.
	var verifyingContract [20]byte
	copy(
		verifyingContract[:],
		common.HexToAddress(evmParams.ContractAddr).Bytes(),
	)
	lnwallet.SetEvmCommitmentParams(input.EvmDomain{
		ChainID:           evmParams.ChainID,
		VerifyingContract: verifyingContract,
	}, tokenDecimals)

	// Record the genesis-block timestamp and the chain's block time so the
	// commitment bridge can translate CLTV-expiry block heights into the
	// block.timestamp deadlines the ChannelManager's HTLC.timelock is
	// compared against. The genesis timestamp is immutable and chain-wide
	// (both peers query the same value), keeping the committed timelock
	// deterministic across peers.
	genesisTs, err := queryEvmGenesisTimestamp(evmClient)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to query EVM genesis "+
			"timestamp: %w", err)
	}
	lnwallet.SetEvmTimelockParams(
		genesisTs, chainreg.EvmBlockTimeSecs(evmParams.ChainID),
	)

	evmWalletController := evmwallet.New(evmwallet.Config{
		KeyRing:       keyRing,
		Client:        evmClient,
		Params:        evmParams,
		TokenDecimals: tokenDecimals,
		GasLimit:      cfg.EvmMode.GasLimit,
		NodeKeyIndex:  cfg.EvmMode.KeyIndex,
	})

	// Surface the node's native-coin (gas) balance at startup. Channel
	// operations pay gas from this balance separately from the ERC20
	// channel asset; with zero gas the node cannot open or settle
	// channels on-chain, so warn loudly rather than fail silently later.
	if gasBal, err := evmWalletController.NativeGasBalance(); err != nil {
		ltndLog.Warnf("EVM: unable to read node gas balance: %v", err)
	} else if gasBal.Sign() == 0 {
		ltndLog.Warnf("EVM: node account has ZERO gas (native coin); " +
			"fund it before opening or settling channels")
	} else {
		ltndLog.Infof("EVM: node gas balance %s wei", gasBal)
	}

	signer := input.NewEvmSigner(keyRing)
	chainIO := evmwallet.NewEvmBlockChainIO(evmClient)

	lnWalletConfig := lnwallet.Config{
		Database:         cfg.ChanStateDB,
		Notifier:         pcc.ChainNotifier,
		WalletController: evmWalletController,
		Signer:           signer,
		FeeEstimator:     pcc.FeeEstimator,
		SecretKeyRing:    keyRing,
		ChainIO:          chainIO,
		// The EVM-overlaid regtest placeholder; EVM does not rely on
		// chaincfg.Params for channel operations, but zpay32 reads the
		// per-sub-network Bech32HRPSegwit from it.
		NetParams:    *cfg.ActiveNetParams.Params,
		AuxLeafStore: cfg.AuxLeafStore,
		AuxSigner:    cfg.AuxSigner,
	}

	activeChainControl, cleanUp, err := chainreg.NewChainControl(
		lnWalletConfig, evmWalletController, pcc,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create EVM chain "+
			"control: %w", err)
	}

	return activeChainControl, cleanUp, nil
}

// queryEvmTokenDecimals reads the ERC20 decimals() of the sub-network's token
// once at startup.
func queryEvmTokenDecimals(client evmnotify.EvmClient, tokenAddr string) (
	uint8, error) {

	data, err := evmnotify.PackDecimals()
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(
		context.Background(), evmDecimalsTimeout,
	)
	defer cancel()

	token := common.HexToAddress(tokenAddr)
	out, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &token,
		Data: data,
	}, nil)
	if err != nil {
		return 0, err
	}

	return evmnotify.UnpackDecimals(out)
}

// queryEvmGenesisTimestamp reads the timestamp of block 0 — an immutable,
// chain-wide value both channel peers observe identically — used to anchor
// the CLTV-height → block.timestamp conversion for HTLC timelocks.
func queryEvmGenesisTimestamp(client evmnotify.EvmClient) (uint64, error) {
	ctx, cancel := context.WithTimeout(
		context.Background(), evmDecimalsTimeout,
	)
	defer cancel()

	hdr, err := client.HeaderByNumber(ctx, big.NewInt(0))
	if err != nil {
		return 0, err
	}

	return hdr.Time, nil
}

// evmChannelStatusFunc returns a callback the chain arbitrator's EVM settler
// uses to read a channel's on-chain ChannelManager status (so it can stop
// re-broadcasting distributeFunds once the channel is CLOSED). It returns nil
// when the EVM backend isn't active, so the arbitrator stays chain-agnostic.
func evmChannelStatusFunc(cc *chainreg.ChainControl) func([32]byte) (uint8,
	error) {

	if cc.Cfg.EvmMode == nil || !cc.Cfg.EvmMode.Active {
		return nil
	}
	client, ok := cc.EvmClient.(evmnotify.EvmClient)
	if !ok {
		return nil
	}
	contract := common.HexToAddress(cc.Cfg.EvmMode.ContractAddress)

	return func(channelID [32]byte) (uint8, error) {
		data, err := evmnotify.PackChannel(channelID)
		if err != nil {
			return 0, err
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), evmDecimalsTimeout,
		)
		defer cancel()

		out, err := client.CallContract(ctx, ethereum.CallMsg{
			To:   &contract,
			Data: data,
		}, nil)
		if err != nil {
			return 0, err
		}

		return evmnotify.UnpackChannelStatus(out)
	}
}
