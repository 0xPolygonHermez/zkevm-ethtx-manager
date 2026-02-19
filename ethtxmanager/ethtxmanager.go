// Package ethtxmanager handles ethereum transactions:  It makes
// calls to send and to aggregate batch, checks possible errors, like wrong nonce or gas limit too low
// and make correct adjustments to request according to it. Also, it tracks transaction receipt and status
// of tx in case tx is rejected and send signals to sequencer/aggregator to resend sequence/batch
package ethtxmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	localCommon "github.com/0xPolygon/zkevm-ethtx-manager/common"
	"github.com/0xPolygon/zkevm-ethtx-manager/etherman"
	"github.com/0xPolygon/zkevm-ethtx-manager/ethtxmanager/sqlstorage"
	"github.com/0xPolygon/zkevm-ethtx-manager/log"
	"github.com/0xPolygon/zkevm-ethtx-manager/types"
	"github.com/0xPolygonHermez/zkevm-synchronizer-l1/synchronizer/l1_check_block"
	signertypes "github.com/agglayer/go_signer/signer/types"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	ethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/holiman/uint256"
)

const failureIntervalInSeconds = 5

var (
	// ErrNotFound it's returned
	ErrNotFound = types.ErrNotFound
	// ErrAlreadyExists when the object already exists
	ErrAlreadyExists = errors.New("already exists")

	// ErrExecutionReverted returned when trying to get the revert message
	// but the call fails without revealing the revert reason
	ErrExecutionReverted = errors.New("execution reverted")
)

// Client for eth tx manager
type Client struct {
	ctx    context.Context
	cancel context.CancelFunc

	cfg      Config
	etherman types.EthermanInterface
	storage  types.StorageInterface
	from     common.Address
}

type pending struct {
	Pending map[common.Address]map[uint64]l1Tx `json:"pending"`
}

type l1Tx struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Nonce    string `json:"nonce"`
	GasPrice string `json:"gasPrice"`
	Gas      string `json:"gas"`
	Value    string `json:"value"`
	Data     string `json:"input"`
}

// This var is for be able to test New function that require to create a Mock of Etherman
var ethTxManagerEthermanFactoryFunc = func(cfg etherman.Config,
	signersConfig []signertypes.SignerConfig) (types.EthermanInterface, error) {
	return etherman.NewClient(cfg, signersConfig)
}

// New creates new eth tx manager
func New(cfg Config) (*Client, error) {
	etherman, err := ethTxManagerEthermanFactoryFunc(cfg.Etherman, cfg.PrivateKeys)
	if err != nil {
		return nil, err
	}

	storage, err := createStorage(cfg.StoragePath)
	if err != nil {
		return nil, err
	}

	publicAddr, err := etherman.PublicAddress()
	if err != nil {
		return nil, fmt.Errorf("ethtxmanager error getting public address: %w", err)
	}
	if len(publicAddr) == 0 {
		return nil, fmt.Errorf("ethtxmanager error getting public address: no public address found")
	}

	client := Client{
		cfg:      cfg,
		etherman: etherman,
		storage:  storage,
		from:     publicAddr[0],
	}

	log.Init(cfg.Log)

	return &client, nil
}

// createStorage instantiates either SQL storage or in memory storage.
// In case dbPath parameter is a non-empty string, it creates SQL storage, otherwise in memory one.
func createStorage(dbPath string) (types.StorageInterface, error) {
	if dbPath == "" {
		// if the provided path is empty, use the in memory sql lite storage
		dbPath = ":memory:"
	}

	return sqlstorage.NewStorage(localCommon.SQLLiteDriverName, dbPath)
}

func pendingL1Txs(URL string, from common.Address, httpHeaders map[string]string) ([]types.MonitoredTx, error) {
	response, err := JSONRPCCall(URL, "txpool_content", httpHeaders)
	if err != nil {
		return nil, err
	}

	var L1Txs pending
	err = json.Unmarshal(response.Result, &L1Txs)
	if err != nil {
		return nil, err
	}

	mTxs := make([]types.MonitoredTx, 0, len(L1Txs.Pending[from]))
	for _, tx := range L1Txs.Pending[from] {
		if common.HexToAddress(tx.From) == from {
			to := common.HexToAddress(tx.To)
			nonce, ok := new(big.Int).SetString(tx.Nonce, 0)
			if !ok {
				return nil, fmt.Errorf("failed to convert nonce %v to big.Int", tx.Nonce)
			}

			value, ok := new(big.Int).SetString(tx.Value, 0)
			if !ok {
				return nil, fmt.Errorf("failed to convert value %v to big.Int", tx.Value)
			}

			gas, ok := new(big.Int).SetString(tx.Gas, 0)
			if !ok {
				return nil, fmt.Errorf("failed to convert gas %v to big.Int", tx.Gas)
			}

			gasPrice, ok := new(big.Int).SetString(tx.GasPrice, 0)
			if !ok {
				return nil, fmt.Errorf("failed to convert gasPrice %v to big.Int", tx.GasPrice)
			}

			data := common.Hex2Bytes(tx.Data)

			// TODO: handle case of blob transaction

			mTx := types.MonitoredTx{
				ID:       ethTypes.NewTx(&ethTypes.LegacyTx{To: &to, Nonce: nonce.Uint64(), Value: value, Data: data}).Hash(),
				From:     common.HexToAddress(tx.From),
				To:       &to,
				Nonce:    nonce.Uint64(),
				Value:    value,
				Data:     data,
				Gas:      gas.Uint64(),
				GasPrice: gasPrice,
				Status:   types.MonitoredTxStatusSent,
				History:  make(map[common.Hash]bool),
			}
			mTxs = append(mTxs, mTx)
		}
	}

	return mTxs, nil
}

