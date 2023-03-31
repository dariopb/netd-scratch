package workernode

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	dhcp "github.com/dariopb/netd/pkg/dhcp"
	"github.com/dariopb/netd/pkg/types"
	pb "github.com/dariopb/netd/proto/netd.v1alpha"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"
	"golang.zx2c4.com/wireguard/wgctrl"
)

type vmVRF struct {
	IsNamespaced bool
	IsShared     bool
	NodeName     string
	outboundIP   string
	outboundNic  *net.Interface
	localDynMech *dhcp.DHCP
}

func (c vmVRF) getNsName(id string, nicID string) string {
	return fmt.Sprintf("%s_alc_%s", c.NodeName, nicID)
}

func (c vmVRF) getNicName(id string, nicID string) string {
	nicName := fmt.Sprintf("alc_%s", id)
	if nicID != "" {
		nicName = nicID
	}
	if len(nicName) > 14 {
		nicName = nicName[:14]
	}

	return nicName
}

func (c vmVRF) CleanupAllVRFs() error {
	log.Infof("Cleaning all VRFs...")

	nss, err := filepath.Glob("/var/run/netns/*_alc_*")
	if err != nil {
		return err
	}

	for _, ns := range nss {
		ns = filepath.Base(ns)
		err := c.RemoveVRF(ns)
		if err != nil {
			log.Errorf("Failed cleaning up vrf for: %s", ns, err.Error())
			return err
		}
	}

	return nil
}

func (c vmVRF) RemoveVRF(ns string) error {
	var err error
	log.Infof("     => removing ns: [%s]", ns)

	cmd := fmt.Sprintf("ip netns delete %s", ns)
	args := strings.Fields(cmd)
	_, err = exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		log.Errorf("Failed executing cmd: [ip netns delete %s]: %s", ns, err.Error())
		return err
	}

	// best effor in the case of not namespaced
	parts := strings.Split(ns, "_")
	if len(parts) == 3 {
		cmd = fmt.Sprintf("ip link del dev %s type wireguard", parts[2])
		args = strings.Fields(cmd)
		_, err = exec.Command(args[0], args[1:]...).Output()
		if err != nil {
			log.Errorf("Failed executing cmd: [ip netns delete %s]: %s", ns, err.Error())
		}
	}

	return nil
}

