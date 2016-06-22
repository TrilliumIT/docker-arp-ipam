package main

import (
	"github.com/TrilliumIT/docker-arp-ipam/driver"
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/urfave/cli"
)

func main() {

	app := cli.NewApp()
	app.Name = "docker-arp-ipam"
	app.Usage = "Docker ARP IPAM Plugin"
	app.Version = "0.3"
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
	}
	app.Action = Run
	app.Run(os.Args)
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
	d, err := driver.NewDriver()
	if err != nil {
		log.Error("Error initializing driver")
		return err
	}
	h := ipam.NewHandler(d)
	return h.ServeTCP(ctx.String("plugin-name"), ctx.String("address"))
}
