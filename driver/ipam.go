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

		check, err := tryAddress(&addr)
		if err != nil {
			log.Errorf("err: ", err)
			return nil, err
		}

		if check {
			log.Errorf("Address already in use: %v", addr)
			return nil, fmt.Errorf("Address already in use: %v", addr)
		}
		ret_addr := net.IPNet{IP: addr, Mask: n.Mask}
		res.Address = ret_addr.String()
		return res, nil
	}

	log.Debugf("Random Address Requested in network %v", n)
	triedAddresses := make(map[string]struct{})
	var e struct{}
	lastAddress := lastAddr(n)
	log.Debugf("Excluding last address: %v", lastAddress)
	triedAddresses[string(lastAddress)] = e
	firstAddress := firstAddr(n)
	log.Debugf("Excluding network address: %v", firstAddress)
	triedAddresses[string(firstAddress)] = e
	ones, maskSize := n.Mask.Size()
	var totalAddresses int
	totalAddresses = 1 << uint8(maskSize-ones)
	log.Debugf("Address avaliable to try: %v", totalAddresses)
	for len(triedAddresses) < totalAddresses {
		try, err := randAddr(n)
		log.Debugf("Trying random address: %v", try)
		if err != nil {
			log.Errorf("Error generating random address: %v", err)
			return nil, err
		}
		if _, ok := triedAddresses[string(try)]; ok {
			log.Debugf("Address already tried: %v", try)
			continue
		}

		check, err := tryAddress(&try)
		if err != nil {
			log.Errorf("err: ", err)
			return nil, err
		}

		if !check {
			log.Debugf("Returning address: %v", try)
			ret_addr := net.IPNet{IP: try, Mask: n.Mask}
			res.Address = ret_addr.String()
			return res, nil
		}
		triedAddresses[string(try)] = e
		log.Debugf("Address in use: %v", try)
	}

	log.Errorf("All avaliable addresses are in use")
	return nil, fmt.Errorf("All avaliable addresses are in use")
}

func tryAddress(ip *net.IP) (bool, error) {
	err := probe(ip)
	if err != nil {
		return true, err
	}
	return checkNeigh(ip)
}

func probe(ip *net.IP) error {
	conn, err := net.Dial("udp", ip.String()+":8765")
	defer conn.Close()
	if err != nil {
		return err
	}
	conn.Write([]byte("probe"))
	return nil
}

// Check neighbor table for IP. Return true if the address is in the neighbor cache
func checkNeigh(ip *net.IP) (bool, error) {
	log.Debugf("Checking local addresses")
	addrs, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		log.Errorf("Error getting local addresses: %v", err)
		return true, err
	}
	for _, addr := range addrs {
		if ip.Equal(addr.IP) {
			log.Debugf("This address is local: %v", ip)
			return true, nil
		}
	}
	log.Debugf("Checking neighbor table for %v", ip)
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
			if ip.Equal(neigh.IP) {
				if neigh.HardwareAddr != nil {
					return true, nil
					log.Debugf("Hardware address found, ip in use")
				}
				if neigh.State == netlink.NUD_INCOMPLETE {
					// Break and try again, the kernel is still trying to resolve
					continue NeighLoop
				}
				log.Debugf("Entry exists, but no hardware address and state is not incomplete. Assuming address is not in use")
				return false, nil
			}
		}
		log.Debugf("IP not found in neighbor table")
		return false, nil
	}
}

func (d *Driver) ReleaseAddress(r *ipam.ReleaseAddressRequest) error {
	log.Debugf("ReleaseAddress: %v", r)
	return nil
}
