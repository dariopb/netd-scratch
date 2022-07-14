package networknode

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	dnsserver "github.com/dariopb/netd/pkg/dnsserver"
	helpers "github.com/dariopb/netd/pkg/helpers"
	registrynode "github.com/dariopb/netd/pkg/registrynode"
	workernode "github.com/dariopb/netd/pkg/workernode"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var banner1 = `
         __                  __       
  ____ _/ /___ _ ___ _____ _/ /___  __
 / __ '/ / __ '/ ___/ ___/ / __/ / / /
/ /_/ / / /_/ / /__/ /  / / /_/ /_/ / 
\__,_/_/\__,_/\___/_/  /_/\__/\__, /  
                             ___/ /
                            /____/   

`
var banner = `
       _                   _         
      | |                 | |        
  __ _| | __ _  ___ _ __ _| |_ _   _ 
 / _' | |/ _' |/ __| '__| | __| | | |
| (_| | | (_| | (__| |  | | |_| |_| |
 \__,_|_|\__,_|\___|_|  |_|\__|\__, |
                                __/ |
                               |___/ 
`

func printVersion() {
	log.Info(fmt.Sprintf("networknode version: %v", version))
	log.Info(fmt.Sprintf("Go Version: %s", runtime.Version()))
	log.Info(fmt.Sprintf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH))
}

var port int
var instancemeta string
var loglevelstr string
var nodeid string
var pubsubEndpoint string
var realm string
var defaultSubnet string
var token string
var hmacKey string
var cleanupVRF bool
var isNodeStack bool
var dataDirPath string
var version string = "0.0.1"

func RunNode() {
	banner = strings.ReplaceAll(banner, "#", "`")
	fmt.Println(banner)

	app := &cli.App{
		EnableBashCompletion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "loglevel",
				Aliases:     []string{"l"},
				Value:       "info",
				Usage:       "debug level, one of: info, debug",
				EnvVars:     []string{"LOGLEVEL"},
				Destination: &loglevelstr,
			},
			&cli.StringFlag{
				Name:        "token",
				Aliases:     []string{"t"},
				Value:       "info",
				Usage:       "bearer token for authorization",
				EnvVars:     []string{"TOKEN"},
				Destination: &token,
				Required:    true,
			},
		},
		Commands: []*cli.Command{
			{
				Name: "workernode",
				//Aliases: []string{"server"},
				Usage:  "runs as a worker node",
				Action: workerNode,

				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:        "nodeid",
						Aliases:     []string{"n"},
						Value:       "",
						Usage:       "id of the node",
						EnvVars:     []string{"NODE_ID"},
						Destination: &nodeid,
						Required:    true,
					},
					&cli.StringFlag{
						Name:        "pubsubEndpoint",
						Aliases:     []string{"e"},
						Value:       "localhost",
						Usage:       "pubsubEndpoint",
						EnvVars:     []string{"PUBSUB_ENDPOINT"},
						Destination: &pubsubEndpoint,
						Required:    true,
					},
					&cli.StringFlag{
						Name:        "realm",
						Aliases:     []string{"r"},
						Value:       "",
						Usage:       "realm/namespace",
						EnvVars:     []string{"REALM"},
						Destination: &realm,
						Required:    true,
					},
					&cli.IntFlag{
						Name:        "port",
						Aliases:     []string{"p"},
						Value:       9999,
						Usage:       "port for the local API endpoint",
						EnvVars:     []string{"PORT"},
						Destination: &port,
						Required:    false,
					},
					&cli.BoolFlag{
						Name:        "cleanupVRF",
						Value:       false,
						Usage:       "true if need to cleanup VRFs",
						EnvVars:     []string{"CLEANUP_VRF"},
						Destination: &cleanupVRF,
						Required:    false,
					},
					&cli.BoolFlag{
						Name:        "isNodeStack",
						Value:       true,
						Usage:       "false to hook up namespaced container stack",
						EnvVars:     []string{"IS_NODE_STACK"},
						Destination: &isNodeStack,
						Required:    false,
					},
					&cli.StringFlag{
						Name:        "hmacKey",
						Aliases:     []string{"k"},
						Value:       "",
						Usage:       "shared secret for authorization",
						EnvVars:     []string{"HMACKEY"},
						Destination: &hmacKey,
						Required:    true,
					},
				},
			},
			{
				Name: "gwnode",
				//Aliases: []string{"server"},
				Usage:  "runs as a gateway node",
				Action: gwNode,

				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:        "instancemetadata",
						Aliases:     []string{"i"},
						Value:       "",
						Usage:       "Azure instance metadata",
						EnvVars:     []string{"INSTANCE_METADATA"},
						Destination: &instancemeta,
						Required:    true,
					},

					&cli.StringFlag{
						Name:        "pubsubEndpoint",
						Aliases:     []string{"e"},
						Value:       "localhost",
						Usage:       "pubsubEndpoint",
						EnvVars:     []string{"PUBSUB_ENDPOINT"},
						Destination: &pubsubEndpoint,
						Required:    true,
					},
				},
			},
			{
				Name: "registrynode",
				//Aliases: []string{"server"},
				Usage:  "runs as a registry node",
				Action: registryNode,

				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:        "port",
						Aliases:     []string{"p"},
						Value:       7778,
						Usage:       "port for the API endpoint",
						EnvVars:     []string{"PORT"},
						Destination: &port,
						Required:    true,
					},
					&cli.StringFlag{
						Name:        "defaultSubnet",
						Aliases:     []string{"s"},
						Value:       "defaultSubnet",
						Usage:       "default subnet to attach containers to",
						EnvVars:     []string{"DEFAULT_SUBNET"},
						Destination: &defaultSubnet,
						Required:    false,
					},
					&cli.StringFlag{
						Name:        "dataDirPath",
						Aliases:     []string{"d"},
						Value:       "./file.db",
						Usage:       "db path",
						EnvVars:     []string{"DATA_DIR_PATH"},
						Destination: &dataDirPath,
						Required:    false,
					},
					&cli.StringFlag{
						Name:        "instancemetadata",
						Aliases:     []string{"i"},
						Value:       "",
						Usage:       "Azure instance metadata",
						EnvVars:     []string{"INSTANCE_METADATA"},
						Destination: &instancemeta,
						Required:    false,
					},
					&cli.StringFlag{
						Name:        "hmacKey",
						Aliases:     []string{"k"},
						Value:       "",
						Usage:       "shared secret for authorization",
						EnvVars:     []string{"HMACKEY"},
						Destination: &hmacKey,
						Required:    true,
					},
				},
			},
		},

		Name:  "gonoscrianmesh",
		Usage: "creates and manages vlan vrf entries",
		//Action: goreverselbserver,
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

