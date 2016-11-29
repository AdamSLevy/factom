// Copyright 2016 Factom Foundation
// Use of this source code is governed by the MIT
// license that can be found in the LICENSE file.

package wsapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FactomProject/btcutil/certs"
	"github.com/FactomProject/factom"
	"github.com/FactomProject/factom/wallet"
	"github.com/FactomProject/factomd/common/interfaces"
	"github.com/FactomProject/web"
)

const APIVersion string = "2.0"

var (
	webServer *web.Server
	fctWallet *wallet.Wallet
	rpcUser   string
	rpcPass   string
	authsha   []byte
)

// httpBasicAuth returns the UTF-8 bytes of the HTTP Basic authentication
// string:
//
//   "Basic " + base64(username + ":" + password)
func httpBasicAuth(username, password string) []byte {
	const header = "Basic "
	base64 := base64.StdEncoding

	b64InputLen := len(username) + len(":") + len(password)
	b64Input := make([]byte, 0, b64InputLen)
	b64Input = append(b64Input, username...)
	b64Input = append(b64Input, ':')
	b64Input = append(b64Input, password...)

	output := make([]byte, len(header)+base64.EncodedLen(b64InputLen))
	copy(output, header)
	base64.Encode(output[len(header):], b64Input)
	return output
}

func genCertPair(certFile string, keyFile string, extraAddress string) error {
	fmt.Println("Generating TLS certificates...")

	org := "factom autogenerated cert"
	validUntil := time.Now().Add(10 * 365 * 24 * time.Hour)

	var externalAddresses []string
	if extraAddress != "" {
		externalAddresses = strings.Split(extraAddress, ",")
		for _, i := range externalAddresses {
			fmt.Printf("adding %s to certificate\n", i)
		}
	}

	cert, key, err := certs.NewTLSCertPair(org, validUntil, externalAddresses)
	if err != nil {
		return err
	}

	// Write cert and key files.
	if err = ioutil.WriteFile(certFile, cert, 0666); err != nil {
		return err
	}
	if err = ioutil.WriteFile(keyFile, key, 0600); err != nil {
		os.Remove(certFile)
		return err
	}

	fmt.Println("Done generating TLS certificates")
	return nil
}