// Add a transaction to be sent and monitored
func (c *Client) Add(ctx context.Context, to *common.Address, value *big.Int,
	data []byte, gasOffset uint64, sidecar *ethTypes.BlobTxSidecar) (common.Hash, error) {
	hash, err := c.add(ctx, to, value, data, gasOffset, sidecar, 0)
	return hash, translateError(err)
}

// AddWithGas adds a transaction to be sent and monitored with a defined gas to be used so it's not estimated
func (c *Client) AddWithGas(ctx context.Context, to *common.Address,
	value *big.Int, data []byte, gasOffset uint64, sidecar *ethTypes.BlobTxSidecar, gas uint64) (common.Hash, error) {
	hash, err := c.add(ctx, to, value, data, gasOffset, sidecar, gas)
	return hash, translateError(err)
}

func (c *Client) add(
	ctx context.Context,
	to *common.Address,
	value *big.Int,
	data []byte,
	gasOffset uint64,
	sidecar *ethTypes.BlobTxSidecar,
	gas uint64,
) (common.Hash, error) {
	var err error

	// get gas price
	gasPrice, err := c.suggestedGasPrice(ctx)
	if err != nil {
		err := fmt.Errorf("failed to get suggested gas price: %w", translateError(err))
		log.Errorf(err.Error())
		return common.Hash{}, err
	}

	var (
		blobFeeCap  *big.Int
		gasTipCap   *big.Int
		estimateGas bool
	)

	if gas == 0 {
		estimateGas = true
	}

	if sidecar != nil {
		// blob gas price estimation
		header, err := c.etherman.GetHeaderByNumber(ctx, nil)
		if err != nil {
			log.Errorf("failed to get header: %v", err)
			return common.Hash{}, err
		}
		parentNumber := new(big.Int).Sub(header.Number, big.NewInt(1))
		parentHeader, err := c.etherman.GetHeaderByNumber(ctx, parentNumber)
		if err != nil {
			log.Errorf("failed to get parent header: %v", err)
			return common.Hash{}, err
		}

		if parentHeader.ExcessBlobGas != nil && parentHeader.BlobGasUsed != nil {
			parentExcessBlobGas := eip4844.CalcExcessBlobGas(&params.ChainConfig{}, parentHeader, header.Time)
			blobFeeCap = eip4844.CalcBlobFee(&params.ChainConfig{}, parentHeader)
			if *header.ExcessBlobGas != parentExcessBlobGas {
				return common.Hash{}, fmt.Errorf("invalid excessBlobGas: have %d, want %d",
					*header.ExcessBlobGas, parentExcessBlobGas)
			}
		} else {
			log.Infof("legacy parent header no blob gas info")
			blobFeeCap = big.NewInt(params.BlobTxMinBlobGasprice)
		}

		gasTipCap, err = c.etherman.GetSuggestGasTipCap(ctx)
		if err != nil {
			log.Errorf("failed to get gas tip cap: %v", err)
			return common.Hash{}, err
		}

		// get gas
		if estimateGas {
			gas, err = c.etherman.EstimateGasBlobTx(ctx, c.from, to, gasPrice, gasTipCap, value, data)
			if err != nil {
				if de, ok := err.(rpc.DataError); ok {
					err = fmt.Errorf("%w (%v)", translateError(err), de.ErrorData())
				}
				err := fmt.Errorf("failed to estimate gas blob tx: %w, data: %v", translateError(err), common.Bytes2Hex(data))
				log.Error(err.Error())
				log.Debugf(
					"failed to estimate gas for blob tx: from: %v, to: %v, value: %v",
					c.from.String(),
					to.String(),
					value.String(),
				)
				return common.Hash{}, err
			}
		}

		// margin
		const multiplier = 10
		gasTipCap = gasTipCap.Mul(gasTipCap, big.NewInt(multiplier))
		gasPrice = gasPrice.Mul(gasPrice, big.NewInt(multiplier))
		blobFeeCap = blobFeeCap.Mul(blobFeeCap, big.NewInt(multiplier))
		gas = gas * 12 / 10 //nolint:mnd
	} else if estimateGas {
		// get gas
		gas, err = c.etherman.EstimateGas(ctx, c.from, to, value, data)
		if err != nil {
			if de, ok := err.(rpc.DataError); ok {
				err = fmt.Errorf("%w (%v)", translateError(err), de.ErrorData())
			}
			err := fmt.Errorf("failed to estimate gas: %w, data: %v", translateError(err), common.Bytes2Hex(data))
			log.Error(err.Error())
			log.Debugf(
				"failed to estimate gas for tx: from: %v, to: %v, value: %v",
				c.from.String(),
				to.String(),
				value.String(),
			)
			if c.cfg.ForcedGas > 0 {
				gas = c.cfg.ForcedGas
			} else {
				return common.Hash{}, err
			}
		}
	}

	// Calculate id
	var tx *ethTypes.Transaction
	if sidecar == nil {
		tx = ethTypes.NewTx(&ethTypes.LegacyTx{
			To:    to,
			Value: value,
			Data:  data,
		})
	} else {
		tx = ethTypes.NewTx(&ethTypes.BlobTx{
			To:         *to,
			Value:      uint256.MustFromBig(value),
			Data:       data,
			BlobHashes: sidecar.BlobHashes(),
			Sidecar:    sidecar,
		})
	}

	id := tx.Hash()

	// create monitored tx
	mTx := types.MonitoredTx{
		ID: id, From: c.from, To: to,
		Value: value, Data: data,
		Gas: gas, GasPrice: gasPrice, GasOffset: gasOffset,
		BlobSidecar:  sidecar,
		BlobGas:      tx.BlobGas(),
		BlobGasPrice: blobFeeCap, GasTipCap: gasTipCap,
		Status:      types.MonitoredTxStatusCreated,
		History:     make(map[common.Hash]bool),
		EstimateGas: estimateGas,
	}

	// add to storage
	err = c.storage.Add(ctx, mTx)
	if err != nil {
		if errors.Is(err, types.ErrAlreadyExists) {
			return id, ErrAlreadyExists
		}

		err := fmt.Errorf("failed to add tx to get monitored: %w", translateError(err))
		log.Errorf(err.Error())
		return common.Hash{}, err
	}

	mTxLog := log.WithFields("types.MonitoredTx", mTx.ID, "createdAt", mTx.CreatedAt)
	mTxLog.Infof("created")

	return id, nil
}

