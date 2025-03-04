package smoke

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/maps"

	"github.com/smartcontractkit/chainlink-ccip/pkg/types/ccipocr3"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	"github.com/smartcontractkit/chainlink/deployment"
	"github.com/smartcontractkit/chainlink/deployment/ccip/changeset"
	testsetups "github.com/smartcontractkit/chainlink/integration-tests/testsetups/ccip"
	"github.com/smartcontractkit/chainlink/v2/core/chains/evm/utils"
	"github.com/smartcontractkit/chainlink/v2/core/gethwrappers/ccip/generated/router"
	"github.com/smartcontractkit/chainlink/v2/core/logger"
)

// Send the following messages
// 1. src -> dest - out of order token transfer to EOA
// 2. src -> dest - ordered USDC token transfer, but with faulty attestation, should be stuck forever
// 3. src -> dest - ordered token transfer, should not be executed because previous message is stuck
// 4. src -> dest - out of order message transfer, should be executed anyway
// 5. src -> dest - ordered token transfer, but from a different sender
//
// All messages should be properly committed, but only 1 and 4, 5 are fully executed.
// Messages 2 and 3 are untouched, because ordering is enforced.
func Test_OutOfOrderExecution(t *testing.T) {
	lggr := logger.TestLogger(t)
	ctx := tests.Context(t)
	config := &changeset.TestConfigs{
		IsUSDC:                   true,
		IsUSDCAttestationMissing: true,
	}
	tenv, _, _ := testsetups.NewLocalDevEnvironmentWithDefaultPrice(t, lggr, config)
	// Inmemory setup used for debugging and development, use instead of docker when needed
	//tenv := changeset.NewMemoryEnvironmentWithJobsAndContracts(t, lggr, 2, 4, config)

	e := tenv.Env
	state, err := changeset.LoadOnchainState(e)
	require.NoError(t, err)

	allChainSelectors := maps.Keys(e.Chains)
	sourceChain, destChain := allChainSelectors[0], allChainSelectors[1]
	ownerSourceChain := e.Chains[sourceChain].DeployerKey
	ownerDestChain := e.Chains[destChain].DeployerKey

	anotherSender, err := pickFirstAvailableUser(tenv, sourceChain, e)
	require.NoError(t, err)

	oneE18 := new(big.Int).SetUint64(1e18)

	srcToken, _, destToken, _, err := changeset.DeployTransferableToken(
		lggr,
		tenv.Env.Chains,
		sourceChain,
		destChain,
		ownerSourceChain,
		ownerDestChain,
		state,
		e.ExistingAddresses,
		"OWNER_TOKEN",
	)
	require.NoError(t, err)

	srcUSDC, destUSDC, err := changeset.ConfigureUSDCTokenPools(lggr, e.Chains, sourceChain, destChain, state)
	require.NoError(t, err)

	err = changeset.UpdateFeeQuoterForUSDC(lggr, e.Chains[sourceChain], state.Chains[sourceChain], destChain, srcUSDC)
	require.NoError(t, err)
	err = changeset.UpdateFeeQuoterForUSDC(lggr, e.Chains[destChain], state.Chains[destChain], sourceChain, destUSDC)
	require.NoError(t, err)

	changeset.MintAndAllow(
		t,
		e,
		state,
		map[uint64][]changeset.MintTokenInfo{
			sourceChain: {
				changeset.NewMintTokenInfo(ownerSourceChain, srcToken, srcUSDC),
				changeset.NewMintTokenWithCustomSender(ownerSourceChain, anotherSender, srcToken),
			},
		},
	)
	require.NoError(t, changeset.AddLanesForAll(e, state))

	tokenTransfer := []router.ClientEVMTokenAmount{
		{
			Token:  srcToken.Address(),
			Amount: oneE18,
		},
	}
	usdcTransfer := []router.ClientEVMTokenAmount{
		{
			Token:  srcUSDC.Address(),
			Amount: oneE18,
		},
	}

	identifier := changeset.SourceDestPair{
		SourceChainSelector: sourceChain,
		DestChainSelector:   destChain,
	}

	startBlocks := make(map[uint64]*uint64)
	expectedStatuses := make(map[uint64]int)

	latesthdr, err := e.Chains[destChain].Client.HeaderByNumber(ctx, nil)
	require.NoError(t, err)
	block := latesthdr.Number.Uint64()
	startBlocks[destChain] = &block

	// Out of order execution to the EOA should be properly executed
	firstReceiver := utils.RandomAddress()
	firstMessage, _ := changeset.Transfer(
		ctx,
		t,
		e,
		state,
		sourceChain,
		destChain,
		tokenTransfer,
		firstReceiver,
		nil,
		changeset.MakeEVMExtraArgsV2(0, true),
	)
	expectedStatuses[firstMessage.SequenceNumber] = changeset.EXECUTION_STATE_SUCCESS
	t.Logf("Out of order messages sent from chain %d to chain %d with sequence number %d",
		sourceChain, destChain, firstMessage.SequenceNumber,
	)

	// Ordered execution should fail because attestation is not present
	secondReceiver := utils.RandomAddress()
	secondMsg, _ := changeset.Transfer(
		ctx,
		t,
		e,
		state,
		sourceChain,
		destChain,
		usdcTransfer,
		secondReceiver,
		nil,
		nil,
	)
	t.Logf("Ordered USDC transfer sent from chain %d to chain %d with sequence number %d",
		sourceChain, destChain, secondMsg.SequenceNumber,
	)

	// Ordered token transfer should fail, because previous message cannot be executed
	thirdReceiver := utils.RandomAddress()
	thirdMessage, _ := changeset.Transfer(
		ctx,
		t,
		e,
		state,
		sourceChain,
		destChain,
		tokenTransfer,
		thirdReceiver,
		nil,
		changeset.MakeEVMExtraArgsV2(0, false),
	)
	t.Logf("Ordered token transfer from chain %d to chain %d with sequence number %d",
		sourceChain, destChain, thirdMessage.SequenceNumber,
	)

	// Out of order programmable token transfer should be executed
	fourthReceiver := state.Chains[destChain].Receiver.Address()
	fourthMessage, _ := changeset.Transfer(
		ctx,
		t,
		e,
		state,
		sourceChain,
		destChain,
		tokenTransfer,
		fourthReceiver,
		[]byte("this message has enough gas to execute"),
		changeset.MakeEVMExtraArgsV2(300_000, true),
	)
	expectedStatuses[fourthMessage.SequenceNumber] = changeset.EXECUTION_STATE_SUCCESS
	t.Logf("Out of order programmable token transfer from chain %d to chain %d with sequence number %d",
		sourceChain, destChain, fourthMessage.SequenceNumber,
	)

	// Ordered token transfer, but using different sender, should be executed
	fifthReceiver := utils.RandomAddress()
	fifthMessage, err := changeset.DoSendRequest(t, e, state,
		changeset.WithSender(anotherSender),
		changeset.WithSourceChain(sourceChain),
		changeset.WithDestChain(destChain),
		changeset.WithEvm2AnyMessage(router.ClientEVM2AnyMessage{
			Receiver:     common.LeftPadBytes(fifthReceiver.Bytes(), 32),
			Data:         nil,
			TokenAmounts: tokenTransfer,
			FeeToken:     common.HexToAddress("0x0"),
			ExtraArgs:    changeset.MakeEVMExtraArgsV2(0, false),
		}))
	require.NoError(t, err)
	expectedStatuses[fifthMessage.SequenceNumber] = changeset.EXECUTION_STATE_SUCCESS
	t.Logf("Ordered message send by %v from chain %d to chain %d with sequence number %d",
		anotherSender.From, sourceChain, destChain, fifthMessage.SequenceNumber,
	)

	// All messages are committed, even these which are going to be reverted during the exec
	_, err = changeset.ConfirmCommitWithExpectedSeqNumRange(
		t,
		e.Chains[sourceChain],
		e.Chains[destChain],
		state.Chains[destChain].OffRamp,
		startBlocks[destChain],
		ccipocr3.NewSeqNumRange(
			ccipocr3.SeqNum(firstMessage.SequenceNumber),
			ccipocr3.SeqNum(fifthMessage.SequenceNumber),
		),
		// We don't verify batching here, so we don't need all messages to be in a single root
		false,
	)
	require.NoError(t, err)

	execStates := changeset.ConfirmExecWithSeqNrsForAll(
		t,
		e,
		state,
		map[changeset.SourceDestPair][]uint64{
			identifier: {
				firstMessage.SequenceNumber,
				fourthMessage.SequenceNumber,
				fifthMessage.SequenceNumber,
			},
		},
		startBlocks,
	)
	require.Equal(t, expectedStatuses, execStates[identifier])

	secondMsgState, err := state.Chains[destChain].OffRamp.GetExecutionState(&bind.CallOpts{Context: ctx}, sourceChain, secondMsg.SequenceNumber)
	require.NoError(t, err)
	require.Equal(t, uint8(changeset.EXECUTION_STATE_UNTOUCHED), secondMsgState)

	thirdMsgState, err := state.Chains[destChain].OffRamp.GetExecutionState(&bind.CallOpts{Context: ctx}, sourceChain, thirdMessage.SequenceNumber)
	require.NoError(t, err)
	require.Equal(t, uint8(changeset.EXECUTION_STATE_UNTOUCHED), thirdMsgState)

	changeset.WaitForTheTokenBalance(ctx, t, destToken.Address(), firstReceiver, e.Chains[destChain], oneE18)
	changeset.WaitForTheTokenBalance(ctx, t, destUSDC.Address(), secondReceiver, e.Chains[destChain], big.NewInt(0))
	changeset.WaitForTheTokenBalance(ctx, t, destToken.Address(), thirdReceiver, e.Chains[destChain], big.NewInt(0))
	changeset.WaitForTheTokenBalance(ctx, t, destToken.Address(), fourthReceiver, e.Chains[destChain], oneE18)
	changeset.WaitForTheTokenBalance(ctx, t, destToken.Address(), fifthReceiver, e.Chains[destChain], oneE18)
}

func pickFirstAvailableUser(
	tenv changeset.DeployedEnv,
	sourceChain uint64,
	e deployment.Environment,
) (*bind.TransactOpts, error) {
	for _, user := range tenv.Users[sourceChain] {
		if user == nil {
			continue
		}
		if user.From != e.Chains[sourceChain].DeployerKey.From {
			return user, nil
		}
	}
	return nil, fmt.Errorf("user not found")
}