// filesExists reports whether the named file or directory exists.
func fileExists(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func Start(w *wallet.Wallet, net string, c factom.RPCConfig) {
	webServer = web.NewServer()
	fctWallet = w

	rpcUser = c.WalletRPCUser
	rpcPass = c.WalletRPCPassword

	h := sha256.New()
	h.Write(httpBasicAuth(rpcUser, rpcPass))
	authsha = h.Sum(nil) //set this in the beginning to prevent timing attacks

	webServer.Post("/v2", handleV2)
	webServer.Get("/v2", handleV2)

	if c.WalletTLSEnable == false {
		webServer.Run(net)
	} else {
		if !fileExists(c.WalletTLSKeyFile) && !fileExists(c.WalletTLSCertFile) {
			err := genCertPair(c.WalletTLSCertFile, c.WalletTLSKeyFile, c.WalletServer)
			if err != nil {
				log.Fatal(err)
			}
		}
		keypair, err := tls.LoadX509KeyPair(c.WalletTLSCertFile, c.WalletTLSKeyFile)
		if err != nil {
			log.Fatal(err)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{keypair},
			MinVersion:   tls.VersionTLS12,
		}
		webServer.RunTLS(net, tlsConfig)
	}
}

func Stop() {
	fctWallet.Close()
	webServer.Close()
}

func checkAuthHeader(r *http.Request) error {
	if "" == rpcUser {
		//no username was specified in the config file or command line, meaning factomd API is open access
		return nil
	}
	authhdr := r.Header["Authorization"]
	if len(authhdr) == 0 {
		fmt.Println("Username and Password expected, but none were received")
		return errors.New("no auth")
	}
	fmt.Println(authhdr)

	h := sha256.New()
	h.Write([]byte(authhdr[0]))
	presentedPassHash := h.Sum(nil)
	cmp := subtle.ConstantTimeCompare(presentedPassHash, authsha) //compare hashes because ConstantTimeCompare takes a constant time based on the slice size.  hashing gives a constant slice size.
	if cmp != 1 {
		fmt.Println("Incorrect Username and/or Password were received")
		return errors.New("bad auth")
	}
	return nil
}

func handleV2(ctx *web.Context) {
	if err := checkAuthHeader(ctx.Request); err != nil {
		remoteIP := ""
		remoteIP += strings.Split(ctx.Request.RemoteAddr, ":")[0]
		fmt.Printf("Unauthorized API client connection attempt from %s\n", remoteIP)
		ctx.ResponseWriter.Header().Add("WWW-Authenticate", `Basic realm="factomd RPC"`)
		http.Error(ctx.ResponseWriter, "401 Unauthorized.", http.StatusUnauthorized)
		return
	}
	body, err := ioutil.ReadAll(ctx.Request.Body)
	if err != nil {
		handleV2Error(ctx, nil, newInvalidRequestError())
		return
	}

	j, err := factom.ParseJSON2Request(string(body))
	if err != nil {
		handleV2Error(ctx, nil, newInvalidRequestError())
		return
	}

	jsonResp, jsonError := handleV2Request(j)

	if jsonError != nil {
		handleV2Error(ctx, j, jsonError)
		return
	}

	ctx.Write([]byte(jsonResp.String()))
}

func handleV2Request(j *factom.JSON2Request) (*factom.JSON2Response, *factom.JSONError) {
	var resp interface{}
	var jsonError *factom.JSONError
	params := []byte(j.Params)

	switch j.Method {
	case "address":
		resp, jsonError = handleAddress(params)
	case "all-addresses":
		resp, jsonError = handleAllAddresses(params)
	case "generate-ec-address":
		resp, jsonError = handleGenerateECAddress(params)
	case "generate-factoid-address":
		resp, jsonError = handleGenerateFactoidAddress(params)
	case "import-addresses":
		resp, jsonError = handleImportAddresses(params)
	case "import-koinify":
		resp, jsonError = handleImportKoinify(params)
	case "wallet-backup":
		resp, jsonError = handleWalletBackup(params)
	case "transactions":
		resp, jsonError = handleAllTransactions(params)
	case "new-transaction":
		resp, jsonError = handleNewTransaction(params)
	case "delete-transaction":
		resp, jsonError = handleDeleteTransaction(params)
	case "tmp-transactions":
		resp, jsonError = handleTmpTransactions(params)
	case "transaction-hash":
		resp, jsonError = handleTransactionHash(params)
	case "add-input":
		resp, jsonError = handleAddInput(params)
	case "add-output":
		resp, jsonError = handleAddOutput(params)
	case "add-ec-output":
		resp, jsonError = handleAddECOutput(params)
	case "add-fee":
		resp, jsonError = handleAddFee(params)
	case "sub-fee":
		resp, jsonError = handleSubFee(params)
	case "sign-transaction":
		resp, jsonError = handleSignTransaction(params)
	case "compose-transaction":
		resp, jsonError = handleComposeTransaction(params)
	case "properties":
		resp, jsonError = handleProperties(params)
	default:
		jsonError = newMethodNotFoundError()
	}
	if jsonError != nil {
		return nil, jsonError
	}

	jsonResp := factom.NewJSON2Response()
	jsonResp.ID = j.ID
	if b, err := json.Marshal(resp); err != nil {
		return nil, newCustomInternalError(err.Error())
	} else {
		jsonResp.Result = b
	}

	return jsonResp, nil
}

func handleAddress(params []byte) (interface{}, *factom.JSONError) {
	req := new(addressRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	resp := new(addressResponse)
	switch factom.AddressStringType(req.Address) {
	case factom.ECPub:
		e, err := fctWallet.GetECAddress(req.Address)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		if e == nil {
			return nil, newCustomInternalError("Wallet: address not found")
		}
		resp = mkAddressResponse(e)
	case factom.FactoidPub:
		f, err := fctWallet.GetFCTAddress(req.Address)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		resp = mkAddressResponse(f)
	default:
		return nil, newCustomInternalError("Invalid address type")
	}

	return resp, nil
}

func handleAllAddresses(params []byte) (interface{}, *factom.JSONError) {
	resp := new(multiAddressResponse)

	fs, es, err := fctWallet.GetAllAddresses()
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	for _, f := range fs {
		a := mkAddressResponse(f)
		resp.Addresses = append(resp.Addresses, a)
	}
	for _, e := range es {
		a := mkAddressResponse(e)
		resp.Addresses = append(resp.Addresses, a)
	}

	return resp, nil
}

func handleGenerateFactoidAddress(params []byte) (interface{}, *factom.JSONError) {
	a, err := fctWallet.GenerateFCTAddress()
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}

	resp := mkAddressResponse(a)

	return resp, nil
}

func handleGenerateECAddress(params []byte) (interface{}, *factom.JSONError) {
	a, err := fctWallet.GenerateECAddress()
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}

	resp := mkAddressResponse(a)

	return resp, nil
}

