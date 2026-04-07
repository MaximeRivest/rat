//go:build windows

package securefs

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestEnsurePrivateDirWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secure")
	if err := EnsurePrivateDir(path); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	assertPrivateACL(t, path, true)
}

func TestOpenPrivateAppendWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secure", "kernel.log")
	f, err := OpenPrivateAppend(path)
	if err != nil {
		t.Fatalf("OpenPrivateAppend: %v", err)
	}
	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertPrivateACL(t, path, false)
}

func assertPrivateACL(t *testing.T, path string, isDir bool) {
	t.Helper()

	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("Owner: %v", err)
	}
	currentUser, err := currentUserSID()
	if err != nil {
		t.Fatalf("currentUserSID: %v", err)
	}
	if !windows.EqualSid(owner, currentUser) {
		t.Fatalf("owner SID = %s, want %s", owner.String(), currentUser.String())
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl == nil {
		t.Fatal("DACL is nil")
	}
	if dacl.AceCount != 2 {
		t.Fatalf("ACE count = %d, want 2", dacl.AceCount)
	}

	entries := make([]*windows.ACCESS_ALLOWED_ACE, dacl.AceCount)
	for i := uint16(0); i < dacl.AceCount; i++ {
		if err := windows.GetAce(dacl, uint32(i), &entries[i]); err != nil {
			t.Fatalf("GetAce(%d): %v", i, err)
		}
	}

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid: %v", err)
	}

	inheritance := uint8(windows.NO_INHERITANCE)
	if isDir {
		inheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}

	seenCurrentUser := false
	seenSystem := false
	for _, ace := range entries {
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case windows.EqualSid(sid, currentUser):
			seenCurrentUser = true
		case windows.EqualSid(sid, systemSID):
			seenSystem = true
		default:
			t.Fatalf("unexpected SID in ACL: %s", sid.String())
		}
		if ace.Mask != windows.GENERIC_ALL {
			t.Fatalf("ACE mask = %#x, want %#x", ace.Mask, windows.GENERIC_ALL)
		}
		if ace.Header.AceFlags != inheritance {
			t.Fatalf("ACE flags = %#x, want %#x", ace.Header.AceFlags, inheritance)
		}
	}
	if !seenCurrentUser || !seenSystem {
		t.Fatalf("ACL missing expected SIDs: currentUser=%v system=%v", seenCurrentUser, seenSystem)
	}
}
