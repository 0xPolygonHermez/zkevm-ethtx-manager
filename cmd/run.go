package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/0xPolygonHermez/zkevm-node"
	"github.com/0xPolygonHermez/zkevm-node/config"
	"github.com/0xPolygonHermez/zkevm-node/db"
	"github.com/0xPolygonHermez/zkevm-node/etherman"
	"github.com/0xPolygonHermez/zkevm-node/ethtxmanager"
	"github.com/0xPolygonHermez/zkevm-node/event"
	"github.com/0xPolygonHermez/zkevm-node/log"
	"github.com/0xPolygonHermez/zkevm-node/merkletree"
	"github.com/0xPolygonHermez/zkevm-node/metrics"
	"github.com/0xPolygonHermez/zkevm-node/pool"
	"github.com/0xPolygonHermez/zkevm-node/pool/pgpoolstorage"
	"github.com/0xPolygonHermez/zkevm-node/state"
	"github.com/0xPolygonHermez/zkevm-node/state/pgstatestorage"
	"github.com/0xPolygonHermez/zkevm-node/state/runtime/executor"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v2"
)

func start(cliCtx *cli.Context) error {
	c, err := config.Load(cliCtx, true)
	if err != nil {
		return err
	}
	setupLog(c.Log)

	if c.Log.Environment == log.EnvironmentDevelopment {
		zkevm.PrintVersion(os.Stdout)
		log.Info("Starting application")
	} else if c.Log.Environment == log.EnvironmentProduction {
		logVersion()
	}

	if c.Metrics.Enabled {
		metrics.Init()
	}
	components := cliCtx.StringSlice(config.FlagComponents)

	// Only runs migration if the component is the synchronizer and if the flag is deactivated
	if !cliCtx.Bool(config.FlagMigrations) {
		runStateMigrations(c.State.DB)
	}
	checkStateMigrations(c.State.DB)

	var (
		eventLog                      *event.EventLog
		cancelFuncs                   []context.CancelFunc
		needsExecutor, needsStateTree bool
	)

	// Core State DB
	stateSqlDB, err := db.NewSQLDB(c.State.DB)
	if err != nil {
		log.Fatal(err)
	}

	etherman, err := newEtherman(*c)
	if err != nil {
		log.Fatal(err)
	}

	// READ CHAIN ID FROM POE SC
	l2ChainID, err := etherman.GetL2ChainID()
	if err != nil {
		log.Fatal(err)
	}

	st := newState(cliCtx.Context, c, l2ChainID, []state.ForkIDInterval{}, stateSqlDB, eventLog, needsExecutor, needsStateTree)
	forkIDIntervals, err := forkIDIntervals(cliCtx.Context, st, etherman, c.NetworkConfig.Genesis.BlockNumber)
	if err != nil {
		log.Fatal("error getting forkIDs. Error: ", err)
	}
	st.UpdateForkIDIntervalsInMemory(forkIDIntervals)

	ethTxManagerStorage, err := ethtxmanager.NewPostgresStorage(c.State.DB)
	if err != nil {
		log.Fatal(err)
	}

	if c.Metrics.ProfilingEnabled {
		go startProfilingHttpServer(c.Metrics)
	}
	for _, component := range components {
		switch component {
		case ETHTXMANAGER:
			etm := createEthTxManager(*c, ethTxManagerStorage, st)
			go etm.Start()
		}
	}

	if c.Metrics.Enabled {
		go startMetricsHttpServer(c.Metrics)
	}

	waitSignal(cancelFuncs)

	return nil
}

func setupLog(c log.Config) {
	log.Init(c)
}

func runStateMigrations(c db.Config) {
	runMigrations(c, db.StateMigrationName)
}

func checkStateMigrations(c db.Config) {
	err := db.CheckMigrations(c, db.StateMigrationName)
	if err != nil {
		log.Fatal(err)
	}
}

func runPoolMigrations(c db.Config) {
	runMigrations(c, db.PoolMigrationName)
}

func runMigrations(c db.Config, name string) {
	log.Infof("running migrations for %v", name)
	err := db.RunMigrationsUp(c, name)
	if err != nil {
		log.Fatal(err)
	}
}

func newEtherman(c config.Config) (*etherman.Client, error) {
	return etherman.NewClient(c.Etherman, c.NetworkConfig.L1Config)
}

func waitSignal(cancelFuncs []context.CancelFunc) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)

	for sig := range signals {
		switch sig {
		case os.Interrupt, os.Kill:
			log.Info("terminating application gracefully...")

			exitStatus := 0
			for _, cancel := range cancelFuncs {
				cancel()
			}
			os.Exit(exitStatus)
		}
	}
}

