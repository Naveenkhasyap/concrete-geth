// Copyright 2023 The concrete-geth Authors
//
// The concrete-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The concrete library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the concrete library. If not, see <http://www.gnu.org/licenses/>.

package testtool

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/concrete"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"golang.org/x/exp/slog"
)

// TODO: WIP -- missing parameter and file validation, and more

var (
	PrintLogs = true
)

func runTestMethod(concreteRegistry concrete.PrecompileRegistry, bytecode []byte, method abi.Method, shouldFail bool) (uint64, error) {
	var (
		key, _          = crypto.HexToECDSA("d17bd946feb884d463d58fb702b94dd0457ca349338da1d732a57856cf777ccd") // 0xCcca11AbAC28D9b6FceD3a9CA73C434f6b33B215
		senderAddress   = crypto.PubkeyToAddress(key.PublicKey)
		contractAddress = common.HexToAddress("cc73570000000000000000000000000000000000")
		gspec           = &core.Genesis{
			GasLimit: 2e9,
			Config:   params.TestChainConfig,
			Alloc: types.GenesisAlloc{
				senderAddress:   {Balance: big.NewInt(1e18)},
				contractAddress: {Balance: common.Big0, Code: bytecode},
			},
		}
		signer   = types.LatestSigner(gspec.Config)
		gasLimit = uint64(1e7)
		setupId  = crypto.Keccak256([]byte("setUp()"))[:4]
	)

	_, _, receipts := core.GenerateChainWithGenesisWithConcrete(gspec, ethash.NewFaker(), 1, concreteRegistry, func(ii int, block *core.BlockGen) {
		for _, id := range [][]byte{setupId, method.ID} {
			tx := types.NewTransaction(block.TxNonce(senderAddress), contractAddress, common.Big0, gasLimit, block.BaseFee(), id)
			signed, err := types.SignTx(tx, signer, key)
			if err != nil {
				panic(err)
			}
			block.AddTx(signed)
		}
	})

	if len(receipts[0]) != 2 {
		return 0, fmt.Errorf("expected 2 receipts, got %d", len(receipts))
	}
	setupReceipt := receipts[0][0]
	testReceipt := receipts[0][1]
	if setupReceipt.Status != types.ReceiptStatusSuccessful {
		return 0, fmt.Errorf("setup failed")
	}
	if (testReceipt.Status == types.ReceiptStatusSuccessful) == shouldFail {
		return 0, fmt.Errorf("test failed")
	}

	if PrintLogs && len(testReceipt.Logs) > 0 {
		fmt.Println("")
		for ii, log := range testReceipt.Logs {
			fmt.Printf("Logs[%d]\n", ii)
			fmt.Println("Address :", log.Address)
			if len(log.Topics) > 0 {
				fmt.Println("Topics  :", log.Topics[0])
				for _, topic := range log.Topics[1:] {
					fmt.Println("         ", topic)
				}
			}
			fmt.Println("Data    : 0x" + hex.EncodeToString(log.Data))
		}
		fmt.Println("")
	}

	return testReceipt.GasUsed, nil
}

func runTestContract(concreteRegistry concrete.PrecompileRegistry, bytecode []byte, ABI abi.ABI) (int, int) {
	passed := 0
	failed := 0
	for _, method := range ABI.Methods {
		if !strings.HasPrefix(method.Name, "test") {
			continue
		}
		shouldFail := strings.HasPrefix(method.Name, "testFail")
		gas, err := runTestMethod(concreteRegistry, bytecode, method, shouldFail)
		if err == nil {
			passed++
			fmt.Printf("[PASS] %s() (gas: %d)\n", method.Name, gas)
		} else {
			failed++
			fmt.Printf("[FAIL] %s() (gas: %d): %s\n", method.Name, gas, err)
		}
	}
	return passed, failed
}

func extractTestData(contractJsonBytes []byte) ([]byte, abi.ABI, string, string, error) {
	var jsonData struct {
		ABI              abi.ABI `json:"abi"`
		DeployedBytecode struct {
			Object string `json:"object"`
		} `json:"deployedBytecode"`
		Metadata struct {
			Settings struct {
				CompilationTarget map[string]string `json:"compilationTarget"`
			} `json:"settings"`
		} `json:"metadata"`
	}
	err := json.Unmarshal(contractJsonBytes, &jsonData)
	if err != nil {
		return nil, abi.ABI{}, "", "", err
	}
	bytecode := common.FromHex(jsonData.DeployedBytecode.Object)
	var testPath, contractName string
	for path, name := range jsonData.Metadata.Settings.CompilationTarget {
		testPath = path
		contractName = name
		break
	}
	return bytecode, jsonData.ABI, testPath, contractName, nil
}

