package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MixinNetwork/mixin/common"
	"github.com/MixinNetwork/mixin/crypto"
	"github.com/MixinNetwork/mixin/storage"
	"github.com/urfave/cli"
)

func createAdressCmd(c *cli.Context) error {
	seed := make([]byte, 64)
	_, err := rand.Read(seed)
	if err != nil {
		return err
	}
	addr := common.NewAddressFromSeed(seed)
	if view := c.String("view"); len(view) > 0 {
		key, err := hex.DecodeString(view)
		if err != nil {
			return err
		}
		copy(addr.PrivateViewKey[:], key)
		addr.PublicViewKey = addr.PrivateViewKey.Public()
	}
	if spend := c.String("spend"); len(spend) > 0 {
		key, err := hex.DecodeString(spend)
		if err != nil {
			return err
		}
		copy(addr.PrivateSpendKey[:], key)
		addr.PublicSpendKey = addr.PrivateSpendKey.Public()
	}
	if c.Bool("public") {
		addr.PrivateViewKey = addr.PublicSpendKey.DeterministicHashDerive()
		addr.PublicViewKey = addr.PrivateViewKey.Public()
	}
	fmt.Printf("address:\t%s\n", addr.String())
	fmt.Printf("view key:\t%s\n", addr.PrivateViewKey.String())
	fmt.Printf("spend key:\t%s\n", addr.PrivateSpendKey.String())
	return nil
}

func decodeAddressCmd(c *cli.Context) error {
	addr, err := common.NewAddressFromString(c.String("address"))
	if err != nil {
		return err
	}
	fmt.Printf("public view key:\t%s\n", addr.PublicViewKey.String())
	fmt.Printf("public spend key:\t%s\n", addr.PublicSpendKey.String())
	fmt.Printf("spend derive private:\t%s\n", addr.PublicSpendKey.DeterministicHashDerive())
	fmt.Printf("spend derive public:\t%s\n", addr.PublicSpendKey.DeterministicHashDerive().Public())
	return nil
}

func updateHeadReference(c *cli.Context) error {
	store, err := storage.NewBadgerStore(c.String("dir"))
	if err != nil {
		return err
	}
	defer store.Close()
	node, err := crypto.HashFromString(c.String("node"))
	if err != nil {
		return err
	}
	round, err := store.ReadRound(node)
	if err != nil {
		return err
	}
	if round == nil {
		return errors.New("node not found")
	}
	fmt.Printf("node: %s round: %d self: %s external: %s\n", round.NodeId.String(), round.Number, round.References.Self.String(), round.References.External.String())
	if round.Number != c.Uint64("round") {
		return fmt.Errorf("round number not match %d", round.Number)
	}
	external, err := hex.DecodeString(c.String("external"))
	if err != nil {
		return err
	}
	copy(round.References.External[:], external)
	return store.UpdateEmptyHeadRound(round.NodeId, round.Number, round.References)
}

func removeGraphEntries(c *cli.Context) error {
	store, err := storage.NewBadgerStore(c.String("dir"))
	if err != nil {
		return err
	}
	defer store.Close()
	return store.RemoveGraphEntries(c.String("prefix"))
}

func validateGraphEntries(c *cli.Context) error {
	store, err := storage.NewBadgerStore(c.String("dir"))
	if err != nil {
		return err
	}
	defer store.Close()
	var state struct{ Id crypto.Hash }
	_, err = store.StateGet("network", &state)
	if err != nil {
		return err
	}
	total, invalid, err := store.ValidateGraphEntries(state.Id)
	if err != nil {
		return err
	}
	fmt.Printf("invalid entries: %d/%d\n", invalid, total)
	return nil
}

