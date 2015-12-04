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
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/appc/spec/schema/types"
)

const (
	mountinfoPath = "/proc/self/mountinfo"
)

var (
	debug bool
)

func init() {
	flag.BoolVar(&debug, "debug", false, "Run in debug mode")

	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

// readLines reads a whole file into memory
// and returns a slice of its lines.
func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// writeLines writes the lines to the given file.
func writeLines(lines []string, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	w := bufio.NewWriter(file)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	return w.Flush()
}

func main() {
	flag.Parse()

	if !debug {
		log.SetOutput(ioutil.Discard)
	}

	podID, err := types.NewUUID(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "UUID is missing or malformed")
		os.Exit(1)
	}

	log.Printf("Stage1: garbage-collecting %q", podID)

	lines, err := readLines(mountinfoPath)
	if err != nil {
		log.Fatalf("readLines: %s", err)
	}

	regex := regexp.MustCompile(fmt.Sprintf(".*%s.*", podID))
	mountMap := make(map[string]struct{})
	var mountList []string
	for _, line := range lines {
		if regex.MatchString(line) {
			splitLine := strings.Split(line, " ")
			mount := splitLine[4]
			if _, seen := mountMap[mount]; !seen {
				mountMap[mount] = struct{}{}
				mountList = append(mountList, mount)
			}
		}
	}

	if len(mountList) == 0 {
		return
	}

	sort.Sort(sort.StringSlice(mountList))
	for _, dest := range mountList {
		log.Printf("Stage1: remounting %q", dest)
		var flags uintptr = syscall.MS_REC | syscall.MS_PRIVATE
		if err := syscall.Mount("", dest, "", flags, ""); err != nil {
			log.Fatalf("Error remounting %q with flags %v: %v", dest, flags, err)
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(mountList)))
	for _, dest := range mountList {
		log.Printf("Stage1: Unmounting %q", dest)
		if err := syscall.Unmount(dest, 0); err != nil {
			log.Fatalf("Error unmounting %v: %v", dest, err)
		}
	}
}
