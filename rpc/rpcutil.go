/*
 * Copyright 2018 The ThunderDB Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package rpc

import (
	"context"
	"net/rpc"

	"github.com/hashicorp/yamux"
	"gitlab.com/thunderdb/ThunderDB/crypto/kms"
	"gitlab.com/thunderdb/ThunderDB/proto"
	"gitlab.com/thunderdb/ThunderDB/route"
	"gitlab.com/thunderdb/ThunderDB/utils/log"
)

// Caller is a wrapper for session pooling and RPC calling.
type Caller struct {
	pool *SessionPool
}

// NewCaller returns a new RPCCaller.
func NewCaller() *Caller {
	return &Caller{
		pool: GetSessionPoolInstance(),
	}
}

//TODO(auxten) maybe a rpc client pool will gain much more performance
// CallNode invokes the named function, waits for it to complete, and returns its error status.
func (c *Caller) CallNode(
	node proto.NodeID, method string, args interface{}, reply interface{}) (err error) {
	return c.CallNodeWithContext(context.Background(), node, method, args, reply)
}

// CallNodeWithContext invokes the named function, waits for it to complete or context timeout, and returns its error status.
func (c *Caller) CallNodeWithContext(
	ctx context.Context, node proto.NodeID, method string, args interface{}, reply interface{}) (err error) {
	conn, err := DialToNode(node, c.pool, method == route.DHTPing.String())
	if err != nil {
		log.Errorf("dialing to node: %s failed: %s", node, err)
		return
	}

	defer func() {
		// call the yamux stream Close explicitly
		//TODO(auxten) maybe a rpc client pool will gain much more performance
		stream, ok := conn.(*yamux.Stream)
		if ok {
			stream.Close()
		}
	}()

	client, err := InitClientConn(conn)
	if err != nil {
		log.Errorf("init RPC client failed: %s", err)
		return
	}

	defer client.Close()

	// TODO(xq262144), golang net/rpc does not support cancel in progress calls
	ch := client.Go(method, args, reply, make(chan *rpc.Call, 1))

	select {
	case <-ctx.Done():
		err = ctx.Err()
	case call := <-ch.Done:
		err = call.Error
	}

	return
}

// GetNodeAddr tries best to get node addr
func GetNodeAddr(id *proto.RawNodeID) (addr string, err error) {
	addr, err = route.GetNodeAddrCache(id)
	if err != nil {
		log.Infof("get node \"%s\" addr failed: %s", addr, err)
		if err == route.ErrUnknownNodeID {
			BPs := route.GetBPs()
			if len(BPs) == 0 {
				log.Errorf("no available BP")
				return
			}
			client := NewCaller()
			reqFN := &proto.FindNodeReq{
				NodeID: proto.NodeID(id.String()),
			}
			respFN := new(proto.FindNodeResp)

			// TODO(auxten) add some random here for bp selection
			for _, bp := range BPs {
				method := "DHT.FindNode"
				err = client.CallNode(bp, method, reqFN, respFN)
				if err != nil {
					log.Errorf("call %s %s failed: %s", bp, method, err)
					continue
				}
				break
			}
			if err == nil {
				route.SetNodeAddrCache(id, respFN.Node.Addr)
				addr = respFN.Node.Addr
			}
		}
	}
	return
}

// GetNodeInfo tries best to get node info
func GetNodeInfo(id *proto.RawNodeID) (nodeInfo *proto.Node, err error) {
	nodeInfo, err = kms.GetNodeInfo(proto.NodeID(id.String()))
	if err != nil {
		log.Infof("get node info from KMS for %s failed: %s", id, err)
		if err == kms.ErrKeyNotFound {
			BPs := route.GetBPs()
			if len(BPs) == 0 {
				log.Errorf("no available BP")
				return
			}
			client := NewCaller()
			reqFN := &proto.FindNodeReq{
				NodeID: proto.NodeID(id.String()),
			}
			respFN := new(proto.FindNodeResp)

			// TODO(auxten) add some random here for bp selection
			for _, bp := range BPs {
				method := "DHT.FindNode"
				err = client.CallNode(bp, method, reqFN, respFN)
				if err != nil {
					log.Errorf("call %s %s failed: %s", bp, method, err)
					continue
				}
				break
			}
			if err == nil {
				nodeInfo = respFN.Node
				errSet := route.SetNodeAddrCache(id, nodeInfo.Addr)
				if errSet != nil {
					log.Warnf("set node addr cache failed: %v", errSet)
				}
				errSet = kms.SetNode(nodeInfo)
				if errSet != nil {
					log.Warnf("set node to kms failed: %v", errSet)
				}
			}
		}
	}
	return
}

// PingBP Send DHT.Ping Request with Anonymous ETLS session
func PingBP(node *proto.Node, BPNodeID proto.NodeID) (err error) {
	client := NewCaller()

	req := &proto.PingReq{
		Node: *node,
	}

	resp := new(proto.PingResp)
	err = client.CallNode(BPNodeID, "DHT.Ping", req, resp)
	if err != nil {
		log.Errorf("call DHT.Ping failed: %v", err)
		return
	}
	log.Debugf("PingBP resp: %v", resp)

	return
}
