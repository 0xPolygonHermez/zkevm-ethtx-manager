package config

// DefaultValues is the default configuration
const DefaultValues = `
[Log]
Environment = "development" # "production" or "development"
Level = "info"
Outputs = ["stderr"]

[Etherman]
URL = "http://localhost:8545"
ForkIDChunkSize = 20000
MultiGasProvider = false
	[Etherman.Etherscan]
		ApiKey = ""

[EthTxManager]
FrequencyToMonitorTxs = "1s"
WaitTxToBeMined = "2m"
ForcedGas = 0
GasPriceMarginFactor = 1
MaxGasPriceLimit = 0
	[State.DB]
		User = "ethtxmanager_user"
		Password = "ethtxmanager_password"
		Name = "ethtxmanager_db"
		Host = "zkevm-ethtxmanager-db"
		Port = "5432"
		EnableLog = false	
		MaxConns = 200

[Metrics]
Host = "0.0.0.0"
Port = 9091
Enabled = false
`
