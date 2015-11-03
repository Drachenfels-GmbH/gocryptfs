package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/rfjakob/gocryptfs/cryptfs"
	"github.com/rfjakob/gocryptfs/pathfs_frontend"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
)

const (
	PROGRAM_NAME = "gocryptfs"

	// Exit codes
	ERREXIT_USAGE      = 1
	ERREXIT_MOUNT      = 3
	ERREXIT_CIPHERDIR  = 6
	ERREXIT_INIT       = 7
	ERREXIT_LOADCONF   = 8
	ERREXIT_PASSWORD   = 9
	ERREXIT_MOUNTPOINT = 10
)

// GitVersion will be set by the build script "build.bash"
var GitVersion = "[version not set - please compile using ./build.bash]"

func initDir(dirArg string, plaintextNames bool) {
	dir, _ := filepath.Abs(dirArg)

	err := checkDirEmpty(dir)
	if err != nil {
		fmt.Printf("Invalid CIPHERDIR: %v\n", err)
		os.Exit(ERREXIT_INIT)
	}

	confName := filepath.Join(dir, cryptfs.ConfDefaultName)
	fmt.Printf("Choose a password for protecting your files.\n")
	password := readPasswordTwice()
	err = cryptfs.CreateConfFile(confName, password, plaintextNames)
	if err != nil {
		fmt.Println(err)
		os.Exit(ERREXIT_INIT)
	}
	fmt.Printf("The filesystem is now ready for mounting.\n")
	os.Exit(0)
}

func usageText() {
	fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] CIPHERDIR MOUNTPOINT\n", PROGRAM_NAME)
	fmt.Fprintf(os.Stderr, "\nOptions:\n")
	flag.PrintDefaults()
}