// Implement the vrf for a network. "Just for now" use ip cmds...
func (c vmVRF) SetupVRF(n *pb.VNet, nic *pb.NicConfiguration, localIndex string) (*types.LocalNicData, error) {
	log.Infof("Going to setup VRF for [%s]  ==> %s on vlanID/localIndex: %s using local mode: %v", n.Id, n.Subnet, localIndex, nic.NicType)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	origns, _ := netns.Get()
	defer func() {
		netns.Set(origns)
		origns.Close()
	}()

	_, ipnet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return nil, err
	}
	nicIP := net.ParseIP(nic.IPConfiguration.IPAddress)
	if nicIP == nil {
		return nil, err
	}
	ipandnet := *ipnet
	ipandnet.IP = nicIP

	nsFinal := c.getNsName(n.Id, nic.Name)
	nicName := c.getNicName(n.Id, nic.Name)
	ns := fmt.Sprintf("%s_tmp", nsFinal)
	nsTempFilename := fmt.Sprintf("/var/run/netns/%s", ns)
	nsFinalFilename := fmt.Sprintf("/var/run/netns/%s", nsFinal)

	privKey := fmt.Sprintf("./keys/%s_priv", nicName) // fmt.Sprintf("/var/run/%s_priv", nicName)
	//pubKey := fmt.Sprintf("./keys/%s_pub", nicName)   //fmt.Sprintf("/var/run/%s_pub", nicName)
	alreadyCreated := false

	if _, err := os.Stat(nsFinalFilename); err == nil {
		log.Infof("    => found netns file, assuming the ns is created")
		alreadyCreated = true
	}

	localOutGwIP := "169.255.0.1"
	localOutIP := fmt.Sprintf("169.255.0.%s", localIndex)
	localOutIPToReport := localOutIP

	if alreadyCreated {
		// I need to re-get local resources and reconcile only the veth config...
		if nic.NicType == pb.NicType_INGRESS_DHCP {
			c.cleanupVethIngressEgress(nsFinal, nic.Name, localOutIP)
			localOutIPToReport, err = c.configureVethIngressEgress(nic, nsFinal, nicName, localOutIP, localOutGwIP, ipnet, nicIP)
			if err != nil {
				c.CleanupVRF(n.Id, nic.Name, localIndex)
				return nil, err
			}
		}

		lnd, err := c.populateNicWGData(nic, nsFinalFilename, nicName, localOutIPToReport, nicIP.String())
		//if err != nil {
		//	c.CleanupVRF(n, nic)
		//	return "", err
		//}

		return lnd, err
	}

	nsEnterCmd := fmt.Sprintf("ip netns exec %s ", ns)

	localGWIP := net.ParseIP(n.GwLocalIPAddress)
	if localGWIP == nil {
		return nil, err
	}

	cmds := make([]Command, 0)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("modprobe wireguard"), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.ip_forward=1", ""), Error: true})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip netns add %s", ns), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set dev lo up", nsEnterCmd), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.ip_forward=1", nsEnterCmd), Error: true})

	// setup wireguard
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link add dev %s_wg type wireguard", nicName), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set dev %s_wg netns %s", nicName, ns), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s wg set %s_wg fwmark 0200 listen-port %s private-key %s", nsEnterCmd, nicName, nic.ListenPort, privKey), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set dev %s_wg up", nsEnterCmd, nicName), Error: true})

	if nic.NicType == pb.NicType_INGRESS_LOCAL ||
		nic.NicType == pb.NicType_INGRESS_DHCP {
		// address is on the wg directly (INGRESS).
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s dev %s_wg", nsEnterCmd, ipandnet.String(), nicName), Error: true})
	} else {
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s dev %s_wg", nsEnterCmd, ipnet.String(), nicName), Error: true})
	}

	// Setup up function specific devices *not* related with veths.
	switch nic.NicType {
	case pb.NicType_TAP_MAIN:
		{
			// TAP on the main ns (legacy)
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip tuntap add %s_t mode tap", nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_t up", nicName), Error: true})

			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link add %s_br type bridge", nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_br up", nicName), Error: true})

			// add it to the bridge
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_t master %s_br", nicName, nicName), Error: true})

			cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -A FORWARD -i %s_br  -j ACCEPT", nicName), Error: true})
		}
	case pb.NicType_CONTAINER:
	case pb.NicType_TAP:
		{
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip tuntap add %s_t mode tap", nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_t netns %s", nicName, ns), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set %s_t up", nsEnterCmd, nicName), Error: true})

			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_t", nsEnterCmd, nicIP.String(), nicName), Error: true})

			// enable proxy arp so tap handles any on link addresses
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.conf.%s_t.proxy_arp=1", nsEnterCmd, nicName), Error: true})

			// the container runtime owns the connecting namespace and that is exposed to the container directly, in order to prevent
			// a, priviledge, container to change the ip settings, we always own our ns.
			// The cni will add the veths between the ns and setup the /32 route to the _v1 interface.
		}
	case pb.NicType_MAIN:
		{
		}
	case pb.NicType_INGRESS_LOCAL:
		{
			// in the namespace
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -A POSTROUTING -o %s_wg -j MASQUERADE", nsEnterCmd, nicName), Error: true})
		}
	case pb.NicType_INGRESS_DHCP:
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -A POSTROUTING -o %s_wg -j MASQUERADE", nsEnterCmd, nicName), Error: true})
	}

	if nic.NicType == pb.NicType_TAP {
		// TAP for VM: vm will really own the address, we need some local IP in the stack.
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_t", nsEnterCmd, localGWIP.String(), nicName), Error: true})
	}

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("touch %s", nsFinalFilename), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("mount --bind %s %s", nsTempFilename, nsFinalFilename), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("umount %s", nsTempFilename), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("rm %s", nsTempFilename), Error: true})

	err = ExecuteCommands(cmds, false)
	if err != nil {
		c.CleanupVRF(n.Id, nic.Name, localIndex)
		return nil, err
	}

	// Now the veths for egress/ingress.
	localOutIPToReport, err = c.configureVethIngressEgress(nic, nsFinal, nicName, localOutIP, localOutGwIP, ipnet, nicIP)
	if err != nil {
		c.CleanupVRF(n.Id, nic.Name, localIndex)
		return nil, err
	}

	lnd, err := c.populateNicWGData(nic, nsFinalFilename, nicName, localOutIPToReport, nicIP.String())

	return lnd, nil
}

