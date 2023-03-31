package workernode

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/coreos/go-iptables/iptables"
	"github.com/dariopb/netd/pkg/reconciler"
	"google.golang.org/grpc/metadata"

	pb "github.com/dariopb/netd/proto/service.v1alpha"
	log "github.com/sirupsen/logrus"
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

	//serviceMapP := &serviceMap
	//serviceMap := *serviceMapP

	endpointMap := make(map[string]*pb.ServiceWatchResponse)
	store := &endpointMap

	r := reconciler.NewReconcilerFromMap[*pb.ServiceWatchResponse, *pb.ServiceWatchResponse](
		func(desiredItem *pb.ServiceWatchResponse,
			localItem *pb.ServiceWatchResponse, id string, opType reconciler.DeltaType) (*pb.ServiceWatchResponse, error) {

			l := log.WithField("Watcher", "serviceLoop")
			mx.Lock()
			defer mx.Unlock()

			if opType == reconciler.SNAPSHOT {
				return nil, nil // this is a map reconciler, snapshots are handled transparently
			}
			if opType == reconciler.UPDATE {
				l.Infof("Adding service/endpoints [%s] [selector: %v] ==> %v", desiredItem.Key, desiredItem.Service.Selectors, desiredItem)
				if v, ok := desiredItem.Service.Selectors["ingress_nic"]; ok {
					err := wn.applyRulesOnNic(serviceClient, "ingress_nic="+v, desiredItem, false)
					if err != nil {
						l.Warnf("Failed applying rules: %v", err)
					}
				}
			}

			if opType == reconciler.DELETE {
				if localItem == nil {
					return nil, nil
				}

				l.Infof("Removing service/endpoints [%s] [selector: %v] ==> %v", localItem.Key, localItem.Service.Selectors, localItem)
				if v, ok := localItem.Service.Selectors["ingress_nic"]; ok {
					err := wn.applyRulesOnNic(serviceClient, "ingress_nic="+v, localItem, true)
					if err != nil {
						l.Warn("Failed deleting rules: %v", err)
					}
				}

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

	err = w.WatchLoop("serviceWatcherLoop",
		func(ctx context.Context) (pb.ServiceD_WatchClient, error) {
			r := &pb.ServiceWatchRequest{
				Id: "",
			}

			//l := log.WithField("Watcher", "serviceLoop")

			ctx = metadata.AppendToOutgoingContext(ctx, "mysession", "serviceWatcherLoop")
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

type ff func(table, chain string, rulespec ...string) error

func (wn *WorkerNode) applyRulesOnNic(sc pb.ServiceDClient, nicTag string, item *pb.ServiceWatchResponse, isDelete bool) error {
	var err error

	lnd, err := wn.GetLocalNicDataForLabel(item.Service.VnetId, nicTag)
	if err != nil {
		return err
	}

	targetIP := ""

	netns, err := ns.GetNS(lnd.NS)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", lnd.NS, err)
	}
	defer netns.Close()

	tab, err := iptables.New()
	if err != nil {
		return err
	}

	ft := tab.AppendUnique
	if isDelete {
		ft = tab.DeleteIfExists

		err = netns.Do(func(hostNS ns.NetNS) error {
			tab.ClearChain("nat", lnd.NicId+"_PRE")
			return nil
		})
		if err != nil {
			return err
		}
	}

	var sourcePort, targetPort int32

	for _, endpoint := range item.Service.Endpoints {
		targetIP = endpoint.IpEndpoint

		if targetIP == "" {
			log.Warn("found empty endpoint while processing service entry: [%v]", endpoint)
			continue
		}

		for _, rule := range item.Service.Rules {
			shouldApply := true

			if tcp := rule.ProtoRule.(*pb.Rule_TcpRule); tcp != nil {
				sourcePort = tcp.TcpRule.Port
				targetPort = tcp.TcpRule.TargetPort
			} else {
				shouldApply = false
			}

			if shouldApply {
				err = ft("nat", "PREROUTING", strings.Split(fmt.Sprintf("-p tcp --dport %d -j DNAT --to-destination %s:%d", sourcePort, lnd.LocalOutIP, sourcePort), " ")...)
				if err != nil {
					return err
				}

				err = netns.Do(func(hostNS ns.NetNS) error {
					err = ft("nat", lnd.NicId+"_PRE", strings.Split(fmt.Sprintf("-d %s -p tcp --dport %d -j DNAT --to-destination %s:%d", lnd.LocalOutIP, sourcePort, targetIP, targetPort), " ")...)
					if err != nil {
						return err
					}
					return nil
				})
				if err != nil {
					return err
				}

				//cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -%s %s -p tcp --dport %d -j DNAT --to-destination %s:%d", "", action, lnd.NicId, sourcePort, lnd.LocalOutIP, targetPort), Error: !isDelete})
				// in the namespace
				//cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -%s PREROUTING -d %s -p tcp --dport %d -j DNAT --to-destination %s", nsEnterCmd, action, lnd.LocalOutIP, targetPort, targetIP), Error: !isDelete})

				//err = ExecuteCommands(cmds, false)
				//if err != nil {
				//	wn.applyRulesOnNic(nicTag, item, true)
				//	return err
				//}
			}
		}
	}

	if !isDelete {
		needsUpdate := false
		resUpdated := make([]*pb.RuleEndpoint, 0)

		if item.Service.RuleEndpoints == nil || len(item.Service.RuleEndpoints) == 0 {
			needsUpdate = true
		}

		for _, rule := range item.Service.Rules {
			for _, re := range item.Service.RuleEndpoints {
				if re.Name == rule.Name {
					if re.Endpoint == "" {
						needsUpdate = true
					}
					break
				}
			}

			if tcp := rule.ProtoRule.(*pb.Rule_TcpRule); tcp != nil {
				sourcePort = tcp.TcpRule.Port
				targetPort = tcp.TcpRule.TargetPort
			}

			resUpdated = append(resUpdated, &pb.RuleEndpoint{
				Name:     rule.Name,
				Endpoint: fmt.Sprintf("%s:%d", lnd.LocalOutIP, sourcePort),
			})
		}

		if needsUpdate {
			log.Infof("Going to update ruleEndpoints for service: [%s], [%v]", item.Id, resUpdated)
			ctx := context.Background()

			item.Service.RuleEndpoints = resUpdated
			sc.Update(ctx, item.Service)
		}
	}

	return nil
}
