package workernode

/*
import (
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/dariopb/netd/pkg/types"
	log "github.com/sirupsen/logrus"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"golang.zx2c4.com/wireguard/windows/driver"
)

type nodeVRF struct {
	IsNamespaced bool
	IsShared     bool
	NodeName     string
}

var adapters = make(map[string]*driver.Adapter)

func (c nodeVRF) getNicName(id string, nicID string) string {
	nicName := fmt.Sprintf("alc_%s", id)
	if nicID != "" {
		nicName = nicID
	}
	if len(nicName) > 14 {
		nicName = nicName[:14]
	}

	return nicName
}

func (c nodeVRF) CleanupAllVRFs() error {
	log.Infof("Cleaning all VRFs...")

	for nicID, adapter := range adapters {
		log.Infof("Closing adapter: [%s]", nicID)

		nsFinalFilename := fmt.Sprintf("c:/alacrity/%s/", nicID)
		os.Remove(nsFinalFilename + "_priv")
		os.Remove(nsFinalFilename + "_pub")

		adapter.Close()
	}

	return nil
}

func (c nodeVRF) RemoveVRF(nicID string) error {
	nsFinalFilename := fmt.Sprintf("c:/alacrity/%s", nicID)

	if adapter, ok := adapters[nicID]; ok {
		adapter.Close()
	}

	os.Remove(nsFinalFilename + "_priv")
	os.Remove(nsFinalFilename + "_pub")

	return nil
}

func getStableGUID(name string) *windows.GUID {
	b := make([]byte, unsafe.Sizeof(windows.GUID{}))
	if _, err := io.ReadFull(hkdf.New(md5.New, []byte(name), nil, nil), b); err != nil {
		return nil
	}
	return (*windows.GUID)(unsafe.Pointer(&b[0]))
}

// Implement the vrf for a network.
func (c nodeVRF) SetupVRF(n *types.VNETDataWorker, nic *types.NicData) (string, error) {
	log.Infof("Going to setup VRF for [%s]  ==> %s on vlanID: %s", n.Metadata.ID, n.Props.Subnet, n.Props.VlanID)

	_, ipnet, err := net.ParseCIDR(n.Props.Subnet)
	if err != nil {
		return "", err
	}
	ipnet.IP = net.ParseIP(nic.Props.IP)
	if ipnet.IP == nil {
		return "", err
	}

	nicName := c.getNicName(n.Metadata.ID, nic.Metadata.ID)
	pubKey, privKey := "", ""

	wgc, err := wgctrl.New()
	if err != nil {
		return "", err
	}
	defer wgc.Close()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	nsFinalFilename := fmt.Sprintf("c:/alacrity/%s", nicName)

	if _, err := os.Stat(nsFinalFilename + "_priv"); err == nil {
		log.Infof("    => found key file, using it")
	} else {
		key, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return "", err
		}

		err = ioutil.WriteFile(nsFinalFilename+"_priv", []byte(key.String()), 0644)
		if err != nil {
			return "", err
		}

		pKey := key.PublicKey()
		err = ioutil.WriteFile(nsFinalFilename+"_pub", []byte(pKey.String()), 0644)
		if err != nil {
			return "", err
		}

		log.Errorf("Generated keypair: pubKey: [%s]", pKey.String())
	}

	buff, err := ioutil.ReadFile(nsFinalFilename + "_pub")
	if err != nil {
		return "", err
	}
	pubKey = strings.TrimSuffix(string(buff), "\n")
	log.Infof("Using keypair: pubKey: [%s]", pubKey)

	buff, err = ioutil.ReadFile(nsFinalFilename + "_priv")
	if err != nil {
		return "", err
	}
	privKey = strings.TrimSuffix(string(buff), "\n")

	adapter, err := driver.OpenAdapter(nicName)
	if err != nil {
		adapter, err = driver.CreateAdapter(nicName, "WireGuard", getStableGUID(nicName))
		if err != nil {
			return "", err
		}

		// Configure the IP address
		luid := adapter.LUID()

		err = luid.SetIPAddresses([]net.IPNet{*ipnet})
		if err != nil {
			return "", err
		}

		key := make([]byte, wgtypes.KeyLen)
		listenPort := nic.Props.ListenPort
		_, err := base64.StdEncoding.Decode(key, []byte(privKey))
		if err != nil {
			return "", err
		}

		wgtconf := wgtypes.Config{
			PrivateKey: (*wgtypes.Key)(key),
			ListenPort: &listenPort,
		}
		wgc.ConfigureDevice(nicName, wgtconf)

		adapters[nicName] = adapter
	}

	err = adapter.SetAdapterState(driver.AdapterStateUp)
	if err != nil {
		return "", err
	}

	err = c.populateNicWGData(nic, nicName, nicName)
	if err != nil {
		c.CleanupVRF(n, nic)
		return "", err
	}

	return pubKey, nil
}

func (c nodeVRF) populateNicWGData(nic *types.NicData, ns string, nicName string) error {
	var err error

	wgc, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer wgc.Close()

	d, err := wgc.Device(nicName)
	if err != nil {
		return err
	}
	nic.Props.ListenPort = d.ListenPort
	nic.Props.PublicKey = d.PublicKey.String()

	return nil
}

// Cleanup the vrf for the network.
func (c nodeVRF) CleanupVRF(n *types.VNETDataWorker, nic *types.NicData) error {
	log.Infof("Going to cleanup VRF for [%s]  ==> %s on vlanID: %s", n.Metadata.ID, n.Props.Subnet, n.Props.VlanID)

	nicName := c.getNicName(n.Metadata.ID, nic.Metadata.ID)
	//nsFinalFilename := fmt.Sprintf("c:/alacrity/%s", nicName)

	if adapter, ok := adapters[nicName]; ok {
		adapter.Close()
	}

	//os.Remove(nsFinalFilename + "_priv")
	//os.Remove(nsFinalFilename + "_pub")

	return nil
}

func (c nodeVRF) AddLocalEndpoint(n *types.VNETDataWorker, nic *types.NicData) error {
	//netsh interface ipv4 add address "temp1" 1.99.0.111 255.255.255.0
	//netsh interface ipv4 add address "temp1" gateway=1.99.0.1 gwmetric=2
	return nil
}

func (c nodeVRF) RemoveLocalEndpoint(n *types.VNETDataWorker, nic *types.NicData) error {
	return nil
}

// Add a new endpoint
func (c nodeVRF) AddEndpoint(n *types.VNETDataWorker, nic *types.NicData, ep *types.NicData) error {
	log.Infof("Adding remote endpoint [%s] on subnet [%s] ==> %v -> %v", ep.Metadata.ID, nic.Props.SubnetID, nic, ep)

	return c.configureEndpoint(n, nic, ep, false)
}

func (c nodeVRF) RemoveEndpoint(n *types.VNETDataWorker, nic *types.NicData, ep *types.NicData) error {
	//var err error
	log.Infof("Removing endpoint [%s] from subnet [%s] ==> %v -> %v", ep.Metadata.ID, nic.Props.SubnetID, nic, ep)

	return c.configureEndpoint(n, nic, ep, true)
}

// Remove a new endpoint
func (c nodeVRF) configureEndpoint(n *types.VNETDataWorker, nic *types.NicData, ep *types.NicData, isRemove bool) error {
	nicName := c.getNicName(n.Metadata.ID, nic.Metadata.ID)

	wgc, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer wgc.Close()

	pk := make([]byte, 32)
	_, err = base64.StdEncoding.Decode(pk, []byte(ep.Props.PublicKey))
	if err != nil {
		return err
	}
	pka := time.Second * 30
	endpointAddr, err := net.ResolveUDPAddr("udp", ep.Props.Endpoint)
	if err != nil {
		return err
	}

	var allowedIP *net.IPNet
	if !ep.Props.IsGW {
		_, allowedIP, err = net.ParseCIDR(ep.Props.IP + "/32")
	} else {
		_, allowedIP, err = net.ParseCIDR(n.Props.Subnet)
		if err != nil {
			return err
		}
	}

	wgtconf := wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:                   *(*wgtypes.Key)(pk),
				AllowedIPs:                  []net.IPNet{*allowedIP},
				Endpoint:                    endpointAddr,
				PersistentKeepaliveInterval: &pka,
				Remove:                      isRemove,
			},
		},
	}
	wgc.ConfigureDevice(nicName, wgtconf)

	return nil
}

*/
