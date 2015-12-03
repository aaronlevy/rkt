// Copyright 2015 The rkt Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	stage1commontypes "github.com/coreos/rkt/stage1_common/types"

	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/appc/spec/schema"
	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/appc/spec/schema/types"

	"github.com/coreos/rkt/common"
	"github.com/coreos/rkt/pkg/sys"
)

const (
	flavor = "fly"
)

type flyMount struct {
	HostPath         string
	TargetPrefixPath string
	RelTargetPath    string
	Fs               string
	Flags            uintptr
}

type volumeMountTuple struct {
	V types.Volume
	M schema.Mount
}

var (
	debug        bool
	netList      common.NetList
	interactive  bool
	privateUsers string
	mdsToken     string
	localhostIP  net.IP
	localConfig  string
)

func init() {
	flag.BoolVar(&debug, "debug", false, "Run in debug mode")
	flag.Var(&netList, "net", "Setup networking")
	flag.BoolVar(&interactive, "interactive", false, "The pod is interactive")
	flag.StringVar(&privateUsers, "private-users", "", "Run within user namespace. Can be set to [=UIDBASE[:NUIDS]]")
	flag.StringVar(&mdsToken, "mds-token", "", "MDS auth token")
	flag.StringVar(&localConfig, "local-config", common.DefaultLocalConfigDir, "Local config path")
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()

	localhostIP = net.ParseIP("127.0.0.1")
	if localhostIP == nil {
		panic("localhost IP failed to parse")
	}
}

func lookupPath(bin string, paths string) (string, error) {
	pathsArr := filepath.SplitList(paths)
	for _, path := range pathsArr {
		binPath := filepath.Join(path, bin)
		binAbsPath, err := filepath.Abs(binPath)
		if err != nil {
			return "", fmt.Errorf("unable to find absolute path for %s", binPath)
		}
		d, err := os.Stat(binAbsPath)
		if err != nil {
			continue
		}
		// Check the executable bit, inspired by os.exec.LookPath()
		if m := d.Mode(); !m.IsDir() && m&0111 != 0 {
			return binAbsPath, nil
		}
	}
	return "", fmt.Errorf("unable to find %q in %q", bin, paths)
}

func withClearedCloExec(lfd int, f func() error) error {
	err := sys.CloseOnExec(lfd, false)
	if err != nil {
		return err
	}
	defer sys.CloseOnExec(lfd, true)

	return f()
}

func writePpid(pid int) error {
	// write ppid file as specified in
	// Documentation/devel/stage1-implementors-guide.md
	out, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Cannot get current working directory: %v\n", err)
	}
	// we are the parent of the process that is PID 1 in the container so we write our PID to "ppid"
	err = ioutil.WriteFile(filepath.Join(out, "ppid"),
		[]byte(fmt.Sprintf("%d\n", pid)), 0644)
	if err != nil {
		return fmt.Errorf("Cannot write ppid file: %v\n", err)
	}
	return nil
}

