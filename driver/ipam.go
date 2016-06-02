package driver

import (
	//gonet "net"
	//"strconv"
	//"errors"
	//"strings"
	"fmt"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/vishvananda/netlink"

	//"golang.org/x/net/context"
	//dockerclient "github.com/docker/engine-api/client"
	//dockertypes "github.com/docker/engine-api/types"
)

type Driver struct {
	ipam.Ipam
}

func NewDriver() (*Driver, error) {
	d := &Driver{}
	return d, nil
}

func (d *Driver) GetCapabilities() (*ipam.CapabilitiesResponse, error) {
	return &ipam.CapabilitiesResponse{
		RequiresMacAddress: false,
	}, nil
}

func (d *Driver) GetDefaultAddressSpaces() (*ipam.AddressSpacesResponse, error) {
	return &ipam.AddressSpacesResponse{
		LocalDefaultAddressSpace: "arp-ipam-default",
		GlobalDefaultAddressSpace: "arp-ipam-default",
	}, nil
}

func (d *Driver) RequestPool(r *ipam.RequestPoolRequest) (*ipam.RequestPoolResponse, error) {
	if r.Pool == "" {
		log.Errorf("Automatic pool assignment not supported")
		return rmd.Errorf("Automatic pool assignment not supported"), nil
	}
	if r.V6 {
		log.Errorf("Automatic V6 pool assignment not supported.")
		return rmd.Errorf("Automatic V6 pool assignment not supported."), nil
	}
	return &ipam.RequestPoolResponse{
		PoolID: r.Pool,
		Pool: r.Pool,
	}, nil
}

func (d *Driver) ReleasePool(r *ipam.ReleasePoolRequest) error {
	return nil
}

func (d *Driver) RequestAddress(r *ipam.RequestAddressRequest) (*ipam.RequestAddressResponse, error) {

	net, err := netlink.ParseIPNet(r.PoolID)
	if err != nil {
		log.Errorf("Unable to parse PoolID: %v", r.PoolID)
		log.Errorf("err: %v", err)
		return nil, err
	}

	//FIXME checkneigh

	if r.Address != "" {
		addr := net.ParseIP(r.Address)
		if addr == nil {
			log.Errorf("Unable to parse address: %v", r.Address)
			return nil, rmd.Errorf("Unable to parse address: %v", r.Address)
		}
		
		check, err := checkNeigh(addr)
		if err != nil {
			log.Errorf("Error checking neighbors for: %v", addr)
			log.Errorf("err: ", err)
			return nil, err
		}

		if check {
			log.Errorf("Address already in use: %v", addr)
			return nil, fmt.Errorf("Address already in use: %v", addr)
		}
	}
}

func checkNeigh(ip *net.IP) (bool, error) {
	var neighs []netlink.Neigh
	var err error
	if net.To4(ip) != nil {
		neighs, err = netlink.NeighList(0, netlink.FAMILY_V4)
	} else {
		neighs, err = netlink.NeighList(0, netlink.FAMILY_V6)
	}
	if err != nil {
		log.Errorf("Error getting ip neighbors")
		return nil, err
	}

	for _, neigh := range neighs {
		if neigh.IP == ip {
			return true, nil
		}
	}

	return false, nil
}

func (d *Driver) ReleaseAddress(r *ipam.ReleaseAddressRequest) error {
	return nil
}
