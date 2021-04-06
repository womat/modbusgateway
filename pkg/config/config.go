package config

import (
	"flag"
	"fmt"
	"os"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"

	"modbusgateway/global"
	"modbusgateway/pkg/debug"
	"modbusgateway/pkg/mbclient"
	"modbusgateway/pkg/webservice"
)

func Init() {
	var err error

	if global.Options, err = loadConfig(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	switch {
	case global.Options.Version:
		// print version number
		fmt.Println(global.VERSION)
		os.Exit(0)

	case global.Options.Webserver.Active:
		// initialize Webservices
		if err := webservice.InitWebService(); err != nil {
			panic(err)
		}

	case global.Options.Debug.Active:
		debug.On()
	}

	global.Clientd = mbclient.NewClient()
	_ = global.Clientd.Start()
}

func loadConfig() (c global.ConfigOptions, err error) {
	var k = koanf.New(".")
	var configFile string
	var quiet, debug bool

	f := flag.NewFlagSet("modbus-gateway", flag.ContinueOnError)

	f.Usage = func() {
		_, _ = fmt.Fprintf(f.Output(), "Usage of %s:\n", f.Name())
		f.PrintDefaults()
		os.Exit(0)
	}

	f.BoolVar(&c.Version, "version", false,
		"print version and exit")
	f.BoolVar(&debug, "debug", false,
		"enable debug information")
	f.StringVar(&configFile, "config", "/opt/womat/config.yaml",
		"Config File eg. /opt/womat/config.yaml")
	f.BoolVar(&quiet, "quiet", false,
		"suppress log messages when setting this option")

	if err = f.Parse(os.Args[1:]); err != nil {
		return
	}

	if c.Version {
		return
	}

	if err = k.Load(file.Provider(configFile), yaml.Parser()); err != nil {
		return
	}

	// Quick unmarshal.
	if err = k.Unmarshal("", &c); err != nil {
		return
	}

	if debug {
		c.Debug.Active = true
	}
	if quiet {
		c.IsQuiet = true
	}
	return
}
