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
	quit       <-chan struct{}
	popCh      chan chan *net.IPNet
	addCh      chan *net.IPNet
	delCh      chan *net.IPNet
}

// Does nothing if net already exists
func (cn *candidateNets) addNet(n *net.IPNet, ns *NeighSubscription) *candidateList {
	cn.lock.Lock()
	defer cn.lock.Unlock()
	if cl, ok := cn.nets[n.String()]; ok {
		return cl
	}
	cl := &candidateList{
		quit:  cn.quit,
		popCh: make(chan chan *net.IPNet),
		addCh: make(chan *net.IPNet),
		delCh: make(chan *net.IPNet),
	}
	go cl.fill(n, ns)
	cn.nets[n.String()] = cl
	return cl
}

func (cl *candidateList) pop(ns *NeighSubscription) *net.IPNet {
	pc := make(chan *net.IPNet)
	defer close(pc)
	cl.popCh <- pc
	return <-pc
}

func (cl *candidateList) fill(n *net.IPNet, ns *NeighSubscription) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	sub := ns.addSub()
	uch := sub.sub
	defer sub.delSub()

mainLoop:
	for {
		select {
		case pc := <-cl.popCh: // pop a suggested address
			for i, p := range cl.candidates {
				if p != nil {
					log.WithField("ip", p).Debug("Popping address from suggestions")
					cl.candidates[i] = nil
					pc <- p
				}
			}
			go func() {
				r, err := getNewRandomUnusedAddr(n, ns)
				if err != nil {
					log.WithError(err).Error("Error getting new random candidate address for pop")
					pc <- nil
				}
				pc <- r
			}()
			continue mainLoop
		case ip := <-cl.addCh:
			for i, p := range cl.candidates {
				if p == nil {
					cl.candidates[i] = ip
				}
			}
			continue mainLoop
		case ip := <-cl.delCh:
			for i, p := range cl.candidates {
				if p.IP.Equal(ip.IP) {
					cl.candidates[i] = nil
					go func() {
						addr, err := getNewRandomUnusedAddr(n, ns)
						if err != nil {
							log.WithError(err).Error("Error getting new random address.")
							return
						}
						cl.addCh <- addr
					}()
					continue mainLoop
				}
			}
			continue mainLoop
		case <-cl.quit:
			return
		case n := <-uch: // We got an update from the arp table
			for _, p := range cl.candidates { // Only do something if it is an ip we care about
				if p != nil && p.IP.Equal(n.IP) {
					continue mainLoop
				}
			}
			break
		case <-t.C: // need to refresh because the timer ticked
			break
		}

		for _, p := range cl.candidates {
			if p == nil {
				continue
			}
			go func() {
				r, err := ns.probeAndWait(p.IP)
				if err != nil {
					log.WithError(err).WithField("ip", p).Error("Error probing candidate IP")
					cl.delCh <- p
					return
				}
				if r {
					log.WithField("ip", p).Debug("Candidate IP in use")
					cl.delCh <- p
				}
			}()
		}
	}
}

func (d *Driver) getRandomUnusedAddr(n *net.IPNet) (*net.IPNet, error) {
	cl := d.candidates.addNet(n, d.ns)
	r := cl.pop(d.ns)
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
			log.WithField("IP", ip).Debug("Returning Random Address")
			return &net.IPNet{IP: ip, Mask: n.Mask}, nil
		}
		log.WithField("ip", ip).Debug("Random address reachable, retrying")
	}
	return nil, fmt.Errorf("All avaliable addresses are in use")
}
