package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/vishvananda/netlink"
	"net"
)

type Driver struct {
	ipam.Ipam
	ncCh  chan<- *neighCheck       // Channel to request a neighbor check
	ncUch chan<- *neighUseNotifier // Channel to close if neighbor becomes in use
	sgCh  chan<- *addrSuggest      // Channel to request a suggested IP
	quit  <-chan struct{}
}

func NewDriver(quit <-chan struct{}) (*Driver, error) {
	log.Debugf("NewDriver")

	ncCh := make(chan *neighCheck)
	ncUch := make(chan *neighUseNotifier)
	sgCh := make(chan *addrSuggest)

	go func() {
		defer close(sgCh)
		defer close(ncUch)
		defer close(ncCh)
		_ = <-quit
	}()

	d := &Driver{
		ncCh:  ncCh,
		ncUch: ncUch,
		sgCh:  sgCh,
		quit:  quit,
	}

	go checkNeigh(ncCh, ncUch, quit)
	go genSuggestions(sgCh, ncCh, ncUch, quit)
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

	addrs, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		log.Errorf("Error getting local addresses: %v", err)
		return nil, err
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
		return nil, fmt.Errorf("Pool is not a local network")
	}

	return &ipam.RequestPoolResponse{
		PoolID: r.Pool,
		Pool:   r.Pool,
	}, nil
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

		if tryAddress(&addr, d.ncCh) {
			log.Errorf("Address already in use: %v", addr)
			return nil, fmt.Errorf("Address already in use: %v", addr)
		}
		ret_addr := net.IPNet{IP: addr, Mask: n.Mask}
		res.Address = ret_addr.String()
		return res, nil
	}

	log.Debugf("Random Address Requested in network %v", n)

	ch := make(chan *net.IPNet)
	defer close(ch)
	ech := make(chan error)
	defer close(ech)
	d.sgCh <- &addrSuggest{
		ipn: n,
		ch:  ch,
		ech: ech,
	}
	select {
	case _ = <-d.quit:
		return nil, fmt.Errorf("Shutting down")
	case err := <-ech:
		log.Errorf("Error getting random address")
		return nil, err
	case ret_addr := <-ch:
		res.Address = ret_addr.String()
	}
	return res, nil
}

func tryAddress(ip *net.IP, ncCh chan<- *neighCheck) bool {
	ch := make(chan bool)
	defer close(ch)
	nc := &neighCheck{
		ip: ip,
		ch: ch,
	}
	ncCh <- nc
	res := <-ch
	return res
}

func (d *Driver) ReleaseAddress(r *ipam.ReleaseAddressRequest) error {
	log.Debugf("ReleaseAddress: %v", r)
	return nil
}