// Remove a transaction from the monitored txs
func (c *Client) Remove(ctx context.Context, id common.Hash) error {
	return translateError(c.storage.Remove(ctx, id))
}

// RemoveAll removes all the monitored txs
func (c *Client) RemoveAll(ctx context.Context) error {
	return translateError(c.storage.Empty(ctx))
}

// ResultsByStatus returns all the results for all the monitored txs matching the provided statuses
// if the statuses are empty, all the statuses are considered.
func (c *Client) ResultsByStatus(ctx context.Context,
	statuses []types.MonitoredTxStatus) ([]types.MonitoredTxResult, error) {
	mTxs, err := c.storage.GetByStatus(ctx, statuses)
	if err != nil {
		return nil, translateError(err)
	}

	results := make([]types.MonitoredTxResult, 0, len(mTxs))

	for _, mTx := range mTxs {
		result, err := c.buildResult(ctx, mTx)
		if err != nil {
			return nil, translateError(err)
		}
		results = append(results, result)
	}

	return results, nil
}

// Result returns the current result of the transaction execution with all the details
// if not found returns ErrNotFound
func (c *Client) Result(ctx context.Context, id common.Hash) (types.MonitoredTxResult, error) {
	mTx, err := c.storage.Get(ctx, id)
	if err != nil {
		return types.MonitoredTxResult{}, translateError(err)
	}

	res, err := c.buildResult(ctx, mTx)
	return res, translateError(err)
}

// setStatusSafe sets the status of a monitored tx to types.MonitoredTxStatusSafe.
func (c *Client) setStatusSafe(ctx context.Context, id common.Hash) error {
	mTx, err := c.storage.Get(ctx, id)
	if err != nil {
		return err
	}
	mTx.Status = types.MonitoredTxStatusSafe
	return c.storage.Update(ctx, mTx)
}

func (c *Client) buildResult(ctx context.Context, mTx types.MonitoredTx) (types.MonitoredTxResult, error) {
	history := mTx.HistoryHashSlice()
	txs := make(map[common.Hash]types.TxResult, len(history))

	// Skip blockchain calls for evicted transactions - they were never successfully sent
	// For evicted transactions, txs map remains empty
	if mTx.Status != types.MonitoredTxStatusEvicted {
		for _, txHash := range history {
			tx, _, err := c.etherman.GetTx(ctx, txHash)
			if !errors.Is(err, ethereum.NotFound) && err != nil {
				return types.MonitoredTxResult{}, err
			}

			receipt, err := c.etherman.GetTxReceipt(ctx, txHash)
			if !errors.Is(err, ethereum.NotFound) && err != nil {
				return types.MonitoredTxResult{}, err
			}

			revertMessage, err := c.etherman.GetRevertMessage(ctx, tx)
			if !errors.Is(err, ethereum.NotFound) && err != nil && err.Error() != ErrExecutionReverted.Error() {
				return types.MonitoredTxResult{}, err
			}

			txs[txHash] = types.TxResult{
				Tx:            tx,
				Receipt:       receipt,
				RevertMessage: revertMessage,
			}
		}
	}

	result := types.MonitoredTxResult{
		ID:                 mTx.ID,
		To:                 mTx.To,
		Nonce:              mTx.Nonce,
		Value:              mTx.Value,
		Data:               mTx.Data,
		MinedAtBlockNumber: mTx.BlockNumber,
		Status:             mTx.Status,
		Txs:                txs,
	}

	return result, nil
}

// Start will start the tx management, reading txs from storage,
// send then to the blockchain and keep monitoring them until they
// get mined
func (c *Client) Start() {
	// If no persistence file is uses check L1 for pending txs
	if c.cfg.StoragePath == "" && c.cfg.ReadPendingL1Txs {
		pendingTxs, err := pendingL1Txs(c.cfg.Etherman.URL, c.from, c.cfg.Etherman.HTTPHeaders)
		if err != nil {
			log.Errorf("failed to get pending txs from L1: %v", err)
		}

		log.Infof("%d L1 pending Txs found", len(pendingTxs))

		for _, mTx := range pendingTxs {
			err := c.storage.Add(context.Background(), mTx)
			if err != nil {
				log.Errorf("failed to add pending tx to storage: %v", err)
			}
		}
	}

	// infinite loop to manage txs as they arrive
	c.ctx, c.cancel = context.WithCancel(context.Background())

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(c.cfg.FrequencyToMonitorTxs.Duration):
			err := c.monitorTxs(context.Background())
			if err != nil {
				c.logErrorAndWait("failed to monitor txs: %v", err)
			}
			err = c.waitMinedTxToBeSafe(context.Background())
			if err != nil {
				c.logErrorAndWait("failed to wait safe tx to be finalized: %v", err)
			}
			err = c.waitSafeTxToBeFinalized(context.Background())
			if err != nil {
				c.logErrorAndWait("failed to wait safe tx to be finalized: %v", err)
			}
		}
	}
}

