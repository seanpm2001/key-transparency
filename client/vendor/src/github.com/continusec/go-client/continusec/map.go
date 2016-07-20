/*
   Copyright 2016 Continusec Pty Ltd

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package continusec

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type mapHashResponse struct {
	MapHash []byte            `json:"map_hash"`
	LogSTH  *treeSizeResponse `json:"mutation_log"`
}

// VerifiableMap is an object used to interact with Verifiable Maps. To construct this
// object, call NewClient(...).VerifiableMap("mapname")
type VerifiableMap struct {
	Client *Client
}

// MutationLog returns a pointer to the underlying Verifiable Log that represents
// a log of mutations to this map. Since this Verifiable Log is managed by this map,
// the log returned cannot be directly added to (to mutate, call Set and Delete methods
// on the map), however all read-only functions are present.
func (self *VerifiableMap) MutationLog() *VerifiableLog {
	return &VerifiableLog{
		Client: self.Client.WithChildPath("/log/mutation"),
	}
}

// TreeHeadLog returns a pointer to the underlying Verifiable Log that represents
// a log of tree heads generated by this map. Since this Verifiable Map is managed by this map,
// the log returned cannot be directly added to however all read-only functions are present.
func (self *VerifiableMap) TreeHeadLog() *VerifiableLog {
	return &VerifiableLog{
		Client: self.Client.WithChildPath("/log/treehead"),
	}
}

// Create will send an API call to create a new map with the name specified when the
// VerifiableMap object was instantiated.
func (self *VerifiableMap) Create() error {
	_, _, err := self.Client.MakeRequest("PUT", "", nil, nil)
	if err != nil {
		return err
	}
	return nil
}

// Destroy will send an API call to delete this map - this operation removes it permanently,
// and renders the name unusable again within the same account, so please use with caution.
func (self *VerifiableMap) Destroy() error {
	_, _, err := self.Client.MakeRequest("DELETE", "", nil, nil)
	if err != nil {
		return err
	}
	return nil
}

func parseHeadersForProof(headers http.Header) ([][]byte, error) {
	prv := make([][]byte, 256)
	actualHeaders, ok := headers[http.CanonicalHeaderKey("X-Verified-Proof")]
	if ok {
		for _, h := range actualHeaders {
			for _, commad := range strings.Split(h, ",") {
				bits := strings.SplitN(commad, "/", 2)
				if len(bits) == 2 {
					idx, err := strconv.Atoi(strings.TrimSpace(bits[0]))
					if err != nil {
						return nil, err
					}
					bs, err := hex.DecodeString(strings.TrimSpace(bits[1]))
					if err != nil {
						return nil, err
					}
					if idx < 256 {
						prv[idx] = bs
					}
				}
			}
		}
	}
	return prv, nil
}

// Get will return the value for the given key at the given treeSize. Pass continusec.Head
// to always get the latest value. factory is normally one of RawDataEntryFactory, JsonEntryFactory or RedactedJsonEntryFactory.
//
// Clients normally instead call VerifiedGet() with a MapTreeHead returned by VerifiedLatestMapState as this will also perform verification of inclusion.
func (self *VerifiableMap) Get(key []byte, treeSize int64, factory VerifiableEntryFactory) (*MapInclusionProof, error) {
	value, headers, err := self.Client.MakeRequest("GET", fmt.Sprintf("/tree/%d/key/h/%s%s", treeSize, hex.EncodeToString(key), factory.Format()), nil, nil)
	if err != nil {
		return nil, err
	}

	prv, err := parseHeadersForProof(headers)
	if err != nil {
		return nil, err
	}

	rv, err := factory.CreateFromBytes(value)
	if err != nil {
		return nil, err
	}

	vts, err := strconv.Atoi(headers.Get("X-Verified-TreeSize"))
	if err != nil {
		return nil, err
	}

	return &MapInclusionProof{
		Value:     rv,
		TreeSize:  int64(vts),
		AuditPath: prv,
		Key:       key,
	}, nil
}

// VerifiedGet gets the value for the given key in the specified MapTreeState, and verifies that it is
// included in the MapTreeHead (wrapped by the MapTreeState) before returning.
// factory is normally one of RawDataEntryFactory, JsonEntryFactory or RedactedJsonEntryFactory.
func (self *VerifiableMap) VerifiedGet(key []byte, mapHead *MapTreeState, factory VerifiableEntryFactory) (VerifiableEntry, error) {
	proof, err := self.Get(key, mapHead.TreeSize(), factory)
	if err != nil {
		return nil, err
	}
	err = proof.Verify(&mapHead.MapTreeHead)
	if err != nil {
		return nil, err
	}
	return proof.Value, nil
}

// Set will generate a map mutation to set the given value for the given key.
// While this will return quickly, the change will be reflected asynchronously in the map.
// Returns an AddEntryResponse which contains the leaf hash for the mutation log entry.
func (self *VerifiableMap) Set(key []byte, value UploadableEntry) (*AddEntryResponse, error) {
	data, err := value.DataForUpload()
	if err != nil {
		return nil, err
	}
	contents, _, err := self.Client.MakeRequest("PUT", "/key/h/"+hex.EncodeToString(key)+value.Format(), data, nil)
	if err != nil {
		return nil, err
	}
	var aer addEntryResponse
	err = json.Unmarshal(contents, &aer)
	if err != nil {
		return nil, err
	}
	return &AddEntryResponse{EntryLeafHash: aer.Hash}, nil
}

// Update will generate a map mutation to set the given value for the given key, conditional on the
// previous leaf hash being that specified by previousLeaf.
// While this will return quickly, the change will be reflected asynchronously in the map.
// Returns an AddEntryResponse which contains the leaf hash for the mutation log entry.
func (self *VerifiableMap) Update(key []byte, value UploadableEntry, previousLeaf MerkleTreeLeaf) (*AddEntryResponse, error) {
	data, err := value.DataForUpload()
	if err != nil {
		return nil, err
	}
	prevLF, err := previousLeaf.LeafHash()
	if err != nil {
		return nil, err
	}

	contents, _, err := self.Client.MakeRequest("PUT", "/key/h/"+hex.EncodeToString(key)+value.Format(), data, [][2]string{
		[2]string{"X-Previous-LeafHash", hex.EncodeToString(prevLF)},
	})
	if err != nil {
		return nil, err
	}
	var aer addEntryResponse
	err = json.Unmarshal(contents, &aer)
	if err != nil {
		return nil, err
	}
	return &AddEntryResponse{EntryLeafHash: aer.Hash}, nil
}

// Delete will set generate a map mutation to delete the value for the given key. Calling Delete
// is equivalent to calling Set with an empty value.
// While this will return quickly, the change will be reflected asynchronously in the map.
// Returns an AddEntryResponse which contains the leaf hash for the mutation log entry.
func (self *VerifiableMap) Delete(key []byte) (*AddEntryResponse, error) {
	contents, _, err := self.Client.MakeRequest("DELETE", "/key/h/"+hex.EncodeToString(key), nil, nil)
	if err != nil {
		return nil, err
	}
	var aer addEntryResponse
	err = json.Unmarshal(contents, &aer)
	if err != nil {
		return nil, err
	}
	return &AddEntryResponse{EntryLeafHash: aer.Hash}, nil
}

// TreeHead returns map root hash for the map at the given tree size. Specify continusec.Head
// to receive a root hash for the latest tree size.
func (self *VerifiableMap) TreeHead(treeSize int64) (*MapTreeHead, error) {
	contents, _, err := self.Client.MakeRequest("GET", fmt.Sprintf("/tree/%d", treeSize), nil, nil)
	if err != nil {
		return nil, err
	}
	var cr mapHashResponse
	err = json.Unmarshal(contents, &cr)
	if err != nil {
		return nil, err
	}
	return &MapTreeHead{
		RootHash: cr.MapHash,
		MutationLogTreeHead: LogTreeHead{
			TreeSize: cr.LogSTH.TreeSize,
			RootHash: cr.LogSTH.Hash,
		},
	}, nil
}

// BlockUntilSize blocks until the map has caught up to a certain size. This polls
// TreeHead() until such time as a new tree hash is produced that is of at least this
// size.
//
// This is intended for test use.
func (self *VerifiableMap) BlockUntilSize(treeSize int64) (*MapTreeHead, error) {
	lastHead := int64(-1)
	timeToSleep := time.Second
	for {
		lth, err := self.TreeHead(Head)
		if err != nil {
			return nil, err
		}
		if lth.TreeSize() >= treeSize {
			return lth, nil
		} else {
			if lth.TreeSize() > lastHead {
				lastHead = lth.TreeSize()
				// since we got a new tree head, reset sleep time
				timeToSleep = time.Second
			} else {
				// no luck, snooze a bit longer
				timeToSleep *= 2
			}
			time.Sleep(timeToSleep)
		}
	}
}

// VerifiedLatestMapState fetches the latest MapTreeState, verifies it is consistent with,
// and newer than, any previously passed state.
func (self *VerifiableMap) VerifiedLatestMapState(prev *MapTreeState) (*MapTreeState, error) {
	head, err := self.VerifiedMapState(prev, Head)
	if err != nil {
		return nil, err
	}

	if prev != nil {
		// this shouldn't go backwards, but perhaps in a distributed system not all nodes are up to date immediately,
		// so we won't consider it an error, but will return the old value in such a case.
		if head.TreeSize() <= prev.TreeSize() {
			return prev, nil
		}
	}

	// all good
	return head, nil
}

// VerifiedMapState returns a wrapper for the MapTreeHead for a given tree size, along with
// a LogTreeHead for the TreeHeadLog that has been verified to contain this map tree head.
// The value returned by this will have been proven to be consistent with any passed prev value.
// Note that the TreeHeadLogTreeHead returned may differ between calls, even for the same treeSize,
// as all future LogTreeHeads can also be proven to contain the MapTreeHead.
//
// Typical clients that only need to access current data will instead use VerifiedLatestMapState()
func (self *VerifiableMap) VerifiedMapState(prev *MapTreeState, treeSize int64) (*MapTreeState, error) {
	if treeSize != 0 && prev != nil && prev.TreeSize() == treeSize {
		return prev, nil
	}

	// Get latest map head
	mapHead, err := self.TreeHead(treeSize)
	if err != nil {
		return nil, err
	}

	// If we have a previous state, then make sure both logs are consistent with it
	if prev != nil {
		// Make sure that the mutation log is consistent with what we had
		err = self.MutationLog().VerifyConsistency(&prev.MapTreeHead.MutationLogTreeHead,
			&mapHead.MutationLogTreeHead)
		if err != nil {
			return nil, err
		}
	}

	// Get the latest tree head for the tree head log
	var prevThlth, thlth *LogTreeHead
	if prev != nil {
		prevThlth = &prev.TreeHeadLogTreeHead
	}

	// Have we verified ourselves yet?
	verifiedInTreeHeadLog := false

	// If we already have a tree head that is the size of our map, then we
	// probably don't need a new one, so try that first.
	if prevThlth != nil && prevThlth.TreeSize >= mapHead.TreeSize() {
		err = self.TreeHeadLog().VerifyInclusion(prevThlth, mapHead)
		if err == nil {
			verifiedInTreeHeadLog = true
			thlth = prevThlth
		} // but it's ok if we fail, since try again below
	}

	// If we weren't able to take a short-cut above, go back to normal processing:
	if !verifiedInTreeHeadLog {
		// Get new tree head
		thlth, err = self.TreeHeadLog().VerifiedLatestTreeHead(prevThlth)
		if err != nil {
			return nil, err
		}

		// And make sure we are in it
		err = self.TreeHeadLog().VerifyInclusion(thlth, mapHead)
		if err != nil {
			return nil, err
		}
	}

	// All good
	return &MapTreeState{
		MapTreeHead:         *mapHead,
		TreeHeadLogTreeHead: *thlth,
	}, nil
}
