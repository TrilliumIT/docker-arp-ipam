package main

import (
	"os"
	"os/signal"
	//"runtime/pprof"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/docker-arp-ipam/driver"
	//"github.com/docker/go-connections/sockets"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/urfave/cli"
)

const version = "0.26"

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

	d := driver.NewDriver(quit, xf, xl)

	dErrCh := make(chan error) // catches an error from driver
	go func() {
		dErrCh <- d.Start()
	}()

	h := ipam.NewHandler(d)
	lErrCh := make(chan error) // catches an error from serveTCP
	go func() {
		lErrCh <- h.ServeTCP(ctx.String("plugin-name"), ctx.String("address"), nil)
	}()

	c := make(chan os.Signal, 32)
	defer close(c)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)

	var retErr error // holds the final return value
	select {
	case <-c:
		log.Debugf("Sigterm caught. Closing")
		/*
			if log.GetLevel() == log.DebugLevel {
					log.Debug("Dumping stack traces for all goroutines")
					if err := pprof.Lookup("goroutine").WriteTo(os.Stdout, 1); err != nil {
						log.WithError(err).Error("Error getting stack trace")
					}
			}
		*/
	case err := <-lErrCh:
		if err != nil {
			log.WithError(err).Error("Error from listener")
			retErr = err
		}
	case err := <-dErrCh:
		if err != nil {
			log.WithError(err).Error("Error from driver")
			retErr = err
		}
	}

	select {
	case err := <-lErrCh:
		if err != nil {
			log.WithError(err).Error("Error from listener")
			retErr = err
		}
	default:
	}
	close(lErrCh)

	close(quit)
	err := <-dErrCh
	if err != nil {
		log.WithError(err).Error("Error from driver")
		retErr = err
	}
	close(dErrCh)

	return retErr
}
