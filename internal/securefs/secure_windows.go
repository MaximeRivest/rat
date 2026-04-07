//go:build windows

package securefs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return applyPrivateACL(path, true)
}

func MakePrivateFile(path string) error {
	return applyPrivateACL(path, false)
}

func OpenPrivateAppend(path string) (*os.File, error) {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := applyPrivateACL(path, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func applyPrivateACL(path string, isDir bool) error {
	currentUser, err := currentUserSID()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}

	inheritance := uint32(windows.NO_INHERITANCE)
	if isDir {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee:           trusteeFromSID(currentUser, windows.TRUSTEE_IS_USER),
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee:           trusteeFromSID(systemSID, windows.TRUSTEE_IS_USER),
		},
	}, nil)
	if err != nil {
		return err
	}

	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
		currentUser,
		nil,
		acl,
		nil,
	)
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func trusteeFromSID(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.TRUSTEE {
	return windows.TRUSTEE{
		TrusteeForm:  windows.TRUSTEE_IS_SID,
		TrusteeType:  trusteeType,
		TrusteeValue: windows.TrusteeValueFromSID(sid),
	}
}