func evaluateMounts(rfs string, app string, p *stage1commontypes.Pod) ([]flyMount, error) {
	imApp := p.Images[app].App
	namedVolumeMounts := map[types.ACName]volumeMountTuple{}

	for _, m := range p.Manifest.Apps[0].Mounts {
		_, exists := namedVolumeMounts[m.Volume]
		if exists {
			log.Fatalf("fly: duplicated mount given: %q", m.Volume)
		}
		namedVolumeMounts[m.Volume] = volumeMountTuple{M: m}
		log.Printf("Adding %+v", namedVolumeMounts[m.Volume])
	}

	// Merge command-line Mounts with ImageManifest's MountPoints
	for _, mp := range imApp.MountPoints {
		tuple, exists := namedVolumeMounts[mp.Name]
		switch {
		case exists && tuple.M.Path != mp.Path:
			return nil, fmt.Errorf("fly: conflicting path information from mount and mountpoint %q", mp.Name)
		case !exists:
			namedVolumeMounts[mp.Name] = volumeMountTuple{M: schema.Mount{Volume: mp.Name, Path: mp.Path}}
			log.Printf("Adding %+v", namedVolumeMounts[mp.Name])
		}
	}

	// Insert the command-line Volumes
	for _, v := range p.Manifest.Volumes {
		// Check if we have a mount for this volume
		tuple, exists := namedVolumeMounts[v.Name]
		if !exists {
			return nil, fmt.Errorf("fly: missing mount for volume %q", v.Name)
		} else if tuple.M.Volume != v.Name {
			// TODO(steveeJ): remove this case. it's merely a safety mechanism regarding the implementation
			return nil, fmt.Errorf("fly: mismatched volume:mount pair: %q != %q", v.Name, tuple.M.Volume)
		}
		namedVolumeMounts[v.Name] = volumeMountTuple{V: v, M: tuple.M}
		log.Printf("Adding %+v", namedVolumeMounts[v.Name])
	}

	// Merge command-line Volumes with ImageManifest's MountPoints
	for _, mp := range imApp.MountPoints {
		// Check if we have a volume for this mountpoint
		tuple, exists := namedVolumeMounts[mp.Name]
		if !exists || tuple.V.Name == "" {
			log.Fatalf("fly: missing volume for mountpoint %q", mp.Name)
		}

		// If empty, fill in ReadOnly bit
		if tuple.V.ReadOnly == nil {
			v := tuple.V
			v.ReadOnly = &mp.ReadOnly
			namedVolumeMounts[mp.Name] = volumeMountTuple{M: tuple.M, V: v}
			log.Printf("Adding %+v", namedVolumeMounts[mp.Name])
		}
	}

	argFlyMounts := []flyMount{}
	var flags uintptr = syscall.MS_BIND | syscall.MS_REC
	for _, tuple := range namedVolumeMounts {
		// Mark the host mount as SHARED so the container's changes to the mount are propagated to the host
		argFlyMounts = append(argFlyMounts,
			flyMount{"", "", tuple.V.Source, "none", syscall.MS_REC | syscall.MS_SHARED},
		)
		argFlyMounts = append(argFlyMounts,
			flyMount{tuple.V.Source, rfs, tuple.M.Path, "none", flags},
		)

		if tuple.V.ReadOnly != nil && *tuple.V.ReadOnly {
			argFlyMounts = append(argFlyMounts,
				flyMount{"", rfs, tuple.M.Path, "none", flags | syscall.MS_REMOUNT | syscall.MS_RDONLY},
			)
		}
	}
	return argFlyMounts, nil
}

