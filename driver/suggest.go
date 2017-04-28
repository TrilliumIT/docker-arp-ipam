package driver

import (
	"fmt"
	"net"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/iputil"
	"github.com/vishvananda/netlink"
)

type candidateNets struct {
	nets map[string]*candidateList // map of network to slice of IPs
	lock sync.Mutex
	quit <-chan struct{}
}

type candidateList struct {
	candidates [candidateSize]*subscription
	quit       <-chan struct{}
	popCh      chan chan *net.IPNet
	addCh      chan *net.IPNet
	delCh      chan *net.IPNet
}

// Does nothing if net already exists
func (cn *candidateNets) addNet(n *net.IPNet, ns *neighSubscription, xf, xl int) *candidateList {
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
	go cl.fill(n, ns, xf, xl)
	cn.nets[n.String()] = cl
	return cl
}

func (cl *candidateList) pop(ns *neighSubscription) *net.IPNet {
	pc := make(chan *net.IPNet)
	defer close(pc)
	cl.popCh <- pc
	return <-pc
}

func (cl *candidateList) fill(n *net.IPNet, ns *neighSubscription, xf, xl int) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	uch := make(chan *netlink.Neigh)
	defer close(uch)

	for _, s := range cl.candidates {
		if s == nil {
			go sendRandomUnusedAddress(n, ns, xf, xl, cl.addCh)
		}
	}

mainLoop:
	for {
		select {
		case pc := <-cl.popCh: // pop a suggested address
			for i, s := range cl.candidates {
				if s == nil {
					go sendRandomUnusedAddress(n, ns, xf, xl, cl.addCh)
					continue
				}
				log.WithField("ip", s.ip).Debug("Popping address from suggestions")
				cl.candidates[i] = nil
				pc <- s.ip
				s.delSub()
				go sendRandomUnusedAddress(n, ns, xf, xl, cl.addCh)
				continue mainLoop
			}
			go sendRandomUnusedAddress(n, ns, xf, xl, pc)
			continue mainLoop
		case ip := <-cl.addCh:
			for i, s := range cl.candidates {
				if s == nil {
					s := ns.addSub(ip)
					cl.candidates[i] = s
					go func() {
						for u := range s.sub {
							uch <- u
						}
					}()
					continue mainLoop
				}
			}
			continue mainLoop
		case ip := <-cl.delCh:
			for i, s := range cl.candidates {
				if s == nil {
					go sendRandomUnusedAddress(n, ns, xf, xl, cl.addCh)
					continue
				}
				if s.ip.IP.Equal(ip.IP) {
					cl.candidates[i] = nil
					s.delSub()
					go sendRandomUnusedAddress(n, ns, xf, xl, cl.addCh)
					continue mainLoop
				}
			}
			continue mainLoop
		case <-cl.quit:
			return
		case <-uch: // We got an update from the arp table
		case <-t.C: // need to refresh because the timer ticked
		}

		for _, s := range cl.candidates {
			if s == nil {
				go sendRandomUnusedAddress(n, ns, xf, xl, cl.addCh)
				continue
			}
			go func(s *subscription) {
				r, err := ns.probeAndWait(s.ip, 15*time.Second)
				if err != nil {
					if _, ok := err.(*probeTimeoutError); ok {
						log.WithError(err).Debug("Timed out probing candidate ip. Trying another")
					} else {
						log.WithError(err).WithField("ip", s.ip.String()).Error("Error probing candidate IP")
					}
					cl.delCh <- s.ip
					return
				}
				if r {
					log.WithField("ip", s).Debug("Candidate IP in use")
					cl.delCh <- s.ip
				}
			}(s)
		}
	}
}

func sendRandomUnusedAddress(n *net.IPNet, ns *neighSubscription, xf, xl int, c chan<- *net.IPNet) {
	addr, err := getNewRandomUnusedAddr(n, 15*time.Second, ns, xf, xl)
	if err != nil {
		log.WithError(err).Error("Error getting new random address.")
		return
	}
	c <- addr
}

func (d *Driver) getRandomUnusedAddr(n *net.IPNet, to time.Duration) (*net.IPNet, error) {
	cl := d.candidates.addNet(n, d.ns, d.xf, d.xl)
	r := cl.pop(d.ns)
	if r != nil {
		return r, nil
	}
	r, err := getNewRandomUnusedAddr(n, to, d.ns, d.xf, d.xl)
	if err != nil {
		log.WithError(err).Error("Error getting new random address")
		return nil, err
	}
	return r, nil
}

func getNewRandomUnusedAddr(n *net.IPNet, to time.Duration, ns *neighSubscription, xf, xl int) (*net.IPNet, error) {
	log.Debugf("Generating Random Address in network %v", n)
	tried := make(map[string]struct{})
	var e struct{}
	ones, maskSize := n.Mask.Size()
	totalAddresses := 1 << uint8(maskSize-ones)
	if totalAddresses > 2 { // This network is not a /30 exclude first and last
		tried[iputil.FirstAddr(n).String()] = e
		tried[iputil.LastAddr(n).String()] = e
	}
	// Add excluded addresses to tried array
	for i := 1; i <= xf; i++ {
		tried[iputil.IPAdd(iputil.FirstAddr(n), i).String()] = e
	}
	for i := 1; i <= xl; i++ {
		tried[iputil.IPAdd(iputil.LastAddr(n), i*-1).String()] = e
	}
	for len(tried) < totalAddresses {
		ip, err := iputil.RandAddr(n)
		if err != nil {
			log.WithError(err).Error("Error generating random address")
			return nil, err
		}
		if _, ok := tried[ip.String()]; ok { // this address was already tried
			continue
		}
		addr := &net.IPNet{
			IP:   ip,
			Mask: n.Mask,
		}
		r, err := ns.probeAndWait(addr, to)
		if err != nil {
			log.WithError(err).Error("Error probing random address")
			continue
		}
		if !r {
			log.WithField("IP", ip).Debug("Returning Random Address")
			return &net.IPNet{IP: ip, Mask: n.Mask}, nil
		}
		log.WithField("ip", ip).Debug("Random address reachable, retrying")
		tried[ip.String()] = e
	}
	return nil, fmt.Errorf("All avaliable addresses are in use")
}