func main() {
	runtime.GOMAXPROCS(4)

	// Parse command line arguments
	var args struct {
		debug, init, zerokey, fusedebug, openssl, passwd, foreground, version,
			plaintextnames bool
		masterkey, mountpoint, cipherdir string
		cpuprofile *string
	}

	flag.Usage = usageText
	flag.BoolVar(&args.debug, "debug", false, "Enable debug output")
	flag.BoolVar(&args.fusedebug, "fusedebug", false, "Enable fuse library debug output")
	flag.BoolVar(&args.init, "init", false, "Initialize encrypted directory")
	flag.BoolVar(&args.zerokey, "zerokey", false, "Use all-zero dummy master key")
	flag.BoolVar(&args.openssl, "openssl", true, "Use OpenSSL instead of built-in Go crypto")
	flag.BoolVar(&args.passwd, "passwd", false, "Change password")
	flag.BoolVar(&args.foreground, "f", false, "Stay in the foreground")
	flag.BoolVar(&args.version, "version", false, "Print version and exit")
	flag.BoolVar(&args.plaintextnames, "plaintextnames", false,
		"Do not encrypt file names - can only be used together with -init")
	flag.StringVar(&args.masterkey, "masterkey", "", "Mount with explicit master key")
	args.cpuprofile = flag.String("cpuprofile", "", "Write cpu profile to specified file")

	flag.Parse()
	if args.version {
		fmt.Printf("%s %s; ", PROGRAM_NAME, GitVersion)
		fmt.Printf("on-disk format %d\n", cryptfs.HEADER_CURRENT_VERSION)
		os.Exit(0)
	}
	if !args.foreground {
		daemonize() // does not return
	}
	if *args.cpuprofile != "" {
		f, err := os.Create(*args.cpuprofile)
		if err != nil {
			fmt.Println(err)
			os.Exit(ERREXIT_INIT)
		}
		fmt.Printf("Writing CPU profile to %s\n", *args.cpuprofile)
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	if args.debug {
		cryptfs.Debug.Enable()
		cryptfs.Debug.Printf("Debug output enabled\n")
	}
	if args.openssl == false {
		fmt.Printf("Openssl disabled\n")
	}
	if args.init {
		if flag.NArg() != 1 && args.plaintextnames == false {
			fmt.Printf("Usage: %s --init [--plaintextnames] CIPHERDIR\n", PROGRAM_NAME)
			os.Exit(ERREXIT_USAGE)
		}
		initDir(flag.Arg(0), args.plaintextnames) // does not return
	}
	if args.passwd {
		if flag.NArg() != 1 {
			fmt.Printf("Usage: %s --passwd CIPHERDIR\n", PROGRAM_NAME)
			os.Exit(ERREXIT_USAGE)
		}
	} else {
		// Normal mount
		if flag.NArg() < 2 {
			usageText()
			os.Exit(ERREXIT_USAGE)
		}
		args.mountpoint, _ = filepath.Abs(flag.Arg(1))
		err := checkDirEmpty(args.mountpoint)
		if err != nil {
			fmt.Printf("Invalid MOUNTPOINT: %v\n", err)
			os.Exit(ERREXIT_MOUNTPOINT)
		}
	}
	args.cipherdir, _ = filepath.Abs(flag.Arg(0))
	err := checkDir(args.cipherdir)
	if err != nil {
		fmt.Printf("Invalid CIPHERDIR: %v\n", err)
		os.Exit(ERREXIT_CIPHERDIR)
	}

	var plaintextNames bool
	var cf *cryptfs.ConfFile
	var currentPassword string
	key := make([]byte, cryptfs.KEY_LEN)
	if args.zerokey {
		fmt.Printf("Zerokey mode active: using all-zero dummy master key.\n")
		plaintextNames = args.plaintextnames
	} else if len(args.masterkey) > 0 {
		key = parseMasterKey(args.masterkey)
		fmt.Printf("Using explicit master key.\n")
	} else {
		// Load config file
		cfname := filepath.Join(args.cipherdir, cryptfs.ConfDefaultName)
		_, err = os.Stat(cfname)
		if err != nil {
			fmt.Printf("Error: %s not found in CIPHERDIR\n", cryptfs.ConfDefaultName)
			fmt.Printf("Please run \"%s --init %s\" first\n", os.Args[0], flag.Arg(0))
			os.Exit(ERREXIT_LOADCONF)
		}
		if args.passwd == true {
			fmt.Printf("Old password: ")
		} else {
			fmt.Printf("Password: ")
		}
		currentPassword = readPassword()
		fmt.Printf("\nDecrypting master key... ")
		cryptfs.Warn.Disable() // Silence DecryptBlock() error messages on incorrect password
		key, cf, err = cryptfs.LoadConfFile(cfname, currentPassword)
		cryptfs.Warn.Enable()
		if err != nil {
			fmt.Println(err)
			os.Exit(ERREXIT_LOADCONF)
		}
		fmt.Printf("done.\n")
	}
	if args.passwd == true {
		fmt.Printf("Please enter the new password.\n")
		newPassword := readPasswordTwice()
		if newPassword == currentPassword {
			fmt.Printf("New and old passwords are identical\n")
			os.Exit(ERREXIT_PASSWORD)
		}
		cf.EncryptKey(key, newPassword)
		err := cf.WriteFile()
		if err != nil {
			fmt.Println(err)
			os.Exit(ERREXIT_INIT)
		}
		fmt.Printf("Password changed.\n")
		os.Exit(0)
	}

	if cf != nil {
		plaintextNames = cf.PlaintextNames()
	}
	srv := pathfsFrontend(key, args.cipherdir, args.mountpoint, args.fusedebug, args.openssl, plaintextNames)

	if args.zerokey == false && len(args.masterkey) == 0 {
		printMasterKey(key)
	} else if args.zerokey == true {
		fmt.Printf("ZEROKEY MODE PROVIDES NO SECURITY AT ALL AND SHOULD ONLY BE USED FOR TESTING.\n")
	} else if len(args.masterkey) > 0 {
		fmt.Printf("THE MASTER KEY IS VISIBLE VIA \"ps -auxwww\", ONLY USE THIS MODE FOR EMERGENCIES.\n")
	}

	fmt.Println("Filesystem ready.")
	// Send notification to our parent
	sendUsr1()
	// Wait for SIGING in the background and unmount ourselves if we get it
	// This prevents a dangling "Transport endpoint is not connected" mountpoint
	handleSigint(srv, args.mountpoint)
	// Jump into server loop. Returns when it gets an umount request from the kernel.
	srv.Serve()
	// main returns with code 0
}

func pathfsFrontend(key []byte, cipherdir string, mountpoint string,
	debug bool, openssl bool, plaintextNames bool) *fuse.Server {

	finalFs := pathfs_frontend.NewFS(key, cipherdir, openssl, plaintextNames)
	pathFsOpts := &pathfs.PathNodeFsOptions{ClientInodes: true}
	pathFs := pathfs.NewPathNodeFs(finalFs, pathFsOpts)
	fuseOpts := &nodefs.Options{
		// These options are to be compatible with libfuse defaults,
		// making benchmarking easier.
		NegativeTimeout: time.Second,
		AttrTimeout:     time.Second,
		EntryTimeout:    time.Second,
	}
	conn := nodefs.NewFileSystemConnector(pathFs.Root(), fuseOpts)
	var mOpts fuse.MountOptions
	mOpts.AllowOther = false
	// Set values shown in "df -T" and friends
	// First column, "Filesystem"
	mOpts.Options = append(mOpts.Options, "fsname="+cipherdir)
	// Second column, "Type", will be shown as "fuse." + Name
	mOpts.Name = "gocryptfs"

	srv, err := fuse.NewServer(conn.RawFS(), mountpoint, &mOpts)
	if err != nil {
		fmt.Printf("Mount failed: %v", err)
		os.Exit(ERREXIT_MOUNT)
	}
	srv.SetDebug(debug)

	return srv
}

func handleSigint(srv *fuse.Server, mountpoint string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		err := srv.Unmount()
		if err != nil {
			fmt.Print(err)
			fmt.Printf("Trying lazy unmount\n")
			cmd := exec.Command("fusermount", "-u", "-z", mountpoint)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
		os.Exit(1)
	}()
}
