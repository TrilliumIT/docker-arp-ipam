package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/vishvananda/netlink"
	"net"
	"sync"
)

type Driver struct {
	ipam.Ipam
	ns   *NeighSubscription
	quit <-chan struct{}
}

func NewDriver(quit <-chan struct{}, wg sync.WaitGroup) (*Driver, error) {
	log.Debugf("NewDriver")
	ns, err := NewNeighSubscription(quit)
	if err != nil {
		log.WithError(err).Error("Error setting up neighbor subscription")
		return nil, err
	}
	d := &Driver{
		ns:   ns,
		quit: quit,
	}
	return d, nil
}

func (d *Driver) GetCapabilities() (*ipam.CapabilitiesResponse, error) {
	log.Debugf("GetCapabilities")
	return &ipam.CapabilitiesResponse{
		RequiresMACAddress: false,
	}, nil
}

func (d *Driver) GetDefaultAddressSpaces() (*ipam.AddressSpacesResponse, error) {
	log.Debugf("GetDefaultAddressSpaces")
	return &ipam.AddressSpacesResponse{
		LocalDefaultAddressSpace:  "arp-ipam-default",
		GlobalDefaultAddressSpace: "arp-ipam-default",
	}, nil
}

func (d *Driver) RequestPool(r *ipam.RequestPoolRequest) (*ipam.RequestPoolResponse, error) {
	log.Debugf("RequestPool: %v", r)
	if r.Pool == "" {
		log.Errorf("Automatic pool assignment not supported")
		return nil, fmt.Errorf("Automatic pool assignment not supported")
	}
	if r.V6 {
		log.Errorf("Automatic V6 pool assignment not supported.")
		return nil, fmt.Errorf("Automatic V6 pool assignment not supported.")
	}
	if r.SubPool != "" {
		log.Errorf("SubPool not supported.")
		return nil, fmt.Errorf("SubPool not supported.")
	}
	n, err := netlink.ParseIPNet(r.Pool)
	if err != nil {
		log.Errorf("Error parsing pool: %v", err)
		return nil, err
	}

	if err := verifyLocalNet(n); err != nil {
		return nil, err
	}

	return &ipam.RequestPoolResponse{
		PoolID: r.Pool,
		Pool:   r.Pool,
	}, nil
}

func verifyLocalNet(n *net.IPNet) error {
	addrs, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		log.Errorf("Error getting local addresses: %v", err)
		return err
	}
	innet := false
	for _, addr := range addrs {
		if n.Contains(addr.IP) {
			innet = true
			break
		}
	}
	if !innet {
		log.Errorf("Pool is not a local network: %v", n)
		return fmt.Errorf("Pool is not a local network")
	}
	return nil
}

func (d *Driver) ReleasePool(r *ipam.ReleasePoolRequest) error {
	log.Debugf("ReleasePool: %v", r)
	return nil
}

func (d *Driver) RequestAddress(r *ipam.RequestAddressRequest) (*ipam.RequestAddressResponse, error) {
	log.Debugf("RequestAddress: %v", r)

	n, err := netlink.ParseIPNet(r.PoolID)
	if err != nil {
		log.Errorf("Unable to parse PoolID: %v", r.PoolID)
		log.Errorf("err: %v", err)
		return nil, err
	}

	if err := verifyLocalNet(n); err != nil {
		return nil, err
	}

	res := &ipam.RequestAddressResponse{}

	if r.Address != "" {
		log.Debugf("Specific Address Requested: %v", r.Address)

		addr := net.ParseIP(r.Address)
		if addr == nil {
			log.Errorf("Unable to parse address: %v", r.Address)
			return nil, fmt.Errorf("Unable to parse address: %v", r.Address)
		}

		if r.Options["RequestAddressType"] == "com.docker.network.gateway" {
			log.Debugf("Gateway requested, approving")
			ret_addr := net.IPNet{IP: addr, Mask: n.Mask}
			res.Address = ret_addr.String()
			return res, nil
		}

		err := d.requestAddress(addr)
		if err != nil {
			log.WithError(err).Error("Error getting specific address")
			return nil, err
		}

		ret_addr := net.IPNet{IP: addr, Mask: n.Mask}
		res.Address = ret_addr.String()
		return res, nil
	}

	log.Debugf("Random Address Requested in network %v", n)
	ret_addr, err := d.getRandomUnusedAddr(n)
	if err != nil {
		log.WithError(err).Error("Error getting random address")
		return nil, err
	}
	res.Address = ret_addr.String()
	return res, nil
}

func (d *Driver) ReleaseAddress(r *ipam.ReleaseAddressRequest) error {
	log.Debugf("ReleaseAddress: %v", r)
	ip := net.ParseIP(r.Address)
	log.Debugf("Deleting entry from arp table for %v", ip)
	neighs, err := netlink.NeighList(0, netlink.FAMILY_ALL)
	if err != nil {
		log.WithError(err).Error("Failed to get arp table")
		return err
	}
	for _, n := range neighs {
		if ip.Equal(n.IP) {
			err := netlink.NeighDel(&n)
			if err != nil {
				log.WithError(err).Error("Failed to delete arp entry for %v", ip)
			}
			return err
		}
	}
	return nil
}
