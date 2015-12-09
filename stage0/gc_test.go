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

package stage0

import (
	"strings"
	"testing"
)

var mountinfo = `71 21 0:39 / /var/lib/rkt rw,relatime shared:26 -
116 71 0:45 / /my/mount/prefix/rootfs rw,relatime shared:32 -
121 116 0:46 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs rw,relatime shared:63 -
146 145 0:27 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/sys/fs/cgroup/systemd rw,nosuid,nodev,noexec,relatime shared:6 -
145 144 0:26 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/sys/fs/cgroup rw,nosuid,nodev,noexec shared:5 -
144 121 0:17 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/sys rw,relatime shared:3 -
180 121 0:19 /nixos /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs ro,relatime shared:1 -
184 183 0:27 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/sys/fs/cgroup/systemd rw,nosuid,nodev,noexec,relatime shared:6 -
183 182 0:26 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/sys/fs/cgroup rw,nosuid,nodev,noexec shared:5 -
182 180 0:17 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/sys rw,relatime shared:3 -
209 206 0:45 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/my/mount/prefix/rootfs rw,relatime shared:32 -
219 218 0:27 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/sys/fs/cgroup/systemd rw,nosuid,nodev,noexec,relatime shared:6 -
218 217 0:26 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/sys/fs/cgroup rw,nosuid,nodev,noexec shared:5 -
217 210 0:17 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/sys rw,relatime shared:3 -
210 209 0:46 / /my/mount/prefix/rootfs/opt/stage2/busybox/rootfs/rootfs/my/mount/prefix/rootfs/opt/stage2/busybox/rootfs rw,relatime shared:63 -
`

func TestMountOrdering(t *testing.T) {
	tests := []struct {
		prefix     string
		ids        []int
		shouldPass bool
	}{
		{
			prefix: "/my/mount/prefix",
			ids:    []int{219, 218, 217, 210, 209, 184, 183, 182, 146, 145, 180, 144, 121, 116},
			//ids:        []int{2, 9, 8, 7, 6},
			shouldPass: true,
		},
	}

	for i, tt := range tests {
		mi := strings.NewReader(mountinfo)
		mnts, err := getMountsForPrefix(tt.prefix, mi)
		if err != nil {
			t.Errorf("problems finding mount points: %v", err)
		}

		if len(mnts) != len(tt.ids) {
			t.Errorf("test  %d: didn't find the expected number of mounts. found %d but wanted %d.", i, len(mnts), len(tt.ids))
			return
		}

		mountIds := make([]int, len(tt.ids))
		match := true
		for j, mnt := range mnts {
			mountIds[j] = mnt.id
			match = match && (mnt.id == tt.ids[j])
		}
		if !match {
			t.Errorf("test #%d: problem with mount ordering; expected\n%v, got\n%v", i, tt.ids, mountIds)
			return
		}
	}
}