func handleImportAddresses(params []byte) (interface{}, *factom.JSONError) {
	req := new(importRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	resp := new(multiAddressResponse)
	for _, v := range req.Addresses {
		switch factom.AddressStringType(v.Secret) {
		case factom.FactoidSec:
			f, err := factom.GetFactoidAddress(v.Secret)
			if err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			if err := fctWallet.InsertFCTAddress(f); err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			a := mkAddressResponse(f)
			resp.Addresses = append(resp.Addresses, a)
		case factom.ECSec:
			e, err := factom.GetECAddress(v.Secret)
			if err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			if err := fctWallet.InsertECAddress(e); err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			a := mkAddressResponse(e)
			resp.Addresses = append(resp.Addresses, a)
		default:
			return nil, newCustomInternalError("address could not be imported")
		}
	}
	return resp, nil
}

func handleImportKoinify(params []byte) (interface{}, *factom.JSONError) {
	req := new(importKoinifyRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	f, err := factom.MakeFactoidAddressFromKoinify(req.Words)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	if err := fctWallet.InsertFCTAddress(f); err != nil {
		return nil, newCustomInternalError(err.Error())
	}

	return mkAddressResponse(f), nil
}

func handleWalletBackup(params []byte) (interface{}, *factom.JSONError) {
	resp := new(walletBackupResponse)

	if seed, err := fctWallet.GetSeed(); err != nil {
		return nil, newCustomInternalError(err.Error())
	} else {
		resp.Seed = seed
	}

	fs, es, err := fctWallet.GetAllAddresses()
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	for _, f := range fs {
		a := mkAddressResponse(f)
		resp.Addresses = append(resp.Addresses, a)
	}
	for _, e := range es {
		a := mkAddressResponse(e)
		resp.Addresses = append(resp.Addresses, a)
	}

	return resp, nil
}

func handleAllTransactions(params []byte) (interface{}, *factom.JSONError) {
	if fctWallet.TXDB() == nil {
		return nil, newCustomInternalError(
			"Wallet does not have a transaction database")
	}
	req := new(txdbRequest)
	if params != nil {
		err := json.Unmarshal(params, req)
		if err != nil {
			return nil, newInvalidParamsError()
		}
	}

	resp := new(multiTransactionResponse)

	switch {
	case req == nil:
		txs, err := fctWallet.TXDB().GetAllTXs()
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		for _, tx := range txs {
			r, err := factoidTxToTransaction(tx)
			if err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			resp.Transactions = append(resp.Transactions, r)
		}
	case req.TxID != "":
		tx, err := fctWallet.TXDB().GetTX(req.TxID)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		r, err := factoidTxToTransaction(tx)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		resp.Transactions = append(resp.Transactions, r)
	case req.Address != "":
		txs, err := fctWallet.TXDB().GetTXAddress(req.Address)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		for _, tx := range txs {
			r, err := factoidTxToTransaction(tx)
			if err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			resp.Transactions = append(resp.Transactions, r)
		}
	case req.Range.End != 0:
		txs, err := fctWallet.TXDB().GetTXRange(req.Range.Start, req.Range.End)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		for _, tx := range txs {
			r, err := factoidTxToTransaction(tx)
			if err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			resp.Transactions = append(resp.Transactions, r)
		}
	default:
		txs, err := fctWallet.TXDB().GetAllTXs()
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		for _, tx := range txs {
			r, err := factoidTxToTransaction(tx)
			if err != nil {
				return nil, newCustomInternalError(err.Error())
			}
			resp.Transactions = append(resp.Transactions, r)
		}
	}

	return resp, nil
}

// transaction handlers

func handleNewTransaction(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	if err := fctWallet.NewTransaction(req.Name); err != nil {
		return nil, newCustomInternalError(err.Error())
	}

	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleDeleteTransaction(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	if err := fctWallet.DeleteTransaction(req.Name); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp := &factom.Transaction{Name: req.Name}
	return resp, nil
}

func handleTmpTransactions(params []byte) (interface{}, *factom.JSONError) {
	resp := new(multiTransactionResponse)
	txs := fctWallet.GetTransactions()

	for name, tx := range txs {
		r, err := factoidTxToTransaction(tx)
		if err != nil {
			return nil, newCustomInternalError(err.Error())
		}
		r.Name = name
		resp.Transactions = append(resp.Transactions, r)
	}

	return resp, nil
}

func handleTransactionHash(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	resp := new(factom.Transaction)
	txs := fctWallet.GetTransactions()

	for name, tx := range txs {
		if name == req.Name {
			resp.Name = name
			resp.TxID = tx.GetSigHash().String()
			return resp, nil
		}
	}

	return nil, newCustomInternalError("Transaction not found")
}

func handleAddInput(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionValueRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	if err := fctWallet.AddInput(req.Name, req.Address, req.Amount); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleAddOutput(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionValueRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	if err := fctWallet.AddOutput(req.Name, req.Address, req.Amount); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleAddECOutput(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionValueRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	if err := fctWallet.AddECOutput(req.Name, req.Address, req.Amount); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleAddFee(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionAddressRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	rate, err := factom.GetRate()
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	if err := fctWallet.AddFee(req.Name, req.Address, rate); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleSubFee(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionAddressRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	rate, err := factom.GetRate()
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	if err := fctWallet.SubFee(req.Name, req.Address, rate); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleSignTransaction(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	if err := fctWallet.SignTransaction(req.Name); err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	tx := fctWallet.GetTransactions()[req.Name]
	resp, err := factoidTxToTransaction(tx)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	resp.Name = req.Name

	return resp, nil
}

func handleComposeTransaction(params []byte) (interface{}, *factom.JSONError) {
	req := new(transactionRequest)
	if err := json.Unmarshal(params, req); err != nil {
		return nil, newInvalidParamsError()
	}

	t, err := fctWallet.ComposeTransaction(req.Name)
	if err != nil {
		return nil, newCustomInternalError(err.Error())
	}
	return t, nil
}

func handleProperties(params []byte) (interface{}, *factom.JSONError) {
	props := new(propertiesResponse)
	props.WalletVersion = fctWallet.GetVersion()
	props.WalletApiVersion = fctWallet.GetApiVersion()
	return props, nil
}

// utility functions

type addressResponder interface {
	String() string
	SecString() string
}

func mkAddressResponse(a addressResponder) *addressResponse {
	r := new(addressResponse)
	r.Public = a.String()
	r.Secret = a.SecString()
	return r
}

func factoidTxToTransaction(t interfaces.ITransaction) (
	*factom.Transaction,
	error,
) {
	r := new(factom.Transaction)
	r.TxID = hex.EncodeToString(t.GetSigHash().Bytes())

	r.BlockHeight = t.GetBlockHeight()
	r.Timestamp = t.GetTimestamp().GetTime()

	if len(t.GetSignatureBlocks()) > 0 {
		if err := t.ValidateSignatures(); err == nil {
			r.IsSigned = true
		}
	}

	if i, err := t.TotalInputs(); err != nil {
		return nil, err
	} else {
		r.TotalInputs = i
	}

	if i, err := t.TotalOutputs(); err != nil {
		return nil, err
	} else {
		r.TotalOutputs = i
	}

	if i, err := t.TotalECs(); err != nil {
		return nil, err
	} else {
		r.TotalECOutputs = i
	}

	for _, v := range t.GetInputs() {
		tmp := new(factom.TransAddress)
		tmp.Address = v.GetUserAddress()
		tmp.Amount = v.GetAmount()
		r.Inputs = append(r.Inputs, tmp)
	}

	for _, v := range t.GetOutputs() {
		tmp := new(factom.TransAddress)
		tmp.Address = v.GetUserAddress()
		tmp.Amount = v.GetAmount()
		r.Outputs = append(r.Outputs, tmp)
	}

	for _, v := range t.GetECOutputs() {
		tmp := new(factom.TransAddress)
		tmp.Address = v.GetUserAddress()
		tmp.Amount = v.GetAmount()
		r.ECOutputs = append(r.ECOutputs, tmp)
	}

	if r.TotalInputs <= r.TotalOutputs+r.TotalECOutputs {
		r.FeesPaid = 0
		r.FeesRequired = r.FeesRequired
	} else {
		r.FeesPaid = r.TotalInputs - (r.TotalOutputs + r.TotalECOutputs)
	}

	// get the ec rate and calulate the fee if it is a new transaction
	if !r.IsSigned {
		rate, err := factom.GetRate()
		if err != nil {
			rate = 0
		}
		if i, err := t.CalculateFee(rate); err != nil {
			return nil, err
		} else {
			r.FeesRequired = i
		}
	}

	return r, nil
}
