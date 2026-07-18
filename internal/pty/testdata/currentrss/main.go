//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -lproc
#include <libproc.h>
#include <stdint.h>
#include <sys/resource.h>

static uint64_t current_rss_bytes(int pid) {
	struct rusage_info_v2 info;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&info) != 0) {
		return 0;
	}

	return info.ri_resident_size;
}
*/
import "C"

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}

	for _, rawPID := range os.Args[1:] {
		pid, err := strconv.Atoi(rawPID)
		if err != nil || pid <= 0 {
			os.Exit(2)
		}

		rss := uint64(C.current_rss_bytes(C.int(pid)))
		if rss == 0 {
			os.Exit(1)
		}

		fmt.Println(rss)
	}
}