func (c vmVRF) configureVethIngressEgress(nic *pb.NicConfiguration,
	ns string, nicName string, localOutIP string, localOutGwIP string, ipnet *net.IPNet, nicIP net.IP) (string, error) {
	var err error

	nsEnterCmd := fmt.Sprintf("ip netns exec %s ", ns)
	localOutIPToReport := localOutIP
	cmds := make([]Command, 0)

	needsVEth := nic.NicType == pb.NicType_TAP ||
		nic.NicType == pb.NicType_MAIN ||
		nic.NicType == pb.NicType_INGRESS_LOCAL || nic.NicType == pb.NicType_INGRESS_DHCP ||
		nic.IsGW

	if needsVEth {
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link add %s_v0 type veth peer name %s_v1", nicName, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_v0 up", nicName), Error: true})

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_v1 netns %s", nicName, ns), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set %s_v1 up", nsEnterCmd, nicName), Error: true})
		// enable proxy arp so v1 handles any on link addresses
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.conf.%s_v1.proxy_arp=1", nsEnterCmd, nicName), Error: true})

		// add a chain for easy cleanup
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -N %s_PRE", nsEnterCmd, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -N %s_POST", nsEnterCmd, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -I PREROUTING -j %s_PRE", nsEnterCmd, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -I POSTROUTING -j %s_POST", nsEnterCmd, nicName), Error: true})

	}

	// Finally configuration related to veths
	switch nic.NicType {
	case pb.NicType_TAP_MAIN:
		{
			// add the veth to the bridge
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_v0 master %s_br", nicName, nicName), Error: true})

			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_v1", nsEnterCmd, localOutGwIP, nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v1", nsEnterCmd, nicIP, nicName), Error: true})
		}
	case pb.NicType_CONTAINER:
	case pb.NicType_TAP:
		{
			// the container runtime owns the connecting namespace and that is exposed to the container directly, in order to prevent
			// a, priviledge, container to change the ip settings, we always own our ns.
			// The cni will add the veths between the ns and setup the /32 route to the _v1 interface.
		}
	case pb.NicType_MAIN:
		{
			ipnet.IP = nicIP

			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_v1", nsEnterCmd, localOutGwIP, nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v1", nsEnterCmd, nicIP.String(), nicName), Error: true})

			// address is on the main namespace (MAIN type)
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s dev %s_v0", "", ipnet.String(), nicName), Error: true})
		}
	case pb.NicType_INGRESS_LOCAL:
		{
			// I need to route local traffic "outside"
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl -w net.ipv4.conf.%s_v0.route_localnet=1", "", nicName), Error: true})

			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -N %s", "", nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -A PREROUTING -m addrtype --dst-type LOCAL -j %s", "", nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -A OUTPUT -m addrtype --src-type LOCAL -d localhost -j %s", "", nicName), Error: true})
			cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -A POSTROUTING -m addrtype --src-type LOCAL --dst-type UNICAST -o %s_v0 -j MASQUERADE", "", nicName), Error: true})
		}
	case pb.NicType_INGRESS_DHCP:
		// get a new dhcp address
		dhcpIP, err := c.localDynMech.Allocate(nicName, c.outboundNic.Name, &dhcp.DHCPOptions{})
		if err != nil {
			//err = fmt.Errorf("Failed to get dhcp lease for [%s]  ==> %s on vlanID/localIndex: %s using local mode: %v:  %v", n.Id, n.Subnet, localIndex, nic.NicType, err)
			//c.CleanupVRF(n.Id, nic.Name, localIndex)
			return "", err
		}

		localOutIPToReport = dhcpIP.IP.String()

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -N %s", "", nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v0", "", localOutIPToReport, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_v1", nsEnterCmd, localOutIPToReport, nicName), Error: true})

		// this is local network, make the IP reachable on the network.
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ebtables -t nat -I PREROUTING -p ARP --arp-op Request --arp-ip-dst %s -j arpreply --arpreply-mac %s", localOutIPToReport, c.outboundNic.HardwareAddr.String()), Error: true})
	}

	// this nw element is going to act as a gw for the subnet, configure it
	if nic.IsGW {
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -A FORWARD -i %s_v0  -j ACCEPT", nicName), Error: true})

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/24 dev %s_v1", nsEnterCmd, localOutIP, nicName), Error: true})
		//cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v1", "", localOutGwIP, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add 0.0.0.0/0 via %s dev %s_v1", nsEnterCmd, localOutGwIP, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -I %s_POST -o %s_v1 -j MASQUERADE", nsEnterCmd, nicName, nicName), Error: true})

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_v0", "", localOutGwIP, nicName), Error: false})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v0", "", localOutIP, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -A POSTROUTING -s %s -j MASQUERADE", "", localOutIP), Error: true})
	}

	err = ExecuteCommands(cmds, false)
	if err != nil {
		c.cleanupVethIngressEgress(ns, nic.Name, localOutIP)
		return "", err
	}

	return localOutIPToReport, nil
}