func stage1() int {
	uuid, err := types.NewUUID(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "UUID is missing or malformed")
		return 1
	}

	root := "."
	p, err := stage1commontypes.LoadPod(root, uuid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load pod: %v\n", err)
		return 1
	}

	if len(p.Manifest.Apps) != 1 {
		log.Fatalf("Flavor %q only supports 1 application per Pod for now.", flavor)
	}

	// TODO: insert environment from manifest
	env := []string{"PATH=/bin:/sbin:/usr/bin:/usr/local/bin"}
	args := p.Manifest.Apps[0].App.Exec
	rfs := filepath.Join(common.AppPath(p.Root, p.Manifest.Apps[0].Name), "rootfs")

	// set close-on-exec flag on RKT_LOCK_FD so it gets correctly closed when invoking
	// network plugins
	lfd, err := common.GetRktLockFD()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get rkt lock fd: %v\n", err)
		return 1
	}

	if err := sys.CloseOnExec(lfd, true); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set FD_CLOEXEC on rkt lock: %v\n", err)
		return 1
	}

	argFlyMounts, err := evaluateMounts(rfs, string(p.Manifest.Apps[0].Name), p)
	if err != nil {
		log.Fatalf("Error evaluating mounts: %v", err)
	}

	effectiveMounts := append(
		[]flyMount{
			{"", "", "/dev", "none", syscall.MS_REC | syscall.MS_SHARED},
			{"/dev", rfs, "/dev", "none", syscall.MS_BIND | syscall.MS_REC},

			{"", "", "/proc", "none", syscall.MS_REC | syscall.MS_SHARED},
			{"/proc", rfs, "/proc", "none", syscall.MS_BIND | syscall.MS_REC},

			{"", "", "/sys", "none", syscall.MS_REC | syscall.MS_SHARED},
			{"/sys", rfs, "/sys", "none", syscall.MS_BIND | syscall.MS_REC},

			{"tmpfs", rfs, "/tmp", "tmpfs", 0},
		},
		argFlyMounts...,
	)

	for _, mount := range effectiveMounts {
		var (
			err            error
			hostPathInfo   os.FileInfo
			targetPathInfo os.FileInfo
		)
		if mount.HostPath != "" && strings.HasPrefix(mount.HostPath, "/") {
			if hostPathInfo, err = os.Stat(mount.HostPath); err != nil {
				log.Fatalf("fly: something is wrong with the host directory %s: \n%v", mount.HostPath, err)
			}
		} else {
			hostPathInfo = nil
		}

		absTargetPath := filepath.Join(mount.TargetPrefixPath, mount.RelTargetPath)
		if absTargetPath != "/" {
			if targetPathInfo, err = os.Stat(absTargetPath); err != nil && !os.IsNotExist(err) {
				log.Fatalf("fly: something is wrong with the target directory %s: \n%v", absTargetPath, err)
			}

			switch {
			case targetPathInfo == nil:
				absTargetPathParent, _ := filepath.Split(absTargetPath)
				if err := os.MkdirAll(absTargetPathParent, 0700); err != nil {
					log.Fatalf("fly: could not create directory %q: \n%v", absTargetPath, err)
				}
				switch {
				case hostPathInfo == nil || hostPathInfo.IsDir():
					if err := os.Mkdir(absTargetPath, 0700); err != nil {
						log.Fatalf("fly: could not create directory %q: \n%v", absTargetPath, err)
					}
				case !hostPathInfo.IsDir():
					file, err := os.OpenFile(absTargetPath, os.O_CREATE, 0700)
					if err != nil {
						log.Fatalf("fly: could not create file %q: \n%v", absTargetPath, err)
					}
					file.Close()
				}
			case hostPathInfo != nil:
				switch {
				case hostPathInfo.IsDir() && !targetPathInfo.IsDir():
					log.Fatalf("fly: can't mount:  %q is a directory while %q is not", mount.HostPath, absTargetPath)
				case !hostPathInfo.IsDir() && targetPathInfo.IsDir():
					log.Fatalf("fly: can't mount:  %q is not a directory while %q is", mount.HostPath, absTargetPath)
				}
			}
		}

		if err := syscall.Mount(mount.HostPath, absTargetPath, mount.Fs, mount.Flags, ""); err != nil {
			log.Fatalf("Error mounting %q on %q with flags %v: %v", mount.HostPath, absTargetPath, mount.Flags, err)
		}
	}

	if err = writePpid(os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 4
	}

	log.Printf("Chroot to %q", rfs)
	if err := syscall.Chroot(rfs); err != nil {
		log.Fatalf("fly: error chrooting: %v", err)
	}

	if err := os.Chdir("/"); err != nil {
		log.Fatalf("fly: couldn't change to root new directory: %v", err)
	}

	log.Printf("Execing %q in %q", args, rfs)
	err = withClearedCloExec(lfd, func() error {
		return syscall.Exec(args[0], args, env)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute %q: %v\n", args[0], err)
		return 7
	}

	return 0
}

func main() {
	flag.Parse()

	if !debug {
		log.SetOutput(ioutil.Discard)
	}

	// move code into stage1() helper so defered fns get run
	os.Exit(stage1())
}
