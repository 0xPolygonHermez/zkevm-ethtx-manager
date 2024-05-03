package ethtxmanager

import (
	"github.com/0xPolygonHermez/zkevm-ethtx-manager/config/types"
	"github.com/0xPolygonHermez/zkevm-ethtx-manager/etherman"
	"github.com/0xPolygonHermez/zkevm-ethtx-manager/log"
)

// Config is configuration for ethereum transaction manager
type Config struct {
	// FrequencyToMonitorTxs frequency of the resending failed txs
	FrequencyToMonitorTxs types.Duration `mapstructure:"FrequencyToMonitorTxs"`
	// WaitTxToBeMined time to wait after transaction was sent to the ethereum
	WaitTxToBeMined types.Duration `mapstructure:"WaitTxToBeMined"`
	// WaitReceiptToBeGenerated time to wait after transaction was mined to get the receipt
	WaitReceiptToBeGenerated types.Duration `mapstructure:"WaitReceiptToBeGenerated"`
	// ConsolidationL1ConfirmationBlocks is the number of blocks to wait for a L1 tx to be consolidated
	ConsolidationL1ConfirmationBlocks uint64 `mapstructure:"ConsolidationL1ConfirmationBlocks"`
	// FinalizationL1ConfirmationBlocks is the number of blocks to wait for a L1 tx to be finalized
	FinalizationL1ConfirmationBlocks uint64 `mapstructure:"FinalizationL1ConfirmationBlocks"`

	// PrivateKeys defines all the key store files that are going
	// to be read in order to provide the private keys to sign the L1 txs
	PrivateKeys []types.KeystoreFileConfig `mapstructure:"PrivateKeys"`

	// ForcedGas is the amount of gas to be forced in case of gas estimation error
	ForcedGas uint64 `mapstructure:"ForcedGas"`

	// GasPriceMarginFactor is used to multiply the suggested gas price provided by the network
	// in order to allow a different gas price to be set for all the transactions and making it
	// easier to have the txs prioritized in the pool, default value is 1.
	//
	// ex:
	// suggested gas price: 100
	// GasPriceMarginFactor: 1
	// gas price = 100
	//
	// suggested gas price: 100
	// GasPriceMarginFactor: 1.1
	// gas price = 110
	GasPriceMarginFactor float64 `mapstructure:"GasPriceMarginFactor"`

	// MaxGasPriceLimit helps avoiding transactions to be sent over an specified
	// gas price amount, default value is 0, which means no limit.
	// If the gas price provided by the network and adjusted by the GasPriceMarginFactor
	// is greater than this configuration, transaction will have its gas price set to
	// the value configured in this config as the limit.
	//
	// ex:
	//
	// suggested gas price: 100
	// gas price margin factor: 20%
	// max gas price limit: 150
	// tx gas price = 120
	//
	// suggested gas price: 100
	// gas price margin factor: 20%
	// max gas price limit: 110
	// tx gas price = 110
	MaxGasPriceLimit uint64 `mapstructure:"MaxGasPriceLimit"`
	// PersistenceFilename is the filename to store the memory storage
	PersistenceFilename string `mapstructure:"PersistenceFilename"`
	// ReadPendingL1Txs is a flag to enable the reading of pending L1 txs
	// It shouldn't be enabled if PersistenceFilename is not empty
	ReadPendingL1Txs bool `mapstructure:"ReadPendingL1Txs"`
	// Etherman configuration
	Etherman etherman.Config `mapstructure:"Etherman"`

	// Log configuration
	Log log.Config `mapstructure:"Log"`
}
