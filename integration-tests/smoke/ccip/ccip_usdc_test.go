package smoke

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"golang.org/x/exp/maps"
	"golang.org/x/sync/errgroup"

	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	"github.com/smartcontractkit/chainlink/deployment"
	"github.com/smartcontractkit/chainlink/deployment/ccip/changeset"
	testsetups "github.com/smartcontractkit/chainlink/integration-tests/testsetups/ccip"
	"github.com/smartcontractkit/chainlink/v2/core/chains/evm/utils"
	"github.com/smartcontractkit/chainlink/v2/core/gethwrappers/ccip/generated/router"
	"github.com/smartcontractkit/chainlink/v2/core/gethwrappers/shared/generated/burn_mint_erc677"
	"github.com/smartcontractkit/chainlink/v2/core/logger"
)

/*
* Chain topology for this test
* 	chainA (USDC, MY_TOKEN)
*			|
*			| ------- chainC (USDC, MY_TOKEN)
*			|
* 	chainB (USDC)
 */
func TestUSDCTokenTransfer(t *testing.T) {
	lggr := logger.TestLogger(t)
	ctx := tests.Context(t)
	config := &changeset.TestConfigs{
		IsUSDC: true,
	}
	tenv, _, _ := testsetups.NewLocalDevEnvironmentWithDefaultPrice(t, lggr, config)
	//tenv := changeset.NewMemoryEnvironmentWithJobsAndContracts(t, lggr, memory.MemoryEnvironmentConfig{
	//	Chains:             3,
	//	NumOfUsersPerChain: 3,
	//	Nodes:              5,
	//	Bootstraps:         1,
	//}, config)

	e := tenv.Env
	state, err := changeset.LoadOnchainState(e)
	require.NoError(t, err)

	allChainSelectors := maps.Keys(e.Chains)
	chainA := allChainSelectors[0]
	chainC := allChainSelectors[1]
	chainB := allChainSelectors[2]

	ownerChainA := e.Chains[chainA].DeployerKey
	ownerChainC := e.Chains[chainC].DeployerKey
	ownerChainB := e.Chains[chainB].DeployerKey

	aChainUSDC, cChainUSDC, err := changeset.ConfigureUSDCTokenPools(lggr, e.Chains, chainA, chainC, state)
	require.NoError(t, err)

	bChainUSDC, _, err := changeset.ConfigureUSDCTokenPools(lggr, e.Chains, chainB, chainC, state)
	require.NoError(t, err)

	aChainToken, _, cChainToken, _, err := changeset.DeployTransferableToken(
		lggr,
		tenv.Env.Chains,
		chainA,
		chainC,
		ownerChainA,
		ownerChainC,
		state,
		e.ExistingAddresses,
		"MY_TOKEN",
	)
	require.NoError(t, err)

	// Add all lanes
	require.NoError(t, changeset.AddLanesForAll(e, state))

	changeset.MintAndAllow(
		t,
		e,
		state,
		map[uint64][]changeset.MintTokenInfo{
			chainA: {
				changeset.NewMintTokenInfo(ownerChainA, aChainUSDC, aChainToken),
			},
			chainB: {
				changeset.NewMintTokenInfo(ownerChainB, bChainUSDC),
			},
			chainC: {
				changeset.NewMintTokenInfo(ownerChainC, cChainUSDC, cChainToken),
			},
		},
	)

	err = updateFeeQuoters(lggr, e, state, chainA, chainB, chainC, aChainUSDC, bChainUSDC, cChainUSDC)
	require.NoError(t, err)

	// MockE2EUSDCTransmitter always mint 1, see MockE2EUSDCTransmitter.sol for more details
	tinyOneCoin := new(big.Int).SetUint64(1)

	tcs := []changeset.TestTransferRequest{
		{
			Name:        "single USDC token transfer to EOA",
			Receiver:    utils.RandomAddress(),
			SourceChain: chainC,
			DestChain:   chainA,
			Tokens: []router.ClientEVMTokenAmount{
				{
					Token:  cChainUSDC.Address(),
					Amount: tinyOneCoin,
				}},
			ExpectedTokenBalances: map[common.Address]*big.Int{
				aChainUSDC.Address(): tinyOneCoin,
			},
			ExpectedStatus: changeset.EXECUTION_STATE_SUCCESS,
		},
		{
			Name:        "multiple USDC tokens within the same message",
			Receiver:    utils.RandomAddress(),
			SourceChain: chainC,
			DestChain:   chainA,
			Tokens: []router.ClientEVMTokenAmount{
				{
					Token:  cChainUSDC.Address(),
					Amount: tinyOneCoin,
				},
				{
					Token:  cChainUSDC.Address(),
					Amount: tinyOneCoin,
				},
			},
			ExpectedTokenBalances: map[common.Address]*big.Int{
				// 2 coins because of the same Receiver
				aChainUSDC.Address(): new(big.Int).Add(tinyOneCoin, tinyOneCoin),
			},
			ExpectedStatus: changeset.EXECUTION_STATE_SUCCESS,
		},
		{
			Name:        "USDC token together with another token transferred to EOA",
			Receiver:    utils.RandomAddress(),
			SourceChain: chainA,
			DestChain:   chainC,
			Tokens: []router.ClientEVMTokenAmount{
				{
					Token:  aChainUSDC.Address(),
					Amount: tinyOneCoin,
				},
				{
					Token:  aChainToken.Address(),
					Amount: new(big.Int).Mul(tinyOneCoin, big.NewInt(10)),
				},
			},
			ExpectedTokenBalances: map[common.Address]*big.Int{
				cChainUSDC.Address():  tinyOneCoin,
				cChainToken.Address(): new(big.Int).Mul(tinyOneCoin, big.NewInt(10)),
			},
			ExpectedStatus: changeset.EXECUTION_STATE_SUCCESS,
		},
		{
			Name:        "USDC programmable token transfer to valid contract receiver",
			Receiver:    state.Chains[chainC].Receiver.Address(),
			SourceChain: chainA,
			DestChain:   chainC,
			Tokens: []router.ClientEVMTokenAmount{
				{
					Token:  aChainUSDC.Address(),
					Amount: tinyOneCoin,
				},
			},
			Data: []byte("hello world"),
			ExpectedTokenBalances: map[common.Address]*big.Int{
				cChainUSDC.Address(): tinyOneCoin,
			},
			ExpectedStatus: changeset.EXECUTION_STATE_SUCCESS,
		},
		{
			Name:        "USDC programmable token transfer with too little gas",
			Receiver:    state.Chains[chainB].Receiver.Address(),
			SourceChain: chainC,
			DestChain:   chainB,
			Tokens: []router.ClientEVMTokenAmount{
				{
					Token:  cChainUSDC.Address(),
					Amount: tinyOneCoin,
				},
			},
			Data: []byte("gimme more gas to execute that!"),
			ExpectedTokenBalances: map[common.Address]*big.Int{
				bChainUSDC.Address(): new(big.Int).SetUint64(0),
			},
			ExtraArgs:      changeset.MakeEVMExtraArgsV2(1, false),
			ExpectedStatus: changeset.EXECUTION_STATE_FAILURE,
		},
		{
			Name:        "USDC token transfer from a different source chain",
			Receiver:    utils.RandomAddress(),
			SourceChain: chainB,
			DestChain:   chainC,
			Tokens: []router.ClientEVMTokenAmount{
				{
					Token:  bChainUSDC.Address(),
					Amount: tinyOneCoin,
				},
			},
			Data: nil,
			ExpectedTokenBalances: map[common.Address]*big.Int{
				cChainUSDC.Address(): tinyOneCoin,
			},
			ExpectedStatus: changeset.EXECUTION_STATE_SUCCESS,
		},
	}

	startBlocks, expectedSeqNums, expectedExecutionStates, expectedTokenBalances :=
		changeset.TransferMultiple(ctx, t, e, state, tcs)

	err = changeset.ConfirmMultipleCommits(
		t,
		e.Chains,
		state.Chains,
		startBlocks,
		false,
		expectedSeqNums,
	)
	require.NoError(t, err)

	execStates := changeset.ConfirmExecWithSeqNrsForAll(
		t,
		e,
		state,
		changeset.SeqNumberRageToSlice(expectedSeqNums),
		startBlocks,
	)
	require.Equal(t, expectedExecutionStates, execStates)

	changeset.WaitForTokenBalances(ctx, t, e.Chains, expectedTokenBalances)
}

func updateFeeQuoters(
	lggr logger.Logger,
	e deployment.Environment,
	state changeset.CCIPOnChainState,
	chainA, chainB, chainC uint64,
	aChainUSDC, bChainUSDC, cChainUSDC *burn_mint_erc677.BurnMintERC677,
) error {
	updateFeeQtrGrp := errgroup.Group{}
	updateFeeQtrGrp.Go(func() error {
		return changeset.UpdateFeeQuoterForUSDC(lggr, e.Chains[chainA], state.Chains[chainA], chainC, aChainUSDC)
	})
	updateFeeQtrGrp.Go(func() error {
		return changeset.UpdateFeeQuoterForUSDC(lggr, e.Chains[chainB], state.Chains[chainB], chainC, bChainUSDC)
	})
	updateFeeQtrGrp.Go(func() error {
		err1 := changeset.UpdateFeeQuoterForUSDC(lggr, e.Chains[chainC], state.Chains[chainC], chainA, cChainUSDC)
		if err1 != nil {
			return err1
		}
		return changeset.UpdateFeeQuoterForUSDC(lggr, e.Chains[chainC], state.Chains[chainC], chainB, cChainUSDC)
	})
	return updateFeeQtrGrp.Wait()
}