func newState(ctx context.Context, c *config.Config, l2ChainID uint64, forkIDIntervals []state.ForkIDInterval, sqlDB *pgxpool.Pool, eventLog *event.EventLog, needsExecutor, needsStateTree bool) *state.State {
	stateDb := pgstatestorage.NewPostgresStorage(c.State, sqlDB)

	// Executor
	var executorClient executor.ExecutorServiceClient
	if needsExecutor {
		executorClient, _, _ = executor.NewExecutorClient(ctx, c.Executor)
	}

	// State Tree
	var stateTree *merkletree.StateTree
	if needsStateTree {
		stateDBClient, _, _ := merkletree.NewMTDBServiceClient(ctx, c.MTClient)
		stateTree = merkletree.NewStateTree(stateDBClient)
	}

	stateCfg := state.Config{
		MaxCumulativeGasUsed:         c.State.Batch.Constraints.MaxCumulativeGasUsed,
		ChainID:                      l2ChainID,
		ForkIDIntervals:              forkIDIntervals,
		MaxResourceExhaustedAttempts: c.Executor.MaxResourceExhaustedAttempts,
		WaitOnResourceExhaustion:     c.Executor.WaitOnResourceExhaustion,
		ForkUpgradeBatchNumber:       c.ForkUpgradeBatchNumber,
		ForkUpgradeNewForkId:         c.ForkUpgradeNewForkId,
		MaxLogsCount:                 c.RPC.MaxLogsCount,
		MaxLogsBlockRange:            c.RPC.MaxLogsBlockRange,
		MaxNativeBlockHashBlockRange: c.RPC.MaxNativeBlockHashBlockRange,
	}
	allLeaves, err := stateDb.GetAllL1InfoRootEntries(ctx, nil)
	if err != nil {
		log.Fatal("error getting all leaves. Error: ", err)
	}
	var leaves [][32]byte
	for _, leaf := range allLeaves {
		leaves = append(leaves, leaf.Hash())
	}

	st := state.NewState(stateCfg, stateDb, executorClient, stateTree, eventLog, nil)
	return st
}

func createPool(cfgPool pool.Config, constraintsCfg state.BatchConstraintsCfg, l2ChainID uint64, st *state.State, eventLog *event.EventLog) *pool.Pool {
	runPoolMigrations(cfgPool.DB)
	poolStorage, err := pgpoolstorage.NewPostgresPoolStorage(cfgPool.DB)
	if err != nil {
		log.Fatal(err)
	}
	poolInstance := pool.NewPool(cfgPool, constraintsCfg, poolStorage, st, l2ChainID, eventLog)
	return poolInstance
}

func createEthTxManager(cfg config.Config, etmStorage *ethtxmanager.PostgresStorage, st *state.State) *ethtxmanager.Client {
	etherman, err := newEtherman(cfg)
	if err != nil {
		log.Fatal(err)
	}

	for _, privateKey := range cfg.EthTxManager.PrivateKeys {
		_, err := etherman.LoadAuthFromKeyStore(privateKey.Path, privateKey.Password)
		if err != nil {
			log.Fatal(err)
		}
	}
	etm := ethtxmanager.New(cfg.EthTxManager, etherman, etmStorage, st)
	return etm
}

func startProfilingHttpServer(c metrics.Config) {
	const two = 2
	mux := http.NewServeMux()
	address := fmt.Sprintf("%s:%d", c.ProfilingHost, c.ProfilingPort)
	lis, err := net.Listen("tcp", address)
	if err != nil {
		log.Errorf("failed to create tcp listener for profiling: %v", err)
		return
	}
	mux.HandleFunc(metrics.ProfilingIndexEndpoint, pprof.Index)
	mux.HandleFunc(metrics.ProfileEndpoint, pprof.Profile)
	mux.HandleFunc(metrics.ProfilingCmdEndpoint, pprof.Cmdline)
	mux.HandleFunc(metrics.ProfilingSymbolEndpoint, pprof.Symbol)
	mux.HandleFunc(metrics.ProfilingTraceEndpoint, pprof.Trace)
	profilingServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: two * time.Minute,
		ReadTimeout:       two * time.Minute,
	}
	log.Infof("profiling server listening on port %d", c.ProfilingPort)
	if err := profilingServer.Serve(lis); err != nil {
		if err == http.ErrServerClosed {
			log.Warnf("http server for profiling stopped")
			return
		}
		log.Errorf("closed http connection for profiling server: %v", err)
		return
	}
}

