package service

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	badger "github.com/dgraph-io/badger/v3"

	"google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	status "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/dariopb/netd/proto/service.v1alpha"

	log "github.com/sirupsen/logrus"
)

type SubscriptionChannel struct {
	ID      string
	Channel chan *pb.ServiceWatchResponse
}

type ServiceServer struct {
	pb.UnimplementedServiceDServer

	db *badger.DB
	//items map[string]*pb.Service

	clientChannels map[string]SubscriptionChannel
	mtx            sync.Mutex
}

func NewServiceServer(dbFilepath string, grpcServer *grpc.Server) (*ServiceServer, error) {
	opts := badger.DefaultOptions(dbFilepath).WithValueLogFileSize(50 * 1024 * 1024)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	n := &ServiceServer{
		db:             db,
		clientChannels: make(map[string]SubscriptionChannel),
	}

	if grpcServer != nil {
		pb.RegisterServiceDServer(grpcServer, n)
	}

	return n, nil
}

func (n *ServiceServer) getResource(realm string, id string) (*pb.Service, error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := []byte(fmt.Sprintf("realm/%s/services/%s", realm, id))
	o := &pb.Service{}

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

func (n *ServiceServer) deleteResource(realm string, id string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := []byte(fmt.Sprintf("realm/%s/services/%s", realm, id))

	err := n.db.DropPrefix([]byte(key))
	if err != nil {
		return err
	}

	n.publishUpdate(id, string(key), nil, false)

	return nil
}

func (n *ServiceServer) listResources(realm string, key string) ([]*pb.Service, error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	prefix := []byte(fmt.Sprintf("realm/%s/services", realm))
	if key != "" {
		prefix = []byte(key)
	}
	list := []*pb.Service{}

	n.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		//prefix := []byte(fmt.Sprintf("%s/", kind))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			kk := item.Key()
			if len(prefix) < len(kk) {
				if bytes.IndexByte(kk[len(prefix)+1:], byte('/')) != -1 {
					continue
				}
			}

			err := item.Value(func(val []byte) error {
				o := &pb.Service{}
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

func (n *ServiceServer) persistAndPublish(realm string, id string, data *pb.Service) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := fmt.Sprintf("realm/%s/services/%s", realm, id)
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

	n.publishUpdate(id, key, data, false)

	return nil
}

func (n *ServiceServer) publishUpdate(id string, key string, data *pb.Service, isSnapshot bool) error {
	action := pb.ServiceWatchResponse_SNAPSHOT

	if !isSnapshot {
		if data != nil {
			action = pb.ServiceWatchResponse_CHANGE
		} else {
			action = pb.ServiceWatchResponse_DELETE
		}
	}

	notification := &pb.ServiceWatchResponse{
		Action:  action,
		Key:     key,
		Id:      id,
		Service: data,
	}

	for clientName, sub := range n.clientChannels {
		//if strings.HasPrefix(key, sub.Kind) &&
		if sub.ID == "" || sub.ID == id {
			log.Infof("Sending update to: [%s] for key: [%s]", clientName, key)
			sub.Channel <- notification
		}
	}

	return nil
}

//Subscribe subscribes to route data
func (n *ServiceServer) Subscribe(clientName string, realm string, id string, key string) (chan *pb.ServiceWatchResponse, error) {
	log.Infof("Subscribing client: [%s] on kind: [%s] and id: [%s]", clientName, "service", id)

	n.mtx.Lock()

	clientChan := make(chan *pb.ServiceWatchResponse, 100)
	n.clientChannels[clientName] = SubscriptionChannel{
		ID:      id,
		Channel: clientChan,
	}
	n.mtx.Unlock()

	// Send the current state on connection
	log.Infof("Sending initial update to: [%s]", clientName)

	list, err := n.listResources(realm, key)
	if err == nil {
		for _, data := range list {
			n.publishUpdate(id, data.Key, data, true)
		}
		// DARIO: TODO: cleanup on empty snap!
		l := len(list)
		if l != 0 {
			n.publishUpdate(id, list[l-1].Key, list[l-1], false)
		}
	}

	return clientChan, nil
}

//Unsubscribe unsubscribes from route data
func (n *ServiceServer) Unsubscribe(clientName string) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	log.Infof("Unsubscribing client: [%s]", clientName)
	clientSub, ok := n.clientChannels[clientName]
	if ok {
		close(clientSub.Channel)
		delete(n.clientChannels, clientName)
	}
}

// List returns all the services defined.
func (n *ServiceServer) List(context.Context, *pb.ServiceListRequest) (*pb.Services, error) {
	log.Debugf("ServiceServer:List called")

	items, err := n.listResources("default", "")
	if err != nil {
		log.Errorf("Failed to enumerate objects: %v", err)
		return nil, err
	}

	vv := &pb.Services{Service: items}
	return vv, nil
}

// Update modifies a service
func (n *ServiceServer) Update(ctx context.Context, item *pb.Service) (*pb.Service, error) {
	log.Debugf("ServiceServer:Update called")
	realm := "default"

	// validate item params

	err := n.persistAndPublish(realm, item.Id, item)
	if err != nil {
		log.Errorf("Failed to persist object: %v", err)
		return nil, err
	}

	return item, nil
}

func (n *ServiceServer) Get(ctx context.Context, req *pb.ServiceGetRequest) (*pb.Service, error) {
	realm := "default"
	item, err := n.getResource(realm, req.Id)
	if err == badger.ErrKeyNotFound {
		err = status.Error(codes.NotFound, err.Error())
	}

	return item, err
}

func (n *ServiceServer) Delete(ctx context.Context, req *pb.ServiceDeleteRequest) (*pb.Empty, error) {
	realm := "default"
	err := n.deleteResource(realm, req.Id)

	res := &pb.Empty{}

	return res, err
}

func (n *ServiceServer) Watch(req *pb.ServiceWatchRequest, stream pb.ServiceD_WatchServer) error {
	log.Debugf("ServiceServer:Watch called")

	p, _ := peer.FromContext(stream.Context())
	peerId := p.Addr.String()

	realm := "default"
	discoveryData, err := n.Subscribe(peerId, realm, req.Id, "")
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

	log.Infof("Watch finishing [err: %v]", err)
	n.Unsubscribe(peerId)

	return nil
}
