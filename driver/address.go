package driver

import (
	"fmt"
	"net"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

const neighChanLen = 256

func (d *Driver) requestAddress(addr *net.IPNet) error {
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

type neighSubscription struct {
	addSubCh chan *subscription
}

type subscription struct {
	ip    *net.IPNet
	sub   chan *netlink.Neigh
	close chan struct{}
}

func (ns *neighSubscription) probeAndWait(addr *net.IPNet) (reachable bool, err error) {
	var known bool
	known, reachable, err = addrStatus(addr.IP)
	if err != nil || known {
		return
	}

	t := time.NewTicker(3 * time.Second)
	to := time.Now().Add(10 * time.Second)
	defer t.Stop()
	sub := ns.addSub(addr)
	uch := sub.sub
	defer sub.delSub()

	probe(addr.IP)
	for {
		select {
		case n := <-uch:
			if !n.IP.Equal(addr.IP) {
				continue
			}
			known, reachable, err = addrStatus(addr.IP)
			if err != nil || known {
				return
			}
		case n := <-t.C:
			known, reachable, err = addrStatus(addr.IP)
			if err != nil || known {
				return
			}
			if n.After(to) {
				return true, fmt.Errorf("Error determining reachability for %v", addr)
			}
			probe(addr.IP)
		}
	}
}

func (ns *neighSubscription) addSub(ip *net.IPNet) *subscription {
	sub := &subscription{
		ip:    ip,
		sub:   make(chan *netlink.Neigh, neighChanLen),
		close: make(chan struct{}),
	}
	go func() { ns.addSubCh <- sub }()
	return sub
}

func (sub *subscription) delSub() {
	close(sub.close)
}

func newNeighSubscription(quit <-chan struct{}) (*neighSubscription, error) {
	ns := &neighSubscription{
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
						if sub.ip.IP.Equal(n.IP) {
							sub.sub <- n
						}
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
		log.WithError(err).WithField("ip", ip).Error("Error creating probe connection.")
		return
	}
	if _, err := conn.Write([]byte("probe")); err != nil {
		log.WithError(err).WithField("ip", ip).Error("Error probing connection.")
	}
	if err := conn.Close(); err != nil {
		log.WithError(err).WithField("ip", ip).Error("Error clossing probe connection.")
	}
	return
}
