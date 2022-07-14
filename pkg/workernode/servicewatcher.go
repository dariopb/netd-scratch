package workernode

import (
	"context"
	"sync"
	"time"

	"github.com/dariopb/netd/pkg/reconciler"
	"google.golang.org/grpc/metadata"

	pb "github.com/dariopb/netd/proto/service.v1alpha"
)

var serviceMap map[string]string
var mx sync.Mutex

func ServiceWatchResponse_GeyKey(a *pb.ServiceWatchResponse) (string, reconciler.DeltaType) {
	return a.Key, (reconciler.DeltaType)(a.Action)
}

func ServiceWatchResponse_AreEqual(a *pb.ServiceWatchResponse, b *pb.ServiceWatchResponse) bool {
	return a.Id == b.Id && a.Key == b.Key
}

func (wn *WorkerNode) watchServiceLoop(ctx context.Context) error {
	if serviceMap == nil {
		serviceMap = make(map[string]string)
	}

	serviceClient := pb.NewServiceDClient(wn.conn)

	ipMapP := &serviceMap
	ipMap := *ipMapP

	endpointMap := make(map[string]*pb.ServiceWatchResponse)
	store := &endpointMap

	r := reconciler.NewReconcilerFromMap[*pb.ServiceWatchResponse, *pb.ServiceWatchResponse](
		func(desiredItem *pb.ServiceWatchResponse,
			localItem *pb.ServiceWatchResponse, id string, opType reconciler.DeltaType) (*pb.ServiceWatchResponse, error) {

			mx.Lock()
			defer mx.Unlock()

			if opType == reconciler.SNAPSHOT {
				ipMap = make(map[string]string)
				*ipMapP = ipMap
			}
			if opType != reconciler.DELETE {

			}

			if opType == reconciler.DELETE {
				//delete(ipMap, localItem.NicConfiguration.IPConfiguration.DnsName)
				return nil, nil
			}

			return desiredItem, nil
		},
		ServiceWatchResponse_AreEqual, store)

	r.Reconcile(store)

	//w, err := reconciler.NewStreamWatcherWithState[pb.NetworkD_WatchIPMappingClient, *pb.WatchIPMappingResponse](2*time.Second,
	//	WatchIPMappingResponse_GeyKey, WatchIPMappingResponse_AreEqual,
	//	m, &d.mx)
	w, err := reconciler.NewStreamWatcher[pb.ServiceD_WatchClient, *pb.ServiceWatchResponse](10*time.Second,
		ServiceWatchResponse_GeyKey, ServiceWatchResponse_AreEqual)
	if err != nil {
		return err
	}

	err = w.WatchLoop("serviceLoop",
		func(ctx context.Context) (pb.ServiceD_WatchClient, error) {
			r := &pb.ServiceWatchRequest{
				Id: "",
			}

			//l := log.WithField("Watcher", "serviceLoop")

			ctx = metadata.AppendToOutgoingContext(ctx, "mysession", "dnsServerLoop")
			watchIP, err := serviceClient.Watch(ctx, r)
			if err != nil {
				return nil, err
			}
			return watchIP, nil
		},
		func(ipMap *pb.ServiceWatchResponse) error {
			//key, op := w.keyer(resp)
			r.ProcessChangeEvent(ipMap, ipMap.Key, reconciler.DeltaType(ipMap.Action))
			return nil
		},
		ctx)

	return nil
}
