package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"path/filepath"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/sei-protocol/sei-chain/utils"
	"github.com/sei-protocol/sei-chain/x/evm/state"
	evmtypes "github.com/sei-protocol/sei-chain/x/evm/types"
	"github.com/sei-protocol/sei-chain/x/evm/types/ethtx"
	abci "github.com/tendermint/tendermint/abci/types"
	tmtypes "github.com/tendermint/tendermint/types"
)

func Replay(a *App) {
	h := a.EvmKeeper.GetReplayedHeight(a.GetCheckCtx()) + 1
	initHeight := a.EvmKeeper.GetReplayInitialHeight(a.GetCheckCtx())
	if h == 1 {
		gendoc, err := tmtypes.GenesisDocFromFile(filepath.Join(DefaultNodeHome, "config/genesis.json"))
		if err != nil {
			panic(err)
		}
		_, err = a.InitChain(context.Background(), &abci.RequestInitChain{
			Time:          time.Now(),
			ChainId:       gendoc.ChainID,
			AppStateBytes: gendoc.AppState,
		})
		if err != nil {
			panic(err)
		}
		initHeight = a.EvmKeeper.GetReplayInitialHeight(a.GetContextForDeliverTx([]byte{}))
	} else {
		a.EvmKeeper.OpenEthDatabase()
	}
	for {
		latestBlock, err := a.EvmKeeper.EthClient.BlockNumber(context.Background())
		if err != nil {
			panic(err)
		}
		if latestBlock < uint64(h+initHeight) {
			a.Logger().Info(fmt.Sprintf("Latest block is %d. Sleeping for a minute", latestBlock))
			time.Sleep(1 * time.Minute)
			continue
		}
		a.Logger().Info(fmt.Sprintf("Replaying block height %d", h+initHeight))
		if h+initHeight >= 19426587 && evmtypes.DefaultChainConfig().CancunTime < 0 {
			a.Logger().Error("Reaching Cancun upgrade height. Turn on Cancun by setting CancunTime in x/evm/types/config.go:DefaultChainConfig() to 0")
			break
		} else if h+initHeight < 19426587 && evmtypes.DefaultChainConfig().CancunTime >= 0 {
			a.Logger().Error("Haven't reached Cancun upgrade height. Turn off Cancun by setting CancunTime in x/evm/types/config.go:DefaultChainConfig() to -1")
			break
		}
		b, err := a.EvmKeeper.EthClient.BlockByNumber(context.Background(), big.NewInt(h+initHeight))
		if err != nil {
			panic(err)
		}
		hash := make([]byte, 8)
		binary.BigEndian.PutUint64(hash, uint64(h))
		_, err = a.FinalizeBlock(context.Background(), &abci.RequestFinalizeBlock{
			Txs:               utils.Map(b.Txs, func(tx *ethtypes.Transaction) []byte { return encodeTx(tx, a.GetTxConfig()) }),
			DecidedLastCommit: abci.CommitInfo{Votes: []abci.VoteInfo{}},
			Height:            h,
			Hash:              hash,
			Time:              time.Now(),
		})
		if err != nil {
			panic(err)
		}
		ctx := a.GetContextForDeliverTx([]byte{})
		s := state.NewDBImpl(ctx, &a.EvmKeeper, false)
		for _, w := range b.Withdrawals() {
			amount := new(big.Int).SetUint64(w.Amount)
			amount = amount.Mul(amount, big.NewInt(params.GWei))
			s.AddBalance(w.Address, amount)
		}
		_, _ = s.Finalize()
		for _, tx := range b.Txs {
			a.Logger().Info(fmt.Sprintf("Verifying tx %s", tx.Hash().Hex()))
			if tx.To() != nil {
				a.EvmKeeper.VerifyBalance(ctx, *tx.To())
			}
			a.EvmKeeper.VerifyTxResult(ctx, tx.Hash())
		}
		_, err = a.Commit(context.Background())
		if err != nil {
			panic(err)
		}
		h++
	}
}

func encodeTx(tx *ethtypes.Transaction, txConfig client.TxConfig) []byte {
	var txData ethtx.TxData
	var err error
	switch tx.Type() {
	case ethtypes.LegacyTxType:
		txData, err = ethtx.NewLegacyTx(tx)
	case ethtypes.DynamicFeeTxType:
		txData, err = ethtx.NewDynamicFeeTx(tx)
	case ethtypes.AccessListTxType:
		txData, err = ethtx.NewAccessListTx(tx)
	case ethtypes.BlobTxType:
		txData, err = ethtx.NewBlobTx(tx)
	}
	if err != nil {
		panic(err)
	}
	msg, err := evmtypes.NewMsgEVMTransaction(txData)
	if err != nil {
		panic(err)
	}
	txBuilder := txConfig.NewTxBuilder()
	if err = txBuilder.SetMsgs(msg); err != nil {
		panic(err)
	}
	txbz, encodeErr := txConfig.TxEncoder()(txBuilder.GetTx())
	if encodeErr != nil {
		panic(encodeErr)
	}
	return txbz
}