// Stop stops the monitored tx management
func (c *Client) Stop() {
	c.cancel()
}

// monitorTxs processes all pending monitored txs
func (c *Client) monitorTxs(ctx context.Context) error {
	iterations, err := c.getMonitoredTxnIteration(ctx)
	if err != nil {
		return fmt.Errorf("failed to get monitored txs: %w", translateError(err))
	}

	log.Debugf("found %v monitored tx to process", len(iterations))

	wg := sync.WaitGroup{}
	wg.Add(len(iterations))
	for _, mTx := range iterations {
		mTx := mTx // force variable shadowing to avoid pointer conflicts
		go func(c *Client, mTx *monitoredTxnIteration) {
			mTxLogger := createMonitoredTxLogger(*mTx.MonitoredTx)
			defer func(mTxLogger *log.Logger) {
				if err := recover(); err != nil {
					mTxLogger.Errorf("monitoring recovered from this err: %v", err)
				}
				wg.Done()
			}(mTxLogger)
			c.monitorTx(ctx, mTx, mTxLogger)
		}(c, mTx)
	}
	wg.Wait()

	return nil
}

// waitMinedTxToBeSafe checks all mined monitored txs and wait to set the tx as safe
func (c *Client) waitMinedTxToBeSafe(ctx context.Context) error {
	statusesFilter := []types.MonitoredTxStatus{types.MonitoredTxStatusMined}
	mTxs, err := c.storage.GetByStatus(ctx, statusesFilter)
	if err != nil {
		return fmt.Errorf("failed to get mined monitored txs: %w", translateError(err))
	}

	log.Debugf("found %v mined monitored tx to process", len(mTxs))

	var safeBlockNumber uint64
	if c.cfg.SafeStatusL1NumberOfBlocks > 0 {
		// Overwrite the number of blocks to consider a tx as safe
		currentBlockNumber, err := c.etherman.GetLatestBlockNumber(ctx)
		if err != nil {
			return fmt.Errorf("failed to get latest block number: %w", translateError(err))
		}

		safeBlockNumber = currentBlockNumber - c.cfg.SafeStatusL1NumberOfBlocks
	} else {
		// Get Safe block Number
		safeBlockNumber, err = l1_check_block.L1SafeFetch.BlockNumber(ctx, c.etherman)
		if err != nil {
			return fmt.Errorf("failed to get safe block number: %v", err)
		}
	}

	for _, mTx := range mTxs {
		if mTx.BlockNumber.Uint64() <= safeBlockNumber {
			mTxLogger := createMonitoredTxLogger(mTx)
			mTxLogger.Infof("safe")
			mTx.Status = types.MonitoredTxStatusSafe
			err := c.storage.Update(ctx, mTx)
			if err != nil {
				return fmt.Errorf("failed to update mined monitored tx: %w", translateError(err))
			}
		}
	}

	return nil
}

// waitSafeTxToBeFinalized checks all safe monitored txs and wait the number of
// l1 blocks configured to finalize the tx
func (c *Client) waitSafeTxToBeFinalized(ctx context.Context) error {
	statusesFilter := []types.MonitoredTxStatus{types.MonitoredTxStatusSafe}
	mTxs, err := c.storage.GetByStatus(ctx, statusesFilter)
	if err != nil {
		return fmt.Errorf("failed to get safe monitored txs: %w", translateError(err))
	}

	log.Debugf("found %v safe monitored tx to process", len(mTxs))

	var finaLizedBlockNumber uint64
	if c.cfg.SafeStatusL1NumberOfBlocks > 0 {
		// Overwrite the number of blocks to consider a tx as finalized
		currentBlockNumber, err := c.etherman.GetLatestBlockNumber(ctx)
		if err != nil {
			return fmt.Errorf("failed to get latest block number: %w", translateError(err))
		}

		finaLizedBlockNumber = currentBlockNumber - c.cfg.FinalizedStatusL1NumberOfBlocks
	} else {
		// Get Network Default value
		finaLizedBlockNumber, err = l1_check_block.L1FinalizedFetch.BlockNumber(ctx, c.etherman)
		if err != nil {
			return fmt.Errorf("failed to get finalized block number: %w", translateError(err))
		}
	}

	for _, mTx := range mTxs {
		if mTx.BlockNumber.Uint64() <= finaLizedBlockNumber {
			mTxLogger := createMonitoredTxLogger(mTx)
			mTxLogger.Infof("finalized")
			mTx.Status = types.MonitoredTxStatusFinalized
			err := c.storage.Update(ctx, mTx)
			if err != nil {
				return fmt.Errorf("failed to update safe monitored tx: %w", translateError(err))
			}
		}
	}

	return nil
}

