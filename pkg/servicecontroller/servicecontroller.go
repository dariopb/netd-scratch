package servicecontroller

import (
	"context"
	"sync"
	"time"

	"github.com/dariopb/netd/pkg/reconciler"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	pbVnet "github.com/dariopb/netd/proto/netd.v1alpha"
	pb "github.com/dariopb/netd/proto/service.v1alpha"

	log "github.com/sirupsen/logrus"
)

type ServiceController struct {
	conn       *grpc.ClientConn
	serviceMap map[string]string
	mx         sync.Mutex
}

type vnetWatcherType struct {
	serviceCount int
	cancelFn     context.CancelFunc
}

// NewServiceController creates a service controller
func NewServiceController(conn *grpc.ClientConn, ctx context.Context) (*ServiceController, error) {
	log.Infof("Creating ServiceController")

	sc := &ServiceController{
		conn:       conn,
		serviceMap: make(map[string]string),
	}

	return sc, nil
}

func ServiceWatchResponse_GeyKey(a *pb.ServiceWatchResponse) (string, reconciler.DeltaType) {
	return a.Key, (reconciler.DeltaType)(a.Action)
}

func ServiceWatchResponse_AreEqual(a *pb.ServiceWatchResponse, b *pb.ServiceWatchResponse) bool {
	return a.Id == b.Id && a.Key == b.Key
}

func WatchIPMappingResponse_GeyKey(a *pbVnet.WatchIPMappingResponse) (string, reconciler.DeltaType) {
	return a.Key, (reconciler.DeltaType)(a.Action)
}

func WatchIPMappingResponse_AreEqual(a *pbVnet.WatchIPMappingResponse, b *pbVnet.WatchIPMappingResponse) bool {
	return a.Id == b.Id && a.Key == b.Key
}

func (sc *ServiceController) WatchServiceLoop(ctx context.Context) error {

	//vnetClient := pbVnet.NewNetworkDClient(sc.conn)
	serviceClient := pb.NewServiceDClient(sc.conn)

	//tagEndpoints := make(map[string]*pb.Service)
	serviceMap := make(map[string]*pb.Service)
	//vnetTagMap := make(map[string]*vnetWatcherType)

	w, err := reconciler.NewStreamWatcher[pb.ServiceD_WatchClient, *pb.ServiceWatchResponse](10*time.Second,
		ServiceWatchResponse_GeyKey, ServiceWatchResponse_AreEqual)
	if err != nil {
		return err
	}

	err = w.WatchLoop("ServiceController-services",
		func(ctx context.Context) (pb.ServiceD_WatchClient, error) {
			r := &pb.ServiceWatchRequest{
				Id: "",
			}

			//l := log.WithField("Watcher", "serviceLoop")

			ctx = metadata.AppendToOutgoingContext(ctx, "mysession", "ServiceController-services")
			watchIP, err := serviceClient.Watch(ctx, r)
			if err != nil {
				return nil, err
			}
			return watchIP, nil
		},
		func(item *pb.ServiceWatchResponse) error {
			sc.mx.Lock()
			defer sc.mx.Unlock()

			if item.Service != nil && item.Service.VnetId == "" {
				log.Errorf("Service: [%s] missing vnetId. Skipping.", item.Service.Id)
				return nil
			}
			opType := reconciler.DeltaType(item.Action)

			if opType == reconciler.SNAPSHOT {
				//tagEndpoints = make(map[string]*pb.Service)
				serviceMap = make(map[string]*pb.Service)
			}
			if opType != reconciler.DELETE {
				if len(item.Service.Selectors) == 0 {
					return nil
				}
				// Changed to couple it with the vnet code, so not needed...
				/*if _, ok := tagEndpoints[item.Service.Selector[0]]; !ok {
					// start watching the vnet if not already
					tagKey := item.Service.VnetId + item.Service.Selector[0]
					vnetWatcher, ok1 := vnetTagMap[tagKey]
					if !ok1 {
						ctxVnet, cancelFn := context.WithCancel(ctx)
						vnetWatcher = &vnetWatcherType{
							cancelFn: cancelFn,
						}
						vnetTagMap[tagKey] = vnetWatcher

						go sc.watchVnetIpMapping(item.Service.VnetId, vnetClient, ctxVnet)
					}

					vnetWatcher.serviceCount++
				}
				*/

				//tagEndpoints[item.Service.Selector[0]] = item.Service
				serviceMap[item.Key] = item.Service
			}

			if opType == reconciler.DELETE {

				/*if old, ok := serviceMap[item.Key]; ok {
					if len(old.Selector) > 0 {
						delete(tagEndpoints, old.Selector[0])

						tagKey := old.VnetId + old.Selector[0]
						vnetWatcher, ok1 := vnetTagMap[tagKey]
						if ok1 {
							vnetWatcher.serviceCount--
							if vnetWatcher.serviceCount == 0 {
								vnetWatcher.cancelFn()
							}

							delete(vnetTagMap, tagKey)
						}
					}
					delete(serviceMap, item.Key)
				}
				*/
				return nil
			}

			return nil
		},
		ctx)

	return nil
}

func (sc *ServiceController) watchVnetIpMapping(vnetId string, vnetClient pbVnet.NetworkDClient, ctx context.Context) error {

	nw, err := reconciler.NewStreamWatcher[pbVnet.NetworkD_WatchIPMappingClient, *pbVnet.WatchIPMappingResponse](10*time.Second,
		WatchIPMappingResponse_GeyKey, WatchIPMappingResponse_AreEqual)
	if err != nil {
		return err
	}

	err = nw.WatchLoop("ServiceController-nics",
		func(ctx context.Context) (pbVnet.NetworkD_WatchIPMappingClient, error) {
			r := &pbVnet.WatchIPMappingRequest{
				VnetID: vnetId,
			}

			//l := log.WithField("Watcher", "serviceLoop")

			ctx = metadata.AppendToOutgoingContext(ctx, "mysession", "ServiceController-nics")
			watchIP, err := vnetClient.WatchIPMapping(ctx, r)
			if err != nil {
				return nil, err
			}
			return watchIP, nil
		},
		func(ipMap *pbVnet.WatchIPMappingResponse) error {
			//key, op := w.keyer(resp)
			//r.ProcessChangeEvent(ipMap, ipMap.Key, reconciler.DeltaType(ipMap.Action))
			return nil
		},
		ctx)

	return err
}