// Worker node function
func workerNode(ctx *cli.Context) error {
	loglevel := log.InfoLevel
	if l, err := log.ParseLevel(loglevelstr); err == nil {
		loglevel = l
	}

	//log.AddHook(ProcessCounter)
	log.SetFormatter(&log.TextFormatter{ForceColors: true, FullTimestamp: true})
	log.SetLevel(loglevel)
	log.SetOutput(os.Stdout)

	printVersion()

	log.Infof("Starting worker node: name: %s, realm: %s, pubsub: %s", nodeid, realm, pubsubEndpoint)

	// Start the registry as well for now for convenience
	_, err := registrynode.NewRegistryNode("./file.db", hmacKey, 4444, "vnet1")
	if err != nil {
		log.Fatal(err)
	}
	time.Sleep(time.Second)

	conn, err := helpers.GetGrpcClient(pubsubEndpoint, token)

	// Start the dns server
	dns := dnsserver.NewDNSServer(6666, "1.1.1.1:53", conn)
	err = dns.StartDnsServer()
	if err != nil {
		log.Fatal(err)
	}

	isNodeStack = true
	_, err = workernode.NewWorkerNode(nodeid, port, conn, "vnet1", isNodeStack, cleanupVRF)
	if err != nil {
		log.Fatal(err)
	}

	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	<-c
	return nil
}

// Worker node function
func gwNode(ctx *cli.Context) error {
	printVersion()

	loglevel := log.InfoLevel
	if l, err := log.ParseLevel(loglevelstr); err == nil {
		loglevel = l
	}

	//log.AddHook(ProcessCounter)
	//log.SetFormatter(&log.TextFormatter{ForceColors: true})
	log.SetLevel(loglevel)
	log.SetOutput(os.Stdout)
	/*
		_, err := gwnode.NewGWNode(instancemeta, pubsubEndpoint, token)
		if err != nil {
			log.Fatal(err)
		}

		c := make(chan os.Signal, 2)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)

		<-c
	*/
	return nil
}

// Registry node function
func registryNode(ctx *cli.Context) error {
	printVersion()

	loglevel := log.InfoLevel
	if l, err := log.ParseLevel(loglevelstr); err == nil {
		loglevel = l
	}

	//log.AddHook(ProcessCounter)
	//log.SetFormatter(&log.TextFormatter{ForceColors: true})
	log.SetLevel(loglevel)
	log.SetOutput(os.Stdout)

	log.Infof("Starting registry node: db: %s, realm: %s, grpc port: %d", dataDirPath, realm, port)

	_, err := registrynode.NewRegistryNode(dataDirPath, hmacKey, port, defaultSubnet)
	if err != nil {
		log.Fatal(err)
	}

	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	time.Sleep(1 * time.Second)

	<-c

	return nil
}