func curlCommandForTx(signedTx *ethTypes.Transaction) string {
	data, err := signedTx.MarshalBinary()
	if err != nil {
		return "err: fails signedTx.MarshalBinary: " + err.Error()
	}
	return fmt.Sprintf(`curl -X POST --data '{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["%s"],"id":1}'
		 -H "Content-Type: application/json" <YOUR_ETHEREUM_NODE_URL>`,
		hexutil.Encode(data))
}

// monitorTx does all the monitoring steps to the monitored tx
func (c *Client) monitorTx(ctx context.Context, mTx *monitoredTxnIteration, logger *log.Logger) {
	var err error
	logger.Info("processing")

	// Check if max retries is configured and if this transaction has exceeded the limit
	if c.cfg.EstimateGasMaxRetries > 0 && mTx.RetryCount >= c.cfg.EstimateGasMaxRetries {
		logger.Debugf("transaction exceeded max retries (%d), evicting from tx manager", c.cfg.EstimateGasMaxRetries)
		mTx.Status = types.MonitoredTxStatusEvicted
		err = c.storage.Update(ctx, *mTx.MonitoredTx)
		if err != nil {
			logger.Errorf("failed to update monitored tx to evicted status: %v", err)
		}
		return
	}

	var signedTx *ethTypes.Transaction
	if !mTx.confirmed {
		// review tx and increase gas and gas price if needed
		if mTx.Status == types.MonitoredTxStatusSent {
			err := c.reviewMonitoredTxGas(ctx, mTx, logger)
			if err != nil {
				logger.Errorf("failed to review monitored tx: %v", err)
				// Increment retry count when gas review fails
				mTx.RetryCount++
				logger.Debugf("incremented retry count to %d after gas review failure", mTx.RetryCount)
				updateErr := c.storage.Update(ctx, *mTx.MonitoredTx)
				if updateErr != nil {
					logger.Errorf("failed to update retry count after gas review failure: %v", updateErr)
				}
				return
			}
		}

		// rebuild transaction
		tx := mTx.Tx()
		logger.Debugf("unsigned tx %v created", tx.Hash().String())

		// sign tx
		signedTx, err = c.etherman.SignTx(ctx, mTx.From, tx)
		if err != nil {
			logger.Errorf("failed to sign tx %v: %v", tx.Hash().String(), err)
			return
		}
		logger.Debugf("signed tx %v created", signedTx.Hash().String())

		// add tx to monitored tx history
		found, err := mTx.AddHistory(signedTx)
		if found {
			logger.Infof("signed tx already existed in the history")
		} else if err != nil {
			logger.Errorf("failed to add signed tx %v to monitored tx history: %v", signedTx.Hash().String(), err)
			return
		} else {
			// update monitored tx changes into storage
			err = c.storage.Update(ctx, *mTx.MonitoredTx)
			if err != nil {
				logger.Errorf("failed to update monitored tx: %v", err)
				return
			}
			logger.Debugf("signed tx added to the monitored tx history")
		}
		logger.Debugf("Sending Tx: %s", curlCommandForTx(signedTx))
		// check if the tx is already in the network, if not, send it
		_, _, err = c.etherman.GetTx(ctx, signedTx.Hash())
		// if not found, send it tx to the network
		if errors.Is(err, ethereum.NotFound) {
			logger.Debugf("signed tx not found in the network")
			err := c.etherman.SendTx(ctx, signedTx)
			if err != nil {
				logger.Warnf("failed to send tx %v to network: %v", signedTx.Hash().String(), err)
				// Add a warning with a curl command to send the transaction manually
				logger.Warnf(`To manually send the transaction, use the following curl command:
						%s"`, curlCommandForTx(signedTx))

				// Increment retry count when sending fails
				mTx.RetryCount++
				logger.Debugf("incremented retry count to %d after send failure", mTx.RetryCount)
				err = c.storage.Update(ctx, *mTx.MonitoredTx)
				if err != nil {
					logger.Errorf("failed to update retry count after send failure: %v", err)
				}
				return
			}
			logger.Infof("signed tx sent to the network: %v", signedTx.Hash().String())
			if mTx.Status == types.MonitoredTxStatusCreated {
				// update tx status to sent
				mTx.Status = types.MonitoredTxStatusSent
				logger.Debugf("status changed to %v", string(mTx.Status))
				// update monitored tx changes into storage
				err = c.storage.Update(ctx, *mTx.MonitoredTx)
				if err != nil {
					logger.Errorf("failed to update monitored tx changes: %v", err)
					return
				}
			}
		} else {
			logger.Warnf("signed tx already found in the network")
		}

		log.Infof("waiting signedTx to be mined...")

		// wait tx to get mined
		confirmed, err := c.etherman.WaitTxToBeMined(ctx, signedTx, c.cfg.WaitTxToBeMined.Duration)
		if err != nil {
			logger.Warnf("failed to wait tx to be mined: %v", err)
			return
		}
		if !confirmed {
			log.Warnf("signedTx not mined yet and timeout has been reached")
			return
		}

		var txReceipt *ethTypes.Receipt
		waitingReceiptTimeout := time.Now().Add(c.cfg.GetReceiptMaxTime.Duration)
		// get tx receipt
		for {
			txReceipt, err = c.etherman.GetTxReceipt(ctx, signedTx.Hash())
			if err != nil {
				if waitingReceiptTimeout.After(time.Now()) {
					time.Sleep(c.cfg.GetReceiptWaitInterval.Duration)
				} else {
					logger.Warnf(
						"failed to get tx receipt for tx %v after %v: %v",
						signedTx.Hash().String(),
						c.cfg.GetReceiptMaxTime,
						err,
					)
					return
				}
			} else {
				break
			}
		}

		mTx.lastReceipt = txReceipt
		mTx.confirmed = confirmed
	}

	// if mined, check receipt and mark as Failed or Confirmed
	if mTx.lastReceipt.Status == ethTypes.ReceiptStatusSuccessful {
		mTx.Status = types.MonitoredTxStatusMined
		mTx.BlockNumber = mTx.lastReceipt.BlockNumber
		logger.Info("mined")
	} else {
		// if we should continue to monitor, we move to the next one and this will
		// be reviewed in the next monitoring cycle
		if c.shouldContinueToMonitorThisTx(ctx, mTx.lastReceipt) {
			return
		}
		// otherwise we understand this monitored tx has failed
		mTx.Status = types.MonitoredTxStatusFailed
		mTx.BlockNumber = mTx.lastReceipt.BlockNumber
		logger.Info("failed")
	}

	// update monitored tx changes into storage
	err = c.storage.Update(ctx, *mTx.MonitoredTx)
	if err != nil {
		logger.Errorf("failed to update monitored tx: %v", err)
		return
	}
}

