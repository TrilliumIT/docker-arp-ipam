package driver

import (
	"fmt"
	"net"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/vishvananda/netlink"
)

const candidateSize = 3

// Driver is the main driver object for the plugin
type Driver struct {
	ipam.Ipam
	ns         *neighSubscription
	candidates *candidateNets
	quit       <-chan struct{}
}

// NewDriver returns a driver object
func NewDriver(quit <-chan struct{}) (*Driver, error) {
	log.Debugf("NewDriver")
	ns, err := newNeighSubscription(quit)
	if err != nil {
		log.WithError(err).Error("Error setting up neighbor subscription")
		return nil, err
	}
	d := &Driver{
		ns:   ns,
		quit: quit,
		candidates: &candidateNets{
			nets: make(map[string]*candidateList),
			quit: quit,
		},
	}
	return d, nil
}

// GetCapabilities is what docker calls when initially connecting
func (d *Driver) GetCapabilities() (*ipam.CapabilitiesResponse, error) {
	log.Debugf("GetCapabilities")
	return &ipam.CapabilitiesResponse{
		RequiresMACAddress: false,
	}, nil
}

// GetDefaultAddressSpaces returns the default address spaces
func (d *Driver) GetDefaultAddressSpaces() (*ipam.AddressSpacesResponse, error) {
	log.Debugf("GetDefaultAddressSpaces")
	return &ipam.AddressSpacesResponse{
		LocalDefaultAddressSpace:  "arp-ipam-default",
		GlobalDefaultAddressSpace: "arp-ipam-default",
	}, nil
}

// RequestPool requests a pool from the driver
func (d *Driver) RequestPool(r *ipam.RequestPoolRequest) (*ipam.RequestPoolResponse, error) {
	log.Debugf("RequestPool: %v", r)
	if r.Pool == "" {
		log.Errorf("Automatic pool assignment not supported")
		return nil, fmt.Errorf("Automatic pool assignment not supported")
	}
	if r.V6 {
		log.Errorf("Automatic V6 pool assignment not supported.")
		return nil, fmt.Errorf("automatic V6 pool assignment not supported")
	}
	if r.SubPool != "" {
		log.Errorf("SubPool not supported.")
		return nil, fmt.Errorf("subPool not supported")
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

// ReleasePool releases a pool
func (d *Driver) ReleasePool(r *ipam.ReleasePoolRequest) error {
	log.Debugf("ReleasePool: %v", r)
	return nil
}

// RequestAddress requests an address
func (d *Driver) RequestAddress(r *ipam.RequestAddressRequest) (*ipam.RequestAddressResponse, error) {
	//todo add a timeout
	log.Debugf("RequestAddress: %v", r)

	n, err := netlink.ParseIPNet(r.PoolID)
	if err != nil {
		log.Errorf("Unable to parse PoolID: %v", r.PoolID)
		log.Errorf("err: %v", err)
		return nil, err
	}

	if err = verifyLocalNet(n); err != nil {
		return nil, err
	}

	res := &ipam.RequestAddressResponse{}

	if r.Address != "" {
		log.Debugf("Specific Address Requested: %v", r.Address)

		addr := &net.IPNet{IP: net.ParseIP(r.Address), Mask: n.Mask}
		if addr == nil {
			log.Errorf("Unable to parse address: %v", r.Address)
			return nil, fmt.Errorf("Unable to parse address: %v", r.Address)
		}

		if r.Options["RequestAddressType"] == "com.docker.network.gateway" {
			log.Debugf("Gateway requested, approving")
			res.Address = addr.String()
			return res, nil
		}

		err = d.requestAddress(addr)
		if err != nil {
			log.WithError(err).Error("Error getting specific address")
			return nil, err
		}

		res.Address = addr.String()
		return res, nil
	}

	log.Debugf("Random Address Requested in network %v", n)
	retAddr, err := d.getRandomUnusedAddr(n)
	if err != nil {
		log.WithError(err).Error("Error getting random address")
		return nil, err
	}
	res.Address = retAddr.String()
	log.WithField("Address", res.Address).Debug("Responding with address")
	return res, nil
}

// ReleaseAddress releases an assigned address
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
				log.WithError(err).WithField("ip", ip).Error("Failed to delete arp entry.")
			}
			return err
		}
	}
	return nil
}
