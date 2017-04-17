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

func (d *Driver) mainLoop(quit <-chan struct{}) error {
	candidateIPs := []*net.IPNet{}
	ns, err := NewNeighSubscription(quit)
	if err != nil {
		log.WithError(err).Error("Error setting up neighbor subscription")
		return err
	}
	for {
		// Check candidateIPs, and replace if necessary
		select {
		case <-quit:
			return
		case req <- d.reqCh:
			k, r, err := ns.probeAndWait(req.ip)
			if err != nil {
				log.WithError(err).Fatal("Error determining if addr is known")
				req.err <- err
				continue
			}
			if r {
				req.err <- fmt.Errorf("Address already in use: %v", req.ip)
			}
			req.err <- nil
			// Case time.ticker to cause periodic refilling of candidate addresses.
		}
	}
}

type addrReq struct {
	ip  *net.IP
	err chan error
}

func addrStatus(addr *net.IP) (known, reachable bool, err error) {
	var neighs []*netlink.Neigh
	neighs, err = netlink.NeighList(0, netlink.FAMILY_V4)
	if err != nil {
		return
	}
	for _, n := range neighs {
		if n.IP.Equal(addr) {
			known = n.state == netlink.NUD_FAILED || n.state == netlink.NUD_REACHABLE
			reachable = known && n.state != netlink.NUD_FAILED
			return
		}
	}
	return
}

func (d *Driver) requestAddress(addr net.IP) error {
	req := &addrReq{
		ip:  addr,
		err: make(chan error),
	}
	d.reqCh <- req
	return <-req.err
}

type NeighSubscription struct {
	addSub        chan chan *netlink.Neigh
	subscriptions []chan *netlink.Neigh
	subLock       sync.Mutex
}

func (ns *NeighSubscription) probeAndWait(addr *net.IP) (known, reachable bool, err error) {
	known, reachable, err = addrStatus(addr)
	if err != nil || k {
		return
	}

	uch := make(chan *netlink.Neigh)
	defer close(uch)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	ns.addSub(uch)
	defer ns.delSub(uch)

	probe(addr)
	for {
		select {
		case n := <-uch:
			if !n.IP.Equal(addr) {
				continue
			}
			known, reachable, err = addrStatus(addr)
			if err != nil || k {
				return
			}
		case <-t:
			known, reachable, err = addrStatus(addr)
			if err != nil || k {
				return
			}
			probe(addr)
		}
	}
}

func (ns *NeighSubscription) addSub(sub chan *netlink.Neigh) {
	subLock.Lock()
	defer sublock.Unlock()
	ns.subscriptions = append(ns.subscriptions, sub)
}

func (ns *NeighSubscription) delSub(sub chan *netlink.Neigh) {
	select {
	case <-sub: // read pending messages to avoid deadlock
	case subLock.Lock():
		defer sublock.Unlock()
		for i, s := range ns.subscriptions {
			if sub == s {
				ns.subscriptions = append(ns.subscriptions[:i], ns.subscriptions[i+1:]...)
				return
			}
		}
	}
}

func NewNeighSubscription(quit <-chan struct{}) (*NeighSubscription, error) {
	ns := &NeighSubscription{
		addSub: make(chan chan *netlink.Neigh),
	}

	s, err := nl.Subscribe(syscall.NETLINK_ROUTE, syscall.RTNLGRP_NEIGH)
	if err != nil {
		return nil, err
	}

	go func() {
		<-quit
		s.Close()
	}()

	go func() {
		for {
			msgs, err := s.Receive()
			if err != nil {
				log.WithError(err).Fatal("Error recieving neighbor update")
			}
			for _, m := range msgs {
				n, err := netlink.NeighDeserialize(m.Data)
				if err != nil {
					log.Errorf("Error deserializing neighbor message %v", m.Data)
				}
				ns.subLock.Lock()
				for _, ch := range ns.subscriptions {
					ch <- n
				}
				ns.subLock.Unlock()
			}
		}
	}()

	return ns
}
