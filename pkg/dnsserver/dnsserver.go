package dnsserver

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dariopb/netd/pkg/reconciler"
	"github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	pb "github.com/dariopb/netd/proto/netd.v1alpha"
	log "github.com/sirupsen/logrus"
)

type DNSServer struct {
	serverEndpoint string

	endpointMap       map[string]*map[string]string
	vnetClient        pb.NetworkDClient
	forwarderEndpoint string
	ttl               int
	ctx               context.Context
	mx                sync.Mutex
}

func NewDNSServer(port int, forwarderEndpoint string, conn *grpc.ClientConn) *DNSServer {

	vnetClient := pb.NewNetworkDClient(conn)

	endpoint := fmt.Sprintf(":%d", port)
	d := &DNSServer{
		serverEndpoint: endpoint,

		endpointMap:       make(map[string]*map[string]string),
		vnetClient:        vnetClient,
		forwarderEndpoint: forwarderEndpoint,
		ttl:               60,
	}

	// for now populate "vnet1"
	m := make(map[string]string)
	d.endpointMap["vnet1"] = &m

	return d
}

func (d *DNSServer) StartDnsServer() error {
	d.ctx = context.Background()

	udpServer := &dns.Server{Addr: d.serverEndpoint, Net: "udp"}
	tcpServer := &dns.Server{Addr: d.serverEndpoint, Net: "tcp"}
	dns.HandleFunc(".", d.handleRequest)
	go func() {
		if err := udpServer.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()
	go func() {
		if err := tcpServer.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	go func() {
		d.watchVNetNicsLoop("vnet1")
	}()

	return nil
}

func (d *DNSServer) handleRequest(w dns.ResponseWriter, r *dns.Msg) {
	domain := r.Question[0].Name

	log.Infof("DNS: clientReq: %s, domain: %s", w.RemoteAddr().String(), domain)

	vnedID := "vnet1"

	d.mx.Lock()
	vnetMap, ok := d.endpointMap[vnedID]
	if !ok {
		d.mx.Unlock()
		d.forward(d.forwarderEndpoint, w, r)
		return
	}

	ip, ok := (*vnetMap)[strings.TrimSuffix(domain, ".")]
	if !ok {
		d.mx.Unlock()
		d.forward(d.forwarderEndpoint, w, r)
		return
	}

	d.mx.Unlock()

	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	rr1 := new(dns.A)
	rr1.Hdr = dns.RR_Header{Name: domain, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: uint32(d.ttl)}

	//rr2 := new(dns.AAAA)
	//rr2.Hdr = dns.RR_Header{Name: domain, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: uint32(d.ttl)}

	rr1.A = net.ParseIP(ip)
	//rr2.AAAA = net.ParseIP(localhostIPv6)

	m.Answer = []dns.RR{rr1}
	w.WriteMsg(m)
}

func (d *DNSServer) forward(addr string, w dns.ResponseWriter, req *dns.Msg) {
	transport := "udp"
	if _, ok := w.RemoteAddr().(*net.TCPAddr); ok {
		transport = "tcp"
	}

	/*	if isTransfer(req) {
			if transport != "tcp" {
				dns.HandleFailed(w, req)
				return
			}
			t := new(dns.Transfer)
			c, err := t.In(req, addr)
			if err != nil {
				dns.HandleFailed(w, req)
				return
			}
			if err = t.Out(w, req, c); err != nil {
				dns.HandleFailed(w, req)
				return
			}
			return
		}
	*/

	c := &dns.Client{Net: transport}
	resp, _, err := c.Exchange(req, addr)
	if err != nil {
		dns.HandleFailed(w, req)
		return
	}
	w.WriteMsg(resp)
}

func WatchIPMappingResponse_GeyKey(a *pb.WatchIPMappingResponse) (string, reconciler.DeltaType) {
	return a.Key, (reconciler.DeltaType)(a.Action)
}

func WatchIPMappingResponse_AreEqual(a *pb.WatchIPMappingResponse, b *pb.WatchIPMappingResponse) bool {
	return a.Id == b.Id && a.Key == b.Key
}

func (d *DNSServer) watchVNetNicsLoop(vnetID string) error {
	ipMapP := d.endpointMap["vnet1"]
	ipMap := *ipMapP

	endpointMap := make(map[string]*pb.WatchIPMappingResponse)
	store := &endpointMap

	r := reconciler.NewReconcilerFromMap[*pb.WatchIPMappingResponse, *pb.WatchIPMappingResponse](
		func(desiredItem *pb.WatchIPMappingResponse,
			localItem *pb.WatchIPMappingResponse, id string, opType reconciler.DeltaType) (*pb.WatchIPMappingResponse, error) {

			d.mx.Lock()
			defer d.mx.Unlock()

			if opType == reconciler.SNAPSHOT {
				ipMap = make(map[string]string)
				*ipMapP = ipMap
			}
			if opType != reconciler.DELETE {
				// register all the possible names
				if desiredItem.NicConfiguration.IPConfiguration.DnsConfig != nil {
					for _, suffix := range desiredItem.NicConfiguration.IPConfiguration.DnsConfig.Search {
						name := desiredItem.NicConfiguration.IPConfiguration.DnsName + "." + suffix
						ipMap[name] = desiredItem.NicConfiguration.IPConfiguration.IPAddress
					}
				}

				ipMap[desiredItem.NicConfiguration.IPConfiguration.DnsName] = desiredItem.NicConfiguration.IPConfiguration.DnsName
			}

			if opType == reconciler.DELETE {
				if localItem.NicConfiguration.IPConfiguration.DnsConfig != nil {
					for _, suffix := range localItem.NicConfiguration.IPConfiguration.DnsConfig.Search {
						name := localItem.NicConfiguration.IPConfiguration.DnsName + "." + suffix
						delete(ipMap, name)
					}
					delete(ipMap, localItem.NicConfiguration.IPConfiguration.DnsName)
				}
				return nil, nil
			}

			return desiredItem, nil
		},
		WatchIPMappingResponse_AreEqual, store)

	r.Reconcile(store)

	//w, err := reconciler.NewStreamWatcherWithState[pb.NetworkD_WatchIPMappingClient, *pb.WatchIPMappingResponse](2*time.Second,
	//	WatchIPMappingResponse_GeyKey, WatchIPMappingResponse_AreEqual,
	//	m, &d.mx)
	w, err := reconciler.NewStreamWatcher[pb.NetworkD_WatchIPMappingClient, *pb.WatchIPMappingResponse](10*time.Second,
		WatchIPMappingResponse_GeyKey, WatchIPMappingResponse_AreEqual)
	if err != nil {
		return err
	}

	err = w.WatchLoop("dnsServerLoop",
		func(ctx context.Context) (pb.NetworkD_WatchIPMappingClient, error) {
			r := &pb.WatchIPMappingRequest{
				VnetID: vnetID,
			}

			//l := log.WithField("vnet", vnetID)

			ctx = metadata.AppendToOutgoingContext(ctx, "mysession", "dnsServerLoop")
			watchIP, err := d.vnetClient.WatchIPMapping(ctx, r)
			if err != nil {
				return nil, err
			}
			return watchIP, nil
		},
		func(ipMap *pb.WatchIPMappingResponse) error {
			//key, op := w.keyer(resp)
			r.ProcessChangeEvent(ipMap, ipMap.Key, reconciler.DeltaType(ipMap.Action))
			return nil
		},
		d.ctx)

	return nil
}
