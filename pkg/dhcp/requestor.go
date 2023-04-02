// Copyright 2015 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dhcpHandler

import (
	"errors"
	"net"
	"sync"
	"time"
)

var errNoMoreTries = errors.New("no more tries")

type DHCP struct {
	mux             sync.Mutex
	leases          map[string]*DHCPLease
	hostNetnsPrefix string
	clientTimeout   time.Duration
	clientResendMax time.Duration
	broadcast       bool
}

type DHCPOptions struct {
	// When requesting IP from DHCP server, carry these options for management purpose.
	// Some fields have default values, and can be override by setting a new option with the same name at here.
	ProvideOptions []ProvideOption `json:"provide"`
	// When requesting IP from DHCP server, claiming these options are necessary. Options are necessary unless `optional`
	// is set to `false`.
	// To override default requesting fields, set `skipDefault` to `false`.
	// If an field is not optional, but the server failed to provide it, error will be raised.
	RequestOptions []RequestOption `json:"request"`
}

// DHCPOption represents a DHCP option. It can be a number, or a string defined in manual dhcp-options(5).
// Note that not all DHCP options are supported at all time. Error will be raised if unsupported options are used.
type DHCPOption string

type ProvideOption struct {
	Option DHCPOption `json:"option"`

	Value           string `json:"value"`
	ValueFromCNIArg string `json:"fromArg"`
}

type RequestOption struct {
	SkipDefault bool `json:"skipDefault"`

	Option DHCPOption `json:"option"`
}

func newDHCP(clientTimeout, clientResendMax time.Duration) *DHCP {
	return &DHCP{
		leases:          make(map[string]*DHCPLease),
		clientTimeout:   clientTimeout,
		clientResendMax: clientResendMax,
	}
}

func generateClientID(interfaceID string, ifName string) string {
	clientID := interfaceID + "-" + ifName
	// defined in RFC 2132, length size can not be larger than 1 octet. So we truncate 254 to make everyone happy.
	if len(clientID) > 254 {
		clientID = clientID[0:254]
	}
	return clientID
}

// Allocate acquires an IP from a DHCP server for a specified container.
// The acquired lease will be maintained until Release() is called.
func (d *DHCP) Allocate(interfaceID string, ifName string, opt *DHCPOptions) (*net.IPNet, error) {
	netNs := "/proc/1/ns/net"

	clientID := generateClientID(interfaceID, ifName)

	opt.ProvideOptions = append(opt.ProvideOptions, ProvideOption{
		Option: "host-name",
		Value:  clientID,
	})

	optsRequesting, optsProviding, err := prepareOptions(opt.ProvideOptions, opt.RequestOptions)
	if err != nil {
		return nil, err
	}

	hostNetns := d.hostNetnsPrefix + netNs
	l, err := AcquireLease(clientID, hostNetns, ifName,
		optsRequesting, optsProviding,
		d.clientTimeout, d.clientResendMax, d.broadcast)
	if err != nil {
		return nil, err
	}

	ip, err := l.IPNet()
	if err != nil {
		l.Stop()
		return nil, err
	}

	d.setLease(clientID, l)

	return ip, nil
}

// Release stops maintenance of the lease acquired in Allocate()
// and sends a release msg to the DHCP server.
func (d *DHCP) Release(containerID string, ifName string) error {
	clientID := generateClientID(containerID, ifName)
	if l := d.getLease(clientID); l != nil {
		l.Stop()
		d.clearLease(clientID)
	}

	return nil
}

func (d *DHCP) getLease(clientID string) *DHCPLease {
	d.mux.Lock()
	defer d.mux.Unlock()

	// TODO(eyakubovich): hash it to avoid collisions
	l, ok := d.leases[clientID]
	if !ok {
		return nil
	}
	return l
}

func (d *DHCP) setLease(clientID string, l *DHCPLease) {
	d.mux.Lock()
	defer d.mux.Unlock()

	// TODO(eyakubovich): hash it to avoid collisions
	d.leases[clientID] = l
}

//func (d *DHCP) clearLease(contID, netName, ifName string) {
func (d *DHCP) clearLease(clientID string) {
	d.mux.Lock()
	defer d.mux.Unlock()

	// TODO(eyakubovich): hash it to avoid collisions
	delete(d.leases, clientID)
}

func NewDhcpHandler(dhcpClientTimeout time.Duration, resendMax time.Duration, broadcast bool,
) *DHCP {
	dhcp := newDHCP(dhcpClientTimeout, resendMax)
	dhcp.hostNetnsPrefix = ""
	dhcp.broadcast = broadcast

	return dhcp
}