func startMetricsHttpServer(c metrics.Config) {
	const ten = 10
	mux := http.NewServeMux()
	address := fmt.Sprintf("%s:%d", c.Host, c.Port)
	lis, err := net.Listen("tcp", address)
	if err != nil {
		log.Errorf("failed to create tcp listener for metrics: %v", err)
		return
	}
	mux.Handle(metrics.Endpoint, promhttp.Handler())

	metricsServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: ten * time.Second,
		ReadTimeout:       ten * time.Second,
	}
	log.Infof("metrics server listening on port %d", c.Port)
	if err := metricsServer.Serve(lis); err != nil {
		if err == http.ErrServerClosed {
			log.Warnf("http server for metrics stopped")
			return
		}
		log.Errorf("closed http connection for metrics server: %v", err)
		return
	}
}

func logVersion() {
	log.Infow("Starting application",
		// node version is already logged by default
		"gitRevision", zkevm.GitRev,
		"gitBranch", zkevm.GitBranch,
		"goVersion", runtime.Version(),
		"built", zkevm.BuildDate,
		"os/arch", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	)
}

func forkIDIntervals(ctx context.Context, st *state.State, etherman *etherman.Client, genesisBlockNumber uint64) ([]state.ForkIDInterval, error) {
	log.Debug("getting forkIDs from db")
	forkIDIntervals, err := st.GetForkIDs(ctx, nil)
	if err != nil && !errors.Is(err, state.ErrStateNotSynchronized) {
		return []state.ForkIDInterval{}, fmt.Errorf("error getting forkIDs from db. Error: %v", err)
	}
	numberForkIDs := len(forkIDIntervals)
	log.Debug("numberForkIDs: ", numberForkIDs)
	// var forkIDIntervals []state.ForkIDInterval
	if numberForkIDs == 0 {
		// Get last L1block Synced
		lastBlock, err := st.GetLastBlock(ctx, nil)
		if err != nil && !errors.Is(err, state.ErrStateNotSynchronized) {
			return []state.ForkIDInterval{}, fmt.Errorf("error checking lastL1BlockSynced. Error: %v", err)
		}
		if lastBlock != nil {
			log.Info("Getting forkIDs intervals. Please wait...")
			// Read Fork ID FROM POE SC
			forkIntervals, err := etherman.GetForks(ctx, genesisBlockNumber, lastBlock.BlockNumber)
			if err != nil {
				return []state.ForkIDInterval{}, fmt.Errorf("error getting forks. Please check the configuration. Error: %v", err)
			} else if len(forkIntervals) == 0 {
				return []state.ForkIDInterval{}, fmt.Errorf("error: no forkID received. It should receive at least one, please check the configuration...")
			}

			dbTx, err := st.BeginStateTransaction(ctx)
			if err != nil {
				return []state.ForkIDInterval{}, fmt.Errorf("error creating dbTx. Error: %v", err)
			}
			log.Info("Storing forkID intervals into db")
			// Store forkIDs
			for _, f := range forkIntervals {
				err := st.AddForkID(ctx, f, dbTx)
				if err != nil {
					log.Errorf("error adding forkID to db. Error: %v", err)
					rollbackErr := dbTx.Rollback(ctx)
					if rollbackErr != nil {
						log.Errorf("error rolling back dbTx. RollbackErr: %s. Error : %v", rollbackErr.Error(), err)
						return []state.ForkIDInterval{}, rollbackErr
					}
					return []state.ForkIDInterval{}, fmt.Errorf("error adding forkID to db. Error: %v", err)
				}
			}
			err = dbTx.Commit(ctx)
			if err != nil {
				log.Errorf("error committing dbTx. Error: %v", err)
				rollbackErr := dbTx.Rollback(ctx)
				if rollbackErr != nil {
					log.Errorf("error rolling back dbTx. RollbackErr: %s. Error : %v", rollbackErr.Error(), err)
					return []state.ForkIDInterval{}, rollbackErr
				}
				return []state.ForkIDInterval{}, fmt.Errorf("error committing dbTx. Error: %v", err)
			}
			forkIDIntervals = forkIntervals
		} else {
			log.Debug("Getting initial forkID")
			forkIntervals, err := etherman.GetForks(ctx, genesisBlockNumber, genesisBlockNumber)
			if err != nil {
				return []state.ForkIDInterval{}, fmt.Errorf("error getting forks. Please check the configuration. Error: %v", err)
			} else if len(forkIntervals) == 0 {
				return []state.ForkIDInterval{}, fmt.Errorf("error: no forkID received. It should receive at least one, please check the configuration...")
			}
			forkIDIntervals = forkIntervals
		}
	}
	return forkIDIntervals, nil
}