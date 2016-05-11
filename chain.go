// Copyright 2015 Factom Foundation
// Use of this source code is governed by the MIT
// license that can be found in the LICENSE file.

package factom

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

type Chain struct {
	ChainID    string
	FirstEntry *Entry
}

func NewChain(e *Entry) *Chain {
	c := new(Chain)
	c.FirstEntry = e

	// create the chainid from a series of hashes of the Entries ExtIDs
	hs := sha256.New()
	for _, id := range e.ExtIDs {
		h := sha256.Sum256(id)
		hs.Write(h[:])
	}
	c.ChainID = hex.EncodeToString(hs.Sum(nil))
	c.FirstEntry.ChainID = c.ChainID

	return c
}

type CHead struct {
	ChainHead string
}

type CommitChainResponse struct {
	Message string
	TxID    string
}

// CommitChain sends the signed ChainID, the Entry Hash, and the Entry Credit
// public key to the factom network. Once the payment is verified and the
// network is commited to publishing the Chain it may be published by revealing
// the First Entry in the Chain.
func CommitChain(c *Chain, name string) error {
	buf := new(bytes.Buffer)

	// 1 byte version
	buf.Write([]byte{0})

	// 6 byte milliTimestamp
	buf.Write(milliTime())

	e := c.FirstEntry

	// 32 byte ChainID Hash
	if p, err := hex.DecodeString(c.ChainID); err != nil {
		return err
	} else {
		// double sha256 hash of ChainID
		buf.Write(shad(p))
	}

	// 32 byte Weld; sha256(sha256(EntryHash + ChainID))
	if cid, err := hex.DecodeString(c.ChainID); err != nil {
		return err
	} else {
		s := append(e.Hash(), cid...)
		buf.Write(shad(s))
	}

	// 32 byte Entry Hash of the First Entry
	buf.Write(e.Hash())

	// 1 byte number of Entry Credits to pay
	if d, err := entryCost(e); err != nil {
		return err
	} else {
		buf.WriteByte(byte(d + 10))
	}

	req := NewJSON2Request("commit-chain", apiCounter(), hex.EncodeToString(buf.Bytes()))
	resp, err := factomdRequest(req)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	/*
		ccr := new(CommitChainResponse)
		if err := json.Unmarshal(resp.Result, ccr); err != nil {
			return nil, err
		}*/

	return nil
}

func RevealChain(c *Chain) error {
	p, err := c.FirstEntry.MarshalBinary()

	if err != nil {
		return err
	}

	req := NewJSON2Request("reveal-chain", apiCounter(), hex.EncodeToString(p))
	resp, err := factomdRequest(req)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}

	//return resp.Result.(*wsapi.RevealChainResponse)., nil
	return nil
}
