package metrics

import "testing"

func TestSkipFilesystem(t *testing.T) {
	for _, tc := range []struct {
		name          string
		fstype, mount string
		opts          []string
		skip          bool
	}{
		// Every installed snap loop-mounts a read-only squashfs that is 100%
		// used by definition — a stock Ubuntu desktop has dozens.
		{"snap squashfs", "squashfs", "/snap/firefox/8702", []string{"ro", "nodev"}, true},
		{"snap mount, odd fstype", "ext4", "/snap/core22/2411", nil, true},
		{"read-only mount", "ext4", "/mnt/backup", []string{"ro", "relatime"}, true},
		{"docker image layer", "ext4", "/var/lib/docker/overlay2/abc/merged", nil, true},
		{"kernel pseudo fs", "cgroup2", "/sys/fs/cgroup", nil, true},

		// Real, fillable storage must still be reported.
		{"root", "ext4", "/", []string{"rw", "relatime"}, false},
		{"home on zfs", "zfs", "/home", []string{"rw"}, false},
		{"efi partition", "vfat", "/boot/efi", []string{"rw"}, false},
		{"windows volume", "NTFS", `C:\`, nil, false},
		// A full /tmp or container root is a genuine problem, not noise.
		{"tmpfs tmp", "tmpfs", "/tmp", []string{"rw"}, false},
		{"container overlay root", "overlay", "/", []string{"rw"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipFilesystem(tc.fstype, tc.mount, tc.opts); got != tc.skip {
				t.Errorf("skipFilesystem(%q,%q,%v) = %v, want %v", tc.fstype, tc.mount, tc.opts, got, tc.skip)
			}
		})
	}
}
