package workernode

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dariopb/netd/pkg/types"
	pb "github.com/dariopb/netd/proto/netd.v1alpha"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"
	"golang.zx2c4.com/wireguard/wgctrl"
	//"github.com/vishvananda/netns"
)

type vmVRF struct {
	IsNamespaced bool
	IsShared     bool
	NodeName     string
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
func (c vmVRF) SetupVRF(n *pb.VNet, nic *pb.NicConfiguration, localIndex string) (string, error) {
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
		return "", err
	}
	nicIP := net.ParseIP(nic.IPConfiguration.IPAddress)
	if nicIP == nil {
		return "", err
	}

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

	if alreadyCreated {
		//err = c.populateNicWGData(nic, nsFinal, nicName)
		//if err != nil {
		//	c.CleanupVRF(n, nic)
		//	return "", err
		//}

		return "", nil
	}

	nsEnterCmd := fmt.Sprintf("ip netns exec %s ", ns)

	localGWIP := net.ParseIP(n.GwLocalIPAddress)
	if localGWIP == nil {
		return "", err
	}

	cmds := make([]Command, 0)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("modprobe wireguard"), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.ip_forward=1", ""), Error: true})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip netns add %s", ns), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set dev lo up", nsEnterCmd), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.ip_forward=1", nsEnterCmd), Error: true})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link add %s_v0 type veth peer name %s_v1", nicName, nicName), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_v0 up", nicName), Error: true})

	if nic.NicType == pb.NicType_TAP {
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip tuntap add %s_t mode tap", nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_t up", nicName), Error: true})

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link add %s_br type bridge", nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_br up", nicName), Error: true})

		// add both to the bridge
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_v0 master %s_br", nicName, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_t master %s_br", nicName, nicName), Error: true})

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -A FORWARD -i %s_br  -j ACCEPT", nicName), Error: true})
	}

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set %s_v1 netns %s", nicName, ns), Error: true})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set %s_v1 up", nsEnterCmd, nicName), Error: true})
	// enable proxy arp so v1 handles any on link addresses (makes it easier for the VM config)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%ssysctl net.ipv4.conf.%s_v1.proxy_arp=1", nsEnterCmd, nicName), Error: true})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_v1", nsEnterCmd, localGWIP.String(), nicName), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v1", nsEnterCmd, nicIP.String(), nicName), Error: true})

	// setup wireguard
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link add dev %s_wg type wireguard", nicName), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link set dev %s_wg netns %s", nicName, ns), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s wg set %s_wg fwmark 0200 listen-port %s private-key %s", nsEnterCmd, nicName, nic.ListenPort, privKey), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip link set dev %s_wg up", nsEnterCmd, nicName), Error: true})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s dev %s_wg", nsEnterCmd, ipnet.String(), nicName), Error: true})

	if nic.NicType == pb.NicType_MAIN {
		ipnet.IP = nicIP
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s dev %s_v0", "", ipnet.String(), nicName), Error: true})
	}

	// this nw element is going to act as a gw for the subnet, configure it
	if nic.IsGW {
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -A FORWARD -i %s_v0  -j ACCEPT", nicName), Error: true})

		localOutGwIP := "169.255.0.1"
		localOutIP := fmt.Sprintf("169.255.0.%s", localIndex)
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/24 dev %s_v1", nsEnterCmd, localOutIP, nicName), Error: true})
		//cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v1", "", localOutGwIP, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add 0.0.0.0/0 via %s dev %s_v1", nsEnterCmd, localOutGwIP, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -A POSTROUTING -o %s_v1 -j MASQUERADE", nsEnterCmd, nicName), Error: true})

		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip add add %s/32 dev %s_v0", "", localOutGwIP, nicName), Error: false})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s ip route add %s/32 dev %s_v0", "", localOutIP, nicName), Error: true})
		cmds = append(cmds, Command{Cmd: fmt.Sprintf("%s iptables -t nat -A POSTROUTING -s %s -j MASQUERADE", "", localOutIP), Error: true})
	}

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("touch %s", nsFinalFilename), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("mount --bind %s %s", nsTempFilename, nsFinalFilename), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("umount %s", nsTempFilename), Error: true})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("rm %s", nsTempFilename), Error: true})

	err = ExecuteCommands(cmds, false)
	if err != nil {
		c.CleanupVRF(n.Id, nic.Name, localIndex)
		return "", err
	}

	return "", nil
}

func (c vmVRF) populateNicWGData(nic *types.NicData, ns string, nicName string) error {
	var err error

	if c.IsNamespaced {
		wgns, err := netns.GetFromName(ns)
		if err != nil {
			return err
		}
		defer wgns.Close()

		err = netns.Set(wgns)
		if err != nil {
			return err
		}
	}

	wgc, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer wgc.Close()

	//d, err := wgc.Device(nicName)
	//if err != nil {
	//	return err
	//}
	//nic.ListenPort = d.ListenPort

	return nil
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
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("ip link del %s_v0 type veth", nicName), Error: false})

	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -D FORWARD -i %s_v0 -j ACCEPT", nicName), Error: false})
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -D FORWARD -i %s_br -j ACCEPT", nicName), Error: false})
	localOutIP := fmt.Sprintf("169.255.0.%s", localIndex)
	cmds = append(cmds, Command{Cmd: fmt.Sprintf("iptables -t nat -D POSTROUTING -s %s -j MASQUERADE", localOutIP), Error: false})

	err := ExecuteCommands(cmds, false)
	if err != nil {
		log.Errorf("Failed executing cmds: %s", cmds, err.Error())
	}

	os.Remove(privKey)
	os.Remove(pubKey)
	os.Remove(nsFinalFilename)

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
