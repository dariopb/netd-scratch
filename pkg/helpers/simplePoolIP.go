package helpers

import (
	"fmt"
	"net"
	"strconv"
	"sync"

	pb "github.com/dariopb/netd/proto/simplePool"
)

type SimplePoolEx struct {
	*pb.SimplePool

	mtx sync.Mutex
}

func NewSimplePoolIP(name string) *SimplePoolEx {
	sp := &pb.SimplePool{
		PoolId:    name,
		ItemsUsed: make(map[string]*pb.PoolItem),
	}

	pool := &SimplePoolEx{
		SimplePool: sp,
	}

	return pool
}

func GetSimplePool(sp *pb.SimplePool) *SimplePoolEx {
	pool := &SimplePoolEx{
		SimplePool: sp,
	}

	return pool
}

func (p *SimplePoolEx) GetInner() *pb.SimplePool {
	return p.SimplePool
}

func (p *SimplePoolEx) InitializeElements(subnet string, reservedIps []string) error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	_, _, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}

	p.Subnet = subnet
	for i, ip := range reservedIps {
		p.ItemsUsed["reserved-"+strconv.Itoa(i)] = &pb.PoolItem{Value: ip, Metadata: "reserved"}
	}

	return nil
}

func (p *SimplePoolEx) GetItem(itemID string, metadata string) (*pb.PoolItem, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	_, subnet, err := net.ParseCIDR(p.Subnet)
	if err != nil {
		return nil, err
	}

	desiredIP := net.ParseIP(itemID)

	reservedIPs := []net.IP{}
	for _, res := range p.ItemsUsed {
		r := net.ParseIP(res.Value)
		if r == nil {
			continue
		}
		reservedIPs = append(reservedIPs, r)
	}

	first := subnet.IP.To4()
	last := net.ParseIP("0.0.0.0").To4()
	copy(last, first)
	mask := subnet.Mask

	for i := 0; i < len(mask); i++ {
		last[i] |= (mask[i] ^ 0xFF)
	}

	item, ok := p.ItemsUsed[metadata]
	if !ok {
		curr := first

		if desiredIP != nil {
			curr = desiredIP
		} else {
			nextIP(&curr)
		}

		for !curr.Equal(last) {
			allocated := false
			for _, r := range reservedIPs {
				if curr.Equal(r) {
					allocated = true
					break
				}
			}

			if !allocated {
				item = &pb.PoolItem{
					Value:    curr.String(),
					Metadata: metadata,
				}
				break
			}

			if desiredIP != nil {
				break
			}

			nextIP(&curr)
		}
	}

	if item == nil {
		return nil, fmt.Errorf("Pool is exhausted")
	}
	p.ItemsUsed[metadata] = item

	return item, nil
}

func nextIP(ip *net.IP) {
	carry := true
	for i := len(*ip) - 1; i >= 0 && carry; i-- {
		if (*ip)[i] == 0xff {
			(*ip)[i] = 0
			carry = true
		}

		(*ip)[i]++
		carry = false
	}
}

func (p *SimplePoolEx) ReleaseItem(itemID string) (string, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	pe, ok := p.ItemsUsed[itemID]
	if !ok {
		return "", fmt.Errorf("Requested itemID [%s] was not in use in the pool", itemID)
	}

	delete(p.ItemsUsed, itemID)
	return pe.Value, nil
}