func (c vmVRF) cleanupVethIngressEgress(ns string, nicName string, localOutIP string) error {
	nsEnterCmd := fmt.Sprintf("ip netns exec %s ", ns)
	cmds := make([]Command, 0)

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link del %s_v0 type veth", nicName), Error: false})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -D FORWARD -i %s_v0 -j ACCEPT", nicName), Error: false})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -D POSTROUTING -s %s -j MASQUERADE", localOutIP), Error: false})

	// cleanup INGRESS config
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -F %s", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -D PREROUTING -m addrtype --dst-type LOCAL -j %s", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -D OUTPUT -m addrtype --src-type LOCAL -d localhost -j %s", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -D POSTROUTING -m addrtype --src-type LOCAL --dst-type UNICAST -o %s_v0 -j MASQUERADE", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -X %s", nicName), Error: false})

	// cleaup the chain
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -F %s_PRE", nsEnterCmd, nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -F %s_POST", nsEnterCmd, nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -D PREROUTING -j %s_PRE", nsEnterCmd, nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -D POSTROUTING -j %s_POST", nsEnterCmd, nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -X %s_PRE", nsEnterCmd, nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%siptables -t nat -X %s_POST", nsEnterCmd, nicName), Error: false})

	// this is local network, make the IP reachable on the network.
	//cmds = append(cmds, Command{Cmd: fmt.Sprintf("ebtables -t nat -I PREROUTING -p ARP --arp-op Request --arp-ip-dst %s -j arpreply --arpreply-mac %s", localOutIPToReport, c.outboundNic.HardwareAddr.String()), Error: true})

	err := ExecuteCommands(cmds, false)
	if err != nil {
		log.Errorf("cleanupVethIngressEgress: failed executing cmds: %s", cmds, err.Error())
	}

	return nil
}

func (c vmVRF) populateNicWGData(nic *pb.NicConfiguration, ns string, nicName string, localOutIP string, nicIP string) (*types.LocalNicData, error) {
	var err error

	if c.IsNamespaced {
		wgns, err := netns.GetFromName(ns)
		if err != nil {
			return nil, err
		}
		defer wgns.Close()

		err = netns.Set(wgns)
		if err != nil {
			return nil, err
		}
	}

	wgc, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	defer wgc.Close()

	//d, err := wgc.Device(nicName)
	//if err != nil {
	//	return err
	//}
	//nic.ListenPort = d.ListenPort

	lnd := &types.LocalNicData{
		NicId:      nicName,
		NS:         ns,
		LocalOutIP: localOutIP,
		IP:         nicIP,
	}

	return lnd, nil
}