// shouldContinueToMonitorThisTx checks the the tx receipt and decides if it should
// continue or not to monitor the monitored tx related to the tx from this receipt
func (c *Client) shouldContinueToMonitorThisTx(ctx context.Context, receipt *ethTypes.Receipt) bool {
	// if the receipt has a is successful result, stop monitoring
	if receipt.Status == ethTypes.ReceiptStatusSuccessful {
		return false
	}

	tx, _, err := c.etherman.GetTx(ctx, receipt.TxHash)
	if err != nil {
		log.Errorf("failed to get tx when monitored tx identified as failed, tx : %v", receipt.TxHash.String(), err)
		return false
	}
	_, err = c.etherman.GetRevertMessage(ctx, tx)
	if err != nil {
		// if the error when getting the revert message is not identified, continue to monitor
		if err.Error() == ErrExecutionReverted.Error() {
			return true
		} else {
			log.Errorf(
				"failed to get revert message for monitored tx identified as failed, tx %v: %v",
				receipt.TxHash.String(),
				err,
			)
		}
	}
	// if nothing weird was found, stop monitoring
	return false
}

// reviewMonitoredTxGas checks if gas fields needs to be updated
// accordingly to the current information stored and the current
// state of the blockchain
func (c *Client) reviewMonitoredTxGas(ctx context.Context, mTx *monitoredTxnIteration, mTxLogger *log.Logger) error {
	mTxLogger.Debug("reviewing")
	isBlobTx := mTx.BlobSidecar != nil
	var (
		err error
		gas uint64
	)

	// get gas price
	gasPrice, err := c.suggestedGasPrice(ctx)
	if err != nil {
		err := fmt.Errorf("failed to get suggested gas price: %w", translateError(err))
		mTxLogger.Errorf(err.Error())
		return err
	}

	// check gas price
	if gasPrice.Cmp(mTx.GasPrice) == 1 {
		mTxLogger.Infof(
			"monitored tx (blob? %t) GasPrice updated from %v to %v",
			isBlobTx,
			mTx.GasPrice.String(),
			gasPrice.String(),
		)
		mTx.GasPrice = gasPrice
	}

	// get gas
	if !mTx.EstimateGas {
		mTxLogger.Info("tx is using a hardcoded gas, avoiding estimate gas")
		return nil
	}
	if mTx.BlobSidecar != nil {
		// blob gas price estimation
		header, err := c.etherman.GetHeaderByNumber(ctx, nil)
		if err != nil {
			log.Errorf("failed to get header: %v", err)
			return err
		}
		parentNumber := new(big.Int).Sub(header.Number, big.NewInt(1))
		parentHeader, err := c.etherman.GetHeaderByNumber(ctx, parentNumber)
		if err != nil {
			log.Errorf("failed to get parent header: %v", err)
			return err
		}

		var blobFeeCap *big.Int
		if parentHeader.ExcessBlobGas != nil && parentHeader.BlobGasUsed != nil {
			parentExcessBlobGas := eip4844.CalcExcessBlobGas(&params.ChainConfig{}, parentHeader, header.Time)
			blobFeeCap = eip4844.CalcBlobFee(&params.ChainConfig{}, parentHeader)
			if *header.ExcessBlobGas != parentExcessBlobGas {
				return fmt.Errorf("invalid excessBlobGas: have %d, want %d", *header.ExcessBlobGas, parentExcessBlobGas)
			}
		} else {
			log.Infof("legacy parent header no blob gas info")
			blobFeeCap = big.NewInt(params.BlobTxMinBlobGasprice)
		}

		gasTipCap, err := c.etherman.GetSuggestGasTipCap(ctx)
		if err != nil {
			log.Errorf("failed to get gas tip cap: %v", err)
			return err
		}

		if gasTipCap.Cmp(mTx.GasTipCap) == 1 {
			mTxLogger.Infof("monitored tx (blob? %t) GasTipCap updated from %v to %v", isBlobTx, mTx.GasTipCap, gasTipCap)
			mTx.GasTipCap = gasTipCap
		}
		if blobFeeCap.Cmp(mTx.BlobGasPrice) == 1 {
			mTxLogger.Infof("monitored tx (blob? %t) BlobFeeCap updated from %v to %v", isBlobTx, mTx.BlobGasPrice, blobFeeCap)
			mTx.BlobGasPrice = blobFeeCap
		}

		gas, err = c.etherman.EstimateGasBlobTx(ctx, mTx.From, mTx.To, mTx.GasPrice, mTx.GasTipCap, mTx.Value, mTx.Data)
		if err != nil {
			if de, ok := err.(rpc.DataError); ok {
				err = fmt.Errorf("%w (%v)", translateError(err), de.ErrorData())
			}
			err := fmt.Errorf("failed to estimate gas blob tx: %w", translateError(err))
			mTxLogger.Errorf(err.Error())
			return err
		}
	} else {
		gas, err = c.etherman.EstimateGas(ctx, mTx.From, mTx.To, mTx.Value, mTx.Data)
		if err != nil {
			if de, ok := err.(rpc.DataError); ok {
				err = fmt.Errorf("%w (%v)", err, de.ErrorData())
			}
			err := fmt.Errorf("failed to estimate gas: %w", translateError(err))
			mTxLogger.Errorf(err.Error())
			return err
		}
	}

	// check gas
	if gas > mTx.Gas {
		mTxLogger.Infof("monitored tx (blob? %t) Gas updated from %v to %v", isBlobTx, mTx.Gas, gas)
		mTx.Gas = gas
	}

	err = c.storage.Update(ctx, *mTx.MonitoredTx)
	if err != nil {
		return fmt.Errorf("failed to update monitored tx changes: %w", err)
	}

	return nil
}

