# zkevm-ethtx-manager
Stateless manager to send transactions to L1.

## Main Funtions
### Add Transaction
`func (c *Client) Add(ctx context.Context, to *common.Address, value *big.Int, data []byte, gasOffset uint64, sidecar *ethTypes.BlobTxSidecar) (common.Hash, error)`

Adds a transaction to be sent to L1. Gas is estimated automatically. The returned hash is calculated over the *to*, *value* and *data* fields.

If a transaction with the same hash already exists in storage, it returns `(id, ErrAlreadyExists)` instead of failing, so the caller can treat the duplicate as a non-fatal condition.

### Add Transaction with Gas
`func (c *Client) AddWithGas(ctx context.Context, to *common.Address, value *big.Int, data []byte, gasOffset uint64, sidecar *ethTypes.BlobTxSidecar, gas uint64) (common.Hash, error)`

Same as `Add` but uses the provided gas value instead of estimating it.

If a transaction with the same hash already exists in storage, it returns `(id, ErrAlreadyExists)` instead of failing, so the caller can treat the duplicate as a non-fatal condition.

### Remove Transaction 
`func (c *Client) Remove(ctx context.Context, id common.Hash) error `

Removes a transaction. Should be called when a finalized transactions is not relevant any more.

### Get Transaction Status
`func (c *Client) Result(ctx context.Context, id common.Hash) (MonitoredTxResult, error)`

Result returns the current result of the transaction execution with all the details.

### Get All Transactions Status
`func (c *Client) ResultsByStatus(ctx context.Context, statuses []MonitoredTxStatus) ([]MonitoredTxResult, error)`

ResultsByStatus returns all the results for all the monitored txs matching the provided statuses.
If the statuses are empty, all the statuses are considered.

### Transactions statuses

- **Created**: the tx was just added to the volatile storage
- **Sent**: transaction was sent to L1
- **Failed**: the tx was already mined and failed with an error that can't be recovered automatically, ex: the data in the tx is invalid and the tx gets reverted
- **Mined**: the tx was already mined and the receipt status is Successful.
- **Safe**: The tx was mined and is considered safe.
- **Finalized**: The tx was mined and is considered finalized.