// Cleanup the vrf for the network. "Just for now" use ip cmds...
func (c vmVRF) CleanupVRF(vnetID string, nicID string, localIndex string) error {
	log.Infof("Going to cleanup VRF for [%s]  ==> %s on vlanID/localIndex: %s", nicID, vnetID, localIndex)

	ns := c.getNsName(nicID, nicID)
	nicName := c.getNicName(nicID, nicID)

	privKey := fmt.Sprintf("./keys/%s_priv", nicName) // fmt.Sprintf("/var/run/%s_priv", nicName)
	pubKey := fmt.Sprintf("./keys/%s_pub", nicName)   //fmt.Sprintf("/var/run/%s_pub", nicName)
	nsFinalFilename := fmt.Sprintf("/var/run/netns/%s", ns)

	cmds := make([]Command, 0)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip netns delete %s_tmp", ns), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip netns delete %s", ns), Error: false})

	//cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link del dev %s_wg type wireguard", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip tuntap del %s_t mode tap", nicName), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link del %s_br type bridge", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -D FORWARD -i %s_br -j ACCEPT", nicName), Error: false})

	err := ExecuteCommands(cmds, false)
	if err != nil {
		log.Errorf("Failed executing cmds: %s", cmds, err.Error())
	}

	localOutIP := fmt.Sprintf("169.255.0.%s", localIndex)
	c.cleanupVethIngressEgress(nsFinalFilename, nicName, localOutIP)

	os.Remove(privKey)
	os.Remove(pubKey)
	os.Remove(nsFinalFilename)

	// Release any dhcp leases
	err = c.localDynMech.Release(nicName, c.outboundNic.Name)
	if err != nil {
		err = fmt.Errorf("Failed to release dhcp lease for [%s]: %v", nicID, err)
	}

	return nil
}

func (c vmVRF) AddLocalEndpoint(n *pb.VNet, nic *pb.NicConfiguration) error {
	return nil
}

func (c vmVRF) RemoveLocalEndpoint(n *pb.VNet, nic *pb.NicConfiguration) error {
	return nil
}

// Add a new endpoint
func (c vmVRF) AddEndpoint(vnetId string, nicId string, nic *pb.NicConfiguration) error {
	var err error
	log.Infof("Adding remote endpoint [%s] on subnet [%s] ==> %v", nicId, nic.Vnet, nic)

	ns := c.getNsName(nicId, nicId)
	nicName := c.getNicName(nicId, nicId)
	nsEnterCmd := fmt.Sprintf("ip netns exec %s ", ns)

	var allowedIP *net.IPNet
	_, allowedIP, err = net.ParseCIDR(nic.IPConfiguration.IPAddress + "/32")
	if err != nil {
		return err
	}

	allowedIPs := allowedIP.String()
	if nic.IsGW {
		_, allowedIP, err = net.ParseCIDR("0.0.0.0/0")
		if err != nil {
			return err
		}

		allowedIPs = allowedIPs + "," + allowedIP.String()
	}

	cmds := make([]Command, 0)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%swg set %s_wg peer %s persistent-keepalive 30 endpoint %s allowed-ips %s", nsEnterCmd, nicName, nic.PublicKey, nic.Endpoint, allowedIPs), Error: true})
	if nic.IsGW {
		// add the default route in the namespace as well
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s dev %s_wg", nsEnterCmd, "0.0.0.0/0", nicName), Error: false})
	}

	err = ExecuteCommands(cmds, false)
	if err != nil {
		return err
	}
	return nil
}

// Remove a new endpoint
func (c vmVRF) RemoveEndpoint(vnetId string, nicId string, nic *pb.NicConfiguration) error {
	var err error
	log.Infof("Removing endpoint [%s] from subnet [%s] ==> %v", nicId, vnetId, nic)

	ns := c.getNsName(nicId, nicId)
	nicName := c.getNicName(nicId, nicId)
	nsEnterCmd := fmt.Sprintf("ip netns exec %s ", ns)

	cmds := make([]Command, 0)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%swg set %s_wg peer %s remove", nsEnterCmd, nicName, nic.PublicKey), Error: true})

	err = ExecuteCommands(cmds, false)
	if err != nil {
		return err
	}
	return nil
}
