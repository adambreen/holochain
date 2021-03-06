// Copyright (C) 2013-2017, The MetaCurrency Project (Eric Harris-Braun, Arthur Brock, et. al.)
// Use of this source code is governed by GPLv3 found in the LICENSE file
//----------------------------------------------------------------------------------------

// gossip implements the gossip protocol for the distributed hash table

package holochain

import (
	"errors"
	"fmt"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/tidwall/buntdb"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Put holds a put or link for gossiping
type Put struct {
	idx int
	M   Message
}

// Gossip holds a gossip message
type Gossip struct {
	Puts []Put
}

// GossipReq holds a gossip request
type GossipReq struct {
	MyIdx   int
	YourIdx int
}

var ErrDHTErrNoGossipersAvailable error = errors.New("no gossipers available")
var ErrDHTExpectedGossipReqInBody error = errors.New("expected gossip request")
var ErrNoSuchIdx error = errors.New("no such change index")

// incIdx adds a new index record to dht for gossiping later
func incIdx(tx *buntdb.Tx, m *Message) (index string, err error) {
	// if message is nil we can't record this for gossiping
	// this should only be the case for the DNA
	if m == nil {
		return
	}

	var idx int
	idx, err = getIntVal("_idx", tx)
	if err != nil {
		return
	}
	idx++
	index = fmt.Sprintf("%d", idx)
	_, _, err = tx.Set("_idx", index, nil)
	if err != nil {
		return
	}

	var msg string

	if m != nil {
		var b []byte
		b, err = ByteEncoder(m)
		if err != nil {
			return
		}
		msg = string(b)
	}
	_, _, err = tx.Set("idx:"+index, msg, nil)
	if err != nil {
		return
	}

	f, err := m.Fingerprint()
	if err != nil {
		return
	}
	_, _, err = tx.Set("f:"+f.String(), index, nil)
	if err != nil {
		return
	}

	return
}

// getIntVal returns an integer value at a given key, and assumes the value 0 if the key doesn't exist
func getIntVal(key string, tx *buntdb.Tx) (idx int, err error) {
	var val string
	val, err = tx.Get(key)
	if err == buntdb.ErrNotFound {
		err = nil
	} else if err != nil {
		return
	} else {
		idx, err = strconv.Atoi(val)
		if err != nil {
			return
		}
	}
	return
}

// GetIdx returns the current put index for gossip
func (dht *DHT) GetIdx() (idx int, err error) {
	err = dht.db.View(func(tx *buntdb.Tx) error {
		var e error
		idx, e = getIntVal("_idx", tx)
		if e != nil {
			return e
		}
		return nil
	})
	return
}

// GetIdxMessage returns the messages that causes the change at a given index
func (dht *DHT) GetIdxMessage(idx int) (msg Message, err error) {
	err = dht.db.View(func(tx *buntdb.Tx) error {
		msgStr, e := tx.Get(fmt.Sprintf("idx:%d", idx))
		if e == buntdb.ErrNotFound {
			return ErrNoSuchIdx
		}
		if e != nil {
			return e
		}
		e = ByteDecoder([]byte(msgStr), &msg)
		if err != nil {
			return e
		}
		return nil
	})
	return
}

//HaveFingerprint returns true if we have seen the given fingerprint
func (dht *DHT) HaveFingerprint(f Hash) (result bool, err error) {
	index, err := dht.GetFingerprint(f)
	if err == nil {
		result = index >= 0
	}
	return
}

// GetFingerprint returns the index that of the message that made a change or -1 if we don't have it
func (dht *DHT) GetFingerprint(f Hash) (index int, err error) {
	index = -1
	err = dht.db.View(func(tx *buntdb.Tx) error {
		idxStr, e := tx.Get("f:" + f.String())
		if e == buntdb.ErrNotFound {
			return nil
		}
		if e != nil {
			return e
		}
		index, e = strconv.Atoi(idxStr)
		if e != nil {
			return e
		}
		return nil
	})
	return
}

// GetPuts returns a list of puts after the given index
func (dht *DHT) GetPuts(since int) (puts []Put, err error) {
	puts = make([]Put, 0)
	err = dht.db.View(func(tx *buntdb.Tx) error {
		err = tx.AscendGreaterOrEqual("idx", string(since), func(key, value string) bool {
			x := strings.Split(key, ":")
			idx, _ := strconv.Atoi(x[1])
			if idx >= since {
				p := Put{idx: idx}
				if value != "" {
					err := ByteDecoder([]byte(value), &p.M)
					if err != nil {
						return false
					}
				}
				puts = append(puts, p)
			}
			return true
		})
		sort.Slice(puts, func(i, j int) bool { return puts[i].idx < puts[j].idx })
		return err
	})
	return
}

// GetGossiper loads returns last known index of the gossiper, and adds them if not didn't exist before
func (dht *DHT) GetGossiper(id peer.ID) (idx int, err error) {
	key := "peer:" + peer.IDB58Encode(id)
	err = dht.db.View(func(tx *buntdb.Tx) error {
		var e error
		idx, e = getIntVal(key, tx)
		if e != nil {
			return e
		}
		return nil
	})
	return
}

// FindGossiper picks a random DHT node to gossip with
func (dht *DHT) FindGossiper() (g peer.ID, err error) {
	glist := make([]peer.ID, 0)

	err = dht.db.View(func(tx *buntdb.Tx) error {
		err = tx.Ascend("peer", func(key, value string) bool {
			x := strings.Split(key, ":")
			id, e := peer.IDB58Decode(x[1])
			if e != nil {
				return false
			}
			//			idx, _ := strconv.Atoi(value)
			glist = append(glist, id)
			return true
		})
		return nil
	})

	if len(glist) == 0 {
		err = ErrDHTErrNoGossipersAvailable
	} else {
		g = glist[rand.Intn(len(glist))]
	}
	return
}

// UpdateGossiper updates a gossiper
func (dht *DHT) UpdateGossiper(id peer.ID, newIdx int) (err error) {
	dht.glog.Logf("updaing %v to %d", id, newIdx)
	err = dht.db.Update(func(tx *buntdb.Tx) error {
		key := "peer:" + peer.IDB58Encode(id)
		idx, e := getIntVal(key, tx)
		if e != nil {
			return e
		}
		if newIdx < idx {
			return nil
		}
		sidx := fmt.Sprintf("%d", newIdx)
		_, _, err = tx.Set(key, sidx, nil)
		if err != nil {
			return err
		}
		return nil
	})
	return
}

func GossipReceiver(h *Holochain, m *Message) (response interface{}, err error) {
	dht := h.dht
	switch m.Type {
	case GOSSIP_REQUEST:
		dht.glog.Logf("GossipReceiver got GOSSIP_REQUEST: %v", m)
		switch t := m.Body.(type) {
		case GossipReq:
			dht.glog.Logf("%v wants my puts since %d and is at %d", m.From, t.YourIdx, t.MyIdx)

			// give the gossiper what they want
			var puts []Put
			puts, err = h.dht.GetPuts(t.YourIdx)
			g := Gossip{Puts: puts}
			response = g

			// check to see what we know they said, and if our record is less
			// that where they are currently at, gossip back
			idx, e := h.dht.GetGossiper(m.From)
			if e == nil && idx < t.MyIdx {
				dht.glog.Logf("we only have %d of %d from %v so gossiping back", idx, t.MyIdx, m.From)

				pi := h.node.Host.Peerstore().PeerInfo(m.From)
				if len(pi.Addrs) == 0 {
					dht.glog.Logf("NO ADDRESSES FOR PEER:%v", pi)
				}

				// queue up a request to gossip back
				dht.gchan <- gossipWithReq{m.From}
			}

		default:
			err = ErrDHTExpectedGossipReqInBody
		}
	default:
		err = fmt.Errorf("message type %d not in holochain-gossip protocol", int(m.Type))
	}
	return
}

// gossipWith gossips with an peer asking for everything after since
func (dht *DHT) gossipWith(id peer.ID) (err error) {
	dht.glog.Logf("with %v", id)

	// gossip loops are possible where a gossip request triggers a gossip back, which
	// if the first gossiping wasn't completed triggers the same gossip, so protect against this
	// with a hash table storing who we are currently gossiping with
	_, gossiping := dht.gossips[id]
	if gossiping {
		return
	}
	dht.gossips[id] = true
	defer func() {
		delete(dht.gossips, id)
	}()

	var myIdx, yourIdx int
	myIdx, err = dht.GetIdx()
	if err != nil {
		return
	}

	yourIdx, err = dht.GetGossiper(id)
	if err != nil {
		return
	}

	var r interface{}
	r, err = dht.h.Send(GossipProtocol, id, GOSSIP_REQUEST, GossipReq{MyIdx: myIdx, YourIdx: yourIdx + 1})
	if err != nil {
		return
	}

	gossip := r.(Gossip)
	puts := gossip.Puts
	dht.glog.Logf("received puts: %v", puts)

	// gossiper has more stuff that we new about before so update the gossipers status
	// and also run their puts
	count := len(puts)
	if count > 0 {
		dht.glog.Logf("running %d puts", count)
		var idx int
		for i, p := range puts {
			idx = i + yourIdx + 1
			/* TODO: Small mystery to be solved, the value of p.idx is always 0 but it should be the actual idx...
			if idx != p.idx {
				dht.glog.Logf("WHOA! idx=%d  p.idx:%d p.M: %v", idx, p.idx, p.M)
			}
			*/
			f, e := p.M.Fingerprint()
			if e == nil {
				dht.glog.Logf("PUT--%d (fingerprint: %v)", idx, f)
				exists, e := dht.HaveFingerprint(f)
				if !exists && e == nil {
					dht.glog.Logf("PUT--%d calling ActionReceiver", idx)
					r, e := ActionReceiver(dht.h, &p.M)
					dht.glog.Logf("PUT--%d ActionReceiver returned %v with err %v", idx, r, e)
				} else {
					if e == nil {
						dht.glog.Logf("already have fingerprint %v", f)
					} else {
						dht.glog.Logf("error in HaveFingerprint %v", e)
					}
				}

			} else {
				dht.glog.Logf("error calculating fingerprint for %v", p)
			}
		}
		err = dht.UpdateGossiper(id, idx)
	}
	return
}

// gossip picks a random node in my neighborhood and sends gossips with it
func (dht *DHT) gossip() (err error) {

	var g peer.ID
	g, err = dht.FindGossiper()
	if err != nil {
		return
	}

	dht.gchan <- gossipWithReq{g}
	return
}

// Gossip gossips every interval
func (dht *DHT) Gossip(interval time.Duration) {
	dht.gossiping = true
	for dht.gossiping {
		err := dht.gossip()
		if err != nil {
			dht.glog.Logf("error: %v", err)
		}
		time.Sleep(interval)
	}
}

// HandleGossipWiths waits on a chanel for gossipWith requests
func (dht *DHT) HandleGossipWiths() (err error) {
	for {
		dht.glog.Log("HandleGossipWiths: waiting for request")
		g, ok := <-dht.gchan
		if !ok {
			dht.glog.Log("HandleGossipWiths: channel closed, breaking")
			break
		}

		err = dht.gossipWith(g.id)
		if err != nil {
			dht.glog.Logf("HandleGossipWiths: got err: %v", err)
		}
	}
	return nil
}
