package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/iputil"
	"net"
)

func (d *Driver) getRandomUnusedAddr(n *net.IPNet) (*net.IPNet, error) {
	log.Debugf("Generating Random Address in network %v", n)
	tried := make(map[string]struct{})
	var e struct{}
	ones, maskSize := n.Mask.Size()
	totalAddresses := 1 << uint8(maskSize-ones)
	if totalAddresses > 2 { // This network is not a /30 exclude first and last
		tried[string(iputil.FirstAddr(n))] = e
		tried[string(iputil.LastAddr(n))] = e
	}
	for len(tried) < totalAddresses {
		ip, err := iputil.RandAddr(n)
		if err != nil {
			log.WithError(err).Error("Error generating random address")
			return nil, err
		}
		if _, ok := tried[string(ip)]; ok { // this address was already tried
			continue
		}
		r, err := d.ns.probeAndWait(ip)
		if err != nil {
			log.WithError(err).Error("Error probing random address")
			continue
		}
		if !r {
			return &net.IPNet{IP: ip, Mask: n.Mask}, nil
		}
		log.WithField("ip", ip).Debug("Random address reachable, retrying")
	}
	return nil, fmt.Errorf("All avaliable addresses are in use")
}
