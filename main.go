package main

import (
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/docker-arp-ipam/driver"
	"github.com/docker/go-connections/sockets"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/urfave/cli"
)

const version = "0.20"

func main() {

	app := cli.NewApp()
	app.Name = "docker-arp-ipam"
	app.Usage = "Docker ARP IPAM Plugin"
	app.Version = version
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "Enable debugging.",
		},
		cli.StringFlag{
			Name:  "plugin-name, name",
			Value: "arp-ipam",
			Usage: "Plugin Name. Useful if you want to run multiple instances of the plugin.",
		},
		cli.StringFlag{
			Name:  "address, addr",
			Value: ":8080",
			Usage: "TCP Address to bind the plugin on.",
		},
		cli.IntFlag{
			Name:  "exclude-first, xf",
			Value: 0,
			Usage: "Exclude the first n addresses from each pool from being provided as random addresses",
		},
		cli.IntFlag{
			Name:  "exclude-last, xl",
			Value: 0,
			Usage: "Exclude the last n addresses from each pool from being provided as random addresses",
		},
	}
	app.Action = Run
	err := app.Run(os.Args)
	if err != nil {
		log.WithError(err).Fatal("Error from app")
	}
}

// Run initializes the driver
func Run(ctx *cli.Context) error {
	if ctx.Bool("debug") {
		log.SetLevel(log.DebugLevel)
	}
	log.SetFormatter(&log.TextFormatter{
		ForceColors:      false,
		DisableColors:    true,
		DisableTimestamp: false,
		FullTimestamp:    true,
	})
	log.WithField("Version", version).Info("Starting")
	xf := ctx.Int("xf")
	xl := ctx.Int("xl")

	quit := make(chan struct{}) // tells other goroutines to quit
	ech := make(chan error)     // catches an error from serveTCP
	var err error               // holds the final return value

	d, err := driver.NewDriver(quit, xf, xl)
	if err != nil {
		log.WithError(err).Error("Error initializing driver")
		return err
	}

	h := ipam.NewHandler(d)
	l, err := sockets.NewTCPSocket(ctx.String("address"), nil)
	if err != nil {
		log.WithError(err).Error("Error creating tcp socket")
		return err
	}

	go func() {
		ech <- h.Serve(l)
	}()

	c := make(chan os.Signal)
	defer close(c)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)

	select {
	case <-c:
		log.Debugf("Sigterm caught. Closing")
		if log.GetLevel() == log.DebugLevel {
			log.Debug("Dumping stack traces for all goroutines")
			if err = pprof.Lookup("goroutine").WriteTo(os.Stdout, 1); err != nil {
				log.WithError(err).Error("Error getting stack trace")
			}
		}
	case err = <-ech:
		log.Error(err)
	}
	close(quit)

	return nil
}