func decodeTransactionCmd(c *cli.Context) error {
	raw, err := hex.DecodeString(c.String("raw"))
	if err != nil {
		return err
	}
	var tx common.SignedTransaction
	err = common.MsgpackUnmarshal(raw, &tx)
	if err != nil {
		return err
	}
	ver := transactionToMap(tx.AsLatestVersion())
	data, err := json.MarshalIndent(ver, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func signTransactionCmd(c *cli.Context) error {
	var raw signerInput
	err := json.Unmarshal([]byte(c.String("raw")), &raw)
	if err != nil {
		return err
	}
	raw.Node = c.String("node")

	seed, err := hex.DecodeString(c.String("seed"))
	if err != nil {
		return err
	}
	if len(seed) != 64 {
		seed = make([]byte, 64)
		_, err := rand.Read(seed)
		if err != nil {
			return err
		}
	}

	tx := common.NewTransaction(raw.Asset)
	for _, in := range raw.Inputs {
		if in.Deposit != nil {
			tx.AddDepositInput(in.Deposit)
		} else {
			tx.AddInput(in.Hash, in.Index)
		}
	}

	for _, out := range raw.Outputs {
		hash := crypto.NewHash(seed)
		seed = append(hash[:], hash[:]...)
		tx.AddOutputWithType(out.Type, out.Accounts, out.Script, out.Amount, seed)
	}

	extra, err := hex.DecodeString(raw.Extra)
	if err != nil {
		return err
	}
	tx.Extra = extra

	key, err := hex.DecodeString(c.String("key"))
	if err != nil {
		return err
	}
	if len(key) != 64 {
		return fmt.Errorf("invalid key length %d", len(key))
	}
	var account common.Address
	copy(account.PrivateViewKey[:], key[:32])
	copy(account.PrivateSpendKey[:], key[32:])

	signed := tx.AsLatestVersion()
	for i, _ := range signed.Inputs {
		err := signed.SignInput(raw, i, []common.Address{account})
		if err != nil {
			return err
		}
	}
	fmt.Println(hex.EncodeToString(signed.Marshal()))
	return nil
}

func sendTransactionCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "sendrawtransaction", []interface{}{
		c.String("raw"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func cancelNodeCmd(c *cli.Context) error {
	seed := make([]byte, 64)
	_, err := rand.Read(seed)
	if err != nil {
		return err
	}
	viewKey, err := crypto.KeyFromString(c.String("view"))
	if err != nil {
		return err
	}
	spendKey, err := crypto.KeyFromString(c.String("spend"))
	if err != nil {
		return err
	}
	receiver, err := common.NewAddressFromString(c.String("receiver"))
	if err != nil {
		return err
	}
	account := common.Address{
		PrivateViewKey:  viewKey,
		PrivateSpendKey: spendKey,
		PublicViewKey:   viewKey.Public(),
		PublicSpendKey:  spendKey.Public(),
	}
	if account.String() != receiver.String() {
		return fmt.Errorf("invalid key and receiver %s %s", account, receiver)
	}

	b, err := hex.DecodeString(c.String("pledge"))
	if err != nil {
		return err
	}
	pledge, err := common.UnmarshalVersionedTransaction(b)
	if err != nil {
		return err
	}
	if pledge.TransactionType() != common.TransactionTypeNodePledge {
		return fmt.Errorf("invalid pledge transaction type %d", pledge.TransactionType())
	}

	b, err = hex.DecodeString(c.String("source"))
	if err != nil {
		return err
	}
	source, err := common.UnmarshalVersionedTransaction(b)
	if err != nil {
		return err
	}
	if source.TransactionType() != common.TransactionTypeScript {
		return fmt.Errorf("invalid source transaction type %d", source.TransactionType())
	}

	if source.PayloadHash() != pledge.Inputs[0].Hash {
		return fmt.Errorf("invalid source transaction hash %s %s", source.PayloadHash(), pledge.Inputs[0].Hash)
	}
	if len(source.Outputs) != 1 || len(source.Outputs[0].Keys) != 1 {
		return fmt.Errorf("invalid source transaction outputs %d %d", len(source.Outputs), len(source.Outputs[0].Keys))
	}
	pig := crypto.ViewGhostOutputKey(&source.Outputs[0].Keys[0], &viewKey, &source.Outputs[0].Mask, 0)
	if pig.String() != receiver.PublicSpendKey.String() {
		return fmt.Errorf("invalid source and receiver %s %s", pig.String(), receiver.PublicSpendKey)
	}

	tx := common.NewTransaction(common.XINAssetId)
	tx.AddInput(pledge.PayloadHash(), 0)
	tx.AddOutputWithType(common.OutputTypeNodeCancel, nil, common.Script{}, pledge.Outputs[0].Amount.Div(100), seed)
	tx.AddScriptOutput([]common.Address{receiver}, common.NewThresholdScript(1), pledge.Outputs[0].Amount.Sub(tx.Outputs[0].Amount), seed)
	tx.Extra = append(pledge.Extra, viewKey[:]...)
	utxo := &common.UTXO{
		Input: common.Input{
			Hash:  pledge.PayloadHash(),
			Index: 0,
		},
		Output: common.Output{
			Type: common.OutputTypeNodePledge,
			Keys: source.Outputs[0].Keys,
			Mask: source.Outputs[0].Mask,
		},
	}
	signed := tx.AsLatestVersion()
	err = signed.SignUTXO(utxo, []common.Address{account})
	if err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(signed.Marshal()))
	return nil
}

func decodePledgeNodeCmd(c *cli.Context) error {
	b, err := hex.DecodeString(c.String("raw"))
	if err != nil {
		return err
	}
	pledge, err := common.UnmarshalVersionedTransaction(b)
	if err != nil {
		return err
	}
	if len(pledge.Extra) != len(crypto.Key{})*2 {
		return fmt.Errorf("invalid extra %s", hex.EncodeToString(pledge.Extra))
	}
	signerPublicSpend, err := crypto.KeyFromString(hex.EncodeToString(pledge.Extra[:32]))
	if err != nil {
		return err
	}
	payeePublicSpend, err := crypto.KeyFromString(hex.EncodeToString(pledge.Extra[32:]))
	if err != nil {
		return err
	}
	signer := common.Address{
		PublicSpendKey: signerPublicSpend,
		PublicViewKey:  signerPublicSpend.DeterministicHashDerive().Public(),
	}
	payee := common.Address{
		PublicSpendKey: payeePublicSpend,
		PublicViewKey:  payeePublicSpend.DeterministicHashDerive().Public(),
	}
	fmt.Printf("signer: %s\n", signer)
	fmt.Printf("payee: %s\n", payee)
	return nil
}

func getRoundLinkCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "getroundlink", []interface{}{
		c.String("from"),
		c.String("to"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func getRoundByNumberCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "getroundbynumber", []interface{}{
		c.String("id"),
		c.Uint64("number"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func getRoundByHashCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "getroundbyhash", []interface{}{
		c.String("hash"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func listSnapshotsCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "listsnapshots", []interface{}{
		c.Uint64("since"),
		c.Uint64("count"),
		c.Bool("sig"),
		c.Bool("tx"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func getSnapshotCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "getsnapshot", []interface{}{
		c.String("hash"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func getTransactionCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "gettransaction", []interface{}{
		c.String("hash"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func getUTXOCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "getutxo", []interface{}{
		c.String("hash"),
		c.Uint64("index"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func listMintDistributionsCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "listmintdistributions", []interface{}{
		c.Uint64("since"),
		c.Uint64("count"),
		c.Bool("tx"),
	})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func getInfoCmd(c *cli.Context) error {
	data, err := callRPC(c.String("node"), "getinfo", []interface{}{})
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func setupTestNetCmd(c *cli.Context) error {
	var signers, payees []common.Address

	randomPubAccount := func() common.Address {
		seed := make([]byte, 64)
		_, err := rand.Read(seed)
		if err != nil {
			panic(err)
		}
		account := common.NewAddressFromSeed(seed)
		account.PrivateViewKey = account.PublicSpendKey.DeterministicHashDerive()
		account.PublicViewKey = account.PrivateViewKey.Public()
		return account
	}
	for i := 0; i < 7; i++ {
		signers = append(signers, randomPubAccount())
		payees = append(payees, randomPubAccount())
	}

	inputs := make([]map[string]string, 0)
	for i, _ := range signers {
		inputs = append(inputs, map[string]string{
			"signer":  signers[i].String(),
			"payee":   payees[i].String(),
			"balance": "10000",
		})
	}
	genesis := map[string]interface{}{
		"epoch": time.Now().Unix(),
		"nodes": inputs,
		"domains": []map[string]string{
			{
				"signer":  signers[0].String(),
				"balance": "50000",
			},
		},
	}
	genesisData, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(genesisData))

	nodes := make([]map[string]string, 0)
	for i, a := range signers {
		nodes = append(nodes, map[string]string{
			"host":   fmt.Sprintf("127.0.0.1:700%d", i+1),
			"signer": a.String(),
		})
	}
	nodesData, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(nodesData))

	for i, a := range signers {
		dir := fmt.Sprintf("/tmp/mixin-700%d", i+1)
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return err
		}

		store, err := storage.NewBadgerStore(dir)
		if err != nil {
			return err
		}
		defer store.Close()

		err = store.StateSet("account", a)
		if err != nil {
			return err
		}

		configData, err := json.MarshalIndent(map[string]interface{}{
			"signer":         a.PrivateSpendKey.String(),
			"listener":       nodes[i]["host"],
			"cache-ttl":      3600,
			"max-cache-size": 128,
		}, "", "  ")
		if err != nil {
			return err
		}

		err = ioutil.WriteFile(dir+"/config.json", configData, 0644)
		if err != nil {
			return err
		}
		err = ioutil.WriteFile(dir+"/genesis.json", genesisData, 0644)
		if err != nil {
			return err
		}
		err = ioutil.WriteFile(dir+"/nodes.json", nodesData, 0644)
		if err != nil {
			return err
		}
	}
	return nil
}

var httpClient *http.Client

func callRPC(node, method string, params []interface{}) ([]byte, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := json.Marshal(map[string]interface{}{
		"method": method,
		"params": params,
	})
	if err != nil {
		panic(err)
	}

	endpoint := "http://" + node
	if strings.HasPrefix(node, "http") {
		endpoint = node
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data  interface{} `json:"data"`
		Error interface{} `json:"error"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	err = dec.Decode(&result)
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("ERROR %s", result.Error)
	}

	return json.Marshal(result.Data)
}

type signerInput struct {
	Inputs []struct {
		Hash    crypto.Hash         `json:"hash"`
		Index   int                 `json:"index"`
		Deposit *common.DepositData `json:"deposit,omitempty"`
		Keys    []crypto.Key        `json:"keys"`
		Mask    crypto.Key          `json:"mask"`
	} `json:"inputs"`
	Outputs []struct {
		Type     uint8            `json:"type"`
		Script   common.Script    `json:"script"`
		Accounts []common.Address `json:"accounts"`
		Amount   common.Integer   `json:"amount"`
	}
	Asset crypto.Hash `json:"asset"`
	Extra string      `json:"extra"`
	Node  string      `json:"-"`
}

func (raw signerInput) ReadUTXO(hash crypto.Hash, index int) (*common.UTXOWithLock, error) {
	utxo := &common.UTXOWithLock{}

	for _, in := range raw.Inputs {
		if in.Hash == hash && in.Index == index && len(in.Keys) > 0 {
			utxo.Keys = in.Keys
			utxo.Mask = in.Mask
			return utxo, nil
		}
	}

	data, err := callRPC(raw.Node, "getutxo", []interface{}{hash.String(), index})
	if err != nil {
		return nil, err
	}
	var out common.UTXOWithLock
	err = json.Unmarshal(data, &out)
	if err != nil {
		return nil, err
	}
	if out.Amount.Sign() == 0 {
		return nil, fmt.Errorf("invalid input %s#%d", hash.String(), index)
	}
	utxo.Keys = out.Keys
	utxo.Mask = out.Mask
	return utxo, nil
}

func (raw signerInput) CheckDepositInput(deposit *common.DepositData, tx crypto.Hash) error {
	return nil
}

func (raw signerInput) ReadLastMintDistribution(group string) (*common.MintDistribution, error) {
	return nil, nil
}

func transactionToMap(tx *common.VersionedTransaction) map[string]interface{} {
	var inputs []map[string]interface{}
	for _, in := range tx.Inputs {
		if in.Hash.HasValue() {
			inputs = append(inputs, map[string]interface{}{
				"hash":  in.Hash,
				"index": in.Index,
			})
		} else if len(in.Genesis) > 0 {
			inputs = append(inputs, map[string]interface{}{
				"genesis": hex.EncodeToString(in.Genesis),
			})
		} else if in.Deposit != nil {
			inputs = append(inputs, map[string]interface{}{
				"deposit": in.Deposit,
			})
		} else if in.Mint != nil {
			inputs = append(inputs, map[string]interface{}{
				"mint": in.Mint,
			})
		}
	}

	var outputs []map[string]interface{}
	for _, out := range tx.Outputs {
		output := map[string]interface{}{
			"type":   out.Type,
			"amount": out.Amount,
		}
		if len(out.Keys) > 0 {
			output["keys"] = out.Keys
		}
		if len(out.Script) > 0 {
			output["script"] = out.Script
		}
		if out.Mask.HasValue() {
			output["mask"] = out.Mask
		}
		if out.Withdrawal != nil {
			output["withdrawal"] = out.Withdrawal
		}
		outputs = append(outputs, output)
	}

	return map[string]interface{}{
		"version":    tx.Version,
		"asset":      tx.Asset,
		"inputs":     inputs,
		"outputs":    outputs,
		"extra":      hex.EncodeToString(tx.Extra),
		"hash":       tx.PayloadHash(),
		"signatures": tx.Signatures,
	}
}
