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
		return rmd.Errorf("Automatic pool assignment not supported"), nil
	}
	if r.V6 {
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
}

func (d *Driver) ReleaseAddress(r *ipam.ReleaseAddressRequest) error {
	return nil
}
