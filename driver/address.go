package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"net"
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
	addSubCh chan *subscription
}

type subscription struct {
	sub   chan *netlink.Neigh
	close chan struct{}
}

func (ns *NeighSubscription) probeAndWait(addr net.IP) (reachable bool, err error) {
	var known bool
	known, reachable, err = addrStatus(addr)
	if err != nil || known {
		return
	}

	t := time.NewTicker(3 * time.Second)
	to := time.Now().Add(10 * time.Second)
	defer t.Stop()
	sub := ns.addSub()
	uch := sub.sub
	defer sub.delSub()

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
		case n := <-t.C:
			known, reachable, err = addrStatus(addr)
			if err != nil || known {
				return
			}
			if n.After(to) {
				return true, fmt.Errorf("Error determining reachability for %v", addr)
			}
			probe(addr)
		}
	}
}

func (ns *NeighSubscription) addSub() *subscription {
	sub := &subscription{
		sub:   make(chan *netlink.Neigh, neighChanLen),
		close: make(chan struct{}),
	}
	go func() { ns.addSubCh <- sub }()
	return sub
}

func (sub *subscription) delSub() {
	close(sub.close)
}

func NewNeighSubscription(quit <-chan struct{}) (*NeighSubscription, error) {
	ns := &NeighSubscription{
		addSubCh: make(chan *subscription, 64),
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
		subscriptions := []*subscription{}
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
				// Add new subscriptions
				for {
					select {
					case sub := <-ns.addSubCh:
						subscriptions = append(subscriptions, sub)
						continue
					default:
					}
					break
				}
				l := len(subscriptions)
				for i := range subscriptions {
					j := l - i - 1 // loop in reverse order
					sub := subscriptions[j]
					select {
					// Delete closed subscriptions
					case <-sub.close:
						subscriptions = append(subscriptions[:j], subscriptions[j+1:]...)
						close(sub.sub)
					// Send the update
					default:
						sub.sub <- n
					}
				}
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