// getMonitoredTxnIteration gets all monitored txs that need to be sent or resent in current monitor iteration
func (c *Client) getMonitoredTxnIteration(ctx context.Context) ([]*monitoredTxnIteration, error) {
	txsToUpdate, err := c.storage.GetByStatus(ctx,
		[]types.MonitoredTxStatus{types.MonitoredTxStatusCreated, types.MonitoredTxStatusSent})
	if err != nil {
		return nil, fmt.Errorf("failed to get txs to update nonces: %w", translateError(err))
	}

	iterations := make([]*monitoredTxnIteration, 0, len(txsToUpdate))
	senderNonces := make(map[common.Address]uint64)

	for _, tx := range txsToUpdate {
		tx := tx

		iteration := &monitoredTxnIteration{MonitoredTx: &tx}
		iterations = append(iterations, iteration)

		updateNonce := iteration.shouldUpdateNonce(ctx, c.etherman)
		if !updateNonce {
			continue
		}

		nonce, ok := senderNonces[tx.From]
		if !ok {
			// if there are no pending txs, we get the pending nonce from the etherman
			nonce, err = c.etherman.PendingNonce(ctx, tx.From)
			if err != nil {
				return nil, fmt.Errorf("failed to get pending nonce for sender: %s. Error: %w", tx.From, err)
			}

			senderNonces[tx.From] = nonce
		}

		iteration.Nonce = nonce
		err = c.storage.Update(ctx, tx)
		if err != nil {
			return nil, fmt.Errorf("failed to update nonce for tx %v: %w", tx.ID.String(), translateError(err))
		}

		senderNonces[tx.From]++
	}

	return iterations, nil
}

func (c *Client) suggestedGasPrice(ctx context.Context) (*big.Int, error) {
	// get gas price
	gasPrice, err := c.etherman.SuggestedGasPrice(ctx)
	if err != nil {
		return nil, err
	}

	// adjust the gas price by the margin factor
	marginFactor := big.NewFloat(0).SetFloat64(c.cfg.GasPriceMarginFactor)
	fGasPrice := big.NewFloat(0).SetInt(gasPrice)
	adjustedGasPrice, _ := big.NewFloat(0).Mul(fGasPrice, marginFactor).Int(big.NewInt(0))

	// if there is a max gas price limit configured and the current
	// adjusted gas price is over this limit, set the gas price as the limit
	if c.cfg.MaxGasPriceLimit > 0 {
		maxGasPrice := big.NewInt(0).SetUint64(c.cfg.MaxGasPriceLimit)
		if adjustedGasPrice.Cmp(maxGasPrice) == 1 {
			adjustedGasPrice.Set(maxGasPrice)
		}
	}

	return adjustedGasPrice, nil
}

// logErrorAndWait used when an error is detected before trying again
func (c *Client) logErrorAndWait(msg string, err error) {
	log.Errorf(msg, err)
	time.Sleep(failureIntervalInSeconds * time.Second)
}

// ResultHandler used by the caller to handle results
// when processing monitored txs
type ResultHandler func(types.MonitoredTxResult)

