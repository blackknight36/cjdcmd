/*
 * You may redistribute this program and/or modify it under the terms of
 * the GNU General Public License as published by the Free Software Foundation,
 * either version 3 of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/blackknight36/go-cjdns/admin"
	"github.com/blackknight36/go-cjdns/config"
	"github.com/blackknight36/go-cjdns/key"
	"io/ioutil"
	"math/rand"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var Version = "0.5.1"

const (
	magicalLinkConstant = 5366870.0 //Determined by cjd way back in the dark ages.

	defaultPingTimeout  = 5000 //5 seconds
	defaultPingCount    = 0
	defaultPingInterval = float64(1)

	defaultLogLevel    = "DEBUG"
	defaultLogFile     = ""
	defaultLogFileLine = -1

	defaultPass      = ""
	defaultAdminBind = ""

	defaultNoDNS = false

	pingCmd       = "ping"
	logCmd        = "log"
	traceCmd      = "traceroute"
	peerCmd       = "peers"
	dumpCmd       = "dump"
	routeCmd      = "route"
	killCmd       = "kill"
	versionCmd    = "version"
	pubKeyToIPcmd = "ip"
	passGenCmd    = "passgen"
	hostCmd       = "host"
	hostNameCmd   = "hostname"
	cleanCfgCmd   = "cleanconfig"
	addPeerCmd    = "addpeer"
	addPassCmd    = "addpass"
	memoryCmd     = "memory"
	cjdnsadminCmd = "cjdnsadmin"
)

var (
	PingTimeout  int
	PingCount    int
	PingInterval float64

	LogLevel    string
	LogFile     string
	LogFileLine int

	fs *flag.FlagSet

	File, OutFile string

	AdminPassword string
	AdminBind     string

	NoDNS bool

	userSpecifiedCjdnsadmin bool
	userCjdnsadmin          string
)

type Route struct {
	IP      string
	Path    string
	RawPath uint64
	Link    float64
	RawLink int64
	Version int64
}

type Data struct {
	User            *admin.Conn
	LoggingStreamID string
}

func init() {

	fs = flag.NewFlagSet("cjdcmd", flag.ExitOnError)
	const (
		usagePingTimeout  = "[ping][traceroute] specify the time in milliseconds cjdns should wait for a response"
		usagePingCount    = "[ping][traceroute] specify the number of packets to send"
		usagePingInterval = "[ping] specify the delay between successive pings"

		usageLogLevel    = "[log] specify the logging level to use"
		usageLogFile     = "[log] specify the cjdns source file you wish to see log output from"
		usageLogFileLine = "[log] specify the cjdns source file line to log"

		usageFile    = "[all] the cjdroute.conf configuration file to use, edit, or view"
		usageOutFile = "[all] the cjdroute.conf configuration file to save to"

		usagePass = "[all] specify the admin password"

		usageNoDNS = "[all] Do not perform DNS lookups (greatly improves speed)"

		usageCjdnsadmin = "[all] Specify the cjdnsadmin file to use"
	)

	fs.StringVar(&File, "file", "", usageFile)
	fs.StringVar(&File, "f", "", usageFile+" (shorthand)")

	fs.StringVar(&OutFile, "outfile", "", usageOutFile)
	fs.StringVar(&OutFile, "o", "", usageOutFile+" (shorthand)")

	fs.IntVar(&PingTimeout, "timeout", defaultPingTimeout, usagePingTimeout)
	fs.IntVar(&PingTimeout, "t", defaultPingTimeout, usagePingTimeout+" (shorthand)")

	fs.Float64Var(&PingInterval, "interval", defaultPingInterval, usagePingInterval)
	fs.Float64Var(&PingInterval, "i", defaultPingInterval, usagePingInterval+" (shorthand)")

	fs.IntVar(&PingCount, "count", defaultPingCount, usagePingCount)
	fs.IntVar(&PingCount, "c", defaultPingCount, usagePingCount+" (shorthand)")

	fs.StringVar(&LogLevel, "level", defaultLogLevel, usageLogLevel)
	fs.StringVar(&LogLevel, "l", defaultLogLevel, usageLogLevel+" (shorthand)")

	fs.StringVar(&LogFile, "logfile", defaultLogFile, usageLogFile)
	fs.IntVar(&LogFileLine, "line", defaultLogFileLine, usageLogFileLine)

	fs.BoolVar(&NoDNS, "nodns", defaultNoDNS, usageNoDNS)

	fs.StringVar(&userCjdnsadmin, "cjdnsadmin", "", usageCjdnsadmin)

	// Seed the PRG
	rand.Seed(time.Now().UTC().UnixNano())
}

func main() {
	//Define the flags, and parse them
	//Clearly a hack but it works for now
	//TODO(inhies): Re-implement flag parsing so flags can have multiple meanings based on the base command (ping, route, etc)
	if len(os.Args) <= 1 {
		usage()
		return
	} else if len(os.Args) == 2 {
		if string(os.Args[1]) == "--help" {
			fs.PrintDefaults()
			return
		}
	} else {
		fs.Parse(os.Args[2:])
	}

	//TODO(inhies): check argv[0] for trailing commands.
	//For example, to run ctraceroute:
	//ln -s /path/to/cjdcmd /usr/bin/ctraceroute like things
	command := os.Args[1]

	arguments := fs.Args()
	data := arguments[fs.NFlag()-fs.NFlag():]

	//Setup variables now so that if the program is killed we can still finish what we're doing
	ping := &Ping{}

	globalData := &Data{&admin.Conn{}, ""}
	var err error
	if File != "" {
		File, err = filepath.Abs(File)
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	if OutFile != "" {
		OutFile, err = filepath.Abs(OutFile)
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	// Check to see if the user specified a cjdnsadmin file to use instead of
	// the default
	if userCjdnsadmin != "" {
		userSpecifiedCjdnsadmin = true
	}

	// capture ctrl+c (actually any kind of kill signal...)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for _ = range c {
			fmt.Printf("\n")
			if command == "log" {
				// Unsubscribe from logging
				err := globalData.User.AdminLog_unsubscribe(globalData.LoggingStreamID)
				if err != nil {
					fmt.Printf("%v\n", err)
					os.Exit(1)
				}
			}
			if command == "ping" {
				//stop pinging and print results
				outputPing(ping)
			}

			// If we have an open connection, close it
			if globalData.User.Conn != nil {
				globalData.User.Conn.Close()
			}

			// Exit with no error
			os.Exit(0)
		}
	}()

	switch command {
	// Generates a .cjdnsadmin file
	case cjdnsadminCmd:
		if File == "" {
			var cjdnsAdmin *admin.CjdnsAdminConfig
			if !userSpecifiedCjdnsadmin {
				cjdnsAdmin, err = loadCjdnsadmin()
				if err != nil {
					fmt.Println("Unable to load configuration file:", err)
					return
				}
			} else {
				cjdnsAdmin, err = readCjdnsadmin(userCjdnsadmin)
				if err != nil {
					fmt.Println("Error loading cjdnsadmin file:", err)
					return
				}
			}
			File = cjdnsAdmin.Config
			if File == "" {
				fmt.Println("Please specify the configuration file in your .cjdnsadmin file or pass the --file flag.")
				return
			}
		}

		fmt.Printf("Loading configuration from: %v... ", File)
		conf, err := readConfig()
		if err != nil {
			fmt.Println("Error loading config:", err)
			return
		}
		fmt.Printf("Loaded\n")

		split := strings.LastIndex(conf.Admin.Bind, ":")
		addr := conf.Admin.Bind[:split]
		port := conf.Admin.Bind[split+1:]
		portInt, err := strconv.Atoi(port)
		if err != nil {
			fmt.Println("Error with cjdns admin bind settings")
			return
		}

		adminOut := admin.CjdnsAdminConfig{
			Addr:     addr,
			Port:     portInt,
			Password: conf.Admin.Password,
			Config:   File,
		}

		jsonout, err := json.MarshalIndent(adminOut, "", "\t")
		if err != nil {
			fmt.Println("Unable to create JSON for .cjdnsadmin")
			return
		}

		if OutFile == "" {
			tUser, err := user.Current()
			if err != nil {
				fmt.Println("I was unable to get your home directory, please manually specify where to save the file with --outfile")
				return
			}
			OutFile = tUser.HomeDir + "/.cjdnsadmin"
		}

		// Check if the output file exists and prompt befoer overwriting
		if _, err := os.Stat(OutFile); err == nil {
			fmt.Printf("Overwrite %v? [y/N]: ", OutFile)
			if !gotYes(false) {
				return
			}
		} else {
			fmt.Println("Saving to", OutFile)
		}

		ioutil.WriteFile(OutFile, jsonout, 0600)

	case cleanCfgCmd:
		// Load the config file
		if File == "" {
			var cjdnsAdmin *admin.CjdnsAdminConfig
			if !userSpecifiedCjdnsadmin {
				cjdnsAdmin, err = loadCjdnsadmin()
				if err != nil {
					fmt.Println("Unable to load configuration file:", err)
					return
				}
			} else {
				cjdnsAdmin, err = readCjdnsadmin(userCjdnsadmin)
				if err != nil {
					fmt.Println("Error loading cjdnsadmin file:", err)
					return
				}
			}
			File = cjdnsAdmin.Config
			if File == "" {
				fmt.Println("Please specify the configuration file in your .cjdnsadmin file or pass the --file flag.")
				return
			}
		}

		fmt.Printf("Loading configuration from: %v... ", File)
		conf, err := config.LoadExtConfig(File)
		if err != nil {
			fmt.Println("Error loading config:", err)
			return
		}
		fmt.Printf("Loaded\n")

		// Get the permissions from the input file
		stats, err := os.Stat(File)
		if err != nil {
			fmt.Println("Error getting permissions for original file:", err)
			return
		}

		if File != "" && OutFile == "" {
			OutFile = File
		}

		// Check if the output file exists and prompt befoer overwriting
		if _, err := os.Stat(OutFile); err == nil {
			fmt.Printf("Overwrite %v? [y/N]: ", OutFile)
			if !gotYes(false) {
				return
			}
		}

		fmt.Printf("Saving configuration to: %v... ", OutFile)
		err = config.SaveConfig(OutFile, conf, stats.Mode())
		if err != nil {
			fmt.Println("Error saving config:", err)
			return
		}
		fmt.Printf("Saved\n")
	case addPassCmd:
		addPassword(data)

	case addPeerCmd:
		addPeer(data)

	case hostNameCmd:
		if len(data) == 0 {
			setHypeDNS("")
			return
		}
		if len(data) == 1 {
			setHypeDNS(data[0])
			return
		}
		if len(data) > 1 {
			fmt.Println("Too many arguments.")
			return
		}
		return

	case hostCmd:
		if len(data) == 0 {
			fmt.Println("Invalid hostname or IPv6 address specified")
			return
		}
		input := data[0]
		validIP, _ := regexp.MatchString(ipRegex, input)
		validHost, _ := regexp.MatchString(hostRegex, input)

		if validIP {
			hostname, err := resolveIP(input)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}
			fmt.Printf("%v\n", hostname)
		} else if validHost {
			ips, err := resolveHost(input)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}
			for _, addr := range ips {
				fmt.Printf("%v has IPv6 address %v\n", data[0], addr)
			}
		} else {
			fmt.Println("Invalid hostname or IPv6 address specified")
			return
		}

	case passGenCmd:
		if len(data) > 0 && len(data[0]) > 0 {
			fmt.Println(data[0] + "_" + randString(25, 50))
		} else {
			fmt.Println(randString(25, 50))
		}


	case pubKeyToIPcmd:
		var ip []byte
		if len(data) > 0 {
			if len(data[0]) == 52 || len(data[0]) == 54 {
				ip = []byte(data[0])
			} else {
				fmt.Println("Invalid public key")
				return
			}
		} else {
			fmt.Println("Invalid public key")
			return
		}
		parsed, err := key.DecodePublic(string(ip))
		if err != nil {
			fmt.Println(err)
			return
		}
		var tText string
		hostname, _ := resolveIP(parsed.String())
		if hostname != "" {
			tText = parsed.String() + " (" + hostname + ")"
		} else {
			tText = parsed.String()
		}
		fmt.Printf("%v\n", tText)

	case traceCmd:
		user, err := adminConnect()
		if err != nil {
			fmt.Println("Error:", err)
			return
		}
		globalData.User = user
		target, err := setTarget(data, true)
		if err != nil {
			fmt.Println("Error:", err)
			return
		}
		doTraceroute(globalData.User, target)

	case routeCmd:

		target, err := setTarget(data, true)
		if err != nil {
			fmt.Println(err)
			return
		}
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}
		var tText string
		hostname, _ := resolveIP(target.Target)
		if hostname != "" {
			tText = target.Target + " (" + hostname + ")"
		} else {
			tText = target.Target
		}
		fmt.Printf("Showing all routes to %v\n", tText)
		globalData.User = user
		table, err := user.NodeStore_dumpTable()
		if err != nil {
			fmt.Println(err)
			return
		}

		table.SortByQuality()

		count := 0
		for _, v := range table {
			if v.IP.String() == target.Target || v.Path.String() == target.Target {
				if v.Link > 1 {
					fmt.Printf("IP: %v -- Version: %d -- Path: %s -- Link: %.0f\n", v.IP, v.Version, v.Path, v.Link)
					count++
				}
			}
		}
		fmt.Println("Found", count, "routes")

	case pingCmd:
		// TODO: allow pinging of entire routing table
		target, err := setTarget(data, true)
		if err != nil {
			fmt.Println(err)
			return
		}
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}
		globalData.User = user
		ping.Target = target.Target

		var tText string

		// If we were given an IP then try to resolve the hostname
		if validIP(target.Supplied) {
			hostname, _ := resolveIP(target.Target)
			if hostname != "" {
				tText = target.Supplied + " (" + hostname + ")"
			} else {
				tText = target.Supplied
			}
			// If we were given a path, resolve the IP
		} else if validPath(target.Supplied) {
			tText = target.Supplied
			table, err := user.NodeStore_dumpTable()
			if err != nil {
				fmt.Println(err)
				return
			}

			for _, v := range table {
				if v.Path.String() == target.Supplied {
					// We have the IP now
					tText = target.Supplied + " (" + v.IP.String() + ")"

					// Try to get the hostname
					hostname, _ := resolveIP(v.IP.String())
					if hostname != "" {
						tText = target.Supplied + " (" + v.IP.String() + " (" + hostname + "))"
					}
				}
			}
			// We were given a hostname, everything is already done for us!
		} else if validHost(target.Supplied) {
			tText = target.Supplied + " (" + target.Target + ")"
		}
		fmt.Printf("PING %v \n", tText)

		if PingCount != defaultPingCount {
			// ping only as much as the user asked for
			for i := 1; i <= PingCount; i++ {
				start := time.Duration(time.Now().UTC().UnixNano())
				err := pingNode(globalData.User, ping)
				if err != nil {
					if err.Error() != "Socket closed" {
						fmt.Println(err)
					}
					return
				}
				fmt.Println(ping.Response)
				// Send 1 ping per second
				now := time.Duration(time.Now().UTC().UnixNano())
				time.Sleep(start + (time.Duration(PingInterval) * time.Second) - now)

			}
		} else {
			// ping until we're told otherwise
			for {
				start := time.Duration(time.Now().UTC().UnixNano())
				err := pingNode(globalData.User, ping)
				if err != nil {
					// Ignore these errors, as they are returned when we kill an in-progress ping
					if err.Error() != "Socket closed" && err.Error() != "use of closed network connection" {
						fmt.Println("ermagherd:", err)
					}
					return
				}
				fmt.Println(ping.Response)
				// Send 1 ping per second
				now := time.Duration(time.Now().UTC().UnixNano())
				time.Sleep(start + (time.Duration(PingInterval) * time.Second) - now)

			}
		}
		outputPing(ping)

	case logCmd:
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}
		globalData.User = user
		response := make(chan *admin.LogMessage)
		globalData.LoggingStreamID, err =
			globalData.User.AdminLog_subscribe(LogLevel, LogFile, LogFileLine, response)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		format := "%d %d %s %s:%d %s\n" // TODO: add user formatted output
		counter := 1

		// Spawn a routine to ping cjdns every 10 seconds to keep the connection alive
		go func() {
			for {
				timeout := 10 * time.Second
				time.Sleep(timeout)
				err := globalData.User.Ping()

				if err != nil {
					fmt.Println("Error sending periodic ping to cjdns:", err)
					return
				}
			}
		}()
		for {
			input, ok := <-response
			if !ok {
				fmt.Println("Error reading log response from cjdns.")
				return
			}
			fmt.Printf(format, counter, input.Time, input.Level, input.File, input.Line, input.Message)
			counter++
		}

	case peerCmd:
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}

		// no target specified, use ourselves
		if len(data) == 0 {
			doOwnPeers(user)
		} else {

			target, err := setTarget(data, true)
			if err != nil {
				fmt.Println(err)
				return
			}
			globalData.User = user
			doPeers(user, target)
		}
	case versionCmd:
		// TODO(inhies): Ping a specific node and return it's cjdns version, or
		// ping all nodes in the routing table and get their versions
		// git log -1 --date=iso --pretty=format:"%ad" <hash>

	case killCmd:
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}
		globalData.User = user
		err = globalData.User.Core_exit()
		if err != nil {
			fmt.Printf("%v\n", err)
			return
		}
		fmt.Println("cjdns is shutting down...")

	case dumpCmd:
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}
		globalData.User = user
		// TODO: add flag to show zero link quality routes, by default hide them
		table, err := user.NodeStore_dumpTable()
		if err != nil {
			fmt.Println(err)
			return
		}

		table.SortByQuality()
		k := 1
		for _, v := range table {
			if v.Link >= 1 {
				fmt.Printf("%d IP: %v -- Version: %d -- Path: %s -- Link: %s\n", k, v.IP, v.Version, v.Path, v.Link)
				k++
			}
		}
	case memoryCmd:
		user, err := adminConnect()
		if err != nil {
			fmt.Println(err)
			return
		}
		globalData.User = user

		response, err := globalData.User.Memory()
		if err != nil {
			fmt.Printf("%v\n", err)
			return
		}
		fmt.Println(response, "bytes")
	default:
		fmt.Println("Invalid command", command)
		usage()
	}
}
