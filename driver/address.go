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

func (d *Driver) tryAddress(addr *net.IPNet) error {
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

func (ns *neighSubscription) addrStatus(addr net.IP) (known, reachable bool) {
	n := ns.check(addr)
	return parseAddrStatus(n)
}

func parseAddrStatus(n *netlink.Neigh) (known, reachable bool) {
	if n != nil {
		known = n.State == netlink.NUD_FAILED || n.State == netlink.NUD_REACHABLE
		reachable = known && n.State != netlink.NUD_FAILED
		return
	}
	return
}

type neighSubscription struct {
	addSubCh chan *subscription
	checkCh  chan *neighCheckReq
}

type subscription struct {
	ip      *net.IPNet
	created time.Time
	sub     chan *netlink.Neigh
	close   chan struct{}
}

func (ns *neighSubscription) probeAndWait(addr *net.IPNet) (reachable bool, err error) {
	var known bool
	known, reachable = ns.addrStatus(addr.IP)
	if known {
		return
	}

	t := time.NewTicker(1 * time.Second)
	to := time.Now().Add(5 * time.Second)
	defer t.Stop()
	sub := ns.addSub(addr)
	defer sub.delSub()

	probe(addr.IP)
	for {
		select {
		case n := <-sub.sub:
			known, reachable = parseAddrStatus(n)
			if known {
				return
			}
		case <-t.C:
		}
		known, reachable = ns.addrStatus(addr.IP)
		if known {
			return
		}
		if time.Now().After(to) {
			return true, fmt.Errorf("Error determining reachability for %v", addr)
		}
		probe(addr.IP)
	}
}

func (ns *neighSubscription) addSub(ip *net.IPNet) *subscription {
	sub := &subscription{
		ip:      ip,
		created: time.Now(),
		sub:     make(chan *netlink.Neigh, neighChanLen),
		close:   make(chan struct{}),
	}
	go func() { ns.addSubCh <- sub }()
	return sub
}

func (sub *subscription) delSub() {
	close(sub.close)
}

type neighCheckReq struct {
	ip    net.IP
	retCh chan *netlink.Neigh
}

type neighUpdate struct {
	time  time.Time
	neigh *netlink.Neigh
}

func newNeighSubscription(quit <-chan struct{}) (*neighSubscription, error) {
	ns := &neighSubscription{
		addSubCh: make(chan *subscription),
		checkCh:  make(chan *neighCheckReq),
	}

	s, err := nl.Subscribe(syscall.NETLINK_ROUTE, syscall.RTNLGRP_NEIGH)
	if err != nil {
		return nil, err
	}

	go func() {
		<-quit
		s.Close()
	}()

	neighSubCh := make(chan []*neighUpdate, neighChanLen)
	go func() {
		for {
			for {
				msgs, err := s.Receive()
				select {
				case <-quit:
					return
				default:
				}

				if err != nil {
					log.WithError(err).Error("Error recieving neighbor update")
				}
				t := time.Now()
				go func(t time.Time) {
					var ns []*neighUpdate
					for _, m := range msgs {
						n, err := netlink.NeighDeserialize(m.Data)
						if err != nil {
							log.Errorf("Error deserializing neighbor message %v", m.Data)
							continue
						}
						ns = append(ns, &neighUpdate{
							time:  t,
							neigh: n,
						})
					}
					neighSubCh <- ns
				}(t)
			}
		}
	}()

	neighs := make(neighbors)
	subs := make(map[string][]*subscription)
	tick := time.NewTicker(3 * time.Second)
	go func() {
		for {
			select {
			case sub := <-ns.addSubCh:
				subs[sub.ip.String()] = append(subs[sub.ip.String()], sub)
				n := neighs[sub.ip.String()]
				// Get the current entry, if it's newer than the sub, return it
				if n != nil && sub.created.Before(n.time) {
					go func(sub *subscription, n *netlink.Neigh) {
						sub.sub <- n
					}(sub, n.neigh)
				}
				continue
			case <-tick.C:
				neighList, err := netlink.NeighList(0, netlink.FAMILY_V4)
				t := time.Now()
				if err != nil {
					log.WithError(err).Error("Error refreshing neighbor table.")
					continue
				}
				for _, n := range neighList {
					on := neighs[n.IP.String()]
					if on == nil || (on.neigh.State != n.State && on.time.Before(t)) {
						neighs[n.IP.String()] = &neighUpdate{
							time:  t,
							neigh: &n,
						}
						neighs.update(&n, subs[n.IP.String()])
					}
				}
				continue
			case neighList := <-neighSubCh:
				for _, n := range neighList {
					on := neighs[n.neigh.IP.String()]
					if on == nil || (on.neigh.State != n.neigh.State && on.time.Before(n.time)) {
						neighs.update(n.neigh, subs[n.neigh.IP.String()])
					}
				}
				continue
			case nc := <-ns.checkCh:
				r := neighs[nc.ip.String()]
				if r != nil {
					nc.retCh <- r.neigh
					continue
				}
				nc.retCh <- nil
				continue
			}
		}
	}()

	return ns, nil
}

func (ns *neighSubscription) check(ip net.IP) *netlink.Neigh {
	nc := &neighCheckReq{
		ip:    ip,
		retCh: make(chan *netlink.Neigh),
	}
	ns.checkCh <- nc
	return <-nc.retCh
}

type neighbors map[string]*neighUpdate

func (neighs neighbors) update(n *netlink.Neigh, subs []*subscription) {
	l := len(subs)
	for i := range subs {
		j := l - i - 1 // loop in reverse order
		sub := subs[j]
		select {
		// Delete closed subs
		case <-sub.close:
			subs = append(subs[:j], subs[j+1:]...)
			close(sub.sub)
		// Send the update
		default:
			go func(sub *subscription, n *netlink.Neigh) {
				sub.sub <- n
			}(sub, n)
		}
	}
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