// ProcessPendingMonitoredTxs will check all monitored txs
// and wait until all of them are either confirmed or failed before continuing
//
// for the confirmed and failed ones, the resultHandler will be triggered
func (c *Client) ProcessPendingMonitoredTxs(ctx context.Context, resultHandler ResultHandler) {
	statusesFilter := []types.MonitoredTxStatus{
		types.MonitoredTxStatusCreated,
		types.MonitoredTxStatusSent,
		types.MonitoredTxStatusFailed,
		types.MonitoredTxStatusMined,
		types.MonitoredTxStatusEvicted,
	}
	// keep running until there are pending monitored txs
	for {
		results, err := c.ResultsByStatus(ctx, statusesFilter)
		if err != nil {
			// if something goes wrong here, we log, wait a bit and keep it in the infinite loop to not unlock the caller.
			log.Errorf("failed to get results by statuses from eth tx manager to monitored txs err: ", err)
			time.Sleep(time.Second)
			continue
		}

		if len(results) == 0 {
			// if there are not pending monitored txs, stop
			return
		}

		for _, result := range results {
			mTxResultLogger := CreateMonitoredTxResultLogger(result)

			// if the result is confirmed, we set it as done do stop looking into this monitored tx
			if result.Status == types.MonitoredTxStatusMined {
				err := c.setStatusSafe(ctx, result.ID)
				if err != nil {
					mTxResultLogger.Errorf("failed to set monitored tx as safe, err: %v", err)
					// if something goes wrong at this point, we skip this result and move to the next.
					// this result is going to be handled again in the next cycle by the outer loop.
					continue
				} else {
					mTxResultLogger.Info("monitored tx safe")
				}
				resultHandler(result)
				continue
			}

			// if the result is failed, we need to go around it and rebuild a batch verification
			if result.Status == types.MonitoredTxStatusFailed {
				resultHandler(result)
				continue
			}

			// if the result is evicted, it exceeded max retries - notify caller
			if result.Status == types.MonitoredTxStatusEvicted {
				resultHandler(result)
				continue
			}

			// if the result is either not confirmed or failed, it means we need to wait until it gets confirmed of failed.
			for {
				// wait before refreshing the result info
				time.Sleep(time.Second)

				// refresh the result info
				result, err := c.Result(ctx, result.ID)
				if err != nil {
					mTxResultLogger.Errorf("failed to get monitored tx result, err: %v", err)
					continue
				}

				// if the result status is mined, safe, finalized, failed or evicted, breaks the wait loop
				if result.Status == types.MonitoredTxStatusMined ||
					result.Status == types.MonitoredTxStatusSafe ||
					result.Status == types.MonitoredTxStatusFinalized ||
					result.Status == types.MonitoredTxStatusFailed ||
					result.Status == types.MonitoredTxStatusEvicted {
					break
				}

				mTxResultLogger.Infof("waiting for monitored tx to get confirmed, status: %v", result.Status.String())
			}
		}
	}
}

// EncodeBlobData encodes data into blob data type
func (c *Client) EncodeBlobData(data []byte) (kzg4844.Blob, error) {
	dataLen := len(data)
	if dataLen > params.BlobTxFieldElementsPerBlob*(params.BlobTxBytesPerFieldElement-1) {
		log.Infof(
			"blob data longer than allowed (length: %v, limit: %v)",
			dataLen,
			params.BlobTxFieldElementsPerBlob*(params.BlobTxBytesPerFieldElement-1),
		)
		return kzg4844.Blob{}, errors.New("blob data longer than allowed")
	}

	// 1 Blob = 4096 Field elements x 32 bytes/field element = 128 KB
	elemSize := params.BlobTxBytesPerFieldElement

	blob := kzg4844.Blob{}
	fieldIndex := -1
	for i := 0; i < len(data); i += (elemSize - 1) {
		fieldIndex++
		if fieldIndex == params.BlobTxFieldElementsPerBlob {
			break
		}
		maxIndex := i + (elemSize - 1)
		if maxIndex > len(data) {
			maxIndex = len(data)
		}
		copy(blob[fieldIndex*elemSize+1:], data[i:maxIndex])
	}
	return blob, nil
}

// MakeBlobSidecar constructs a blob tx sidecar
func (c *Client) MakeBlobSidecar(blobs []kzg4844.Blob) *ethTypes.BlobTxSidecar {
	commitments := make([]kzg4844.Commitment, 0, len(blobs))
	proofs := make([]kzg4844.Proof, 0, len(blobs))

	for _, blob := range blobs {
		// avoid memory aliasing
		auxBlob := blob
		c, _ := kzg4844.BlobToCommitment(&auxBlob)
		p, _ := kzg4844.ComputeBlobProof(&auxBlob, c)

		commitments = append(commitments, c)
		proofs = append(proofs, p)
	}

	return &ethTypes.BlobTxSidecar{
		Blobs:       blobs,
		Commitments: commitments,
		Proofs:      proofs,
	}
}

// From returns the sender (from) address associated with the client
func (c *Client) From() common.Address {
	return c.from
}

// createMonitoredTxLogger creates an instance of logger with all the important
// fields already set for a types.MonitoredTx
func createMonitoredTxLogger(mTx types.MonitoredTx) *log.Logger {
	return log.WithFields(
		"monitoredTxId", mTx.ID,
		"createdAt", mTx.CreatedAt,
		"from", mTx.From,
		"to", mTx.To,
	)
}

// CreateLogger creates an instance of logger with all the important
// fields already set for a types.MonitoredTx without requiring an instance of
// types.MonitoredTx, this should be use in for callers before calling the ADD
// method
func CreateLogger(monitoredTxId common.Hash, from common.Address, to *common.Address) *log.Logger {
	return log.WithFields(
		"monitoredTxId", monitoredTxId.String(),
		"from", from,
		"to", to,
	)
}

// CreateMonitoredTxResultLogger creates an instance of logger with all the important
// fields already set for a types.MonitoredTxResult
func CreateMonitoredTxResultLogger(mTxResult types.MonitoredTxResult) *log.Logger {
	return log.WithFields(
		"monitoredTxId", mTxResult.ID.String(),
	)
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	// If the error text is "not found" we return ErrNotFound
	if err.Error() == ethereum.NotFound.Error() {
		return ErrNotFound
	}
	// This is redundant, but just in case somebody change the error text
	if err.Error() == types.ErrNotFound.Error() {
		return ErrNotFound
	}
	return err
}
