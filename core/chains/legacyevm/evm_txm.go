package legacyevm

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-common/pkg/sqlutil"
	evmclient "github.com/smartcontractkit/chainlink/v2/core/chains/evm/client"
	evmconfig "github.com/smartcontractkit/chainlink/v2/core/chains/evm/config"
	"github.com/smartcontractkit/chainlink/v2/core/chains/evm/gas"
	"github.com/smartcontractkit/chainlink/v2/core/chains/evm/gas/rollups"
	httypes "github.com/smartcontractkit/chainlink/v2/core/chains/evm/headtracker/types"
	"github.com/smartcontractkit/chainlink/v2/core/chains/evm/logpoller"
	"github.com/smartcontractkit/chainlink/v2/core/chains/evm/txmgr"
	"github.com/smartcontractkit/chainlink/v2/core/logger"
)

func newEvmTxm(
	ds sqlutil.DataSource,
	cfg evmconfig.EVM,
	evmRPCEnabled bool,
	databaseConfig txmgr.DatabaseConfig,
	listenerConfig txmgr.ListenerConfig,
	client evmclient.Client,
	lggr logger.Logger,
	logPoller logpoller.LogPoller,
	opts ChainRelayOpts,
	headTracker httypes.HeadTracker,
	clientsByChainID map[string]rollups.DAClient,
) (txm txmgr.TxManager,
	estimator gas.EvmFeeEstimator,
	err error,
) {
	chainID := cfg.ChainID()
	if !evmRPCEnabled {
		txm = &txmgr.NullTxManager{ErrMsg: fmt.Sprintf("Ethereum is disabled for chain %d", chainID)}
		return txm, nil, nil
	}

	lggr = lggr.Named("Txm")
	lggr.Infow("Initializing EVM transaction manager",
		"bumpTxDepth", cfg.GasEstimator().BumpTxDepth(),
		"maxInFlightTransactions", cfg.Transactions().MaxInFlight(),
		"maxQueuedTransactions", cfg.Transactions().MaxQueued(),
		"nonceAutoSync", cfg.NonceAutoSync(),
		"limitDefault", cfg.GasEstimator().LimitDefault(),
	)

	// build estimator from factory
	if opts.GenGasEstimator == nil {
		if estimator, err = gas.NewEstimator(lggr, client, cfg.ChainType(), chainID, cfg.GasEstimator(), clientsByChainID); err != nil {
			return nil, nil, fmt.Errorf("failed to initialize estimator: %w", err)
		}
	} else {
		estimator = opts.GenGasEstimator(chainID)
	}

	if opts.GenTxManager == nil {
		txm, err = txmgr.NewTxm(
			ds,
			cfg,
			txmgr.NewEvmTxmFeeConfig(cfg.GasEstimator()),
			cfg.Transactions(),
			cfg.NodePool().Errors(),
			databaseConfig,
			listenerConfig,
			client,
			lggr,
			logPoller,
			opts.KeyStore,
			estimator,
			headTracker)
	} else {
		txm = opts.GenTxManager(chainID)
	}
	return
}
