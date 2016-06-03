package driver

import (
	//gonet "net"
	//"strconv"
	//"errors"
	//"strings"
	"net"
	"fmt"
	"crypto/rand"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/vishvananda/netlink"
	"github.com/llimllib/ipaddress"

	//"golang.org/x/net/context"
	//dockerclient "github.com/docker/engine-api/client"
	//dockertypes "github.com/docker/engine-api/types"
)

type Driver struct {
	ipam.Ipam
}

func NewDriver() (*Driver, error) {
	log.Debugf("NewDriver")
	d := &Driver{}
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
		LocalDefaultAddressSpace: "arp-ipam-default",
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
	return &ipam.RequestPoolResponse{
		PoolID: r.Pool,
		Pool: r.Pool,
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

	//FIXME checkneigh

	if r.Address != "" {
		addr := net.ParseIP(r.Address)
		if addr == nil {
			log.Errorf("Unable to parse address: %v", r.Address)
			return nil, fmt.Errorf("Unable to parse address: %v", r.Address)
		}
		
		check, err := tryAddress(&addr)
		if err != nil {
			log.Errorf("err: ", err)
			return nil, err
		}

		if check {
			log.Errorf("Address already in use: %v", addr)
			return nil, fmt.Errorf("Address already in use: %v", addr)
		}
		ret_addr := net.IPNet{ IP: addr, Mask: n.Mask }
		res.Address = ret_addr.String()
		return res, nil
	}

	triedAddresses := make(map[string]struct{})
	var e struct{}
	triedAddresses[string(ipaddress.LastAddress(n))] = e
	triedAddresses[string(n.IP)] = e
	ones, maskSize := n.Mask.Size()
	var totalAddresses int
	totalAddresses = 1<<uint8(maskSize - ones)
	for len(triedAddresses) < totalAddresses {
		try, err := randAddr(n)
		if err != nil {
			log.Errorf("Error generating random address: %v", err)
			return nil, err
		}
		if _, ok := triedAddresses[string(try)]; ok { continue }

		check, err := tryAddress(&try)
		if err != nil {
			log.Errorf("err: ", err)
			return nil, err
		}

		if !check {
			ret_addr := net.IPNet{ IP: try, Mask: n.Mask }
			res.Address = ret_addr.String()
			return res, nil
		}
		triedAddresses[string(try)] = e
	}

	log.Errorf("All avaliable addresses are in use")
	return nil, fmt.Errorf("All avaliable addresses are in use")
}

func randAddr(n *net.IPNet) (net.IP, error) {
	// ip & (mask | random) should generate a random ip
	ip := n.IP.To4()
	if ip != nil {
		rand_bytes := make([]byte, 4)
		_, err := rand.Read(rand_bytes)
		if err != nil {
			return nil, err
		}
		return net.IP{
			ip[0] & (n.Mask[0] | rand_bytes[0]),
			ip[1] & (n.Mask[1] | rand_bytes[1]),
			ip[2] & (n.Mask[2] | rand_bytes[2]),
			ip[3] & (n.Mask[3] | rand_bytes[3]),
		}, nil
	}

	rand_bytes := make([]byte, 16)
	_, err := rand.Read(rand_bytes)
	if err != nil {
		return nil, err
	}
	return net.IP{
		ip[0] & (n.Mask[0] | rand_bytes[0]),
		ip[1] & (n.Mask[1] | rand_bytes[1]),
		ip[2] & (n.Mask[2] | rand_bytes[2]),
		ip[3] & (n.Mask[3] | rand_bytes[3]),
		ip[4] & (n.Mask[4] | rand_bytes[4]),
		ip[5] & (n.Mask[5] | rand_bytes[5]),
		ip[6] & (n.Mask[6] | rand_bytes[6]),
		ip[7] & (n.Mask[7] | rand_bytes[7]),
		ip[8] & (n.Mask[8] | rand_bytes[8]),
		ip[9] & (n.Mask[9] | rand_bytes[9]),
		ip[10] & (n.Mask[10] | rand_bytes[10]),
		ip[11] & (n.Mask[11] | rand_bytes[11]),
		ip[12] & (n.Mask[12] | rand_bytes[12]),
		ip[13] & (n.Mask[13] | rand_bytes[13]),
		ip[14] & (n.Mask[14] | rand_bytes[14]),
		ip[15] & (n.Mask[15] | rand_bytes[15]),
	}, nil
}

func tryAddress(ip *net.IP) (bool, error) {
	firstcheck, err := checkNeigh(ip)
	if err != nil {
		return true, err
	}
	if firstcheck {
		return firstcheck, err
	}
	err = probe(ip)
	if err != nil {
		return true, err
	}
	return checkNeigh(ip)
}

func probe(ip *net.IP) (error) {
	conn, err := net.Dial("udp", ip.String() + ":8765")
	defer conn.Close()
	if err != nil {
		return err
	}
	conn.Write([]byte("probe"))
	return nil
}

// Check neighbor table for IP. Return true if the address is in the neighbor cache
func checkNeigh(ip *net.IP) (bool, error) {
	NeighLoop:
		for {
			var neighs []netlink.Neigh
			var err error
			if ip.To4() != nil {
				neighs, err = netlink.NeighList(0, netlink.FAMILY_V4)
			} else {
				neighs, err = netlink.NeighList(0, netlink.FAMILY_V6)
			}
			if err != nil {
				log.Errorf("Error getting ip neighbors")
				return true, err
			}

			for _, neigh := range neighs {
				if &neigh.IP == ip {
					if neigh.HardwareAddr != nil {
						return true, nil
					}
					if neigh.State == netlink.NUD_INCOMPLETE {
						// Break and try again, the kernel is still trying to resolve
						continue NeighLoop
					}
					return false, nil
				}
			}
			return false, nil
		}
}

func (d *Driver) ReleaseAddress(r *ipam.ReleaseAddressRequest) error {
	log.Debugf("ReleaseAddress: %v", r)
	return nil
}
