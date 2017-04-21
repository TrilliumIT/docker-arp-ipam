package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/iputil"
	"github.com/vishvananda/netlink"
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
	candidates [candidateSize]*subscription
	quit       <-chan struct{}
	popCh      chan chan *net.IPNet
	addCh      chan *net.IPNet
	delCh      chan *net.IPNet
}

// Does nothing if net already exists
func (cn *candidateNets) addNet(n *net.IPNet, ns *neighSubscription) *candidateList {
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

func (cl *candidateList) pop(ns *neighSubscription) *net.IPNet {
	pc := make(chan *net.IPNet)
	defer close(pc)
	cl.popCh <- pc
	return <-pc
}

func (cl *candidateList) fill(n *net.IPNet, ns *neighSubscription) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	uch := make(chan *netlink.Neigh)
	defer close(uch)

mainLoop:
	for {
		select {
		case pc := <-cl.popCh: // pop a suggested address
			for i, s := range cl.candidates {
				if s != nil {
					log.WithField("ip", s.ip).Debug("Popping address from suggestions")
					cl.candidates[i] = nil
					pc <- s.ip
					s.delSub()
					continue mainLoop
				}
			}
			go sendRandomUnusedAddress(n, ns, pc)
			continue mainLoop
		case ip := <-cl.addCh:
			for i, p := range cl.candidates {
				if p == nil {
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
				if s.ip.IP.Equal(ip.IP) {
					cl.candidates[i] = nil
					s.delSub()
					go sendRandomUnusedAddress(n, ns, cl.addCh)
					continue mainLoop
				}
			}
			continue mainLoop
		case <-cl.quit:
			return
		case <-uch: // We got an update from the arp table
			break
		case <-t.C: // need to refresh because the timer ticked
			break
		}

		for _, p := range cl.candidates {
			if p == nil {
				go sendRandomUnusedAddress(n, ns, cl.addCh)
			}
			go func() {
				r, err := ns.probeAndWait(p.ip)
				if err != nil {
					log.WithError(err).WithField("ip", p).Error("Error probing candidate IP")
					cl.delCh <- p.ip
					return
				}
				if r {
					log.WithField("ip", p).Debug("Candidate IP in use")
					cl.delCh <- p.ip
				}
			}()
		}
	}
}

func sendRandomUnusedAddress(n *net.IPNet, ns *neighSubscription, c chan<- *net.IPNet) {
	addr, err := getNewRandomUnusedAddr(n, ns)
	if err != nil {
		log.WithError(err).Error("Error getting new random address.")
		return
	}
	c <- addr
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

func getNewRandomUnusedAddr(n *net.IPNet, ns *neighSubscription) (*net.IPNet, error) {
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
		addr := &net.IPNet{
			IP:   ip,
			Mask: n.Mask,
		}
		r, err := ns.probeAndWait(addr)
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
