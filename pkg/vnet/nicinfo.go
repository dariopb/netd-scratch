package vnetserver

import (
	"fmt"

	badger "github.com/dgraph-io/badger/v3"

	codes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	status "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/dariopb/netd/proto/netd.v1alpha"

	log "github.com/sirupsen/logrus"
)

func (n *VNetServer) getResourceNic(realm string, vnetId string, id string) (*pb.NicConfiguration, error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := []byte(fmt.Sprintf("realm/%s/nics/vnets/%s/nics/%s", realm, vnetId, id))
	o := &pb.NicConfiguration{}

	err := n.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		err = item.Value(func(v []byte) error {
			if err := protojson.Unmarshal(v, o); err != nil {
				return err
			}
			return nil
		})

		return err
	})
	if err != nil {
		return nil, err
	}

	return o, nil
}

func (n *VNetServer) deleteResourceNic(realm string, vnetId string, id string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := []byte(fmt.Sprintf("realm/%s/nics/vnets/%s/nics/%s", realm, vnetId, id))

	err := n.db.DropPrefix([]byte(key))
	if err != nil {
		return err
	}

	n.publishUpdateNic(id, string(key), nil, nil, false)

	return nil
}

func (n *VNetServer) listResourcesNic(realm string, vnetId string, key string) ([]*pb.NicConfiguration, error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	prefix := []byte(fmt.Sprintf("realm/%s/nics/vnets/%s", realm, vnetId))
	if key != "" {
		prefix = []byte(key)
	}
	list := []*pb.NicConfiguration{}

	n.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		//prefix := []byte(fmt.Sprintf("%s/", kind))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			kk := item.Key()
			//if len(prefix) < len(kk) {
			//	if bytes.IndexByte(kk[len(prefix)+1:], byte('/')) != -1 {
			//		continue
			//	}
			//}

			err := item.Value(func(val []byte) error {
				o := &pb.NicConfiguration{}
				if err := protojson.Unmarshal(val, o); err != nil {
					return err
				}

				list = append(list, o)
				return nil
			})
			if err != nil {
				log.Errorf("Error processing element: %v", err)
				continue
			}
			if len(prefix) == len(kk) {
				break
			}
		}
		return nil
	})

	return list, nil
}

func (n *VNetServer) persistAndPublishNic(realm string, vnetId string, id string, data *pb.NicConfiguration) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := fmt.Sprintf("realm/%s/nics/vnets/%s/nics/%s", realm, vnetId, id)
	data.Key = key
	jsonData, err := protojson.Marshal(data)
	if err != nil {
		return err
	}

	err = n.db.Update(func(txn *badger.Txn) error {
		err := txn.Set([]byte(key), jsonData)
		return err
	})
	if err != nil {
		log.Errorf("Failed to persist object: %v", err)
		return err
	}

	n.publishUpdateNic(id, key, data, nil, false)

	return nil
}

func (n *VNetServer) publishUpdateNic(id string, key string, data *pb.NicConfiguration, targetSub *SubscriptionChannelNic, isSnap bool) error {
	action := pb.WatchIPMappingResponse_SNAPSHOT

	if !isSnap {
		if data != nil {
			action = pb.WatchIPMappingResponse_CHANGE
		} else {
			action = pb.WatchIPMappingResponse_DELETE
		}
	}

	notification := &pb.WatchIPMappingResponse{
		Action:           action,
		Key:              key,
		Id:               id,
		NicConfiguration: data,
	}

	if targetSub == nil {
		for clientName, sub := range n.clientChannelsNic {
			//if strings.HasPrefix(key, sub.Kind) &&
			if sub.ID == "" || sub.ID == id {
				log.Infof("Sending update to: [%s] for key: [%s]", clientName, key)
				sub.Channel <- notification
			}
		}
	} else {
		if targetSub.ID == "" || targetSub.ID == id {
			log.Infof("Sending update to: [%s] for key: [%s]", targetSub.ClientId, key)
			targetSub.Channel <- notification
		}
	}

	return nil
}

//Subscribe subscribes to route data
func (n *VNetServer) SubscribeNic(clientName string, realm string, vnetId string, id string, key string) (chan *pb.WatchIPMappingResponse, error) {
	log.Infof("Subscribing client: [%s] on kind: [%s] and vnet: [%s], id: [%s]", clientName, "nic", vnetId, id)

	n.mtx.Lock()

	clientChan := make(chan *pb.WatchIPMappingResponse, 100)
	targetSub := &SubscriptionChannelNic{
		ID:       id,
		ClientId: clientName,
		Channel:  clientChan,
	}
	n.clientChannelsNic[clientName] = *targetSub
	n.mtx.Unlock()

	// Send the current state on connection
	log.Infof("Sending initial update to: [%s]", clientName)

	list, err := n.listResourcesNic(realm, vnetId, key)
	if err == nil {
		for _, data := range list {
			n.publishUpdateNic(id, data.Key, data, targetSub, true)
		}

		// DARIO: TODO: cleanup on empty snap!
		l := len(list)
		if l != 0 {
			n.publishUpdateNic(id, list[l-1].Key, list[l-1], targetSub, false)
		}
	}

	return clientChan, nil
}

//Unsubscribe unsubscribes from route data
func (n *VNetServer) UnsubscribeNic(clientName string) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	log.Infof("Unsubscribing client: [%s]", clientName)
	clientSub, ok := n.clientChannelsNic[clientName]
	if ok {
		close(clientSub.Channel)
		delete(n.clientChannelsNic, clientName)
	}
}

func (n *VNetServer) WatchIPMapping(req *pb.WatchIPMappingRequest, stream pb.NetworkD_WatchIPMappingServer) error {
	log.Debugf("VNetSErver:WatchIPMapping called")

	p, _ := peer.FromContext(stream.Context())
	peerId := p.Addr.String()
	md, ok := metadata.FromIncomingContext(stream.Context())
	if ok {
		v := md.Get("mysession")
		if len(v) > 0 {
			peerId = peerId + "-" + v[0]
		}
	}

	realm := "default"
	_, err := n.getResource(realm, req.VnetID)
	if err == badger.ErrKeyNotFound {
		err = status.Error(codes.NotFound, err.Error())
		return err
	}

	discoveryData, err := n.SubscribeNic(peerId, realm, req.VnetID, "", "")
	if discoveryData == nil {
		log.WithError(err).Error("Failed with: %v", err)
		return err
	}

loop:
	for {
		select {
		case data := <-discoveryData:
			if err := stream.Send(data); err != nil {
				return err
			}

		case <-stream.Context().Done():
			break loop
		}
	}

	log.Infof("WatchIPMapping finishing [err: %v]", err)
	n.UnsubscribeNic(peerId)

	return nil
}