func extractTestDataFromPath(contractJsonPath string) ([]byte, abi.ABI, string, string, error) {
	contractJsonBytes, err := os.ReadFile(contractJsonPath)
	if err != nil {
		return nil, abi.ABI{}, "", "", err
	}
	return extractTestData(contractJsonBytes)
}

func getFileNames(dir string, ext string) ([]string, error) {
	var fileNames []string

	files, err := os.ReadDir(dir)
	if err != nil {
		return fileNames, err
	}

	for _, file := range files {
		filePath := filepath.Join(dir, file.Name())
		if file.IsDir() {
			subDirFileNames, err := getFileNames(filePath, ext)
			if err != nil {
				return fileNames, err
			}
			fileNames = append(fileNames, subDirFileNames...)
		} else if ext == "" || filepath.Ext(file.Name()) == ext {
			fileNames = append(fileNames, file.Name())
		}
	}

	return fileNames, nil
}

func getTestPaths(testDir, outDir string) ([]string, error) {
	paths := make([]string, 0)
	seenFiles := make(map[string]struct{})
	testFileNames, err := getFileNames(testDir, ".sol")
	if err != nil {
		return nil, err
	}
	for _, fileName := range testFileNames {
		if _, ok := seenFiles[fileName]; ok {
			continue
		}
		seenFiles[fileName] = struct{}{}
		contractNames, err := getFileNames(filepath.Join(outDir, fileName), ".json")
		if err != nil {
			return nil, err
		}
		for _, contractName := range contractNames {
			path := filepath.Join(outDir, fileName, contractName)
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func runTestPaths(concreteRegistry concrete.PrecompileRegistry, contractJsonPaths []string) (int, int) {
	var totalPassed, totalFailed int
	startTime := time.Now()

	for _, path := range contractJsonPaths {
		bytecode, ABI, testPath, contractName, err := extractTestDataFromPath(path)
		if err != nil {
			fmt.Printf("Error extracting test data from %s: %s\n", path, err)
			continue
		}
		fmt.Printf("\nRunning tests for %s:%s\n", testPath, contractName)
		passed, failed := runTestContract(concreteRegistry, bytecode, ABI)
		totalPassed += passed
		totalFailed += failed
	}

	timeMs := float64(time.Since(startTime).Microseconds()) / 1000

	var result string
	if totalFailed == 0 {
		result = "ok"
	} else {
		result = "FAILED"
	}

	fmt.Printf("\nTest result: %s. %d passed; %d failed; finished in %.2fms\n", result, totalPassed, totalFailed, timeMs)

	return totalPassed, totalFailed
}

func setGethVerbosity(lvl slog.Level) func() {
	handler := log.Root()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, lvl, true)))
	return func() {
		log.SetDefault(handler)
	}
}

type TestConfig struct {
	Contract string
	TestDir  string
	OutDir   string
}

func RunTestContract(concreteRegistry concrete.PrecompileRegistry, bytecode []byte, ABI abi.ABI) (int, int) {
	resetGethLogger := setGethVerbosity(log.LevelWarn)
	defer resetGethLogger()
	return runTestContract(concreteRegistry, bytecode, ABI)
}

func Test(concreteRegistry concrete.PrecompileRegistry, config TestConfig) (int, int) {
	resetGethLogger := setGethVerbosity(log.LevelWarn)
	defer resetGethLogger()

	// Get test paths
	var testPaths []string

	if config.Contract != "" {
		parts := strings.SplitN(config.Contract, ":", 2)
		if len(parts) != 2 {
			fmt.Printf("Invalid contract: %s. Must follow format Path:Contract\n", config.Contract)
			os.Exit(1)
		}
		_, fileName := filepath.Split(parts[0])
		contractName := parts[1]
		path := filepath.Join(config.OutDir, fileName, contractName+".json")
		testPaths = []string{path}
	} else {
		var err error
		testPaths, err = getTestPaths(config.TestDir, config.OutDir)
		if err != nil {
			fmt.Printf("Error getting test paths: %s\n", err)
			os.Exit(1)
		}
	}

	// Run tests
	return runTestPaths(concreteRegistry, testPaths)
}
