// Copyright (C) Immutability, LLC - All Rights Reserved
// Unauthorized copying of this file, via any medium is strictly prohibited
// Proprietary and confidential
// Written by Ino Murko <ino@omg.network>, July 2021

package ethereum

import (
	"bytes"
	"context"
	b64 "encoding/base64"
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/omgnetwork/immutability-eth-plugin/contracts/ovm_scc"
	"github.com/omgnetwork/immutability-eth-plugin/util"
	"golang.org/x/crypto/sha3"
)

const ovm string = "ovm"

func OvmPaths(b *PluginBackend) []*framework.Path {
	return []*framework.Path{

		{
			Pattern:         ContractPath(ovm, "appendStateBatch"),
			HelpSynopsis:    "Submits the state batch",
			HelpDescription: "Allows the sequencer to submit the state root batch.",
			Fields: map[string]*framework.FieldSchema{
				"name":    {Type: framework.TypeString, Description: "Name of the wallet."},
				"address": {Type: framework.TypeString, Description: "The address in the wallet."},
				"contract": {
					Type:        framework.TypeString,
					Description: "The address of the Block Controller.",
				},
				"gas_price": {
					Type:        framework.TypeString,
					Description: "The gas price for the transaction in wei.",
				},
				"nonce": {
					Type:        framework.TypeString,
					Description: "The nonce for the transaction.",
				},
				"should_start_at_element": {
					Type:        framework.TypeString,
					Description: "Index of the element at which this batch should start.",
				},
				"batch": {
					Type:        framework.TypeStringSlice,
					Description: "Batch of state roots.",
				},
			},
			ExistenceCheck: pathExistenceCheck,
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.CreateOperation: b.pathOvmAppendStateBatch,
			},
		},
	}
}

//this goes into L1
func (b *PluginBackend) pathOvmAppendStateBatch(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	log.Print(util.PrettyPrint(data))

	config, err := b.configured(ctx, req)
	if err != nil {
		return nil, err
	}
	address := data.Get("address").(string)
	name := data.Get("name").(string)
	contractAddress := common.HexToAddress(data.Get("contract").(string))
	accountJSON, err := readAccount(ctx, req, name, address)
	if err != nil || accountJSON == nil {
		return nil, fmt.Errorf("error reading address")
	}

	chainID := util.ValidNumber(config.ChainID)
	if chainID == nil {
		return nil, fmt.Errorf("invalid chain ID")
	}

	client, err := ethclient.Dial(config.getRPCURL())
	if err != nil {
		return nil, err
	}

	walletJSON, err := readWallet(ctx, req, name)
	if err != nil {
		return nil, err
	}

	wallet, account, err := getWalletAndAccount(*walletJSON, accountJSON.Index)
	if err != nil {
		return nil, err
	}

	// get the AppendStateBatch function arguments from JSON
	inputShouldStartAtElement, ok := data.GetOk("should_start_at_element")
	if !ok {
		return nil, fmt.Errorf("invalid should_start_at_element")
	}
	shouldStartAtElement := util.ValidNumber(inputShouldStartAtElement.(string))
	if shouldStartAtElement == nil {
		return nil, fmt.Errorf("invalid should_start_at_element")
	}

	inputBatch, ok := data.GetOk("batch")
	if !ok {
		return nil, fmt.Errorf("invalid batch")
	}
	var inputBatchArr []string = inputBatch.([]string)
	var batch = make([][32]byte, len(inputBatchArr))

	log.Println("hexutil.Encode(buf)")
	for i, s := range inputBatchArr {
		batchElement, err := b64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("invalid batch element - not base64")
		}
		var buf []byte
		hash := sha3.NewLegacyKeccak256()
		hash.Write(batchElement)
		buf = hash.Sum(buf)
		if len(buf) != 32 {
			return nil, fmt.Errorf("invalid batch element - not the right size")
		}
		batchByteElement := [32]byte{}
		copy(batchByteElement[:], buf[0:32])
		batch[i] = batchByteElement
	}
	// log.Print(batch)
	// get the AppendStateBatch function arguments from JSON DONE

	instance, err := ovm_scc.NewOvmScc(contractAddress, client)
	if err != nil {
		return nil, err
	}
	callOpts := &bind.CallOpts{}

	transactOpts, err := b.NewWalletTransactor(chainID, wallet, account)
	if err != nil {
		return nil, err
	}
	// transactOpts needs gas etc. Use supplied gas_price
	gasPriceRaw := data.Get("gas_price").(string)
	if gasPriceRaw == "" {
		return nil, fmt.Errorf("invalid gas_price")
	}
	transactOpts.GasPrice = util.ValidNumber(gasPriceRaw)

	// //transactOpts needs nonce. Use supplied nonce
	nonceRaw := data.Get("nonce").(string)
	if nonceRaw == "" {
		return nil, fmt.Errorf("invalid nonce")
	}
	transactOpts.Nonce = util.ValidNumber(nonceRaw)

	sccSession := &ovm_scc.OvmSccSession{
		Contract:     instance,  // Generic contract caller binding to set the session for
		CallOpts:     *callOpts, // Call options to use throughout this session
		TransactOpts: *transactOpts,
	}

	tx, err := sccSession.AppendStateBatch(batch, shouldStartAtElement)
	if err != nil {
		return nil, err
	}

	var signedTxBuff bytes.Buffer
	tx.EncodeRLP(&signedTxBuff)
	return &logical.Response{
		Data: map[string]interface{}{
			"contract":           contractAddress.Hex(),
			"transaction_hash":   tx.Hash().Hex(),
			"signed_transaction": hexutil.Encode(signedTxBuff.Bytes()),
			"from":               account.Address.Hex(),
			"nonce":              tx.Nonce(),
			"gas_price":          tx.GasPrice(),
			"gas_limit":          tx.Gas(),
		},
	}, nil
}
