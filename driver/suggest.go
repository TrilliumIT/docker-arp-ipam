package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/iputil"
	"net"
	"sync"
	"time"
)

type candidateNets struct {
	nets map[string]*candidateList // map of network to slice of IPs
	lock sync.Mutex
	quit <-chan struct{}
}

type candidateList struct {
	candidates [candidateSize]*net.IPNet
	lock       sync.Mutex
	quit       <-chan struct{}
}

// Does nothing if net already exists
func (cn *candidateNets) addNet(n *net.IPNet, ns *NeighSubscription) *candidateList {
	cn.lock.Lock()
	defer cn.lock.Unlock()
	if cl, ok := cn.nets[n.String()]; ok {
		return cl
	}
	cl := &candidateList{
		quit: cn.quit,
	}
	go cl.fill(n, ns)
	cn.nets[n.String()] = cl
	return cl
}

func (cl *candidateList) pop() *net.IPNet {
	cl.lock.Lock()
	defer cl.lock.Unlock()
	for i, p := range cl.candidates {
		if p != nil {
			cl.candidates[i] = nil
			return p
		}
	}
	return nil
}

func (cl *candidateList) contains(ip net.IP) bool {
	cl.lock.Lock()
	defer cl.lock.Unlock()
	for _, p := range cl.candidates {
		if p.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func (cl *candidateList) fill(n *net.IPNet, ns *NeighSubscription) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	uch := ns.addSub()
	defer ns.delSub(uch)

	for {
		select {
		case n := <-uch:
			if !cl.contains(n.IP) {
				continue
			}
		case <-t.C:
		case <-cl.quit:
			return
		}

		cl.lock.Lock()
		for i, p := range cl.candidates {
			if p == nil {
				continue
			}
			r, err := ns.probeAndWait(p.IP)
			if err != nil {
				log.WithError(err).WithField("ip", p).Error("Error probing candidate IP")
				cl.candidates[i] = nil
				continue
			}
			if r {
				log.WithField("ip", p).Debug("Candidate IP in use")
				cl.candidates[i] = nil
				continue
			}
		}
		for i, p := range cl.candidates {
			if p != nil {
				continue
			}
			var err error
			cl.candidates[i], err = getNewRandomUnusedAddr(n, ns)
			if err != nil {
				log.WithError(err).Error("Error getting new random candidate address")
			}
		}
		cl.lock.Unlock()
	}
}

func (d *Driver) getRandomUnusedAddr(n *net.IPNet) (*net.IPNet, error) {
	cl := d.candidates.addNet(n, d.ns)
	r := cl.pop()
	if r != nil {
		return r, nil
	}
	r, err := getNewRandomUnusedAddr(n, d.ns)
	if err != nil {
		log.WithError(err).Error("Error getting new random address")
		return nil, err
	}
	return r, nil
}

func getNewRandomUnusedAddr(n *net.IPNet, ns *NeighSubscription) (*net.IPNet, error) {
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
		r, err := ns.probeAndWait(ip)
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
