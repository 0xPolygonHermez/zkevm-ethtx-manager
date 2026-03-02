package ethtxmanager

import (
	context "context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path"
	"testing"
	"time"

	localCommon "github.com/0xPolygon/zkevm-ethtx-manager/common"
	"github.com/0xPolygon/zkevm-ethtx-manager/etherman"
	"github.com/0xPolygon/zkevm-ethtx-manager/ethtxmanager/sqlstorage"
	"github.com/0xPolygon/zkevm-ethtx-manager/mocks"
	"github.com/0xPolygon/zkevm-ethtx-manager/types"
	signertypes "github.com/agglayer/go_signer/signer/types"
	"github.com/ethereum/go-ethereum"
	common "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errGenericNotFound = errors.New("not found")

func TestTxManagerExploratory(t *testing.T) {
	t.Skip("skipping test")
	storagePath := path.Join(t.TempDir(), "txmanager.sqlite")
	storage, err := sqlstorage.NewStorage(localCommon.SQLLiteDriverName, storagePath)
	require.NoError(t, err)
	url := os.Getenv("L1URL")
	ethClient, err := ethclient.Dial(url)
	require.NoError(t, err)
	ethermanClient := &etherman.Client{
		EthClient: ethClient,
	}
	sut := &Client{
		etherman: ethermanClient,
		storage:  storage,
	}
	ctx := context.Background()
	_, err = sut.Result(ctx, common.HexToHash("0x1"))
	require.Error(t, err)
	//fmt.Print(monitoredTx)
	txs, err := sut.ResultsByStatus(ctx, nil)
	require.NoError(t, err)
	fmt.Print(txs)
}

func TestAdd(t *testing.T) {
	testData := newTestData(t, true)
	to := common.HexToAddress("0x1")
	testData.ethermanMock.EXPECT().SuggestedGasPrice(testData.ctx).Return(nil, errGenericNotFound)
	_, err := testData.sut.Add(testData.ctx, &to, big.NewInt(1), []byte{}, 0, nil)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = testData.sut.AddWithGas(testData.ctx, &to, big.NewInt(1), []byte{}, 0, nil, 0)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAddAlreadyExists(t *testing.T) {
	to := common.HexToAddress("0x1")
	value := big.NewInt(1)
	data := []byte{}
	expectedID := ethtypes.NewTx(&ethtypes.LegacyTx{To: &to, Value: value, Data: data}).Hash()

	t.Run("AddWithGas returns ErrAlreadyExists and the tx hash", func(t *testing.T) {
		testData := newTestData(t, true)
		testData.ethermanMock.EXPECT().SuggestedGasPrice(testData.ctx).Return(big.NewInt(1), nil).Once()
		testData.storageMock.EXPECT().Add(testData.ctx, mock.Anything).Return(types.ErrAlreadyExists).Once()

		hash, err := testData.sut.AddWithGas(testData.ctx, &to, value, data, 0, nil, 21000)
		require.ErrorIs(t, err, ErrAlreadyExists)
		require.Equal(t, expectedID, hash)
	})

	t.Run("Add returns ErrAlreadyExists and the tx hash", func(t *testing.T) {
		testData := newTestData(t, true)
		testData.ethermanMock.EXPECT().SuggestedGasPrice(testData.ctx).Return(big.NewInt(1), nil).Once()
		testData.ethermanMock.EXPECT().EstimateGas(testData.ctx, testData.sut.from, &to, value, data).Return(uint64(21000), nil).Once()
		testData.storageMock.EXPECT().Add(testData.ctx, mock.Anything).Return(types.ErrAlreadyExists).Once()

		hash, err := testData.sut.Add(testData.ctx, &to, value, data, 0, nil)
		require.ErrorIs(t, err, ErrAlreadyExists)
		require.Equal(t, expectedID, hash)
	})
}

func TestRemove(t *testing.T) {
	testData := newTestData(t, false)
	err := testData.sut.Remove(testData.ctx, common.HexToHash("0x1"))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestResult(t *testing.T) {
	testData := newTestData(t, false)
	_, err := testData.sut.Result(testData.ctx, common.HexToHash("0x1"))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGetMonitoredTxnIteration(t *testing.T) {
	ctx := context.Background()
	etherman := mocks.NewEthermanInterface(t)
	storage, err := sqlstorage.NewStorage(localCommon.SQLLiteDriverName,
		path.Join(t.TempDir(), "txmanager.sqlite"))
	require.NoError(t, err)

	client := &Client{
		etherman: etherman,
		storage:  storage,
	}

	tests := []struct {
		name           string
		storageTxn     *types.MonitoredTx
		ethermanNonce  uint64
		shouldUpdate   bool
		expectedResult []*monitoredTxnIteration
		expectedError  error
	}{
		{
			name:           "No transactions to update",
			expectedError:  nil,
			expectedResult: []*monitoredTxnIteration{},
		},
		{
			name: "Transaction should not update nonce",
			storageTxn: &types.MonitoredTx{
				ID:          common.HexToHash("0x1"),
				From:        common.HexToAddress("0x1"),
				BlockNumber: big.NewInt(10),
				Status:      types.MonitoredTxStatusSent,
				History: map[common.Hash]bool{
					common.HexToHash("0x1"): true,
				},
			},
			shouldUpdate: false,
			expectedResult: []*monitoredTxnIteration{
				{
					MonitoredTx: &types.MonitoredTx{
						ID:          common.HexToHash("0x1"),
						From:        common.HexToAddress("0x1"),
						BlockNumber: big.NewInt(10),
						Status:      types.MonitoredTxStatusSent,
						History: map[common.Hash]bool{
							common.HexToHash("0x1"): true,
						},
					},
					confirmed:   true,
					lastReceipt: &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful},
				},
			},
			expectedError: nil,
		},
		{
			name: "Transaction should update nonce",
			storageTxn: &types.MonitoredTx{
				ID:          common.HexToHash("0x1"),
				From:        common.HexToAddress("0x1"),
				Status:      types.MonitoredTxStatusCreated,
				BlockNumber: big.NewInt(10),
			},
			shouldUpdate:  true,
			ethermanNonce: 1,
			expectedResult: []*monitoredTxnIteration{
				{
					MonitoredTx: &types.MonitoredTx{
						ID:          common.HexToHash("0x1"),
						From:        common.HexToAddress("0x1"),
						Status:      types.MonitoredTxStatusCreated,
						Nonce:       1,
						BlockNumber: big.NewInt(10),
					},
				},
			},
			expectedError: nil,
		},
		{
			name: "Error getting pending nonce",
			storageTxn: &types.MonitoredTx{
				ID:          common.HexToHash("0x1"),
				From:        common.HexToAddress("0x1"),
				Status:      types.MonitoredTxStatusCreated,
				BlockNumber: big.NewInt(10),
			},
			shouldUpdate:  true,
			expectedError: errors.New("failed to get pending nonce for sender: 0x1. Error: some error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, storage.Empty(ctx))
			if tt.storageTxn != nil {
				require.NoError(t, storage.Add(ctx, *tt.storageTxn))
			}

			etherman.ExpectedCalls = nil

			if tt.shouldUpdate {
				etherman.On("PendingNonce", ctx, common.HexToAddress("0x1")).Return(tt.ethermanNonce, tt.expectedError).Once()
			} else if len(tt.expectedResult) > 0 {
				etherman.On("CheckTxWasMined", ctx, mock.Anything).Return(tt.expectedResult[0].confirmed, tt.expectedResult[0].lastReceipt, nil)
			}

			result, err := client.getMonitoredTxnIteration(ctx)
			if tt.expectedError != nil {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.expectedError.Error())
			} else {
				require.NoError(t, err)
				if len(tt.expectedResult) > 0 {
					require.Len(t, result, len(tt.expectedResult))
					compareTxsWithoutDates(t, *tt.expectedResult[0].MonitoredTx, *result[0].MonitoredTx)
				} else {
					require.Empty(t, result)
				}

				// now check from storage
				if len(tt.expectedResult) > 0 {
					dbTxns, err := storage.GetByStatus(ctx, []types.MonitoredTxStatus{tt.storageTxn.Status})
					require.NoError(t, err)
					require.Len(t, dbTxns, 1)
					require.Equal(t, tt.expectedResult[0].MonitoredTx.Nonce, dbTxns[0].Nonce)
				}
			}

			etherman.AssertExpectations(t)
		})
	}
}

func TestNew(t *testing.T) {
	mockEtherman := mocks.NewEthermanInterface(t)
	ethTxManagerEthermanFactoryFunc = func(cfg etherman.Config, signersConfig []signertypes.SignerConfig) (types.EthermanInterface, error) {
		return mockEtherman, nil
	}
	mockEtherman.EXPECT().PublicAddress().Return([]common.Address{common.HexToAddress("0x1")}, nil).Once()
	sut, err := New(Config{})
	require.NoError(t, err)
	require.NotNil(t, sut)
}

// compareTxsWithout dates compares the two MonitoredTx instances, but without dates, since some functions are altering it
func compareTxsWithoutDates(t *testing.T, expected, actual types.MonitoredTx) {
	t.Helper()

	expected.CreatedAt = time.Time{}
	expected.UpdatedAt = time.Time{}
	actual.CreatedAt = time.Time{}
	actual.UpdatedAt = time.Time{}

	require.Equal(t, expected, actual)
}

type testEthTxManagerData struct {
	storageMock  *mocks.StorageInterface
	ethermanMock *mocks.EthermanInterface
	sut          *Client
	ctx          context.Context
}

func newTestData(t *testing.T, useMockStorage bool) *testEthTxManagerData {
	t.Helper()
	var storageMock *mocks.StorageInterface
	ethermanMock := mocks.NewEthermanInterface(t)
	sut := &Client{
		etherman: ethermanMock,
	}
	if useMockStorage {
		storageMock = mocks.NewStorageInterface(t)
		sut.storage = storageMock
	} else {
		storagePath := path.Join(t.TempDir(), "txmanager.sqlite")
		storageInstance, err := sqlstorage.NewStorage(localCommon.SQLLiteDriverName, storagePath)
		require.NoError(t, err)
		sut.storage = storageInstance
	}

	return &testEthTxManagerData{
		storageMock:  storageMock,
		ethermanMock: ethermanMock,
		sut:          sut,
		ctx:          context.Background(),
	}
}

func TestMonitorTxEstimateGasMaxRetries(t *testing.T) {
	tests := []struct {
		name                      string
		estimateGasMaxRetries     uint64
		retryCount                uint64
		shouldEvict               bool
		storageUpdateShouldFail   bool
		expectedStatus            types.MonitoredTxStatus
		expectedStorageUpdateCall bool
	}{
		{
			name:                      "Unlimited retries (EstimateGasMaxRetries = 0) - should not evict",
			estimateGasMaxRetries:     0,
			retryCount:                100,
			shouldEvict:               false,
			expectedStatus:            types.MonitoredTxStatusCreated,
			expectedStorageUpdateCall: false,
		},
		{
			name:                      "Retry count below max retries - should not evict",
			estimateGasMaxRetries:     5,
			retryCount:                3,
			shouldEvict:               false,
			expectedStatus:            types.MonitoredTxStatusCreated,
			expectedStorageUpdateCall: false,
		},
		{
			name:                      "Retry count equals max retries - should evict",
			estimateGasMaxRetries:     5,
			retryCount:                5,
			shouldEvict:               true,
			expectedStatus:            types.MonitoredTxStatusEvicted,
			expectedStorageUpdateCall: true,
		},
		{
			name:                      "Retry count exceeds max retries - should evict",
			estimateGasMaxRetries:     3,
			retryCount:                10,
			shouldEvict:               true,
			expectedStatus:            types.MonitoredTxStatusEvicted,
			expectedStorageUpdateCall: true,
		},
		{
			name:                      "Eviction with storage update failure",
			estimateGasMaxRetries:     2,
			retryCount:                5,
			shouldEvict:               true,
			storageUpdateShouldFail:   true,
			expectedStatus:            types.MonitoredTxStatusEvicted,
			expectedStorageUpdateCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testData := newTestData(t, true)
			testData.sut.cfg = Config{
				EstimateGasMaxRetries: tt.estimateGasMaxRetries,
			}

			// Create a monitored transaction with specific retry count
			mTx := &monitoredTxnIteration{
				MonitoredTx: &types.MonitoredTx{
					ID:         common.HexToHash("0x123"),
					From:       common.HexToAddress("0x456"),
					To:         &common.Address{},
					Status:     types.MonitoredTxStatusCreated,
					RetryCount: tt.retryCount,
					History:    make(map[common.Hash]bool),
					Value:      big.NewInt(0),
					Data:       []byte{},
					Gas:        21000,
					GasPrice:   big.NewInt(1000000000),
				},
			}

			if tt.expectedStorageUpdateCall {
				if tt.storageUpdateShouldFail {
					testData.storageMock.EXPECT().Update(testData.ctx, mock.MatchedBy(func(tx types.MonitoredTx) bool {
						return tx.Status == types.MonitoredTxStatusEvicted &&
							tx.ID == mTx.ID &&
							tx.RetryCount == tt.retryCount
					})).Return(errors.New("storage update failed")).Once()
				} else {
					testData.storageMock.EXPECT().Update(testData.ctx, mock.MatchedBy(func(tx types.MonitoredTx) bool {
						return tx.Status == types.MonitoredTxStatusEvicted &&
							tx.ID == mTx.ID &&
							tx.RetryCount == tt.retryCount
					})).Return(nil).Once()
				}
			}

			// For non-evicted cases, setup minimal mocks to prevent function from failing
			if !tt.shouldEvict {
				// Mock the basic operations that monitorTx will try to perform
				testData.ethermanMock.EXPECT().SignTx(testData.ctx, mock.Anything, mock.Anything).Return(ethtypes.NewTx(&ethtypes.LegacyTx{}), nil).Maybe()
				testData.storageMock.EXPECT().Update(testData.ctx, mock.Anything).Return(nil).Maybe()
				testData.ethermanMock.EXPECT().GetTx(testData.ctx, mock.Anything).Return(nil, false, errGenericNotFound).Maybe()
				testData.ethermanMock.EXPECT().SendTx(testData.ctx, mock.Anything).Return(nil).Maybe()
				testData.ethermanMock.EXPECT().WaitTxToBeMined(testData.ctx, mock.Anything, mock.Anything).Return(false, nil).Maybe()
			}

			logger := createMonitoredTxLogger(*mTx.MonitoredTx)
			testData.sut.monitorTx(testData.ctx, mTx, logger)

			require.Equal(t, tt.expectedStatus, mTx.Status, "Transaction status should match expected")
			testData.storageMock.AssertExpectations(t)
		})
	}
}

func TestMonitorTxEstimateGasMaxRetriesIntegration(t *testing.T) {
	// This test uses real storage to verify the complete flow
	testData := newTestData(t, false)
	testData.sut.cfg = Config{
		EstimateGasMaxRetries: 3,
	}

	mTx := types.MonitoredTx{
		ID:         common.HexToHash("0x123"),
		From:       common.HexToAddress("0x456"),
		To:         &common.Address{},
		Status:     types.MonitoredTxStatusCreated,
		RetryCount: 3, // Equals max retries, should be evicted
		History:    make(map[common.Hash]bool),
		Value:      big.NewInt(0),
		Data:       []byte{},
		Gas:        21000,
		GasPrice:   big.NewInt(1000000000),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err := testData.sut.storage.Add(testData.ctx, mTx)
	require.NoError(t, err)

	iteration := &monitoredTxnIteration{
		MonitoredTx: &mTx,
	}
	logger := createMonitoredTxLogger(mTx)
	testData.sut.monitorTx(testData.ctx, iteration, logger)

	// Verify the transaction was evicted
	require.Equal(t, types.MonitoredTxStatusEvicted, iteration.Status)

	// Verify the change was persisted to storage
	storedTx, err := testData.sut.storage.Get(testData.ctx, mTx.ID)
	require.NoError(t, err)
	require.Equal(t, types.MonitoredTxStatusEvicted, storedTx.Status)
	require.Equal(t, uint64(3), storedTx.RetryCount)
}

func TestProcessPendingMonitoredTxs(t *testing.T) {
	t.Run("No transactions - returns immediately", func(t *testing.T) {
		testData := newTestData(t, true)
		testData.storageMock.EXPECT().GetByStatus(mock.Anything, mock.Anything).Return([]types.MonitoredTx{}, nil).Once()

		var callCount int
		resultHandler := func(result types.MonitoredTxResult) { callCount++ }

		testData.sut.ProcessPendingMonitoredTxs(testData.ctx, resultHandler)
		require.Equal(t, 0, callCount)
	})

	t.Run("Mined transaction - calls handler", func(t *testing.T) {
		testData := newTestData(t, true)
		tx := types.MonitoredTx{
			ID: common.HexToHash("0x1"), Status: types.MonitoredTxStatusMined,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}

		testData.storageMock.EXPECT().GetByStatus(mock.Anything, mock.Anything).Return([]types.MonitoredTx{tx}, nil).Once()
		testData.storageMock.EXPECT().Get(mock.Anything, tx.ID).Return(tx, nil).Once()
		testData.storageMock.EXPECT().Update(mock.Anything, mock.Anything).Return(nil).Once()
		testData.storageMock.EXPECT().GetByStatus(mock.Anything, mock.Anything).Return([]types.MonitoredTx{}, nil).Once()

		var status types.MonitoredTxStatus
		resultHandler := func(result types.MonitoredTxResult) { status = result.Status }

		testData.sut.ProcessPendingMonitoredTxs(testData.ctx, resultHandler)
		require.Equal(t, types.MonitoredTxStatusMined, status)
	})

	t.Run("Evicted transaction - calls handler", func(t *testing.T) {
		testData := newTestData(t, true)
		tx := types.MonitoredTx{
			ID: common.HexToHash("0x1"), Status: types.MonitoredTxStatusEvicted,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}

		testData.storageMock.EXPECT().GetByStatus(mock.Anything, mock.Anything).Return([]types.MonitoredTx{tx}, nil).Once()
		testData.storageMock.EXPECT().Get(mock.Anything, tx.ID).Return(tx, nil).Maybe() // For buildResult
		testData.storageMock.EXPECT().GetByStatus(mock.Anything, mock.Anything).Return([]types.MonitoredTx{}, nil).Once()

		var status types.MonitoredTxStatus
		resultHandler := func(result types.MonitoredTxResult) { status = result.Status }

		testData.sut.ProcessPendingMonitoredTxs(testData.ctx, resultHandler)
		require.Equal(t, types.MonitoredTxStatusEvicted, status)
	})
}

func TestMonitorTxGasReviewFailureRetryIncrement(t *testing.T) {
	t.Run("Gas review failure - increments retry count", func(t *testing.T) {
		testData := newTestData(t, true)

		mTx := &monitoredTxnIteration{
			MonitoredTx: &types.MonitoredTx{
				ID:          common.HexToHash("0x123"),
				Status:      types.MonitoredTxStatusSent,
				RetryCount:  2,
				EstimateGas: true,
			},
		}

		// Mock gas review failure
		testData.ethermanMock.EXPECT().SuggestedGasPrice(testData.ctx).Return(nil, errors.New("gas price error")).Once()

		// Expect retry count update
		testData.storageMock.EXPECT().Update(testData.ctx, mock.MatchedBy(func(tx types.MonitoredTx) bool {
			return tx.RetryCount == 3 && tx.ID == mTx.ID
		})).Return(nil).Once()

		logger := createMonitoredTxLogger(*mTx.MonitoredTx)
		testData.sut.monitorTx(testData.ctx, mTx, logger)

		require.Equal(t, uint64(3), mTx.RetryCount)
	})

	t.Run("Gas review failure - storage update fails", func(t *testing.T) {
		testData := newTestData(t, true)

		mTx := &monitoredTxnIteration{
			MonitoredTx: &types.MonitoredTx{
				ID:          common.HexToHash("0x123"),
				Status:      types.MonitoredTxStatusSent,
				RetryCount:  1,
				EstimateGas: true,
			},
		}

		// Mock gas review failure
		testData.ethermanMock.EXPECT().SuggestedGasPrice(testData.ctx).Return(nil, errors.New("gas price error")).Once()

		// Expect retry count update to fail
		testData.storageMock.EXPECT().Update(testData.ctx, mock.MatchedBy(func(tx types.MonitoredTx) bool {
			return tx.RetryCount == 2
		})).Return(errors.New("storage error")).Once()

		logger := createMonitoredTxLogger(*mTx.MonitoredTx)
		testData.sut.monitorTx(testData.ctx, mTx, logger)

		// Retry count should still be incremented even if storage update fails
		require.Equal(t, uint64(2), mTx.RetryCount)
	})
}

func TestMonitorTxSendFailureRetryIncrement(t *testing.T) {
	t.Run("Send failure - increments retry count", func(t *testing.T) {
		testData := newTestData(t, true)
		testData.sut.cfg.EstimateGasMaxRetries = 10 // Ensure we don't hit eviction

		mTx := &monitoredTxnIteration{
			MonitoredTx: &types.MonitoredTx{
				ID:         common.HexToHash("0x123"),
				Status:     types.MonitoredTxStatusCreated,
				RetryCount: 1,
				Value:      big.NewInt(0),
				Data:       []byte{},
				Gas:        21000,
				GasPrice:   big.NewInt(1000000000),
				History:    make(map[common.Hash]bool),
			},
		}

		// Mock signing and transaction existence check, but fail on SendTx
		testData.ethermanMock.EXPECT().SignTx(testData.ctx, mock.Anything, mock.Anything).Return(ethtypes.NewTx(&ethtypes.LegacyTx{}), nil).Once()
		testData.ethermanMock.EXPECT().GetTx(testData.ctx, mock.Anything).Return(nil, false, ethereum.NotFound).Once()
		testData.ethermanMock.EXPECT().SendTx(testData.ctx, mock.Anything).Return(errors.New("network error")).Once()

		// Mock storage updates - first for history, second for retry count after send failure
		testData.storageMock.EXPECT().Update(testData.ctx, mock.Anything).Return(nil).Times(2)

		logger := createMonitoredTxLogger(*mTx.MonitoredTx)
		testData.sut.monitorTx(testData.ctx, mTx, logger)

		require.Equal(t, uint64(2), mTx.RetryCount)
	})
}
