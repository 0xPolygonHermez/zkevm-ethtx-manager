package types

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var (
	// ErrNotFound when the object is not found
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists when the object already exists
	ErrAlreadyExists = errors.New("already exists")
)

// EthermanInterface defines a set of methods for interacting with the Ethereum blockchain,
// including transaction management, gas estimation, signing, and retrieving blockchain information.
type EthermanInterface interface {
	// GetTx retrieves a transaction by its hash from the blockchain.
	// Returns the transaction, a boolean indicating if it exists in the pending pool, and an error if any.
	GetTx(ctx context.Context, txHash common.Hash) (*types.Transaction, bool, error)

	// GetTxReceipt retrieves the receipt of a transaction using its hash.
	// Returns the transaction receipt and an error if the receipt is not found or retrieval fails.
	GetTxReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)

	// WaitTxToBeMined waits for a transaction to be mined or until the provided timeout expires.
	// Returns true if the transaction was mined and an error if the transaction fails or times out.
	WaitTxToBeMined(ctx context.Context, tx *types.Transaction, timeout time.Duration) (bool, error)

	// SendTx broadcasts a signed transaction to the Ethereum network.
	// Returns an error if the transaction cannot be sent.
	SendTx(ctx context.Context, tx *types.Transaction) error

	// CurrentNonce retrieves the current nonce of a specific account
	// from the latest block (used for non-pending transactions).
	// Returns the nonce and an error if the nonce cannot be retrieved.
	CurrentNonce(ctx context.Context, account common.Address) (uint64, error)

	// PendingNonce retrieves the pending nonce of a specific account (used for pending transactions).
	// Returns the nonce and an error if the nonce cannot be retrieved.
	PendingNonce(ctx context.Context, account common.Address) (uint64, error)

	// SuggestedGasPrice retrieves the currently suggested gas price from the Ethereum network.
	// Returns the suggested gas price in wei and an error if the gas price cannot be retrieved.
	SuggestedGasPrice(ctx context.Context) (*big.Int, error)

	// EstimateGas estimates the amount of gas required to execute a transaction between 'from' and 'to'.
	// Takes the sender and recipient addresses, the value being sent, and the transaction data.
	// Returns the estimated gas amount and an error if the estimation fails.
	EstimateGas(ctx context.Context, from common.Address, to *common.Address, value *big.Int, data []byte) (uint64, error)

	// EstimateGasBlobTx estimates the amount of gas required to execute a Blob transaction
	// (with extra fields such as gasFeeCap and gasTipCap).
	// Takes the sender and recipient addresses, the gas fee cap, gas tip cap, value, and transaction data.
	// Returns the estimated gas and an error if the estimation fails.
	EstimateGasBlobTx(
		ctx context.Context,
		from common.Address,
		to *common.Address,
		gasFeeCap *big.Int,
		gasTipCap *big.Int,
		value *big.Int,
		data []byte,
	) (uint64, error)

	// CheckTxWasMined checks whether a transaction with the given hash was mined.
	// Returns true if the transaction was mined, along with the receipt and an error if any.
	CheckTxWasMined(ctx context.Context, txHash common.Hash) (bool, *types.Receipt, error)

	// SignTx signs a transaction using the private key of the sender.
	// Returns the signed transaction and an error if the signing fails.
	SignTx(ctx context.Context, sender common.Address, tx *types.Transaction) (*types.Transaction, error)

	// GetRevertMessage retrieves the revert reason for a failed transaction.
	// Returns the revert message string and an error if the revert reason cannot be retrieved.
	GetRevertMessage(ctx context.Context, tx *types.Transaction) (string, error)

	// GetLatestBlockNumber retrieves the number of the latest block in the blockchain.
	// Returns the block number and an error if it cannot be retrieved.
	GetLatestBlockNumber(ctx context.Context) (uint64, error)

	// GetHeaderByNumber retrieves the block header for a specific block number.
	// If the block number is nil, it retrieves the latest block header.
	// Returns the block header and an error if it cannot be retrieved.
	GetHeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)

	// GetSuggestGasTipCap retrieves the currently suggested gas tip cap from the Ethereum network.
	// Returns the gas tip cap and an error if it cannot be retrieved.
	GetSuggestGasTipCap(ctx context.Context) (*big.Int, error)

	// HeaderByNumber is an alias for GetHeaderByNumber. It retrieves the block header for a specific block number.
	// Returns the block header and an error if it cannot be retrieved.
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)

	// PublicAddress returns the public addresses of the signers
	PublicAddress() ([]common.Address, error)
}

// StorageInterface defines the methods required to interact with
// the storage layer for managing MonitoredTx entities.
type StorageInterface interface {
	// Add inserts a new MonitoredTx into the storage.
	// It takes a context and the MonitoredTx to be added.
	// Returns an error if the transaction cannot be stored.
	//  error: ErrAlreadyExists if a transaction with the same ID already exists in the storage.
	//     the ID is the hash of the transaction
	Add(ctx context.Context, mTx MonitoredTx) error

	// Remove deletes a MonitoredTx from the storage using its ID (common.Hash).
	// Returns an error if the transaction cannot be found or removed.
	Remove(ctx context.Context, id common.Hash) error

	// Get retrieves a MonitoredTx from the storage by its ID.
	// Returns the MonitoredTx if found, or an error if it doesn't exist.
	Get(ctx context.Context, id common.Hash) (MonitoredTx, error)

	// GetByStatus retrieves all MonitoredTx entities with a matching status.
	// Takes a list of MonitoredTxStatus to filter the transactions.
	// Returns a slice of MonitoredTx and an error if any occurs during retrieval.
	GetByStatus(ctx context.Context, statuses []MonitoredTxStatus) ([]MonitoredTx, error)

	// GetByBlock retrieves MonitoredTx transactions that have a block number
	// between the specified fromBlock and toBlock.
	// If either block number is nil, it will be ignored in the query.
	// Returns a slice of MonitoredTx and an error if any occurs.
	GetByBlock(ctx context.Context, fromBlock, toBlock *uint64) ([]MonitoredTx, error)

	// Update modifies an existing MonitoredTx in the storage.
	// It takes a context and the MonitoredTx object with updated fields.
	// Returns an error if the transaction cannot be updated.
	Update(ctx context.Context, mTx MonitoredTx) error

	// Empty removes all MonitoredTx entities from the storage.
	// This is typically used for clearing all data or resetting the state.
	// Returns an error if the operation fails.
	Empty(ctx context.Context) error
}
