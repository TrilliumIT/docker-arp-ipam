package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"net"
	"sync"
	"syscall"
	"time"
)

const neighChanLen = 256

func (d *Driver) requestAddress(addr net.IP) error {
	r, err := d.ns.probeAndWait(addr)
	if err != nil {
		log.WithError(err).Fatal("Error determining if addr is reachable")
		return err
	}
	if r {
		return fmt.Errorf("Address already in use: %v", addr)
	}
	return nil
}

func addrStatus(addr net.IP) (known, reachable bool, err error) {
	var neighs []netlink.Neigh
	neighs, err = netlink.NeighList(0, netlink.FAMILY_V4)
	if err != nil {
		return
	}
	for _, n := range neighs {
		if n.IP.Equal(addr) {
			known = n.State == netlink.NUD_FAILED || n.State == netlink.NUD_REACHABLE
			reachable = known && n.State != netlink.NUD_FAILED
			return
		}
	}
	return
}

type NeighSubscription struct {
	subscriptions []chan *netlink.Neigh
	subLock       sync.Mutex
}

func (ns *NeighSubscription) probeAndWait(addr net.IP) (reachable bool, err error) {
	var known bool
	known, reachable, err = addrStatus(addr)
	if err != nil || known {
		return
	}

	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	uch := ns.addSub()
	defer ns.delSub(uch)

	probe(addr)
	for {
		select {
		case n := <-uch:
			if !n.IP.Equal(addr) {
				continue
			}
			known, reachable, err = addrStatus(addr)
			if err != nil || known {
				return
			}
		case <-t.C:
			known, reachable, err = addrStatus(addr)
			if err != nil || known {
				return
			}
			probe(addr)
		}
	}
}

func (ns *NeighSubscription) addSub() chan *netlink.Neigh {
	sub := make(chan *netlink.Neigh, neighChanLen)
	ns.subLock.Lock()
	defer ns.subLock.Unlock()
	ns.subscriptions = append(ns.subscriptions, sub)
	return sub
}

func (ns *NeighSubscription) delSub(sub chan *netlink.Neigh) {
	defer close(sub)
	// Can't select on a lock, workaround by locking in a goroutine then selecting
	lch := make(chan struct{})
	go func() {
		ns.subLock.Lock()
		close(lch)
	}()
	defer ns.subLock.Unlock()

	select {
	case <-sub: // read pending messages to avoid deadlock while waiting for sublock
	case <-lch:
		for i, s := range ns.subscriptions {
			if sub == s {
				ns.subscriptions = append(ns.subscriptions[:i], ns.subscriptions[i+1:]...)
				return
			}
		}
	}
}

func NewNeighSubscription(quit <-chan struct{}) (*NeighSubscription, error) {
	ns := &NeighSubscription{}

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
				log.WithError(err).Error("Error recieving neighbor update")
				return
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

	return ns, nil
}

func probe(ip net.IP) {
	conn, err := net.Dial("udp", ip.String()+":8765")
	if err != nil {
		log.Error(err)
		return
	}
	defer conn.Close()
	conn.Write([]byte("probe"))
	return
}
