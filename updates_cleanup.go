package main

import (
	"os"
	"path/filepath"
)

type updateDownloadOwner struct {
	Product string `json:"product"`
	Root    string `json:"root"`
	ID      string `json:"id"`
	SHA256  string `json:"sha256"`
}

func discardUpdateDownload(dir, base, root string) {
	if dir == "" || filepath.Dir(dir) != base || len(filepath.Base(dir)) != 32 || noUpdateLinks(dir) != nil {
		return
	}
	if _, err := os.Lstat(filepath.Join(dir, "plan.json")); !os.IsNotExist(err) {
		return
	} // May be needed for recovery.
	var owner updateDownloadOwner
	path := filepath.Join(dir, "download-owner.json")
	if readUpdateJSON(path, &owner) != nil || owner.Product != "LocalBroadcast" || owner.Root != root || owner.ID != filepath.Base(dir) || !digestPattern.MatchString(owner.SHA256) {
		return
	}
	archive := filepath.Join(dir, "package.zip")
	if _, err := os.Lstat(archive); err == nil {
		if checkUpdateHash(archive, owner.SHA256) != nil {
			return
		}
		if os.Remove(archive) != nil {
			return
		}
	} else if !os.IsNotExist(err) {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(dir) // Unknown files prevent directory removal.
}
